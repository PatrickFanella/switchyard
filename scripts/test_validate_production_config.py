import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from validate_production_config import parse_dotenv, validate


REPO_ROOT = Path(__file__).resolve().parent.parent


class ProductionConfigTest(unittest.TestCase):
    def setUp(self) -> None:
        self.env = {
            "SWITCHYARD_WEB_TRANSPORT": "tailscale",
            "SWITCHYARD_WEB_BIND_HOST": "100.65.164.66",
            "SWITCHYARD_WEB_TRUSTED_PROXY_CIDR": "100.77.128.80/32",
            "SWITCHYARD_DASHBOARD_CIDR": "127.0.0.1/32",
            "SWITCHYARD_PUBLIC_URL": "https://switchyard.example.test",
            "SWITCHYARD_REVERSE_PROXY_AUTH": "true",
            "SWITCHYARD_BIND_HOST": "127.0.0.1",
            "SWITCHYARD_MODEL_BIND_HOST": "100.65.164.66",
            "POSTGRES_PORT": "5432",
            "API_PORT": "18080",
            "LLAMA_LINE_PORT": "11434",
            "WEB_PORT": "8081",
        }

    def assert_rejected(self, name: str, value: str, message: str) -> None:
        self.env[name] = value
        self.assertIn(message, validate(self.env))

    def test_accepts_approved_tailscale_contract(self) -> None:
        self.assertEqual(validate(self.env), [])

    def test_rejects_http_transport(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_WEB_TRANSPORT",
            "http",
            "production requires SWITCHYARD_WEB_TRANSPORT=tailscale",
        )

    def test_rejects_non_cgnat_web_bind(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_WEB_BIND_HOST",
            "10.0.0.50",
            "SWITCHYARD_WEB_BIND_HOST must be an IPv4 address within 100.64.0.0/10",
        )

    def test_rejects_non_cgnat_proxy(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_WEB_TRUSTED_PROXY_CIDR",
            "10.0.0.200/32",
            "SWITCHYARD_WEB_TRUSTED_PROXY_CIDR must be within 100.64.0.0/10",
        )

    def test_rejects_broad_proxy_cidr(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_WEB_TRUSTED_PROXY_CIDR",
            "100.77.128.0/24",
            "SWITCHYARD_WEB_TRUSTED_PROXY_CIDR must be an explicit /32",
        )

    def test_rejects_proxy_without_explicit_prefix(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_WEB_TRUSTED_PROXY_CIDR",
            "100.77.128.80",
            "SWITCHYARD_WEB_TRUSTED_PROXY_CIDR must be an explicit /32",
        )

    def test_rejects_same_tailscale_peer(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_WEB_TRUSTED_PROXY_CIDR",
            "100.65.164.66/32",
            "SWITCHYARD_WEB_BIND_HOST and SWITCHYARD_WEB_TRUSTED_PROXY_CIDR must identify distinct Tailscale peers",
        )

    def test_rejects_broad_dashboard_cidr(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_DASHBOARD_CIDR",
            "0.0.0.0/0",
            "SWITCHYARD_DASHBOARD_CIDR must be a literal loopback IPv4 /32",
        )

    def test_rejects_remote_dashboard_peer(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_DASHBOARD_CIDR",
            "100.77.128.80/32",
            "SWITCHYARD_DASHBOARD_CIDR must be a literal loopback IPv4 /32",
        )

    def test_rejects_non_https_public_url(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_PUBLIC_URL",
            "http://switchyard.example.test",
            "SWITCHYARD_PUBLIC_URL must be an absolute HTTPS URL",
        )

    def test_rejects_malformed_public_url(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_PUBLIC_URL",
            "https://[",
            "SWITCHYARD_PUBLIC_URL must be an absolute HTTPS URL",
        )

    def test_rejects_public_url_credentials(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_PUBLIC_URL",
            "https://user:secret@switchyard.example.test",
            "SWITCHYARD_PUBLIC_URL must be an absolute HTTPS URL",
        )

    def test_rejects_public_url_query(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_PUBLIC_URL",
            "https://switchyard.example.test/?token=secret",
            "SWITCHYARD_PUBLIC_URL must be an absolute HTTPS URL",
        )

    def test_rejects_disabled_reverse_proxy_auth(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_REVERSE_PROXY_AUTH",
            "false",
            "SWITCHYARD_REVERSE_PROXY_AUTH must be true",
        )

    def test_rejects_non_loopback_control_bind(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_BIND_HOST",
            "100.65.164.66",
            "SWITCHYARD_BIND_HOST must be a literal loopback IPv4 address",
        )

    def test_rejects_localhost_control_bind(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_BIND_HOST",
            "localhost",
            "SWITCHYARD_BIND_HOST must be a literal loopback IPv4 address",
        )

    def test_rejects_ipv6_control_bind_not_supported_by_compose_publish_syntax(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_BIND_HOST",
            "::1",
            "SWITCHYARD_BIND_HOST must be a literal loopback IPv4 address",
        )

    def test_rejects_control_model_listener_collision(self) -> None:
        self.env["SWITCHYARD_MODEL_BIND_HOST"] = "127.0.0.1"
        self.assert_rejected(
            "LLAMA_LINE_PORT",
            "18080",
            "control and model listeners must not collide",
        )

    def test_accepts_non_tailscale_model_bind(self) -> None:
        self.env["SWITCHYARD_MODEL_BIND_HOST"] = "192.168.1.20"
        self.assertEqual(validate(self.env), [])

    def test_rejects_wildcard_model_bind(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_MODEL_BIND_HOST",
            "0.0.0.0",
            "SWITCHYARD_MODEL_BIND_HOST must be a literal non-unspecified IPv4 address",
        )

    def test_rejects_malformed_model_bind(self) -> None:
        self.assert_rejected(
            "SWITCHYARD_MODEL_BIND_HOST",
            "model.internal",
            "SWITCHYARD_MODEL_BIND_HOST must be a literal non-unspecified IPv4 address",
        )

    def test_rejects_invalid_listener_ports(self) -> None:
        for name in ("POSTGRES_PORT", "API_PORT", "LLAMA_LINE_PORT", "WEB_PORT"):
            with self.subTest(name=name):
                env = dict(self.env)
                env[name] = "not-a-port"
                self.assertIn(f"{name} must be an integer port", validate(env))

    def test_rejects_all_host_listener_collisions(self) -> None:
        cases = (
            ("postgres", "control", {"POSTGRES_PORT": "18080"}),
            (
                "postgres",
                "model",
                {"SWITCHYARD_MODEL_BIND_HOST": "127.0.0.1", "LLAMA_LINE_PORT": "5432"},
            ),
            (
                "postgres",
                "web",
                {"SWITCHYARD_WEB_BIND_HOST": "127.0.0.1", "WEB_PORT": "5432"},
            ),
            (
                "control",
                "model",
                {"SWITCHYARD_MODEL_BIND_HOST": "127.0.0.1", "LLAMA_LINE_PORT": "18080"},
            ),
            (
                "control",
                "web",
                {"SWITCHYARD_WEB_BIND_HOST": "127.0.0.1", "WEB_PORT": "18080"},
            ),
            (
                "model",
                "web",
                {"SWITCHYARD_MODEL_BIND_HOST": "100.65.164.66", "LLAMA_LINE_PORT": "8081"},
            ),
        )
        for first, second, values in cases:
            with self.subTest(first=first, second=second):
                env = dict(self.env)
                env.update(values)
                self.assertIn(f"{first} and {second} listeners must not collide", validate(env))


