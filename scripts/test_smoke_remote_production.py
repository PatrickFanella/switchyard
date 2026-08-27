#!/usr/bin/env python3

import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent
SCRIPT = ROOT / "scripts/smoke-remote-production.sh"
TOKEN = "read-token-secret"
MODEL = "qwen3:8b"
REVISION = "0123456789abcdef0123456789abcdef01234567"
DEPLOYMENT_ID = "production-a"


FAKE_CURL = r'''#!/usr/bin/env python3
import json
import os
import sys

args = sys.argv[1:]
with open(os.environ["CURL_LOG"], "a", encoding="utf-8") as stream:
    stream.write(json.dumps(args) + "\n")
url = args[-1]
if url == "https://web.example/":
    body = os.environ.get("FAKE_WEB_ROOT", '<!doctype html><title>Switchyard</title>')
elif url == "https://control.example/":
    body = '{"ok":true}'
elif url == "https://web.example/healthz":
    body = os.environ.get("FAKE_WEB_HEALTH", '{"ok":true}')
elif url == "https://control.example/api/operator-safety":
    body = '{"warnings":[]}'
elif url == "https://control.example/api/operator-summary":
    body = json.dumps({
        "schema_version": "1.0",
        "deployment": {
            "source_revision": os.environ.get("FAKE_LIVE_REVISION", "0123456789abcdef0123456789abcdef01234567"),
            "deployment_id": os.environ.get("FAKE_LIVE_DEPLOYMENT_ID", "production-a"),
        },
    })
elif url == "https://model.example/health":
    body = '{"ok":true}'
elif url == "https://model.example/broker/status":
    body = '{}'
elif url == "https://model.example/api/tags":
    body = '{"models":[{"name":"qwen3:8b"},{"name":"other"}]}'
elif url == "https://model.example/api/runs":
    body = os.environ.get("FAKE_RUNS_STATUS", "404")
elif url == "https://model.example/api/generate":
    body = json.dumps({"model": os.environ.get("FAKE_OLLAMA_MODEL", "qwen3:8b"), "response": "ok"})
elif url == "https://model.example/v1/chat/completions":
    body = json.dumps({"model": os.environ.get("FAKE_OPENAI_MODEL", "ollama/qwen3:8b"), "choices":[{"message":{"content":"ok"}}]})
elif url == "https://public.example":
    body = "302\n" + os.environ.get("FAKE_REDIRECT_URL", "https://auth.example/login")
else:
    print("unexpected URL", file=sys.stderr)
    sys.exit(90)
if url == os.environ.get("FAKE_OVERSIZED_URL"):
    body += "x" * 1048577
if "--max-filesize" in args:
    maximum = int(args[args.index("--max-filesize") + 1])
    if len(body.encode()) > maximum:
        print("response exceeded --max-filesize", file=sys.stderr)
        sys.exit(63)
sys.stdout.write(body)
'''


class SmokeRemoteProductionTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.bin = self.root / "bin"
        self.bin.mkdir()
        curl = self.bin / "curl"
        curl.write_text(FAKE_CURL)
        curl.chmod(0o755)
        self.log = self.root / "curl.log"
        self.env = {
            "PATH": f"{self.bin}:{os.environ['PATH']}",
            "CURL_LOG": str(self.log),
            "PYTHONOPTIMIZE": "1",
            "SWITCHYARD_PRODUCTION_WEB_URL": "https://web.example/",
            "SWITCHYARD_PRODUCTION_CONTROL_URL": "https://control.example/",
            "SWITCHYARD_PRODUCTION_MODEL_URL": "https://model.example/",
            "SWITCHYARD_PUBLIC_URL": "https://public.example/",
            "SWITCHYARD_AUTH_ORIGIN": "https://auth.example",
            "SWITCHYARD_EXPECTED_SOURCE_REVISION": REVISION,
            "SWITCHYARD_EXPECTED_DEPLOYMENT_ID": DEPLOYMENT_ID,
            "SWITCHYARD_CONTROL_READ_TOKEN": TOKEN,
            "SWITCHYARD_SMOKE_MODEL": MODEL,
        }

    def run_smoke(self, env=None):
        return subprocess.run(
            [str(SCRIPT)],
            cwd=ROOT,
            env=env or self.env,
            text=True,
            capture_output=True,
            check=False,
        )

    def calls(self):
        if not self.log.exists():
            return []
        return [json.loads(line) for line in self.log.read_text().splitlines()]

    def test_requires_every_variable_before_network(self) -> None:
        for name in (
            "SWITCHYARD_PRODUCTION_WEB_URL",
            "SWITCHYARD_PRODUCTION_CONTROL_URL",
            "SWITCHYARD_PRODUCTION_MODEL_URL",
            "SWITCHYARD_PUBLIC_URL",
            "SWITCHYARD_AUTH_ORIGIN",
            "SWITCHYARD_EXPECTED_SOURCE_REVISION",
            "SWITCHYARD_EXPECTED_DEPLOYMENT_ID",
            "SWITCHYARD_CONTROL_READ_TOKEN",
            "SWITCHYARD_SMOKE_MODEL",
        ):
            with self.subTest(name=name):
                env = self.env.copy()
                env.pop(name)
                result = self.run_smoke(env)
                self.assertEqual(result.returncode, 2)
                self.assertIn(f"{name} is required", result.stderr)
                self.assertEqual(self.calls(), [])

    def test_rejects_malformed_urls_before_network(self) -> None:
        cases = (
            ("SWITCHYARD_PRODUCTION_WEB_URL", "web.example"),
            ("SWITCHYARD_PRODUCTION_CONTROL_URL", "https://user@control.example"),
            ("SWITCHYARD_PRODUCTION_MODEL_URL", "ftp://model.example"),
            ("SWITCHYARD_PUBLIC_URL", "https://public.example?token=value"),
            ("SWITCHYARD_AUTH_ORIGIN", "http://auth.example"),
            ("SWITCHYARD_AUTH_ORIGIN", "https://auth.example/login"),
        )
        for name, value in cases:
            with self.subTest(name=name, value=value):
                env = self.env.copy()
                env[name] = value
                result = self.run_smoke(env)
                self.assertEqual(result.returncode, 2)
                self.assertEqual(self.calls(), [])

    def test_rejects_malformed_expected_identity_before_network(self) -> None:
        for name, value in (
            ("SWITCHYARD_EXPECTED_SOURCE_REVISION", "A" * 40),
            ("SWITCHYARD_EXPECTED_SOURCE_REVISION", "a" * 39),
            ("SWITCHYARD_EXPECTED_DEPLOYMENT_ID", "unsafe identity"),
            ("SWITCHYARD_EXPECTED_DEPLOYMENT_ID", "a" * 129),
        ):
            with self.subTest(name=name, value=value):
                env = self.env.copy()
                env[name] = value
                result = self.run_smoke(env)
                self.assertEqual(result.returncode, 2)
                self.assertEqual(self.calls(), [])

    def test_probes_remote_read_only_contract(self) -> None:
        result = self.run_smoke()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn(TOKEN, result.stdout + result.stderr)
        calls = self.calls()
        self.assertEqual([call[-1] for call in calls], [
            "https://web.example/",
            "https://web.example/healthz",
            "https://control.example/api/operator-safety",
            "https://control.example/api/operator-summary",
            "https://model.example/health",
            "https://model.example/broker/status",
            "https://model.example/api/tags",
            "https://model.example/api/runs",
            "https://model.example/api/generate",
            "https://model.example/v1/chat/completions",
            "https://public.example",
        ])
        self.assertTrue(all(call[0] == "--disable" for call in calls))
        self.assertNotIn("Authorization: Bearer", " ".join(calls[0]))
        self.assertNotIn("Authorization: Bearer", " ".join(calls[1]))
        for call in calls[2:4]:
            self.assertIn(f"Authorization: Bearer {TOKEN}", call)
        for call in calls[4:]:
            self.assertNotIn(f"Authorization: Bearer {TOKEN}", call)
        self.assertEqual(json.loads(calls[8][calls[8].index("-d") + 1])["model"], MODEL)
        self.assertEqual(json.loads(calls[9][calls[9].index("-d") + 1])["model"], f"ollama/{MODEL}")
        self.assertIn("--max-time", calls[0])
        self.assertIn("--connect-timeout", calls[0])
        for call in calls:
            self.assertEqual(call[call.index("--max-filesize") + 1], "1048576")
        self.assertEqual(calls[7][calls[7].index("--write-out") + 1], "%{http_code}")
        self.assertIn("--head", calls[-1])

    def test_rejects_wrong_live_revision_or_deployment(self) -> None:
        for name, value in (
            ("FAKE_LIVE_REVISION", "f" * 40),
            ("FAKE_LIVE_DEPLOYMENT_ID", "production-b"),
        ):
            with self.subTest(name=name):
                env = self.env.copy()
                env[name] = value
                result = self.run_smoke(env)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(self.calls()[-1][-1], "https://control.example/api/operator-summary")

    def test_rejects_wrong_auth_origin(self) -> None:
        env = self.env.copy()
        env["FAKE_REDIRECT_URL"] = "https://auth.example.attacker.test/login"
        result = self.run_smoke(env)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("did not match", result.stderr)

    def test_rejects_wrong_model_in_live_response(self) -> None:
        env = self.env.copy()
        env["FAKE_OLLAMA_MODEL"] = "other"
        result = self.run_smoke(env)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.calls()[-1][-1], "https://model.example/api/generate")

    def test_web_health_is_probed_separately(self) -> None:
        env = self.env.copy()
        env["FAKE_WEB_HEALTH"] = '{"ok":false}'
        result = self.run_smoke(env)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(
            [call[-1] for call in self.calls()],
            ["https://web.example/", "https://web.example/healthz"],
        )

    def test_rejects_control_body_at_wrong_web_target(self) -> None:
        env = self.env.copy()
        env["SWITCHYARD_PRODUCTION_WEB_URL"] = "https://control.example/"
        result = self.run_smoke(env)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual([call[-1] for call in self.calls()], ["https://control.example/"])

    def test_rejects_oversized_response(self) -> None:
        env = self.env.copy()
        env["FAKE_OVERSIZED_URL"] = "https://web.example/"
        result = self.run_smoke(env)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("exceeded --max-filesize", result.stderr)
        self.assertEqual([call[-1] for call in self.calls()], ["https://web.example/"])

    def test_script_has_no_local_host_inspection(self) -> None:
        script = SCRIPT.read_text()
        self.assertNotIn("systemctl", script)
        self.assertNotIn("nft", script)
        self.assertNotIn("SWITCHYARD_CONTROL_API_TOKEN", script)
        self.assertNotIn("assert ", script)

    def test_rejects_control_route_on_model_listener(self) -> None:
        env = self.env.copy()
        env["FAKE_RUNS_STATUS"] = "200"
        result = self.run_smoke(env)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.calls()[-1][-1], "https://model.example/api/runs")


if __name__ == "__main__":
    unittest.main()
