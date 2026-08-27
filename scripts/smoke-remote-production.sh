#!/usr/bin/env bash
set -euo pipefail

required_vars=(
  SWITCHYARD_PRODUCTION_WEB_URL
  SWITCHYARD_PRODUCTION_CONTROL_URL
  SWITCHYARD_PRODUCTION_MODEL_URL
  SWITCHYARD_PUBLIC_URL
  SWITCHYARD_AUTH_ORIGIN
  SWITCHYARD_EXPECTED_SOURCE_REVISION
  SWITCHYARD_EXPECTED_DEPLOYMENT_ID
  SWITCHYARD_CONTROL_READ_TOKEN
  SWITCHYARD_SMOKE_MODEL
)
for name in "${required_vars[@]}"; do
  if [[ -z "${!name-}" ]]; then
    printf '%s is required\n' "$name" >&2
    exit 2
  fi
done

python3 - "$SWITCHYARD_PRODUCTION_WEB_URL" "$SWITCHYARD_PRODUCTION_CONTROL_URL" "$SWITCHYARD_PRODUCTION_MODEL_URL" "$SWITCHYARD_PUBLIC_URL" "$SWITCHYARD_AUTH_ORIGIN" <<'PY'
import sys
from urllib.parse import urlsplit

for index, value in enumerate(sys.argv[1:], 1):
    try:
        parsed = urlsplit(value)
        valid = (
            parsed.scheme in ("http", "https")
            and bool(parsed.netloc)
            and parsed.hostname is not None
            and parsed.username is None
            and parsed.password is None
            and not parsed.query
            and not parsed.fragment
        )
        if index == 5:
            valid = valid and parsed.scheme == "https" and parsed.path in ("", "/")
        if not valid:
            raise ValueError
        _ = parsed.port
    except ValueError:
        sys.stderr.write("remote production URL configuration is invalid\n")
        sys.exit(2)
PY

if [[ ! "$SWITCHYARD_EXPECTED_SOURCE_REVISION" =~ ^[0-9a-f]{40}$ ]]; then
  echo "SWITCHYARD_EXPECTED_SOURCE_REVISION must be a full lowercase Git SHA" >&2
  exit 2
fi
if [[ ! "$SWITCHYARD_EXPECTED_DEPLOYMENT_ID" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]]; then
  echo "SWITCHYARD_EXPECTED_DEPLOYMENT_ID must be a bounded safe identity" >&2
  exit 2
fi

web_url=${SWITCHYARD_PRODUCTION_WEB_URL%/}
control_url=${SWITCHYARD_PRODUCTION_CONTROL_URL%/}
model_url=${SWITCHYARD_PRODUCTION_MODEL_URL%/}
public_url=${SWITCHYARD_PUBLIC_URL%/}
auth_origin=${SWITCHYARD_AUTH_ORIGIN%/}
model=$SWITCHYARD_SMOKE_MODEL
curl_args=(--disable --silent --show-error --connect-timeout 5 --max-time 20 --max-filesize 1048576)
auth_args=(-H "Authorization: Bearer $SWITCHYARD_CONTROL_READ_TOKEN")

curl_json() {
  curl "${curl_args[@]}" --fail "$@"
}

check_web_root() {
  python3 -c '
import sys

if "<title>Switchyard</title>" not in sys.stdin.read():
    sys.stderr.write("remote production web root did not match Switchyard\n")
    sys.exit(1)
'
}

check_json() {
  python3 -c '
import json
import sys

mode = sys.argv[1]
expected = sys.argv[2:]
try:
    value = json.load(sys.stdin)
    valid = False
    if mode == "health":
        valid = isinstance(value, dict) and value.get("ok") is True
    elif mode == "safety":
        valid = isinstance(value, dict) and "warnings" in value
    elif mode == "summary":
        deployment = value.get("deployment", {}) if isinstance(value, dict) else {}
        valid = (
            value.get("schema_version") == "1.0"
            and deployment.get("source_revision") == expected[0]
            and deployment.get("deployment_id") == expected[1]
        )
    elif mode == "object":
        valid = isinstance(value, dict)
    elif mode == "tags":
        valid = isinstance(value, dict) and expected[0] in [item.get("name") for item in value.get("models", []) if isinstance(item, dict)]
    elif mode == "ollama":
        valid = isinstance(value, dict) and value.get("model") == expected[0] and bool(value.get("response", "").strip())
    elif mode == "openai":
        valid = (
            isinstance(value, dict)
            and value.get("model") == expected[0]
            and bool(value.get("choices", [{}])[0].get("message", {}).get("content", "").strip())
        )
except (AttributeError, IndexError, TypeError, ValueError, json.JSONDecodeError):
    valid = False
if not valid:
    sys.stderr.write("remote production response did not match the required contract\n")
    sys.exit(1)
' "$@"
}

curl_json "$web_url/" | check_web_root
curl_json "$web_url/healthz" | check_json health
curl_json "${auth_args[@]}" "$control_url/api/operator-safety" | check_json safety
curl_json "${auth_args[@]}" "$control_url/api/operator-summary" |
  check_json summary "$SWITCHYARD_EXPECTED_SOURCE_REVISION" "$SWITCHYARD_EXPECTED_DEPLOYMENT_ID"

curl_json "$model_url/health" | check_json health
curl_json "$model_url/broker/status" | check_json object
curl_json "$model_url/api/tags" | check_json tags "$model"

runs_status=$(curl "${curl_args[@]}" --output /dev/null --write-out '%{http_code}' "$model_url/api/runs")
if [[ "$runs_status" != "404" ]]; then
  echo "model listener exposed a control route" >&2
  exit 1
fi

ollama_request=$(python3 -c 'import json,sys; print(json.dumps({"model":sys.argv[1],"prompt":"Reply with exactly: ok"}))' "$model")
curl_json -H 'Content-Type: application/json' -d "$ollama_request" "$model_url/api/generate" |
  check_json ollama "$model"

openai_model="ollama/$model"
openai_request=$(python3 -c 'import json,sys; print(json.dumps({"model":sys.argv[1],"messages":[{"role":"user","content":"Reply with exactly: ok"}]}))' "$openai_model")
curl_json -H 'Content-Type: application/json' -d "$openai_request" "$model_url/v1/chat/completions" |
  check_json openai "$openai_model"

redirect=$(curl "${curl_args[@]}" --head --output /dev/null --write-out $'%{http_code}\n%{redirect_url}' "$public_url")
redirect_status=${redirect%%$'\n'*}
redirect_url=${redirect#*$'\n'}
if [[ "$redirect_status" != "302" && "$redirect_status" != "303" && "$redirect_status" != "307" ]]; then
  echo "public route did not return an authentication redirect" >&2
  exit 1
fi
python3 - "$redirect_url" "$auth_origin" <<'PY'
import sys
from urllib.parse import urlsplit

try:
    redirect = urlsplit(sys.argv[1])
    expected = urlsplit(sys.argv[2])
    valid = (
        redirect.scheme in ("http", "https")
        and redirect.username is None
        and redirect.password is None
        and redirect.scheme == expected.scheme
        and redirect.netloc == expected.netloc
    )
except ValueError:
    valid = False
if not valid:
    sys.stderr.write("public redirect origin did not match SWITCHYARD_AUTH_ORIGIN\n")
    sys.exit(1)
PY

echo "Switchyard remote production smoke passed"
