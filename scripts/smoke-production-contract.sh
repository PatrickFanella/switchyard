#!/usr/bin/env bash
set -euo pipefail

control_url=${SWITCHYARD_PRODUCTION_CONTROL_URL:-http://${SWITCHYARD_BIND_HOST:-127.0.0.1}:${API_PORT:-18080}}
model_url=${SWITCHYARD_PRODUCTION_MODEL_URL:-http://${SWITCHYARD_MODEL_BIND_HOST:-${SWITCHYARD_BIND_HOST:-127.0.0.1}}:${LLAMA_LINE_PORT:-11434}}
public_url=${SWITCHYARD_PUBLIC_URL:-}
token=${SWITCHYARD_CONTROL_API_TOKEN:-}
probe_user=${SWITCHYARD_CONTROL_PROBE_USER:-}
proxy_token=${SWITCHYARD_REVERSE_PROXY_TOKEN:-}
model=${SWITCHYARD_SMOKE_MODEL:-}

if [[ -z "$public_url" ]]; then
  echo "Set validated SWITCHYARD_PUBLIC_URL" >&2
  exit 2
fi
if [[ -n "${SWITCHYARD_PRODUCTION_PUBLIC_URL:-}" && "$SWITCHYARD_PRODUCTION_PUBLIC_URL" != "$public_url" ]]; then
  echo "SWITCHYARD_PRODUCTION_PUBLIC_URL must match SWITCHYARD_PUBLIC_URL" >&2
  exit 2
fi

if [[ -z "$token" && -z "$probe_user" ]]; then
  echo "Set SWITCHYARD_CONTROL_API_TOKEN or SWITCHYARD_CONTROL_PROBE_USER" >&2
  exit 2
fi
if [[ -z "$token" && -z "$proxy_token" ]]; then
  echo "Set SWITCHYARD_REVERSE_PROXY_TOKEN when using SWITCHYARD_CONTROL_PROBE_USER" >&2
  exit 2
fi
auth_args=()
if [[ -n "$token" ]]; then
  auth_args=(-H "Authorization: Bearer $token")
else
  auth_args=(-H "Remote-User: $probe_user" -H "X-Switchyard-Proxy-Token: $proxy_token")
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
  model=$(printf '%s' "$tags" | python3 -c '
import json,sys
models=json.load(sys.stdin).get("models",[])
generative=[m["name"] for m in models if "embed" not in m.get("name","").lower() and not m.get("name","").lower().startswith("bge")]
assert generative
print(generative[0])
')
fi

status=$(curl --silent --output /dev/null --write-out '%{http_code}' "$model_url/api/runs")
[[ "$status" == 404 ]]

curl_json -H 'Content-Type: application/json' \
  -d "{\"model\":\"$model\",\"prompt\":\"Reply with exactly: ok\"}" \
  "$model_url/api/generate" |
  python3 -c 'import json,sys; assert json.load(sys.stdin).get("response","").strip()'

curl_json -H 'Content-Type: application/json' \
  -d "{\"model\":\"ollama/$model\",\"messages\":[{\"role\":\"user\",\"content\":\"Reply with exactly: ok\"}]}" \
  "$model_url/v1/chat/completions" |
  python3 -c 'import json,sys; assert json.load(sys.stdin)["choices"][0]["message"]["content"].strip()'

headers=$(mktemp)
trap 'rm -f "$headers"' EXIT
curl --silent --show-error --head --max-time 20 --output /dev/null --dump-header "$headers" "$public_url"
awk 'NR == 1 { exit !($2 == 302 || $2 == 303 || $2 == 307) }' "$headers"
grep -qi '^location: .*auth\.' "$headers"

if command -v systemctl >/dev/null 2>&1; then
  systemctl is-active --quiet switchyard-web-gate.service
fi
if command -v nft >/dev/null 2>&1; then
  nft_output=""
  if nft_output=$(nft list table inet switchyard_web_gate 2>/dev/null); then
    :
  elif command -v sudo >/dev/null 2>&1 && sudo -n test -s /etc/switchyard/web-gate.nft 2>/dev/null; then
    nft_output=$(sudo -n nft list table inet switchyard_web_gate)
  fi
  if [[ -n "$nft_output" ]]; then
    grep -q 'counter packets' <<<"$nft_output"
    grep -q 'allow Tailscale proxy to web' <<<"$nft_output"
    grep -q 'drop other ingress to web' <<<"$nft_output"
    grep -q 'drop local access to web' <<<"$nft_output"
  fi
fi

if [[ -n "${SWITCHYARD_REMOTE_SMOKE_COMMAND:-}" ]]; then
  bash -lc "$SWITCHYARD_REMOTE_SMOKE_COMMAND"
fi

echo "Switchyard production contract smoke passed"
