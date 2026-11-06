import copy
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('admission', Path(__file__).with_name('verify-web-admission.py'))
admission = importlib.util.module_from_spec(spec)
spec.loader.exec_module(admission)


class NativeAdmissionTests(unittest.TestCase):
    def test_image_and_container_default_user_encodings_share_one_startup_contract(self):
        expected = {'User': '', 'Entrypoint': ['/docker-entrypoint.sh'],
                    'Cmd': ['nginx', '-g', 'daemon off;'], 'WorkingDir': '/', 'StopSignal': 'SIGQUIT'}
        for user in [None, '']:
            self.assertEqual(admission.startup(dict(expected, User=user), expected), expected)
        for key, value in [('User', '1000:1000'), ('User', 'nginx'), ('Cmd', ['sh']),
                           ('Entrypoint', ['/other-entrypoint.sh']), ('WorkingDir', '/tmp'), ('StopSignal', 'SIGTERM')]:
            with self.subTest(field=key, value=value):
                self.assertNotEqual(admission.startup(dict(expected, **{key: value}), expected), expected)

    def test_optimized_python_cannot_disable_guards(self):
        result = subprocess.run([sys.executable, '-O', str(Path(__file__).with_name('verify-web-admission.py')), '--help'], capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(b'requires enabled validation', result.stderr)

    def test_native_drift_and_foreign_runtime_identity_are_refused(self):
        original = {'Id': 'native-id', 'Image': 'native-image', 'State': {
            'StartedAt': 'registered-start', 'Running': True, 'Restarting': False,
            'Health': {'Status': 'healthy'}}, 'Config': {
                'Env': ['GITEA_GIT_USERNAME=open-pilot', 'GITEA_TOKEN=fixture']}}
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / 'native-config';path.write_bytes(b'registered')
            contract = {'files': {str(path): admission.sha(b'registered')}, 'native': {'switchyard-api-1': {
                'id': original['Id'], 'image': original['Image'], 'started_at': original['State']['StartedAt'],
                'environment_sha256': admission.sha(json.dumps(sorted(original['Config']['Env'])).encode()),
                'runtime_guard': 'registered-guard'}}}
            current = copy.deepcopy(original)
            class Receiver:
                NoRedirect = object()
                @staticmethod
                def inspect(name):
                    return current
                @staticmethod
                def runtime_guard(container):
                    return 'registered-guard'
            class Opener:
                @staticmethod
                def open(request, timeout):
                    return io.StringIO('{"login":"open-pilot"}')
            with patch.object(admission.urllib.request, 'build_opener', return_value=Opener()):
                admission.check_native(contract, Receiver)
                changes = [lambda c: c.update(Id='other'), lambda c: c.update(Image='other'),
                           lambda c: c['State'].update(StartedAt='restarted'),
                           lambda c: c['State'].update(Running=False),
                           lambda c: c['State']['Health'].update(Status='unhealthy'),
                           lambda c: c['Config']['Env'].append('NEW_SETTING=changed')]
                for change in changes:
                    current = copy.deepcopy(original);change(current)
                    with self.assertRaises(AssertionError):admission.check_native(contract, Receiver)
                current = copy.deepcopy(original);path.write_bytes(b'changed')
                with self.assertRaises(AssertionError):admission.check_native(contract, Receiver)
                path.write_bytes(b'registered')
            class WrongIdentity:
                @staticmethod
                def open(request, timeout):
                    return io.StringIO('{"login":"subcult-agent"}')
            with patch.object(admission.urllib.request, 'build_opener', return_value=WrongIdentity()):
                with self.assertRaises(AssertionError):admission.check_native(contract, Receiver)


if __name__ == '__main__':
    unittest.main()
