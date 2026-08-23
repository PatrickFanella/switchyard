#!/usr/bin/env python3

import base64
import json
import tempfile
import threading
import unittest
from datetime import datetime, timedelta, timezone
from http.client import InvalidURL
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest import mock
from urllib.parse import parse_qs, urlsplit

import alert_lifecycle


REVISION = "a" * 40


class FakeState:
    def __init__(self) -> None:
        self.posts = []
        self.alert_gets = []
        self.ntfy_gets = []
        self.ntfy_authorizations = []
        self.resolved = False
        self.invalid_ntfy = False
        self.fail_first_post = False
        self.fail_resolution_attempts = 0
        self.resolution_attempts = 0
        self.same_ntfy_message_id = False
        self.redirect_url: str | None = None


def handler_for(state: FakeState):
    class Handler(BaseHTTPRequestHandler):
        def do_POST(self) -> None:
            if self.path != "/api/v2/alerts":
                self.send_error(404)
                return
            body = self.rfile.read(int(self.headers["Content-Length"]))
            alerts = json.loads(body)
            state.posts.append({"alerts": alerts, "authorization": self.headers.get("Authorization")})
            resolving = " resolved " in alerts[0]["annotations"]["summary"]
            if resolving:
                state.resolution_attempts += 1
            if state.redirect_url:
                self.send_response(307)
                self.send_header("Location", state.redirect_url)
                self.end_headers()
                return
            if state.fail_first_post and len(state.posts) == 1:
                self.send_error(500)
                return
            if resolving and state.resolution_attempts <= state.fail_resolution_attempts:
                self.send_error(500)
                return
            state.resolved = resolving
            self.send_response(200)
            self.end_headers()

        def do_GET(self) -> None:
            parsed = urlsplit(self.path)
            if parsed.path == "/api/v2/alerts":
                state.alert_gets.append(parse_qs(parsed.query))
                correlation = state.posts[0]["alerts"][0]["labels"]["switchyard_correlation"]
                alerts = [] if state.resolved else [{
                    "labels": {"switchyard_correlation": correlation},
                    "fingerprint": "abc123",
                    "status": {"state": "active"},
                }]
                self._json(json.dumps(alerts))
                return
            if parsed.path == "/switchyard/json":
                state.ntfy_gets.append(parse_qs(parsed.query))
                state.ntfy_authorizations.append(self.headers.get("Authorization"))
                if state.invalid_ntfy:
                    self._json("not-json\n")
                    return
                correlation = state.posts[0]["alerts"][0]["labels"]["switchyard_correlation"]
                status = "resolved" if state.resolved else "firing"
                message_id = "firing-message" if not state.resolved or state.same_ntfy_message_id else "resolved-message"
                self._json(
                    json.dumps({"event": "message", "id": message_id, "message": f"{status} {correlation}"}) + "\n"
                )
                return
            self.send_error(404)

        def _json(self, body: str) -> None:
            encoded = body.encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(encoded)))
            self.end_headers()
            self.wfile.write(encoded)

        def log_message(self, format: str, *_args) -> None:
            pass

    return Handler


