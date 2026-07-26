#!/usr/bin/env bash
set -euo pipefail

control_url=${SWITCHYARD_PRODUCTION_CONTROL_URL:-http://127.0.0.1:28080}
model_url=${SWITCHYARD_PRODUCTION_MODEL_URL:-http://127.0.0.1:11434}
public_url=${SWITCHYARD_PRODUCTION_PUBLIC_URL:-https://switchyard.subcult.tv}
token=${SWITCHYARD_CONTROL_API_TOKEN:-}
probe_user=${SWITCHYARD_CONTROL_PROBE_USER:-}
model=${SWITCHYARD_SMOKE_MODEL:-}

if [[ -z "$token" && -z "$probe_user" ]]; then
  echo "Set SWITCHYARD_CONTROL_API_TOKEN or SWITCHYARD_CONTROL_PROBE_USER" >&2
  exit 2
fi
auth_args=()
if [[ -n "$token" ]]; then
  auth_args=(-H "Authorization: Bearer $token")
else
  auth_args=(-H "Remote-User: $probe_user")
fi

curl_json() {
  curl --fail --silent --show-error --max-time 20 "$@"
}

curl_json "$control_url/readyz" | python3 -c 'import json,sys; assert json.load(sys.stdin)["ready"] is True'
curl_json "${auth_args[@]}" "$control_url/api/operator-safety" |
  python3 -c 'import json,sys; data=json.load(sys.stdin); assert "warnings" in data'
curl_json "${auth_args[@]}" "$control_url/api/operator-summary" |
  python3 -c 'import json,sys; data=json.load(sys.stdin); assert data["schema_version"] == "1.0"'

curl_json "$model_url/health" | python3 -c 'import json,sys; assert json.load(sys.stdin)["ok"] is True'
curl_json "$model_url/broker/status" >/dev/null
tags=$(curl_json "$model_url/api/tags")
printf '%s' "$tags" | python3 -c 'import json,sys; assert isinstance(json.load(sys.stdin).get("models"), list)'
if [[ -z "$model" ]]; then
  model=$(printf '%s' "$tags" | python3 -c 'import json,sys; models=json.load(sys.stdin).get("models",[]); assert models; print(models[0]["name"])')
fi

status=$(curl --silent --output /dev/null --write-out '%{http_code}' "$model_url/api/runs")
[[ "$status" == 404 ]]

curl_json -H 'Content-Type: application/json' \
  -d "{\"model\":\"$model\",\"prompt\":\"Reply with exactly: ok\"}" \
  "$model_url/api/generate" |
  python3 -c 'import json,sys; assert json.load(sys.stdin).get("response","").strip().lower() == "ok"'

curl_json -H 'Content-Type: application/json' \
  -d "{\"model\":\"ollama/$model\",\"messages\":[{\"role\":\"user\",\"content\":\"Reply with exactly: ok\"}]}" \
  "$model_url/v1/chat/completions" |
  python3 -c 'import json,sys; assert "ok" in json.load(sys.stdin)["choices"][0]["message"]["content"].lower()'

headers=$(mktemp)
trap 'rm -f "$headers"' EXIT
curl --silent --show-error --head --max-time 20 --output /dev/null --dump-header "$headers" "$public_url"
awk 'NR == 1 { exit !($2 == 302 || $2 == 303 || $2 == 307) }' "$headers"
grep -qi '^location: .*auth\.' "$headers"

if [[ -n "${SWITCHYARD_REMOTE_SMOKE_COMMAND:-}" ]]; then
  bash -lc "$SWITCHYARD_REMOTE_SMOKE_COMMAND"
fi

echo "Switchyard production contract smoke passed"
