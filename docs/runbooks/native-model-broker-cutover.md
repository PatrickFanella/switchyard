# Native Model Broker Cutover Runbook

## Purpose

Operate Switchyard as the llama-line-compatible model ingress on `11434`.

## Live topology

| Component | Owner | Expected endpoint | Notes |
| --- | --- | --- | --- |
| Switchyard compat ingress | Kvant / Switchyard Compose | `http://10.0.0.50:11434` | Replaces the old llama-line process; the production listener is bound to Kvant's LAN address. |
| Switchyard API | NUC / Switchyard Compose | `http://127.0.0.1:18080` or internal `api:8080` | Native control plane. |
| Switchyard frontend | Kvant / Switchyard Compose via Almaz Caddy | `https://switchyard.subcult.tv` | Authelia-protected; the approved private web hop uses the hosts' Tailscale addresses. |
| Ollama upstream | NUC host | `http://127.0.0.1:11435` on the host, `http://172.31.250.1:11435` from production Compose | Production must use the exact bridge gateway URL. |
| Phoenix | Almaz | `http://10.0.0.200:6006` | OTLP/HTTP traces with `x-project-name=switchyard`. |
| Subcorp clients | `10.0.0.56` containers | `http://10.0.0.50:11434` | `/api/chat` returns plain Ollama JSON; `/v1/chat/completions` supports OpenAI JSON/SSE. |

## Preconditions

- `docker compose ps` shows `switchyard-api-1` running.
- `docker inspect switchyard-api-1 switchyard-web-1 switchyard-postgres-1 switchyard-opencode-1 --format '{{.Name}} {{.HostConfig.RestartPolicy.Name}}'` reports `unless-stopped` so the stack comes back after Docker starts.
- `ss -ltnp 'sport = :11434'` shows Docker publishing `11434`.
- `systemctl show ollama -p Environment` includes `OLLAMA_HOST=0.0.0.0:11435` when Docker must reach host Ollama.
- `.env` pins `SWITCHYARD_API_CONTAINER_IPV4=172.31.250.4`, `SWITCHYARD_WEB_CONTAINER_IPV4=172.31.250.10`, and `OLLAMA_BASE_URL=http://172.31.250.1:11435`.
- The root-owned helper manages only Almaz `/32` to web `.10/32` TCP/80 in `ufw-user-forward` and API `.4/32` to gateway `.1/32` TCP/11435 in `ufw-user-input`. Never add a bridge-subnet allow.
- Keep `SWITCHYARD_BIND_HOST=127.0.0.1` so API/control and Postgres remain loopback-bound. Production sets `SWITCHYARD_WEB_BIND_HOST=100.65.164.66` and `SWITCHYARD_TAILSCALE_PROXY_IPV4=100.77.128.80`. The host nft gate sees Almaz before Docker DNAT. UFW allows only Almaz through `tailscale0` and the pinned bridge to `172.31.250.10:80`. nginx sees the original Almaz address, so set `SWITCHYARD_WEB_TRUSTED_PROXY_CIDR=100.77.128.80/32`. The API still requires the hop token and authenticated user. Keep Tailscale credentials outside Compose and do not fall back to plaintext LAN HTTP.
- Export the deployed environment and run `make production-config-check`. This does not prove the pending central policy or target route required by Gitea issues #18 and #19.

Install or refresh the gate, then reload the main service:

```bash
sudo SWITCHYARD_ENV_FILE=/absolute/path/to/switchyard.env ./deploy/install-web-gate.sh
sudo systemctl reload switchyard.service
```

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

Expected: `302` redirect to Authelia. The route lives in `/opt/server/management/config/caddy/Caddyfile`. Its upstream must use Kvant's Tailscale address. The nft gate admits only Almaz (`100.77.128.80`) before DNAT. UFW admits only Almaz to the pinned web container after DNAT, and nginx admits only Almaz `/32`.

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
