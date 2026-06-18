# Native Model Broker Cutover Runbook

## Purpose

Operate Switchyard as the llama-line-compatible model ingress on `11434`.

## Live topology

| Component | Owner | Expected endpoint | Notes |
| --- | --- | --- | --- |
| Switchyard compat ingress | NUC / Switchyard Compose | `http://127.0.0.1:11434` and `http://10.0.0.50:11434` | Replaces the old llama-line process. |
| Switchyard API | NUC / Switchyard Compose | `http://127.0.0.1:28080` or internal `api:8080` | Native control plane. |
| Ollama upstream | NUC host | `http://127.0.0.1:11435` on the host, `http://host.docker.internal:11435` from Compose | Real model server; never point `OLLAMA_BASE_URL` at `11434`. |
| Phoenix | Almaz | `http://10.0.0.200:6006` | OTLP/HTTP traces with `x-project-name=switchyard`. |
| Subcorp clients | `10.0.0.56` containers | `http://10.0.0.50:11434` | `/api/chat` returns plain Ollama JSON; `/v1/chat/completions` supports OpenAI JSON/SSE. |

## Preconditions

- `docker compose ps` shows `switchyard-api-1` running.
- `ss -ltnp 'sport = :11434'` shows Docker publishing `11434`.
- `systemctl show ollama -p Environment` includes `OLLAMA_HOST=0.0.0.0:11435` when Docker must reach host Ollama.
- UFW allows Docker bridge traffic to host Ollama on `11435`.
- `.env` sets `LLAMA_LINE_PORT=11434` and points `OLLAMA_BASE_URL` at the real Ollama upstream, not Switchyard itself.

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

Expected:

- `/health` returns `{"ok":true}`.
- `/api/tags` lists upstream Ollama models.
- `/api/generate` contains `"response":"ok"`.
- `/v1/chat/completions` contains an OpenAI-compatible message with `ok`.

You can run the same checks with the repository script:

```bash
scripts/smoke-model-broker.sh
```

## Remote client smoke

Run from a Subcorp-style container:

```bash
curl -fsS http://10.0.0.50:11434/health
curl -fsS -H 'Content-Type: application/json' \
  -d '{"model":"qwen3.5:latest","messages":[{"role":"user","content":"Reply with exactly: ok"}]}' \
  http://10.0.0.50:11434/api/chat
```

Expected: `/api/chat` returns plain Ollama JSON with `message.content` equal to `ok`.

## Incident triage

1. If `11434` refuses connections, check Switchyard container state and port ownership:

   ```bash
   docker ps --format '{{.Names}}\t{{.Status}}\t{{.Ports}}'
   ss -ltnp 'sport = :11434'
   ```

2. If `/health` works but `/api/tags` hangs, check Docker-to-host Ollama reachability and UFW rules for Docker bridge networks to port `11435`:

   ```bash
   curl -fsS http://127.0.0.1:11435/api/tags
   docker exec switchyard-api-1 sh -lc 'wget -qO- http://host.docker.internal:11435/api/tags >/dev/null && echo ok'
   sudo ufw status numbered
   ```

3. If `/api/generate` fails, check API logs and upstream Ollama:

   ```bash
   docker compose logs api --tail=200
   curl -fsS http://127.0.0.1:11435/api/tags
   ```

4. If Subcorp fails but local checks pass, test from inside the Subcorp container before changing Switchyard:

   ```bash
   docker exec subcorp-worker sh -lc 'wget -qO- http://10.0.0.50:11434/health'
   ```

5. If traces are missing, check `OTEL_EXPORTER_OTLP_ENDPOINT` and Phoenix project header `x-project-name=switchyard` in the API container environment.

## Rollback

1. Stop Switchyard owning `11434` only if necessary:

   ```bash
   docker compose stop api
   ```

2. Restart the old llama-line process only if its binary/config are still intentionally retained.
3. Verify rollback:

   ```bash
   curl -fsS http://127.0.0.1:11434/broker/status
   ss -ltnp 'sport = :11434'
   ```

4. Record which service owns `11434` before declaring rollback complete.
