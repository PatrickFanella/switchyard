# llama-line

`llama-line` is a Go HTTP broker/gateway for `ollama`.
Clients connect to `llama-line` instead of talking to `ollama` directly.
It serialises GPU inference through a priority queue: higher-priority requests go first, and equal priorities are FIFO. It streams SSE queue status to waiting clients on the same connection, then forwards the upstream response when dequeued.
Non-inference endpoints are proxied immediately.

## Overview

### Architecture

```text
Client → llama-line (:11434) → ollama (:11435)
```

### Why it exists

- `ollama` GPU inference contention is expensive.
- Strict serialisation avoids concurrent in-flight model work.
- Clients keep one connection open and receive live queue updates.
- Non-inference traffic stays transparent and unqueued.

### Key features

- Priority queue for inference requests: higher priority first, equal priority FIFO
- One in-flight request per upstream; multiple upstreams can run concurrently
- SSE queue-position and elapsed-wait updates while queued
- Immediate proxying for non-inference endpoints
- Disconnect handling for queued and in-flight clients
- Retry with backoff when upstream `ollama` is unavailable
- YAML config with env and CLI overrides
- Static API-key auth

## How It Works

### Web UI

- Local: `http://127.0.0.1:11434/ui/`
- Public: `https://llama-line.subcult.tv/ui/` behind Authelia

### Request lifecycle

1. Authenticate request.
2. Check whether the path is an inference endpoint.
3. If queued and the queue is full, return `503`.
4. Enqueue the request.
5. Stream SSE status updates while waiting.
6. Dequeue when it reaches the front.
7. Forward to upstream `ollama`.
8. Pipe the `ollama` response back to the client.

### Routing

Inference requests are queued:

- `/api/generate`
- `/api/chat`
- `/api/embed`
- `/v1/chat/completions`
- `/v1/completions`
- `/v1/embeddings`

All other routes are proxied immediately without queuing.

### OCQ upstream on almaz

llama-line can route OpenCode provider-prefixed models to OCQ on almaz as a normal upstream.
Client `Authorization` is used only by llama-line; it is not forwarded upstream.
Configure a per-upstream bearer token and send it to OCQ:

```yaml
upstreams:
  - name: ocq-almaz
    url: http://almaz:8088
    health_path: /healthz
    models:
      - "openai/gpt-*"
      - "github-copilot/*"
    auth:
      type: bearer
      token_env: OCQ_GATEWAY_KEY
```

Each upstream can set `health_path` to override the default `/api/version` probe path; use this for non-Ollama-compatible gateways such as custom OpenAI-compatible proxies.
OCQ supports both `/health` and `/healthz`; `/healthz` is recommended for llama-line probes.
Name OCQ upstreams with `ocq` in the upstream name, such as `ocq-almaz`, so llama-line treats them as sessionful and disables dedup/cache for those requests.

OCQ owns OpenCode sessions for this path. Example request:

```json
{"model":"openai/gpt-5.4-mini","messages":[{"role":"user","content":"hi"}],"stream":false,"ocq_session_id":"keep-me"}
```

If OCQ returns `x-ocq-session`, llama-line copies it to the response header when possible.
Under SSE/status flushing this header is best-effort; for non-streaming JSON responses,
llama-line also injects `ocq_session_id` into the body when the upstream header is present.
llama-line sends `X-Llama-Line-Request-ID` upstream for correlation and copies OCQ status headers (`x-ocq-request-id`, `x-ocq-status`, and `x-ocq-streaming-mode`) back to the caller when OCQ provides them.

### SSE status events

While waiting in queue, clients receive SSE `data:` events with queue position and elapsed wait.

```text
data: {"position":1,"wait_seconds":12,"status":"queued"}
```

When `ollama` is temporarily unreachable, waiting clients receive retry status updates until upstream recovers.

### Disconnect handling

- Queued client disconnects: request is dropped from the queue.
- In-flight client disconnects: upstream request is cancelled.

### Upstream retry

If `ollama` is unavailable, the broker retries with exponential backoff up to `ollama_retry_max_secs`.
The front-of-queue request stays active until upstream becomes reachable or the request times out.

## Requirements

- Go 1.21+ to build from source
- `ollama` running locally or reachable over HTTP

## Installation

### Build and install via Make

```bash
make build && make install
```

### Install with `go install`

```bash
go install git.subcult.tv/PatrickFanella/llama-line/cmd/app@latest
```

### Install from source

```bash
git clone https://git.subcult.tv/PatrickFanella/llama-line.git
cd llama-line
make build
sudo cp bin/llama-line /usr/local/bin/llama-line
```

## Configuration

Operational notes live in:

- `docs/app.md` — full app/operator guide, including `add-app`
- `docs/operations/configuration.md` — local config boundaries and sensitive files
- `docs/operations/runtime.md` — systemd/manual runtime
- `docs/operations/backups.md` — Postgres backup/restore
- `docs/operations/phoenix.md` — Phoenix data/auth notes
- `docs/operations/evals.md` — Phoenix dataset/eval workflow

