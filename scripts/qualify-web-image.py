#!/usr/bin/env python3
"""Check actual nginx responses without an API or a published listener."""
import json
from pathlib import PurePosixPath
import re
import subprocess
import sys
import time


def run(args):
    return subprocess.check_output(args, stderr=subprocess.PIPE)


def main():
    image, revision = sys.argv[1:]
    assert re.fullmatch(r'[0-9a-f]{40}', revision)
    info = json.loads(run(['docker', 'image', 'inspect', image]))[0]
    assert info['Config']['Labels']['org.opencontainers.image.revision'] == revision
    assert info['Config']['Labels']['org.opencontainers.image.source'] == 'https://git.subcult.tv/subculture-collective/switchyard'
    # nginx resolves the API name at startup. The isolated fixture exercises
    # static responses; /healthz remains a live native-API readiness check.
    container = run(['docker', 'run', '-d', '--network', 'none', '--add-host', 'api:127.0.0.1',
                     '-e', 'SWITCHYARD_WEB_TRUSTED_PROXY_CIDR=127.0.0.1/32',
                     '-e', 'SWITCHYARD_DASHBOARD_CIDR=127.0.0.1/32',
                     '-e', 'NGINX_ENVSUBST_FILTER=SWITCHYARD_', image]).decode().strip()

    def fetch(path, status=200):
        result = subprocess.run(['docker', 'exec', container, 'wget', '-S', '-O', '-',
                                 'http://127.0.0.1' + path], capture_output=True)
        assert ('HTTP/1.1 ' + str(status)).encode() in result.stderr, path
        return result.stdout, result.stderr.lower()

    try:
        deadline = time.monotonic() + 30
        while True:
            try:
                fetch('/')
                break
            except AssertionError:
                assert time.monotonic() < deadline
                time.sleep(.25)
        for query in ['', '?v=' + revision, '?v=stale']:
            body, headers = fetch('/build-info.json' + query)
            assert json.loads(body) == {'source_commit': revision, 'repository': 'subculture-collective/switchyard'}
            assert b'cache-control: no-store' in headers
        files = run(['docker', 'exec', container, 'find', '/usr/share/nginx/html', '-type', 'f']).decode().splitlines()
        served = 0
        for filename in files:
            path = '/' + str(PurePosixPath(filename).relative_to('/usr/share/nginx/html'))
            if path.endswith('.map'):
                fetch(path, 404)
                continue
            assert fetch(path)[0] == run(['docker', 'exec', container, 'cat', filename]), path
            served += 1
        fetch('/not-a-source.map', 404)
        root, _ = fetch('/')
        assert b'og:site_name' in root and b'summary_large_image' in root and b'https://switchyard.subcult.tv/' in root
        print(json.dumps({'result': 'PASS', 'source_commit': revision, 'image': info['Id'],
                          'served_files': served, 'uncached_marker_queries': 3}))
    finally:
        run(['docker', 'rm', '-f', container])


if __name__ == '__main__':
    main()
