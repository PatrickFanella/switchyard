# llama-line App Guide
**Version 1.0.0** · llama-line maintainers · 2026-05-26 · App + operator guide

---

## AI READING INSTRUCTION

Read `[SPEC]` and `[BUG]` blocks for authoritative facts.
Read `[NOTE]` only if additional context is needed.
`[?]` blocks are unverified — treat with lower confidence.

---

## 1. Purpose

**[SPEC]**
- `llama-line` is a Go HTTP broker/gateway for Ollama.
- Clients call `llama-line` instead of calling Ollama directly.
- Inference requests are queued to control GPU contention.
- Non-inference Ollama endpoints proxy through immediately.
- App version in binary: `v0.4.0`.

**[NOTE]**
Use `llama-line` when many local tools, agents, or apps share one Ollama GPU and concurrent inference causes slowdowns, failures, or unstable latency.

---

## 2. Core Architecture

**[SPEC]**
```text
client -> llama-line (:11434) -> ollama (:11435)
```

**[SPEC]**
- Binary entrypoint: `cmd/app/main.go`.
- Config package: `internal/config/`.
- HTTP server and handlers: `internal/server/`.
- Queue/dispatcher: `internal/queue/`.
- Upstream proxy: `internal/proxy/`.
- API key store: `internal/keystore/`.
- Metrics/alerts: `internal/metrics/`.
- Persistence/search/stats: `internal/persistence/`.
- Phoenix tracing: `internal/tracer/`.

**[SPEC]**
- One dispatcher runs per configured upstream.
- Requests route to upstreams by exact model or glob pattern.
- Empty upstream model list is fallback.
- Legacy `ollama_url` is used when `upstreams: []`.

---

## 3. Request Behavior

**[SPEC]**
Queued inference endpoints:

| Method | Path |
| --- | --- |
| `POST` | `/api/generate` |
| `POST` | `/api/chat` |
| `POST` | `/api/embed` |
| `POST` | `/v1/chat/completions` |
| `POST` | `/v1/completions` |
| `POST` | `/v1/embeddings` |

**[SPEC]**
- All queued endpoints require `Authorization: Bearer <client-key>`.
- Higher-priority clients dequeue before lower-priority clients.
- Equal-priority requests are FIFO.
- Waiting clients receive SSE queue status on the same HTTP connection.
- Client disconnect drops queued requests and cancels in-flight upstream requests.
- Queue full returns `503`.
- Queue wait timeout returns `504`.
- Request timeout returns `504`.

**[SPEC]**
Pass-through endpoints:
- All non-inference paths proxy immediately to the primary Ollama upstream.
- Pass-through endpoints still require client API key auth.
- Examples: `/api/tags`, `/api/version`, `/api/ps`, `/api/show`, `/api/pull`.

---

## 4. Configuration

**[SPEC]**
Config source priority:

```text
CLI flags > environment variables > config file > defaults
```

**[SPEC]**
Default config path:

```bash
config.yaml
```

**[SPEC]**
Common config fields:

| Field | Purpose |
| --- | --- |
| `listen_addr` | Address where `llama-line` listens. |
| `ollama_url` | Legacy single upstream URL. |
| `upstreams` | Multi-upstream routing config. |
| `keys` | Client API keys and per-client behavior. |
| `admin_key` | Admin API/UI action key. |
| `max_queue_depth` | Queue capacity before `503`. |
| `request_timeout_secs` | Max upstream request duration. |
| `queue_wait_timeout_secs` | Max queue wait duration. |
| `heartbeat_interval_secs` | SSE heartbeat interval. |
| `upstream_health_probe_interval_secs` | Background health probe cadence. |
| `upstream_health_probe_timeout_secs` | Timeout per upstream health probe. |
| `metrics_enabled` | Enables Prometheus endpoint. |
| `database.enabled` | Enables Postgres persistence/search/stats. |
| `embeddings.enabled` | Enables semantic indexing/search. |
| `phoenix.enabled` | Enables OTLP tracing to Phoenix. |

**[SPEC]**
Minimal config:

```yaml
listen_addr: "0.0.0.0:11434"
ollama_url: "http://127.0.0.1:11435"
upstream_health_probe_interval_secs: 15
upstream_health_probe_timeout_secs: 2
admin_key: replace-with-admin-secret
keys:
  - name: my-app
    key: replace-with-client-secret
    priority: 0
```

### Upstream health probes

llama-line probes each configured upstream in the background and caches the result for `/broker/status` and the web UI. The default probe path is `/api/version`, which matches Ollama. For non-Ollama-compatible upstreams, configure `health_path` on that upstream:

```yaml
upstreams:
  - name: ocq-almaz
    url: http://almaz:8088
    health_path: /healthz
```

Tune probe cadence globally with `upstream_health_probe_interval_secs` and `upstream_health_probe_timeout_secs`.

---

## 5. Add an App / Client Key

**[SPEC]**
Interactive command:

```bash
llama-line add-app --config config.yaml
```

**[SPEC]**
The command:
- prompts for app name;
- generates a UUID v4 API key;
- prompts for priority;
- prompts for deduplication;
- prompts for cache TTL seconds;
- prompts for cache max entries;
- appends the new entry under `keys:` in YAML;
- prints the generated key once;
- asks before running the reload command.

**[SPEC]**
Flags:

| Flag | Default | Purpose |
| --- | --- | --- |
| `--config` | `config.yaml` | Config file to update. |
| `--no-reload` | `false` | Edit config only; do not run reload command. |
| `--reload-command` | `systemctl restart llama-line` | Shell command run after config update when confirmed. |

**[SPEC]**
Config-only example:

```bash
llama-line add-app --config /etc/llama-line/config.yaml --no-reload
```

**[SPEC]**
Custom reload example:

