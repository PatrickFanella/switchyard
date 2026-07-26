# Production Hardening + Runbook Implementation Plan

> **Historical plan:** The initial cutover runbook and smoke path were delivered. Current production operations live under `docs/runbooks/`.

> **For agentic workers:** Execute this plan task-by-task. Recommended path:
> dispatch a fresh subagent per task, review each result with `review-quality`,
> then continue. For complex multi-agent splits, use
> `parallel-feature-development`, `team-composition-patterns`, and
> `team-communication-protocols`. Steps use checkbox (`- [ ]`) syntax for
> tracking.

**Goal:** Make the live Switchyard llama-line replacement operable, recoverable, and easy to diagnose from documented commands.

**Architecture:** Keep this slice documentation-first with one optional smoke-test script. Capture the verified live topology, cutover state, rollback path, and failure triage steps without changing runtime behavior. Use commands that prove the path from local callers and Subcorp-style remote containers through Switchyard to upstream Ollama.

**Tech Stack:** Markdown runbooks, Bash smoke script, Docker Compose, curl, systemd/UFW operational notes.

---

## Approved Scope

- Document that Switchyard owns the llama-line-compatible port `11434`.
- Document upstream Ollama on `11435`, Phoenix on Almaz, and Subcorp client compatibility.
- Add repeatable smoke tests for health, broker status, tags, generate, and OpenAI-compatible chat.
- Add rollback and incident triage steps.
- Do not change API behavior in this slice.

## File Structure Target

- Create: `docs/runbooks/native-model-broker-cutover.md` — canonical operator runbook.
- Create: `scripts/smoke-model-broker.sh` — local smoke checks with safe defaults.
- Modify: `docs/runbooks/README.md` — link the new runbook.
- Modify: `README.md` — add a short pointer to the runbook, not a duplicate.

## Task 1: Add the Cutover Runbook

**Files:**
- Create: `docs/runbooks/native-model-broker-cutover.md`

- [ ] **Step 1: Write the runbook skeleton**

Create `docs/runbooks/native-model-broker-cutover.md` with:

```markdown
# Native Model Broker Cutover Runbook

## Purpose

Operate Switchyard as the llama-line-compatible model ingress on `11434`.

## Live topology

| Component | Owner | Expected endpoint | Notes |
| --- | --- | --- | --- |
| Switchyard compat ingress | NUC / Switchyard Compose | `http://127.0.0.1:11434` and `http://10.0.0.50:11434` | Replaces old llama-line process. |
| Switchyard API | NUC / Switchyard Compose | `http://127.0.0.1:28080` or internal `api:8080` | Native control plane. |
| Ollama upstream | NUC host | `http://127.0.0.1:11435` host, `http://host.docker.internal:11435` from Compose | Real model server; never point `OLLAMA_BASE_URL` at `11434`. |
| Phoenix | Almaz | `http://10.0.0.200:6006` | OTLP/HTTP traces with `x-project-name=switchyard`. |
| Subcorp clients | 10.0.0.56 containers | `http://10.0.0.50:11434` | `/api/chat` returns plain Ollama JSON; `/v1/chat/completions` supports OpenAI/SSE. |

## Preconditions

- `docker compose ps` shows `switchyard-api-1` running.
- `ss -ltnp 'sport = :11434'` shows Docker publishing `11434`.
- `systemctl show ollama -p Environment` includes `OLLAMA_HOST=0.0.0.0:11435` when Docker must reach host Ollama.
- UFW allows Docker bridge traffic to host Ollama on `11435`.

## Smoke tests

Run from the Switchyard host:

```bash
curl -fsS http://127.0.0.1:11434/health
curl -fsS http://127.0.0.1:11434/broker/status
curl -fsS http://127.0.0.1:11434/api/tags
curl -fsS -H 'Content-Type: application/json' \
  -d '{"model":"qwen3.5:latest","prompt":"Reply with exactly: ok"}' \
  http://127.0.0.1:11434/api/generate
curl -fsS -H 'Content-Type: application/json' \
  -d '{"model":"ollama/qwen3.5:latest","messages":[{"role":"user","content":"Reply with exactly: ok"}]}' \
  http://127.0.0.1:11434/v1/chat/completions
```

