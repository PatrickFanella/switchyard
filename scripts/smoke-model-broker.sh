#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${SWITCHYARD_MODEL_BROKER_URL:-http://127.0.0.1:11434}"
MODEL="${SWITCHYARD_SMOKE_MODEL:-qwen3.5:latest}"

echo "== health =="
curl -fsS "$BASE_URL/health"
echo

echo "== broker status =="
curl -fsS "$BASE_URL/broker/status"
echo

echo "== tags =="
curl -fsS "$BASE_URL/api/tags" >/tmp/switchyard-smoke-tags.json
python3 - <<'PY'
import json

with open('/tmp/switchyard-smoke-tags.json', encoding='utf-8') as handle:
    data = json.load(handle)
print('models=', len(data.get('models', [])))
PY

echo "== generate =="
curl -fsS -H 'Content-Type: application/json' \
  -d "{\"model\":\"$MODEL\",\"prompt\":\"Reply with exactly: ok\"}" \
  "$BASE_URL/api/generate" >/tmp/switchyard-smoke-generate.json
python3 - <<'PY'
import json

with open('/tmp/switchyard-smoke-generate.json', encoding='utf-8') as handle:
    data = json.load(handle)
response = data.get('response', '').strip().lower()
if response != 'ok':
    raise SystemExit(f'expected response ok, got {response!r}')
print('response=ok')
PY

echo "== openai chat compat =="
curl -fsS -H 'Content-Type: application/json' \
  -d "{\"model\":\"ollama/$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"Reply with exactly: ok\"}]}" \
  "$BASE_URL/v1/chat/completions" >/tmp/switchyard-smoke-chat.json
python3 - <<'PY'
import json

with open('/tmp/switchyard-smoke-chat.json', encoding='utf-8') as handle:
    data = json.load(handle)
content = data['choices'][0]['message']['content'].strip().lower()
if 'ok' not in content:
    raise SystemExit(f'expected ok in chat completion, got {content!r}')
print('chat=ok')
PY
