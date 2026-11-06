#!/usr/bin/env python3
"""Read-only native admission for the host-owned frontend deployment profile."""
import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess
import urllib.request

if not __debug__:
    raise RuntimeError('Web admission requires enabled validation checks')


def sha(data):
    return hashlib.sha256(data).hexdigest()


def run(args):
    result = subprocess.run(args, capture_output=True, timeout=30)
    assert result.returncode == 0, 'Required native admission command failed'
    return result.stdout


def check_native(contract, receiver):
    for filename, digest in contract['files'].items():
        assert sha(Path(filename).read_bytes()) == digest, 'Native configuration changed'
    for name, expected in contract['native'].items():
        current = receiver.inspect(name)
        actual = {'id': current['Id'], 'image': current['Image'], 'started_at': current['State']['StartedAt'],
                  'environment_sha256': sha(json.dumps(sorted(current['Config']['Env'])).encode()),
                  'runtime_guard': receiver.runtime_guard(current)}
        assert actual == expected, 'Native runtime changed'
        assert current['State']['Running'] and not current['State']['Restarting']
        assert current['State'].get('Health', {}).get('Status', 'healthy') == 'healthy'
    api = receiver.inspect('switchyard-api-1')
    environment = dict(value.split('=', 1) for value in api['Config']['Env'])
    assert environment['GITEA_GIT_USERNAME'] == 'open-pilot'
    # The runtime identity is intentionally separate from the deployment agent.
    request = urllib.request.Request('https://git.subcult.tv/api/v1/user',
                                     headers={'Authorization': 'token ' + environment['GITEA_TOKEN'],
                                              'User-Agent': 'SUBCULT-deploy/1'})
    opener = urllib.request.build_opener(receiver.NoRedirect)
    with opener.open(request, timeout=10) as response:
        assert json.load(response)['login'] == 'open-pilot'


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--profile', type=Path, required=True)
    args = parser.parse_args()
    profile = json.loads(args.profile.read_text())
    assert set(profile['services']) == {'web'} and profile['repository'] == 'subculture-collective/switchyard'
    assert sha(Path(__file__).read_bytes()) == profile['hook_sha256']
    receiver_path = Path(profile['receiver_path'])
    assert sha(receiver_path.read_bytes()) == profile['receiver_sha256']
    spec = importlib.util.spec_from_file_location('receiver', receiver_path)
    receiver = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(receiver)
    contract_path = Path(profile['native_contract'])
    assert sha(contract_path.read_bytes()) == profile['native_contract_sha256']
    contract = json.loads(contract_path.read_text())
    assert sha(Path(profile['compose_file']).read_bytes()) == profile['compose_sha256']
    revision = run(['git', 'ls-remote', 'https://git.subcult.tv/subculture-collective/switchyard.git', 'refs/heads/main']).decode().split()[0]
    receiver.assert_current_main(profile, revision)
    cache = profile['source_cache']
    run(['git', '--git-dir', cache, 'fetch', '--depth=1', 'https://git.subcult.tv/subculture-collective/switchyard.git', revision])
    for path, expected in contract['source_objects'].items():
        assert run(['git', '--git-dir', cache, 'rev-parse', revision + ':' + path]).decode().strip() == expected, 'Review native/UI compatibility before admission'
    web = receiver.inspect('switchyard-web-1')
    assert web['NetworkSettings']['Networks']['switchyard_default']['IPAddress'] == contract['web_ipv4']
    assert {key: web['Config'].get(key) for key in contract['web_startup']} == contract['web_startup']
    image = receiver.inspect(receiver.image_tag(profile, 'web', revision), 'image')
    assert {key: image['Config'].get(key) for key in contract['web_startup']} == contract['web_startup']
    assert sorted(image['Config']['Env']) == contract['web_image_environment']
    check_native(contract, receiver)
    receiver.assert_current_main(profile, revision)
    print(json.dumps({'result': 'PASS', 'source_commit': revision, 'runtime_identity': 'open-pilot',
                      'native_containers': len(contract['native']), 'source_compatibility': 'PASS'}))


if __name__ == '__main__':
    try:
        main()
    except Exception:
        raise SystemExit('Frontend admission failed; inspect native runtime and host contract privately')
