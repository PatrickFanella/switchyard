# Native Model Broker Cutover Runbook

## Purpose

Operate Switchyard as the llama-line-compatible model ingress on `11434`.

## Live topology

| Component | Owner | Expected endpoint | Notes |
| --- | --- | --- | --- |
| Switchyard compat ingress | Kvant / Switchyard Compose | `http://10.0.0.50:11434` | Replaces the old llama-line process; the production listener is bound to Kvant's LAN address. |
| Switchyard API | NUC / Switchyard Compose | `http://127.0.0.1:28080` or internal `api:8080` | Native control plane. |
| Switchyard frontend | Kvant / Switchyard Compose via Almaz Caddy | `https://switchyard.subcult.tv` | Authelia-protected; Kvant binds web on `10.0.0.50`, and Almaz Caddy proxies to `http://10.0.0.50:28081`. |
| Ollama upstream | NUC host | `http://127.0.0.1:11435` on the host, `http://host.docker.internal:11435` from Compose | Real model server; never point `OLLAMA_BASE_URL` at `11434`. |
| Phoenix | Almaz | `http://10.0.0.200:6006` | OTLP/HTTP traces with `x-project-name=switchyard`. |
| Subcorp clients | `10.0.0.56` containers | `http://10.0.0.50:11434` | `/api/chat` returns plain Ollama JSON; `/v1/chat/completions` supports OpenAI JSON/SSE. |

## Preconditions

- `docker compose ps` shows `switchyard-api-1` running.
- `docker inspect switchyard-api-1 switchyard-web-1 switchyard-postgres-1 switchyard-opencode-1 --format '{{.Name}} {{.HostConfig.RestartPolicy.Name}}'` reports `unless-stopped` so the stack comes back after Docker starts.
- `ss -ltnp 'sport = :11434'` shows Docker publishing `11434`.
- `systemctl show ollama -p Environment` includes `OLLAMA_HOST=0.0.0.0:11435` when Docker must reach host Ollama.
- UFW allows Docker bridge traffic to host Ollama on `11435`.
- `.env` sets `LLAMA_LINE_PORT=11434`, sets `SWITCHYARD_MODEL_BIND_HOST` to the NUC LAN address when remote clients need ingress, and points `OLLAMA_BASE_URL` at the real Ollama upstream, not Switchyard itself.
- Keep `SWITCHYARD_BIND_HOST=127.0.0.1` so API/control and Postgres remain loopback-bound. Set `SWITCHYARD_WEB_BIND_HOST=10.0.0.50` and `SWITCHYARD_WEB_TRUSTED_PROXY_CIDR=10.0.0.200/32`; nginx denies direct web access and accepts only Almaz's exact LAN address. Treat this as a trusted-LAN fallback and migrate to an authenticated encrypted proxy hop when available.

## Smoke tests

Run from the Switchyard host:

```bash
curl -fsS http://10.0.0.50:11434/health
curl -fsS http://10.0.0.50:11434/broker/status
curl -fsS http://10.0.0.50:11434/api/tags
curl -fsS -H 'Content-Type: application/json' \
  -d '{"model":"qwen3.5:latest","prompt":"Reply with exactly: ok"}' \
  http://10.0.0.50:11434/api/generate
curl -fsS -H 'Content-Type: application/json' \
  -d '{"model":"ollama/qwen3.5:latest","messages":[{"role":"user","content":"Reply with exactly: ok"}]}' \
  http://10.0.0.50:11434/v1/chat/completions
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

## Frontend smoke

From any host with DNS access:

```bash
curl -I https://switchyard.subcult.tv
```

Expected before authentication: `302` redirect to `https://auth.subcult.tv/...`. This confirms the route is live and protected by Authelia.

From Almaz, the local Caddy route can be checked without public DNS/TLS:

```bash
curl -I -H 'Host: switchyard.subcult.tv' http://127.0.0.1
```

Expected: `302` redirect to Authelia. The route lives in `/opt/server/management/config/caddy/Caddyfile` and proxies to `10.0.0.50:28081`. Direct access to Kvant's web listener is denied; only Almaz (`10.0.0.200/32`) may reach it.

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
   curl -fsS http://10.0.0.50:11434/broker/status
   ss -ltnp 'sport = :11434'
   ```

4. Record which service owns `11434` before declaring rollback complete.