```bash
llama-line add-app \
  --config /etc/llama-line/config.yaml \
  --reload-command "sudo systemctl restart llama-line"
```

**[SPEC]**
Generated YAML shape:

```yaml
keys:
  - name: my-new-app
    key: 11111111-2222-4333-8444-555555555555
    priority: 5
    deduplicate: true
    cache_ttl_secs: 60
    cache_max_entries: 100
```

**[BUG] Duplicate app names rejected**
- Symptom: `llama-line add-app` exits with `app "<name>" already exists`.
- Cause: `keys:` already contains an entry with the same `name`.
- Fix: choose a new app name or edit/revoke the existing key intentionally.

**[BUG] Reload command may require sudo**
- Symptom: config updates but reload fails with permission/service error.
- Cause: default reload command is `systemctl restart llama-line`, which may need privileges or may not match non-systemd deployments.
- Fix: rerun with `--reload-command "sudo systemctl restart llama-line"`, use a deployment-specific command, or use `--no-reload` and reload manually.

---

## 6. Admin API and Web UI

**[SPEC]**
- Admin endpoints are mounted under `/admin/*`.
- Admin auth uses `X-Admin-Key: <admin_key>` or `Authorization: Bearer <admin_key>`.
- Admin API returns `403` when `admin_key` is empty.
- Web UI path: `/ui/`.
- Public deployments should put the UI behind external auth when exposed beyond localhost.

**[SPEC]**
Admin capabilities:

| Area | Endpoints |
| --- | --- |
| Queue | list, drain, drop queued request, inspect/cancel in-flight request |
| Keys | list metadata, create key, revoke key, update name/priority |
| Logs | recent/stream app logs and audit logs |
| Search | logs, requests, semantic search when persistence/embeddings enabled |
| Stats | persisted request summary counters and latency stats |
| Feedback | proxy human feedback to Phoenix |

**[NOTE]**
Admin-created keys are active immediately and are persisted to the loaded `config.yaml` so they survive service restarts.

---

## 7. Observability

**[SPEC]**
- Prometheus metrics are exposed at `metrics_path` when `metrics_enabled: true`.
- Alert webhooks can fire for queue-full and upstream-unavailable events.
- JSON app logs are produced by the configured logger.
- Optional audit logs capture request/response metadata.
- Optional Postgres persistence stores request metadata, app logs, and search/stats data.
- Optional Phoenix tracing emits OpenInference-compatible spans.

**[SPEC]**
Phoenix config:

```yaml
phoenix:
  enabled: true
  otlp_endpoint: "http://localhost:6006/v1/traces"
  project_name: "llama-line"
  project_mode: "static"
```

**[SPEC]**
Phoenix `project_mode` values:

| Value | Behavior |
| --- | --- |
| `static` | All traces go to `phoenix.project_name`. |
| `client` | Traces route to projects named after client API key names. |

---

## 8. Runtime Operations

**[SPEC]**
Build:

```bash
make build
```

**[SPEC]**
Install:

```bash
make install
```

**[SPEC]**
Run locally:

```bash
make run
```

**[SPEC]**
Restart local installed binary with repo config:

```bash
make restart
```

**[SPEC]**
Systemd status:

```bash
systemctl status llama-line --no-pager
journalctl -u llama-line -n 100 --no-pager
curl -fsS http://127.0.0.1:11434/broker/status
```

**[SPEC]**
Install/update systemd service from repo helper:

```bash
sudo ./deploy/install-systemd.sh
```

---

## 9. Client Usage

**[SPEC]**
Clients send their app key as a bearer token:

```http
Authorization: Bearer <key>
```

**[SPEC]**
OpenAI-compatible chat example:

```bash
curl http://127.0.0.1:11434/v1/chat/completions \
  -H "Authorization: Bearer $LLAMA_LINE_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"qwen3:14b","messages":[{"role":"user","content":"Say hi"}],"stream":false}'
```

**[SPEC]**
Ollama chat example:

```bash
curl http://127.0.0.1:11434/api/chat \
  -H "Authorization: Bearer $LLAMA_LINE_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"qwen3:14b","messages":[{"role":"user","content":"Say hi"}],"stream":false}'
```

---

## 10. Security Notes

**[SPEC]**
- Keep `config.yaml` uncommitted when it contains real keys.
- Use separate values for `admin_key` and client keys.
- Do not expose admin endpoints without network controls and strong auth.
- Keep request body persistence disabled unless storage/access controls are acceptable.
- Audit/request bodies may contain prompts, secrets, tool outputs, and private model responses.

**[BUG] Unauthorized client requests**
- Symptom: `401 {"error":"unauthorized"}`.
- Cause: missing, malformed, or unknown `Authorization: Bearer <key>` header.
- Fix: use a key from `keys:` or create a durable key with `llama-line add-app`.

**[BUG] Admin API disabled**
- Symptom: `/admin/*` returns `403`.
- Cause: `admin_key` is empty or omitted.
- Fix: set `admin_key` in config and reload/restart the service.

---

## 11. Related Documents

**[SPEC]**
- `README.md` — quick overview and install/config summary.
- `docs/api.md` — complete API reference and response shapes.
- `docs/architecture.md` — package layout and request lifecycle.
- `docs/operations/configuration.md` — local config and sensitive file boundaries.
- `docs/operations/runtime.md` — runtime/start/status flows.
- `docs/operations/backups.md` — Postgres backup/restore.
- `docs/operations/phoenix.md` — Phoenix setup and operations.
- `docs/operations/evals.md` — Phoenix dataset/eval workflow.

---

## 12. Changelog

**[SPEC]**
- `1.0.0` — Initial HADS app guide covering core broker behavior, configuration, add-app, operations, observability, client usage, and security notes.
