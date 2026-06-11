#!/usr/bin/env python3
import json
import sys
import urllib.request

import yaml

cfg = yaml.safe_load(open("config.yaml", "r", encoding="utf-8"))
url = cfg.get("alert_webhook_url")
if not url:
    raise SystemExit("alert_webhook_url is empty")

payload = {
    "event": "route_test",
    "message": "llama-line alert webhook configured",
    "detail": {"safe_test": True},
}
req = urllib.request.Request(
    url,
    data=json.dumps(payload).encode(),
    headers={"Content-Type": "application/json", "User-Agent": "llama-line-alert-test/1.0"},
    method="POST",
)
with urllib.request.urlopen(req, timeout=10) as resp:
    print(resp.status)
    sys.exit(0 if 200 <= resp.status < 300 else 1)