Config loading priority:

```text
CLI flags > env vars > config file > defaults
```

| Field | Type | Env Var | Default | Description |
| --- | --- | --- | --- | --- |
| `listen_addr` | string | `LLAMA_LINE_LISTEN_ADDR` | required | Address to listen on; must be set in config (see `config.example.yaml`: `0.0.0.0:11434`) |
| `ollama_url` | string | `LLAMA_LINE_OLLAMA_URL` | required unless `upstreams` is set | Legacy single-upstream Ollama URL; ignored when `upstreams` is non-empty |
| `max_queue_depth` | int | `LLAMA_LINE_MAX_QUEUE_DEPTH` | `20` | Max queued requests per upstream before `503` |
| `request_timeout_secs` | int | `LLAMA_LINE_REQUEST_TIMEOUT_SECS` | `300` | Per-request timeout in seconds |
| `queue_wait_timeout_secs` | int | `LLAMA_LINE_QUEUE_WAIT_TIMEOUT_SECS` | `600` | Max queue wait in seconds |
| `heartbeat_interval_secs` | int | `LLAMA_LINE_HEARTBEAT_INTERVAL_SECS` | `5` | SSE heartbeat interval in seconds |
| `ollama_retry_max_secs` | int | `LLAMA_LINE_OLLAMA_RETRY_MAX_SECS` | `60` | Max backoff seconds when upstream is unreachable |
| `upstream_health_probe_interval_secs` | int | `LLAMA_LINE_UPSTREAM_HEALTH_PROBE_INTERVAL_SECS` | `15` | Seconds between background upstream health probes |
| `upstream_health_probe_timeout_secs` | int | `LLAMA_LINE_UPSTREAM_HEALTH_PROBE_TIMEOUT_SECS` | `2` | Per-probe timeout in seconds |
| `log_level` | string | `LLAMA_LINE_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error` |
| `audit_log_path` | string | none | empty | JSONL metadata audit log path for non-streaming inference requests |
| `audit_log_include_body` | bool | none | `false` | Include full request/response bodies in audit logs; keep disabled unless debugging locally |
| `database.enabled` | bool | none | `false` | Enable Postgres persistence for request metadata and optional app logs/bodies |
| `database.url` | string | none | empty | Postgres DSN; required when database persistence is enabled |
| `database.persist_app_logs` | bool | none | `false` | Persist structured application logs to Postgres |
| `database.persist_request_bodies` | bool | none | `false` | Persist request/response bodies to Postgres when `body_read_enabled` is also true |
| `database.body_read_enabled` | bool | none | `false` | Second explicit gate for body storage/readback in DB-backed UI/search |
| `embeddings.enabled` | bool | none | `false` | Enable pgvector migrations, async request embedding jobs, and semantic search |
| `embeddings.model` | string | none | `qwen3-embedding:0.6b` | Ollama embedding model used with `/api/embed` |
| `phoenix.enabled` | bool | none | `false` | Emit OpenInference-compatible OTLP traces to Arize Phoenix |
| `phoenix.otlp_endpoint` | string | none | empty | Phoenix OTLP HTTP endpoint, e.g. `http://localhost:6006/v1/traces` |
| `phoenix.project_name` | string | none | `llama-line` | Phoenix project for static trace routing |
| `phoenix.project_mode` | string | none | `static` | `static` uses `project_name`; `client` routes traces to projects named after API key clients |
| `keys` | list | none | required | API key list `[{name, key}]` |

### Example config

```yaml
listen_addr: "0.0.0.0:11434"
ollama_url: "http://127.0.0.1:11435"
max_queue_depth: 20
request_timeout_secs: 300
queue_wait_timeout_secs: 600
heartbeat_interval_secs: 5
ollama_retry_max_secs: 60
upstream_health_probe_interval_secs: 15
upstream_health_probe_timeout_secs: 2
log_level: info
audit_log_path: ""
audit_log_include_body: false
database:
  enabled: false
  url: "postgres://llama:line@127.0.0.1:5432/llama_line?sslmode=disable"
  max_conns: 4
  run_migrations: true
  persist_app_logs: false
  persist_request_bodies: false
  body_read_enabled: false
  retention_days: 30
embeddings:
  enabled: false
  model: qwen3-embedding:0.6b
  dimension: 768
  worker_count: 1
  max_chunk_chars: 4000
phoenix:
  enabled: false
  otlp_endpoint: "http://localhost:6006/v1/traces"
  project_name: "llama-line"
  project_mode: "static"
keys:
  - name: my-app
    key: change-me
  - name: second-app
    key: change-me-too
upstreams:
  - name: ollama
    url: http://127.0.0.1:11435
    # no models — fallback for normal Ollama requests
  - name: ocq-almaz
    url: http://almaz:8088
    models:
      - "openai/gpt-*"
      - "github-copilot/*"
    auth:
      type: bearer
      token_env: OCQ_GATEWAY_KEY
```

