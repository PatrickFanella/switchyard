import unittest

from validate_production_config import validate


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
            "API_PORT": "18080",
            "LLAMA_LINE_PORT": "11434",
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
            "SWITCHYARD_DASHBOARD_CIDR must be a valid explicit IPv4 /32",
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
            "SWITCHYARD_BIND_HOST must be loopback",
        )

    def test_rejects_control_model_listener_collision(self) -> None:
        self.env["SWITCHYARD_MODEL_BIND_HOST"] = "127.0.0.1"
        self.assert_rejected(
            "LLAMA_LINE_PORT",
            "18080",
            "control and model listeners must not collide",
        )


if __name__ == "__main__":
    unittest.main()