Expected: health succeeds, tags list models, generate response contains `"response":"ok"`, chat completion contains `ok`.

## Remote client smoke

From a Subcorp-style container:

```bash
curl -fsS http://10.0.0.50:11434/health
curl -fsS -H 'Content-Type: application/json' \
  -d '{"model":"qwen3.5:latest","messages":[{"role":"user","content":"Reply with exactly: ok"}]}' \
  http://10.0.0.50:11434/api/chat
```

Expected: `/api/chat` returns plain Ollama JSON with `message.content` equal to `ok`.

## Incident triage

1. If `11434` refuses connections, check `docker ps` and port ownership with `ss -ltnp 'sport = :11434'`.
2. If `/health` works but `/api/tags` hangs, check Docker-to-host Ollama reachability and UFW rules for `172.17.0.0/16` and `172.20.0.0/16` to port `11435`.
3. If `/api/generate` fails, check `docker compose logs api --tail=200` and `curl http://127.0.0.1:11435/api/tags` on the host.
4. If Subcorp fails but local checks pass, test from inside the Subcorp container before changing Switchyard.
5. If traces are missing, check `OTEL_EXPORTER_OTLP_ENDPOINT` and Phoenix project header `x-project-name=switchyard`.

## Rollback

1. Stop Switchyard owning `11434` only if necessary: `docker compose stop api`.
2. Restart the old llama-line process only if the binary/config are still intentionally retained.
3. Verify rollback with `curl http://127.0.0.1:11434/broker/status`.
4. Record which service owns `11434` before declaring rollback complete.
```

- [ ] **Step 2: Verify no secrets are included**

Run: `rg -n "TOKEN|PASSWORD|API_KEY|Bearer|secret" docs/runbooks/native-model-broker-cutover.md`

Expected: no secret values; references to auth headers are generic only.

## Task 2: Add a Local Smoke Script

**Files:**
- Create: `scripts/smoke-model-broker.sh`

- [ ] **Step 1: Create the script**

```bash
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
data=json.load(open('/tmp/switchyard-smoke-tags.json'))
print('models=', len(data.get('models', [])))
PY

echo "== generate =="
curl -fsS -H 'Content-Type: application/json' \
  -d "{\"model\":\"$MODEL\",\"prompt\":\"Reply with exactly: ok\"}" \
  "$BASE_URL/api/generate" >/tmp/switchyard-smoke-generate.json
python3 - <<'PY'
import json, sys
data=json.load(open('/tmp/switchyard-smoke-generate.json'))
response=data.get('response','').strip().lower()
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
data=json.load(open('/tmp/switchyard-smoke-chat.json'))
content=data['choices'][0]['message']['content'].strip().lower()
if 'ok' not in content:
    raise SystemExit(f'expected ok in chat completion, got {content!r}')
print('chat=ok')
PY
```

- [ ] **Step 2: Make it executable**

Run: `chmod +x scripts/smoke-model-broker.sh`

- [ ] **Step 3: Run it locally**

Run: `scripts/smoke-model-broker.sh`

Expected: health/status/tags/generate/chat checks all pass.

## Task 3: Link the Runbook

**Files:**
- Modify: `docs/runbooks/README.md`
- Modify: `README.md`

- [ ] **Step 1: Update runbook index**

Add this bullet to `docs/runbooks/README.md`:

```markdown
- [Native Model Broker Cutover](native-model-broker-cutover.md) — operate and troubleshoot Switchyard on the llama-line-compatible `11434` ingress.
```

- [ ] **Step 2: Update README pointer**

Add this sentence near the model-broker Docker notes in `README.md`:

```markdown
For live cutover operation, rollback, and smoke commands, see [`docs/runbooks/native-model-broker-cutover.md`](docs/runbooks/native-model-broker-cutover.md).
```

- [ ] **Step 3: Verify docs and smoke script**

Run: `make verify`

Expected: verification passes; no generated binaries are committed.
