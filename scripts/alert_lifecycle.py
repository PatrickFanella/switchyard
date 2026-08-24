#!/usr/bin/env python3

"""Exercise Alertmanager-to-ntfy firing and resolved delivery."""

import base64
import hashlib
import json
import os
import re
import sys
import time
import unicodedata
import uuid
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from http.client import InvalidURL
from pathlib import Path
from typing import Callable, Mapping
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode, urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener


class HarnessError(Exception):
    """A safe-to-print lifecycle failure."""


class _NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


_OPENER = build_opener(_NoRedirect)
CLEANUP_MAX_ATTEMPTS = 3


@dataclass(frozen=True)
class Config:
    alertmanager_url: str
    ntfy_topic_url: str
    revision: str
    receiver: str
    timeout: float
    poll_interval: float
    evidence_path: Path
    alertmanager_authorization: str | None
    ntfy_authorization: str | None


def _authorization(env: Mapping[str, str], prefix: str) -> str | None:
    bearer = env.get(f"{prefix}_BEARER_TOKEN", "")
    username = env.get(f"{prefix}_BASIC_USERNAME", "")
    password = env.get(f"{prefix}_BASIC_PASSWORD", "")
    if any(unicodedata.category(character) == "Cc" for value in (bearer, username, password) for character in value):
        raise HarnessError(f"{prefix} credentials contain control characters")
    if bearer and (username or password):
        raise HarnessError(f"{prefix} credentials are ambiguous")
    if bool(username) != bool(password):
        raise HarnessError(f"{prefix} basic credentials are incomplete")
    if bearer:
        return f"Bearer {bearer}"
    if username:
        encoded = base64.b64encode(f"{username}:{password}".encode()).decode()
        return f"Basic {encoded}"
    return None


def _validated_url(value: str, name: str, allow_local: bool, *, topic: bool) -> str:
    if not value:
        raise HarnessError(f"{name} is required")
    parsed = urlsplit(value)
    try:
        parsed.port
    except ValueError:
        raise HarnessError(f"{name} has an invalid port") from None
    if parsed.username or parsed.password or parsed.query or parsed.fragment:
        raise HarnessError(f"{name} must not contain credentials, query, or fragment")
    local_hosts = {"localhost", "127.0.0.1", "::1"}
    if parsed.scheme != "https":
        if not (allow_local and parsed.scheme == "http" and parsed.hostname in local_hosts):
            raise HarnessError(f"{name} must use HTTPS")
    if not parsed.hostname:
        raise HarnessError(f"{name} must include a hostname")
    if topic and parsed.path.rstrip("/") == "":
        raise HarnessError(f"{name} must include a topic path")
    return value.rstrip("/")


def _bounded_float(env: Mapping[str, str], name: str, default: str, low: float, high: float) -> float:
    try:
        value = float(env.get(name, default))
    except ValueError as exc:
        raise HarnessError(f"{name} must be numeric") from exc
    if not low <= value <= high:
        raise HarnessError(f"{name} must be between {low:g} and {high:g} seconds")
    return value