### Add an app/key

Interactive helper:

```bash
llama-line add-app --config config.yaml
```

It prompts for app settings, generates a UUID API key, appends it under `keys:`, then asks before running `systemctl restart llama-line`. For config-only updates:

```bash
llama-line add-app --config config.yaml --no-reload
```

Keys created, updated, or revoked from the admin web UI are applied immediately and persisted to the loaded `config.yaml`.

### Auditing Tool Failures

Audit logs redact bodies by default and include metadata such as advertised tool names, body sizes, malformed JSON detection, and tool error summaries found in prior tool messages.

If tool calls fail with `permission denied while trying to connect to the docker API at unix:///var/run/docker.sock`, llama-line is not the failing component. The client-side tool executor is trying to use Docker without socket access. Fix the executor runtime by granting Docker socket access to its service user, configuring rootless Docker, or disabling Docker-backed tool execution.

### Postgres Persistence, Search, and Stats

Database persistence is disabled by default. When enabled, llama-line writes request metadata to Postgres asynchronously and can also persist app logs and full request/response bodies.

Full bodies may contain prompts, secrets, tool outputs, and private model responses. Keep `persist_request_bodies` and `body_read_enabled` disabled unless storage and access controls are acceptable.

Embeddings are optional and require pgvector. Enable `embeddings.enabled` only after the base database path works. Current schema expects `dimension: 768`.

Admin DB endpoints:

- `GET /admin/search/logs` — full-text app-log search
- `GET /admin/search/requests` — full-text request/audit search
- `POST /admin/search/semantic` — pgvector semantic search over request chunks
- `GET /admin/stats/summary` — ops/quality counters, token totals, and latency percentiles

### Phoenix Observability

llama-line can emit OpenInference-compatible OTLP traces to Arize Phoenix while keeping native Postgres stats/search.

Start Phoenix locally or on almaz:

```bash
docker compose -f docker/phoenix.yml up
```

Phoenix UI: `http://localhost:6006`.

Configure llama-line:

```yaml
phoenix:
  enabled: true
  otlp_endpoint: "http://localhost:6006/v1/traces"
```

If Phoenix and llama-line share a Docker network, use `http://phoenix:6006/v1/traces`. If Phoenix runs on almaz and llama-line is outside that network, use `http://almaz:6006/v1/traces`.

Each non-streaming inference request emits a root LLM span with model, client, status, input/output messages, token counts, and child spans for queue wait, upstream call, and tool calls/errors. Token counts are parsed from Ollama `prompt_eval_count` and `eval_count` when present.

Human feedback can be proxied to Phoenix:

```bash
curl -X POST http://localhost:11434/admin/feedback \
  -H "X-Admin-Key: $ADMIN_KEY" \
  -H "Content-Type: application/json" \
  -d '{"span_id":"<phoenix-span-id>","name":"quality","label":"positive","score":1,"explanation":"Good answer"}'
```

Bootstrap managed Phoenix projects, annotation configs, prompt registry entries,
seed datasets, and baseline experiment shells:

```bash
python3 scripts/phoenix_bootstrap.py --base-url http://10.0.0.200:6006
```

If Phoenix auth is enabled, also pass `--api-key <system-key>`.

## API Keys

Define keys in YAML:

```yaml
keys:
  - name: app-a
    key: secret-1
  - name: app-b
    key: secret-2
```

Send one key as:

```http
Authorization: Bearer <key>
```

`GET /broker/status` does not require auth.

## CLI Flags

- `--config <path>` — config file path
- `--log-level <level>` — override log level
- `--version` — print version and exit

## API Reference

See `docs/api.md` for full request/response shapes, SSE event format, and error codes.

## Systemd Deployment

1. Move `ollama` to port `11435` with a systemd drop-in:

```ini
[Service]
Environment=OLLAMA_HOST=127.0.0.1:11435
```

2. Install the `llama-line` binary.
3. Create `/etc/llama-line/config.yaml`.
4. Install and enable `deploy/llama-line.service`.
5. Verify the broker:

```bash
curl http://localhost:11434/broker/status
```

## Upgrading

```bash
make build && make install
sudo systemctl restart llama-line
```

## Development

```bash
make verify
```

`make verify` runs `fmt`, `vet`, and `test`.

```bash
make run
```

`make run` starts the broker with `config.example.yaml`.

```bash
make restart
```

`make restart` builds, installs to `/usr/local/bin/llama-line`, stops any broker recorded in `tmp/llama-line.pid`, and runs the installed binary in the foreground with `config.yaml` so logs stream in your terminal. Override with `RUN_BIN=...`, `RUN_CONFIG=...`, or `PID_FILE=...`.

```bash
go test ./...
```

## License

GPL-3.0-only.