class ProductionConfigCLITest(unittest.TestCase):
    def test_parses_dotenv_without_expansion(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".env"
            path.write_text(
                "export PLAIN=$DO_NOT_EXPAND\nSINGLE='$ALSO_LITERAL'\nDOUBLE=\"$STILL_LITERAL\" # comment\n",
                encoding="utf-8",
            )

            self.assertEqual(
                parse_dotenv(path),
                {
                    "PLAIN": "$DO_NOT_EXPAND",
                    "SINGLE": "$ALSO_LITERAL",
                    "DOUBLE": "$STILL_LITERAL",
                },
            )

    def test_validates_env_file_without_outputting_secrets(self) -> None:
        values = {
            "SWITCHYARD_WEB_TRANSPORT": "tailscale",
            "SWITCHYARD_WEB_BIND_HOST": "100.65.164.66",
            "SWITCHYARD_WEB_TRUSTED_PROXY_CIDR": "100.77.128.80/32",
            "SWITCHYARD_DASHBOARD_CIDR": "127.0.0.1/32",
            "SWITCHYARD_PUBLIC_URL": "https://switchyard.example.test",
            "SWITCHYARD_REVERSE_PROXY_AUTH": "true",
            "SWITCHYARD_BIND_HOST": "127.0.0.1",
            "SWITCHYARD_MODEL_BIND_HOST": "100.65.164.66",
            "POSTGRES_PORT": "5432",
            "API_PORT": "18080",
            "LLAMA_LINE_PORT": "11434",
            "WEB_PORT": "8081",
            "SWITCHYARD_CONTROL_API_TOKEN": "never-print-this-secret",
        }
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".env"
            path.write_text(
                "\n".join(f"{name}={value}" for name, value in values.items()),
                encoding="utf-8",
            )
            result = subprocess.run(
                [sys.executable, str(REPO_ROOT / "scripts/validate_production_config.py"), "--env-file", str(path)],
                capture_output=True,
                text=True,
                env={},
                check=False,
            )

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("never-print-this-secret", result.stdout + result.stderr)

    def test_env_file_errors_do_not_output_values(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".env"
            path.write_text('SECRET="never-print-this-secret" trailing\n', encoding="utf-8")
            result = subprocess.run(
                [sys.executable, str(REPO_ROOT / "scripts/validate_production_config.py"), "--env-file", str(path)],
                capture_output=True,
                text=True,
                env={},
                check=False,
            )

        self.assertEqual(result.returncode, 1)
        self.assertNotIn("never-print-this-secret", result.stdout + result.stderr)

    def test_service_validates_env_file_before_start_and_reload(self) -> None:
        unit = (REPO_ROOT / "deploy/switchyard.service").read_text(encoding="utf-8")
        validator = "scripts/validate_production_config.py --env-file __REPO_ROOT__/.env"
        self.assertIn(f"ExecStartPre=/usr/bin/python3 __REPO_ROOT__/{validator}", unit)
        self.assertLess(unit.index("ExecStartPre="), unit.index("ExecStart=/usr/bin/docker"))
        self.assertLess(unit.index("ExecReload=/usr/bin/python3"), unit.index("ExecReload=/usr/bin/docker"))
        self.assertEqual(unit.count(validator), 2)

    def test_smoke_rejects_different_public_url(self) -> None:
        result = subprocess.run(
            ["bash", str(REPO_ROOT / "scripts/smoke-production-contract.sh")],
            capture_output=True,
            text=True,
            env={
                "PATH": os.environ["PATH"],
                "SWITCHYARD_PUBLIC_URL": "https://switchyard.example.test",
                "SWITCHYARD_PRODUCTION_PUBLIC_URL": "https://other.example.test",
            },
            check=False,
        )

        self.assertEqual(result.returncode, 2)
        self.assertIn("must match SWITCHYARD_PUBLIC_URL", result.stderr)

    def test_smoke_requires_validated_public_url(self) -> None:
        result = subprocess.run(
            ["bash", str(REPO_ROOT / "scripts/smoke-production-contract.sh")],
            capture_output=True,
            text=True,
            env={"PATH": os.environ["PATH"]},
            check=False,
        )

        self.assertEqual(result.returncode, 2)
        self.assertIn("Set validated SWITCHYARD_PUBLIC_URL", result.stderr)

    def test_smoke_derives_control_and_model_urls_from_listener_environment(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            directory_path = Path(directory)
            curl_path = directory_path / "curl"
            log_path = directory_path / "urls.log"
            curl_path.write_text(
                """#!/usr/bin/env python3
import os
from pathlib import Path
import sys

args = sys.argv[1:]
url = [arg for arg in args if arg.startswith(("http://", "https://"))][-1]
with Path(os.environ["CURL_LOG"]).open("a", encoding="utf-8") as log:
    log.write(url + "\\n")
if "--dump-header" in args:
    header_path = Path(args[args.index("--dump-header") + 1])
    header_path.write_text("HTTP/1.1 302 Found\\r\\nLocation: https://auth.example.test/\\r\\n\\r\\n", encoding="utf-8")
elif "--write-out" in args:
    print("404", end="")
elif url.endswith("/readyz"):
    print('{"ready":true}')
elif url.endswith("/api/operator-safety"):
    print('{"warnings":[]}')
elif url.endswith("/api/operator-summary"):
    print('{"schema_version":"1.0"}')
elif url.endswith("/health"):
    print('{"ok":true}')
elif url.endswith("/api/tags"):
    print('{"models":[{"name":"test"}]}')
elif url.endswith("/api/generate"):
    print('{"response":"ok"}')
elif url.endswith("/v1/chat/completions"):
    print('{"choices":[{"message":{"content":"ok"}}]}')
else:
    print('{}')
""",
                encoding="utf-8",
            )
            curl_path.chmod(0o755)
            result = subprocess.run(
                ["bash", str(REPO_ROOT / "scripts/smoke-production-contract.sh")],
                capture_output=True,
                text=True,
                env={
                    "PATH": f"{directory}{os.pathsep}{os.environ['PATH']}",
                    "CURL_LOG": str(log_path),
                    "SWITCHYARD_PUBLIC_URL": "https://switchyard.example.test",
                    "SWITCHYARD_CONTROL_API_TOKEN": "smoke-token",
                    "SWITCHYARD_BIND_HOST": "127.0.0.2",
                    "API_PORT": "28081",
                    "SWITCHYARD_MODEL_BIND_HOST": "192.0.2.10",
                    "LLAMA_LINE_PORT": "22434",
                    "SWITCHYARD_SMOKE_MODEL": "test",
                },
                check=False,
            )

            urls = log_path.read_text(encoding="utf-8").splitlines()

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("http://127.0.0.2:28081/readyz", urls)
        self.assertIn("http://192.0.2.10:22434/health", urls)


if __name__ == "__main__":
    unittest.main()