def load_config(env: Mapping[str, str]) -> Config | None:
    if env.get("SWITCHYARD_ALERT_LIFECYCLE_TEST") != "1":
        return None

    allow_local = env.get("SWITCHYARD_ALERT_LIFECYCLE_ALLOW_LOCAL_TEST") == "1"
    alertmanager_url = _validated_url(
        env.get("SWITCHYARD_ALERTMANAGER_URL", ""),
        "SWITCHYARD_ALERTMANAGER_URL",
        allow_local,
        topic=False,
    )
    ntfy_topic_url = _validated_url(
        env.get("SWITCHYARD_NTFY_TOPIC_URL", ""),
        "SWITCHYARD_NTFY_TOPIC_URL",
        allow_local,
        topic=True,
    )
    revision = env.get("SWITCHYARD_SOURCE_REVISION", "")
    if not re.fullmatch(r"[0-9a-f]{40}", revision):
        raise HarnessError("SWITCHYARD_SOURCE_REVISION must be a full lowercase 40-character Git SHA")
    receiver = env.get("SWITCHYARD_ALERT_RECEIVER", "").strip()
    if not receiver:
        raise HarnessError("SWITCHYARD_ALERT_RECEIVER is required")
    timeout = _bounded_float(env, "SWITCHYARD_ALERT_LIFECYCLE_TIMEOUT", "120", 1, 300)
    poll_interval = _bounded_float(env, "SWITCHYARD_ALERT_LIFECYCLE_POLL_INTERVAL", "2", 0.1, 30)
    if poll_interval > timeout:
        raise HarnessError("SWITCHYARD_ALERT_LIFECYCLE_POLL_INTERVAL must not exceed timeout")

    evidence_value = env.get("SWITCHYARD_ALERT_LIFECYCLE_EVIDENCE_PATH", "")
    if not evidence_value:
        raise HarnessError("SWITCHYARD_ALERT_LIFECYCLE_EVIDENCE_PATH is required")
    evidence_path = Path(evidence_value)
    if not evidence_path.parent.is_dir() or evidence_path.is_dir():
        raise HarnessError("alert lifecycle evidence parent must be an existing directory")

    return Config(
        alertmanager_url=alertmanager_url,
        ntfy_topic_url=ntfy_topic_url,
        revision=revision,
        receiver=receiver,
        timeout=timeout,
        poll_interval=poll_interval,
        evidence_path=evidence_path,
        alertmanager_authorization=_authorization(env, "SWITCHYARD_ALERTMANAGER"),
        ntfy_authorization=_authorization(env, "SWITCHYARD_NTFY"),
    )


def _timestamp(now: datetime) -> str:
    return now.astimezone(timezone.utc).isoformat().replace("+00:00", "Z")