class AlertLifecycleTest(unittest.TestCase):
    def setUp(self) -> None:
        self.state = FakeState()
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), handler_for(self.state))
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.tempdir = tempfile.TemporaryDirectory()

    def tearDown(self) -> None:
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()
        self.tempdir.cleanup()

    def env(self) -> dict[str, str]:
        base = f"http://127.0.0.1:{self.server.server_port}"
        return {
            "SWITCHYARD_ALERT_LIFECYCLE_TEST": "1",
            "SWITCHYARD_ALERT_LIFECYCLE_ALLOW_LOCAL_TEST": "1",
            "SWITCHYARD_ALERTMANAGER_URL": base,
            "SWITCHYARD_NTFY_TOPIC_URL": base + "/switchyard",
            "SWITCHYARD_SOURCE_REVISION": REVISION,
            "SWITCHYARD_ALERT_RECEIVER": "on-call",
            "SWITCHYARD_ALERT_LIFECYCLE_TIMEOUT": "2",
            "SWITCHYARD_ALERT_LIFECYCLE_POLL_INTERVAL": "0.1",
            "SWITCHYARD_ALERT_LIFECYCLE_EVIDENCE_PATH": str(Path(self.tempdir.name) / "evidence.json"),
            "SWITCHYARD_ALERTMANAGER_BEARER_TOKEN": "alert-secret",
            "SWITCHYARD_NTFY_BASIC_USERNAME": "ntfy-user",
            "SWITCHYARD_NTFY_BASIC_PASSWORD": "ntfy-secret",
        }

    def test_firing_and_resolved_flow_writes_redacted_evidence(self) -> None:
        config = alert_lifecycle.load_config(self.env())
        assert config is not None
        firing_at = datetime(2026, 8, 22, 12, tzinfo=timezone.utc)
        resolved_at = firing_at + timedelta(minutes=1)
        utcnow = lambda: resolved_at if self.state.posts else firing_at
        alert_lifecycle.Lifecycle(config, utcnow=utcnow).run()

        self.assertEqual(len(self.state.posts), 2)
        firing, resolved = [post["alerts"][0] for post in self.state.posts]
        self.assertEqual(firing["labels"], resolved["labels"])
        correlation = firing["labels"]["switchyard_correlation"]
        self.assertIn(correlation, firing["annotations"]["summary"])
        self.assertIn(correlation, firing["annotations"]["description"])
        self.assertGreater(firing["endsAt"], firing["startsAt"])
        self.assertEqual(resolved["startsAt"], firing["startsAt"])
        self.assertEqual(resolved["endsAt"], resolved_at.isoformat().replace("+00:00", "Z"))
        self.assertGreaterEqual(resolved["endsAt"], resolved["startsAt"])
        self.assertEqual(self.state.posts[0]["authorization"], "Bearer alert-secret")
        self.assertTrue(
            all(
                set(get) == {"poll", "since"} and get["poll"] == ["1"]
                for get in self.state.ntfy_gets
            )
        )
        self.assertTrue(
            all(
                set(get) == {"filter"} and correlation in get["filter"][0]
                for get in self.state.alert_gets
            )
        )
        expected_ntfy_auth = "Basic " + base64.b64encode(b"ntfy-user:ntfy-secret").decode()
        self.assertTrue(all(value == expected_ntfy_auth for value in self.state.ntfy_authorizations))

        evidence_text = config.evidence_path.read_text(encoding="utf-8")
        evidence = json.loads(evidence_text)
        self.assertEqual(evidence["result"], "passed")
        self.assertEqual(evidence["alert"], {"fingerprint": "abc123", "status": "resolved"})
        self.assertEqual(
            evidence["ntfy"],
            {"firing_message_id": "firing-message", "resolved_message_id": "resolved-message"},
        )
        self.assertNotIn("switchyard", evidence["endpoints"]["topic_sha256"])
        for secret in ("alert-secret", "ntfy-secret", "ntfy-user", "/switchyard", "Authorization"):
            self.assertNotIn(secret, evidence_text)

    def test_failure_after_firing_still_posts_resolution(self) -> None:
        self.state.invalid_ntfy = True
        config = alert_lifecycle.load_config(self.env())
        assert config is not None
        with self.assertRaisesRegex(alert_lifecycle.HarnessError, "invalid newline JSON"):
            alert_lifecycle.Lifecycle(config).run()
        self.assertEqual(len(self.state.posts), 2)
        self.assertEqual(
            self.state.posts[1]["alerts"][0]["startsAt"],
            self.state.posts[0]["alerts"][0]["startsAt"],
        )
        evidence = json.loads(config.evidence_path.read_text(encoding="utf-8"))
        self.assertEqual(evidence["result"], "failed")

    def test_ambiguous_firing_post_failure_still_posts_resolution(self) -> None:
        self.state.fail_first_post = True
        config = alert_lifecycle.load_config(self.env())
        assert config is not None
        with self.assertRaisesRegex(alert_lifecycle.HarnessError, "HTTP 500"):
            alert_lifecycle.Lifecycle(config).run()
        self.assertEqual(len(self.state.posts), 2)
        self.assertEqual(
            self.state.posts[1]["alerts"][0]["startsAt"],
            self.state.posts[0]["alerts"][0]["startsAt"],
        )

    def test_failed_resolution_is_retried_once(self) -> None:
        self.state.fail_resolution_attempts = 1
        config = alert_lifecycle.load_config(self.env())
        assert config is not None
        with self.assertRaisesRegex(alert_lifecycle.HarnessError, "HTTP 500"):
            alert_lifecycle.Lifecycle(config).run()
        self.assertEqual(len(self.state.posts), 3)
        firing, failed_resolution, retried_resolution = [post["alerts"][0] for post in self.state.posts]
        self.assertEqual(failed_resolution["startsAt"], firing["startsAt"])
        self.assertEqual(retried_resolution["startsAt"], firing["startsAt"])
        self.assertTrue(self.state.resolved)

    def test_same_ntfy_message_id_cannot_satisfy_firing_and_resolved(self) -> None:
        self.state.same_ntfy_message_id = True
        env = self.env()
        env["SWITCHYARD_ALERT_LIFECYCLE_TIMEOUT"] = "1"
        config = alert_lifecycle.load_config(env)
        assert config is not None

        with self.assertRaisesRegex(alert_lifecycle.HarnessError, "timed out"):
            alert_lifecycle.Lifecycle(config).run()

        evidence = json.loads(config.evidence_path.read_text(encoding="utf-8"))
        self.assertEqual(evidence["result"], "failed")
        self.assertEqual(evidence["ntfy"]["firing_message_id"], "firing-message")
        self.assertIsNone(evidence["ntfy"]["resolved_message_id"])

    def test_cleanup_resolution_retries_are_bounded_and_record_failure(self) -> None:
        self.state.fail_resolution_attempts = 10
        config = alert_lifecycle.load_config(self.env())
        assert config is not None

        with self.assertRaisesRegex(alert_lifecycle.HarnessError, "HTTP 500"):
            alert_lifecycle.Lifecycle(config).run()

        self.assertEqual(self.state.resolution_attempts, 4)
        evidence_text = config.evidence_path.read_text(encoding="utf-8")
        evidence = json.loads(evidence_text)
        self.assertEqual(evidence["result"], "failed")
        self.assertEqual(
            evidence["cleanup"],
            {"attempts": 3, "error": "upstream returned HTTP 500", "status": "failed"},
        )
        self.assertNotIn("alert-secret", evidence_text)

    def test_redirect_does_not_forward_credentials(self) -> None:
        target_state = FakeState()
        target = ThreadingHTTPServer(("127.0.0.1", 0), handler_for(target_state))
        thread = threading.Thread(target=target.serve_forever, daemon=True)
        thread.start()
        self.state.redirect_url = f"http://127.0.0.1:{target.server_port}/api/v2/alerts"
        try:
            config = alert_lifecycle.load_config(self.env())
            assert config is not None
            with self.assertRaisesRegex(alert_lifecycle.HarnessError, "HTTP 307"):
                alert_lifecycle.Lifecycle(config).run()
            self.assertEqual(target_state.posts, [])
        finally:
            target.shutdown()
            target.server_close()
            thread.join()

    def test_network_urls_require_https_without_local_override(self) -> None:
        env = self.env()
        env.pop("SWITCHYARD_ALERT_LIFECYCLE_ALLOW_LOCAL_TEST")
        with self.assertRaisesRegex(alert_lifecycle.HarnessError, "must use HTTPS"):
            alert_lifecycle.load_config(env)
        self.assertEqual(self.state.posts, [])

    def test_disabled_configuration_does_not_require_inputs(self) -> None:
        self.assertIsNone(alert_lifecycle.load_config({}))

    def test_rejects_credentials_in_url_before_network(self) -> None:
        env = self.env()
        env["SWITCHYARD_ALERTMANAGER_URL"] = "https://user:secret@example.test"
        with self.assertRaisesRegex(alert_lifecycle.HarnessError, "must not contain credentials"):
            alert_lifecycle.load_config(env)
        self.assertEqual(self.state.posts, [])

    def test_rejects_malformed_port_before_network(self) -> None:
        env = self.env()
        env["SWITCHYARD_ALERTMANAGER_URL"] = "https://example.test:not-a-port"
        with self.assertRaisesRegex(alert_lifecycle.HarnessError, "invalid port"):
            alert_lifecycle.load_config(env)
        self.assertEqual(self.state.posts, [])

    def test_rejects_control_characters_in_authorization_values(self) -> None:
        cases = (
            {"SWITCHYARD_ALERTMANAGER_BEARER_TOKEN": "secret\r\nX-Evil: yes"},
            {
                "SWITCHYARD_ALERTMANAGER_BEARER_TOKEN": "",
                "SWITCHYARD_ALERTMANAGER_BASIC_USERNAME": "user\nname",
                "SWITCHYARD_ALERTMANAGER_BASIC_PASSWORD": "password",
            },
            {
                "SWITCHYARD_ALERTMANAGER_BEARER_TOKEN": "",
                "SWITCHYARD_ALERTMANAGER_BASIC_USERNAME": "username",
                "SWITCHYARD_ALERTMANAGER_BASIC_PASSWORD": "pass\x7fword",
            },
        )
        for values in cases:
            with self.subTest(values=values):
                env = self.env()
                env.update(values)
                with self.assertRaises(alert_lifecycle.HarnessError) as raised:
                    alert_lifecycle.load_config(env)
                self.assertEqual(
                    str(raised.exception),
                    "SWITCHYARD_ALERTMANAGER credentials contain control characters",
                )
                self.assertIsNone(raised.exception.__cause__)
        self.assertEqual(self.state.posts, [])

    def test_request_construction_errors_are_redacted(self) -> None:
        config = alert_lifecycle.load_config(self.env())
        assert config is not None
        lifecycle = alert_lifecycle.Lifecycle(config)
        with mock.patch.object(alert_lifecycle, "Request", side_effect=ValueError("alert-secret")):
            with self.assertRaises(alert_lifecycle.HarnessError) as raised:
                lifecycle._alertmanager_alerts()
        self.assertEqual(str(raised.exception), "upstream request is invalid")
        self.assertIsNone(raised.exception.__cause__)

    def test_request_send_errors_are_redacted(self) -> None:
        config = alert_lifecycle.load_config(self.env())
        assert config is not None
        lifecycle = alert_lifecycle.Lifecycle(config)
        with mock.patch.object(alert_lifecycle._OPENER, "open", side_effect=InvalidURL("ntfy-secret")):
            with self.assertRaises(alert_lifecycle.HarnessError) as raised:
                lifecycle._alertmanager_alerts()
        self.assertEqual(str(raised.exception), "upstream request is invalid")
        self.assertIsNone(raised.exception.__cause__)

    def test_ntfy_metadata_cannot_satisfy_correlation(self) -> None:
        config = alert_lifecycle.load_config(self.env())
        assert config is not None
        lifecycle = alert_lifecycle.Lifecycle(config)
        message = {
            "topic": f"firing-{lifecycle.correlation}",
            "title": "unrelated",
            "message": "unrelated",
        }
        self.assertFalse(lifecycle._matches_ntfy_message(message, "firing"))

    def test_ntfy_non_message_event_cannot_satisfy_correlation(self) -> None:
        config = alert_lifecycle.load_config(self.env())
        assert config is not None
        lifecycle = alert_lifecycle.Lifecycle(config)
        message = {
            "event": "open",
            "id": "message-id",
            "message": f"firing {lifecycle.correlation}",
        }
        self.assertFalse(lifecycle._matches_ntfy_message(message, "firing"))


if __name__ == "__main__":
    unittest.main()