class Lifecycle:
    def __init__(
        self,
        config: Config,
        *,
        clock: Callable[[], float] = time.monotonic,
        sleep: Callable[[float], None] = time.sleep,
        utcnow: Callable[[], datetime] = lambda: datetime.now(timezone.utc),
    ) -> None:
        self.config = config
        self.clock = clock
        self.sleep = sleep
        self.utcnow = utcnow
        self.correlation = uuid.uuid4().hex
        self.deadline = clock() + config.timeout
        self.firing_posted = False
        self.resolution_posted = False
        self.firing_starts_at: datetime | None = None
        self.firing_message_id: str | None = None
        self.labels = {
            "alertname": "SwitchyardAlertLifecycleTest",
            "switchyard_correlation": self.correlation,
            "receiver": config.receiver,
        }
        parsed_am = urlsplit(config.alertmanager_url)
        parsed_ntfy = urlsplit(config.ntfy_topic_url)
        self.evidence = {
            "revision": config.revision,
            "correlation": self.correlation,
            "receiver": config.receiver,
            "endpoints": {
                "alertmanager_hostname": parsed_am.hostname,
                "ntfy_hostname": parsed_ntfy.hostname,
                "topic_sha256": hashlib.sha256(parsed_ntfy.path.encode()).hexdigest(),
            },
            "alert": {"fingerprint": None, "status": "not_started"},
            "ntfy": {"firing_message_id": None, "resolved_message_id": None},
            "timestamps": {"started_at": _timestamp(self.utcnow())},
            "result": "failed",
        }

    def _remaining(self) -> float:
        remaining = self.deadline - self.clock()
        if remaining <= 0:
            raise HarnessError("alert lifecycle timed out")
        return remaining

    def _request(self, request: Request, *, cleanup: bool = False) -> bytes:
        timeout = min(self.config.timeout, 10) if cleanup else min(self._remaining(), 10)
        try:
            with _OPENER.open(request, timeout=timeout) as response:
                return response.read()
        except HTTPError as exc:
            code = exc.code
            exc.close()
            raise HarnessError(f"upstream returned HTTP {code}") from None
        except (ValueError, InvalidURL):
            raise HarnessError("upstream request is invalid") from None
        except (URLError, TimeoutError, OSError):
            raise HarnessError("upstream request failed") from None

    def _build_request(self, url: str, **kwargs) -> Request:
        try:
            return Request(url, **kwargs)
        except (ValueError, InvalidURL):
            raise HarnessError("upstream request is invalid") from None

    def _post_alert(self, status: str, *, cleanup: bool = False) -> None:
        now = self.utcnow()
        firing = status == "firing"
        if firing:
            if self.firing_starts_at is None:
                self.firing_starts_at = now
        elif self.firing_starts_at is None:
            raise HarnessError("cannot resolve an alert that was not fired")
        starts_at = self.firing_starts_at
        alert = {
            "labels": self.labels,
            "annotations": {
                "summary": f"Switchyard alert lifecycle {status} {self.correlation}",
                "description": f"Correlation {self.correlation}; receiver {self.config.receiver}; revision {self.config.revision}",
            },
            "startsAt": _timestamp(starts_at),
            "endsAt": _timestamp(
                starts_at + timedelta(minutes=10)
                if firing
                else max(now - timedelta(seconds=1), starts_at)
            ),
            "generatorURL": "https://switchyard.invalid/alert-lifecycle",
        }
        headers = {"Content-Type": "application/json"}
        if self.config.alertmanager_authorization:
            headers["Authorization"] = self.config.alertmanager_authorization
        request = self._build_request(
            self.config.alertmanager_url + "/api/v2/alerts",
            data=json.dumps([alert], separators=(",", ":")).encode(),
            headers=headers,
            method="POST",
        )
        if firing:
            self.firing_posted = True
        self._request(request, cleanup=cleanup)
        key = "firing_posted_at" if firing else "resolved_posted_at"
        self.evidence["timestamps"][key] = _timestamp(self.utcnow())

    def _alertmanager_alerts(self) -> list[dict]:
        query = urlencode({"filter": f'switchyard_correlation="{self.correlation}"'})
        headers = {}
        if self.config.alertmanager_authorization:
            headers["Authorization"] = self.config.alertmanager_authorization
        raw = self._request(self._build_request(self.config.alertmanager_url + "/api/v2/alerts?" + query, headers=headers))
        try:
            value = json.loads(raw)
        except (json.JSONDecodeError, UnicodeDecodeError):
            raise HarnessError("Alertmanager returned invalid JSON") from None
        if not isinstance(value, list):
            raise HarnessError("Alertmanager returned an invalid alert list")
        return [item for item in value if isinstance(item, dict)]

    def _wait_alertmanager_firing(self) -> None:
        while True:
            for alert in self._alertmanager_alerts():
                labels = alert.get("labels")
                status = alert.get("status")
                if isinstance(labels, dict) and labels.get("switchyard_correlation") == self.correlation:
                    fingerprint = alert.get("fingerprint")
                    if isinstance(fingerprint, str) and re.fullmatch(r"[0-9a-fA-F]+", fingerprint):
                        self.evidence["alert"]["fingerprint"] = fingerprint
                    state = status.get("state") if isinstance(status, dict) else None
                    self.evidence["alert"]["status"] = state if state in {"active", "suppressed", "unprocessed"} else "active"
                    self.evidence["timestamps"]["alertmanager_firing_observed_at"] = _timestamp(self.utcnow())
                    return
            self.sleep(min(self.config.poll_interval, self._remaining()))

    def _ntfy_messages(self, since: int) -> list[dict]:
        query = urlencode({"poll": "1", "since": str(since)})
        headers = {}
        if self.config.ntfy_authorization:
            headers["Authorization"] = self.config.ntfy_authorization
        raw = self._request(self._build_request(self.config.ntfy_topic_url + "/json?" + query, headers=headers))
        messages = []
        try:
            for line in raw.splitlines():
                if line.strip():
                    value = json.loads(line)
                    if isinstance(value, dict):
                        messages.append(value)
        except (json.JSONDecodeError, UnicodeDecodeError):
            raise HarnessError("ntfy returned invalid newline JSON") from None
        return messages

    def _wait_ntfy(self, status: str, since: int) -> None:
        while True:
            for message in self._ntfy_messages(since):
                if self._matches_ntfy_message(message, status):
                    message_id = message["id"].strip()
                    if status == "firing":
                        self.firing_message_id = message_id
                    self.evidence["ntfy"][f"{status}_message_id"] = message_id
                    self.evidence["timestamps"][f"ntfy_{status}_observed_at"] = _timestamp(self.utcnow())
                    return
            self.sleep(min(self.config.poll_interval, self._remaining()))

    def _matches_ntfy_message(self, message: dict, status: str) -> bool:
        message_id = message.get("id")
        if message.get("event") != "message" or not isinstance(message_id, str) or not message_id.strip():
            return False
        if status == "resolved" and (self.firing_message_id is None or message_id.strip() == self.firing_message_id):
            return False
        values = (message.get("title"), message.get("message"))
        text = " ".join(value for value in values if isinstance(value, str)).lower()
        return self.correlation in text and status in text

    def _observe_resolved(self) -> None:
        while True:
            matching = []
            for alert in self._alertmanager_alerts():
                labels = alert.get("labels")
                if isinstance(labels, dict) and labels.get("switchyard_correlation") == self.correlation:
                    matching.append(alert)
            if not matching:
                self.evidence["alert"]["status"] = "resolved"
                self.evidence["timestamps"]["alertmanager_resolved_observed_at"] = _timestamp(self.utcnow())
                return
            self.sleep(min(self.config.poll_interval, self._remaining()))

    def _resolve(self, *, cleanup: bool = False) -> None:
        self._post_alert("resolved", cleanup=cleanup)
        self.resolution_posted = True

    def _cleanup_resolution(self) -> None:
        for attempt in range(1, CLEANUP_MAX_ATTEMPTS + 1):
            try:
                self._resolve(cleanup=True)
            except HarnessError as exc:
                if attempt == CLEANUP_MAX_ATTEMPTS:
                    self.evidence["cleanup"] = {
                        "attempts": attempt,
                        "error": str(exc),
                        "status": "failed",
                    }
            else:
                self.evidence["cleanup"] = {"attempts": attempt, "status": "succeeded"}
                return

    def run(self) -> None:
        since = int(self.utcnow().timestamp()) - 1
        failure: HarnessError | None = None
        self._write_evidence()
        try:
            self._post_alert("firing")
            self._wait_alertmanager_firing()
            self._wait_ntfy("firing", since)
            self._resolve()
            self._observe_resolved()
            self._wait_ntfy("resolved", since)
            self.evidence["result"] = "passed"
        except HarnessError as exc:
            failure = exc
        finally:
            if self.firing_posted and not self.resolution_posted:
                self._cleanup_resolution()
            self.evidence["timestamps"]["finished_at"] = _timestamp(self.utcnow())
            self._write_evidence()
        if failure:
            raise failure

    def _write_evidence(self) -> None:
        temporary = self.config.evidence_path.with_name(self.config.evidence_path.name + ".tmp")
        temporary.write_text(json.dumps(self.evidence, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        temporary.replace(self.config.evidence_path)


def main() -> int:
    try:
        config = load_config(os.environ)
        if config is None:
            print("alert lifecycle disabled")
            return 0
        Lifecycle(config).run()
    except HarnessError as exc:
        print(f"alert lifecycle failed: {exc}", file=sys.stderr)
        return 1
    except OSError:
        print("alert lifecycle failed: could not write evidence", file=sys.stderr)
        return 1
    print("alert lifecycle passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
