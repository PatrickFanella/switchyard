# llama-line API Reference

`llama-line` is an HTTP broker/gateway in front of Ollama. v0.4.0.

Module: `git.subcult.tv/PatrickFanella/llama-line`

## Authentication

All endpoints require:

```http
Authorization: Bearer <key>
```

Exceptions:

- `GET /broker/status` — no auth required
- `GET /metrics` — no auth required (when metrics enabled)
- `/admin/*` — uses `X-Admin-Key` (see Admin API)
- `/ui/*` — no auth (admin actions use admin key from localStorage)

Invalid or missing credentials return `401`:

```json
{"error":"unauthorized"}
```

Keys are defined in config as `{name, key, priority}` pairs or managed at runtime via the Admin API.

## Inference Endpoints (Queued)

These endpoints are queued; one runs at a time per upstream:

- `POST /api/generate`
- `POST /api/chat`
- `POST /api/embed`
- `POST /v1/chat/completions`
- `POST /v1/completions`
- `POST /v1/embeddings`

### Common behavior

- Auth required.
- Request is enqueued. Higher-priority API keys dequeue first; equal-priority FIFO.
- Response is always `Content-Type: text/event-stream`.
- While waiting, the client receives SSE status events.
- `request_id` correlates all events for a single request.
- `position` is 1-based while queued; omitted once in-flight.
- `wait_seconds` is seconds since enqueue.
- `status` is `"queued"` while waiting; `"ollama_unavailable"` on error; `"dropped_by_admin"` when drained.
- A heartbeat SSE event is sent every `heartbeat_interval_secs` seconds.
- On success, the Ollama response follows as SSE:
  - **Non-streaming** (`stream:false`): one `data: <json>\n\n` event with the full response.
  - **Streaming** (`stream:true`): Ollama's SSE chunks pass through as-is.
- Client disconnect removes the request from the queue immediately.
- If dedup is enabled for the client key, identical in-flight requests share the result (fan-out).
- If caching is enabled, a cache hit returns immediately with `X-Llama-Line-Cache: HIT`.
- The broker extracts the `model` field from the request body and routes to the matching upstream queue.

### SSE status event

```text
data: {"request_id":"f4d7b7095d8e255cfa8cb53a80493fe7","position":1,"wait_seconds":12,"status":"queued"}
```

### SSE error event (terminal — no response follows)

```text
data: {"request_id":"f4d7b7095d8e255cfa8cb53a80493fe7","status":"ollama_unavailable","message":"connection refused"}
```

`status` is always the normalized value `"ollama_unavailable"`. `message` carries the detail.

### SSE dropped event (terminal — request removed by admin)

```text
data: {"request_id":"f4d7b7095d8e255cfa8cb53a80493fe7","status":"dropped_by_admin"}
```

### Final response (non-streaming)

```text
data: {"id":"chatcmpl-123","object":"chat.completion","choices":[...],"usage":{...}}
```

### Final response (streaming)

```text
data: {"id":"chatcmpl-123","object":"chat.completion.chunk","choices":[{"delta":{"content":"Hi"}}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","choices":[{"delta":{"content":"!"}}]}

data: [DONE]
```

### Pre-response errors

- `503` queue full:

```json
{"error":"queue full","queue_depth":5,"max_depth":10}
```

- `504` request timeout:

```json
{"error":"request timeout"}
```

- `504` queue wait timeout:

```json
{"error":"queue wait timeout"}
```

- `502` Ollama unreachable after retries:

```json
{"error":"<message>"}
```

## Pass-Through Endpoints

All non-inference paths are proxied to the primary Ollama upstream immediately.

- Auth required.
- Response is Ollama's response verbatim.
- Broker adds `X-Ollama-Broker: true` to the response.
- `502` is returned if Ollama is unreachable.

Examples: `GET /api/tags`, `GET /api/version`, `POST /api/pull`, `POST /api/delete`, `GET /api/ps`, `POST /api/show`

## GET /broker/status

- Auth: not required
- Returns broker state across all upstreams

```json
{
  "queue_depth": 2,
  "max_depth": 10,
  "in_flight_client": "my-app",
  "in_flight_id": "f4d7b7...",
  "connected_clients": 3,
  "queue": [
    {"id":"abc","client":"my-app","priority":5,"position":1,"wait_seconds":3}
  ],
  "upstreams": [
    {
      "name": "default",
      "url": "http://127.0.0.1:11435",
      "models": [],
      "queue_depth": 2,
      "max_depth": 10,
      "in_flight_client": "my-app",
      "in_flight_id": "f4d7b7...",
      "queue": [...],
      "healthy": true,
      "last_probe_at": "2026-06-08T12:00:00Z",
      "last_probe_ms": 12,
      "last_probe_error": "",
      "consecutive_failures": 0
    }
  ],
  "cache_stats": [
    {"client":"my-app","hits":14,"misses":3,"size":10}
  ]
}
```

`healthy` is omitted until the first background probe completes. When an upstream probe fails, `healthy` is `false`, `last_probe_error` contains the latest failure summary, and `consecutive_failures` increments until a successful probe resets it to `0`.

## Admin API

All admin endpoints require:

```http
X-Admin-Key: <admin_key>
```

or `Authorization: Bearer <admin_key>`. Returns `403` if `AdminKey` is not configured.

### Queue management

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/admin/queue` | List queued requests |
| `DELETE` | `/admin/queue` | Drain entire queue (sends `dropped_by_admin` to each) |
| `DELETE` | `/admin/queue/{id}` | Drop a single queued request by ID |
| `GET` | `/admin/inflight` | Get the current in-flight request |
| `POST` | `/admin/inflight/cancel` | Cancel the current in-flight request |

### Key management

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/admin/keys` | List all API keys (metadata only, no values) |
| `POST` | `/admin/keys` | Create a new key — returns value once (201) |
| `DELETE` | `/admin/keys/{name}` | Revoke a key |
| `PATCH` | `/admin/keys/{name}` | Update key name or priority |

#### Create key request

```json
{"name": "my-client", "priority": 5}
```

#### Create key response (201)

```json
{"name":"my-client","priority":5,"key":"<generated-uuid>"}
```

The key value is returned once and not stored in plaintext.

#### List keys response

```json
[
  {"name":"augr","priority":10,"deduplicate":true,"cache_ttl_secs":300,"cache_max_entries":200},
  {"name":"default","priority":0,"deduplicate":false,"cache_ttl_secs":0,"cache_max_entries":0}
]
```

#### Update key request

```json
{"name": "new-name", "priority": 3}
```

### Persistence-backed search and stats

These endpoints require `X-Admin-Key` and `database.enabled: true`. Semantic search also requires `embeddings.enabled: true` and pgvector migrations.

#### GET /admin/search/logs

Search persisted structured application logs.

Query params: `q`, `level`, `client`, `request_id`, `since`, `until`, `limit`.

```json
{
  "entries": [
    {"id":1,"ts":"2026-05-24T00:00:00Z","level":"INFO","msg":"request queued","fields":{},"raw_json":"{...}","client":"subcorp"}
  ]
}
```

#### GET /admin/search/requests

Search persisted request/audit events.

Query params: `q`, `client`, `model`, `status`, `path`, `since`, `until`, `errors_only`, `limit`.

```json
{
  "entries": [
    {
      "request_id":"abc",
      "ts":"2026-05-24T00:00:00Z",
      "client":"subcorp",
      "model":"qwen3:14b",
      "path":"/api/chat",
      "status":"success",
      "duration_ms":1200,
      "queue_ms":10,
      "upstream_ms":1190,
      "prompt_tokens":42,
      "completion_tokens":128,
      "tool_error_count":0
    }
  ]
}
```

#### POST /admin/search/semantic

Embed the query with the configured Ollama embedding model and search pgvector chunks.

```json
{"q":"docker permission error","limit":20,"client":"subcorp"}
```

```json
{
  "entries": [
    {"request_id":"abc","ts":"2026-05-24T00:00:00Z","client":"subcorp","model":"qwen3:14b","path":"/api/chat","status":"success","role":"tool_error","content":"permission denied...","score":0.82}
  ]
}
```

#### GET /admin/stats/summary

Return aggregate ops and quality metrics. Query params: `since`, `until` (RFC3339). Defaults to the last hour.

```json
{
  "total_requests":100,
  "success_count":92,
  "error_count":8,
  "cache_hit_count":12,
  "tool_error_count":3,
  "malformed_count":1,
  "p50_ms":850,
  "p95_ms":4200,
  "p99_ms":9000,
  "queue_p95_ms":120,
  "upstream_p95_ms":4000,
  "total_prompt_tokens":42000,
  "total_completion_tokens":128000
}
```

#### POST /admin/feedback

Proxy a human annotation to Phoenix's span annotations API. Requires `X-Admin-Key` or Bearer admin auth and `phoenix.enabled: true`.

```json
{
  "span_id": "abc123",
  "name": "quality",
  "label": "positive",
  "score": 1.0,
  "explanation": "Good answer"
}
```

Success response:

```json
{"ok":true}
```

Returns `503` when Phoenix is not configured and `502` when Phoenix rejects or cannot receive the annotation.

## Observability

### Phoenix OTLP tracing

When `phoenix.enabled: true`, llama-line emits OpenInference-compatible OTLP spans to `phoenix.otlp_endpoint`, usually `http://localhost:6006/v1/traces` or `http://phoenix:6006/v1/traces` on a shared Docker network.

Root LLM spans include:

- `openinference.span.kind = "LLM"`
- `llm.model_name`
- `llm.input_messages.N.message.role/content`
- `llm.output_messages.0.message.role/content`
- `llm.token_count.prompt`, `llm.token_count.completion`, `llm.token_count.total`
- `llama_line.client`, `llama_line.path`, `llama_line.status`

Child spans include `llm.queue_wait`, `llm.upstream_call`, and `tool.call` spans for tool names/errors found in audited request messages.

### GET /metrics

Prometheus metrics endpoint. Requires `metrics_enabled: true` in config. No auth.

Default path: `/metrics` (configurable via `metrics_path`).

Exposed metrics:

| Metric | Type | Description |
|--------|------|-------------|
| `llama_line_queue_depth` | Gauge | Current queue depth |
| `llama_line_queue_wait_seconds` | Histogram | Time spent waiting in queue |
| `llama_line_inference_duration_seconds` | Histogram | Ollama inference duration |
| `llama_line_ollama_unavailable_total` | Counter | Times Ollama was unreachable |
| `llama_line_requests_total` | Counter | Total requests by outcome label |
| `llama_line_connected_clients` | Gauge | Currently connected SSE clients |

### Alerting webhooks

Configure `alert_webhook_url` and `alert_queue_full_threshold` in config to receive POST callbacks:

```json
{"event":"queue_full","message":"queue depth reached alert threshold","timestamp":"2026-05-25T04:12:03Z","detail":{"depth":180,"max_depth":200,"threshold":180}}
{"event":"ollama_unavailable","message":"ollama is not reachable","timestamp":"2026-05-25T04:12:03Z"}
```

## Web UI

`GET /ui/` — embedded dashboard. No auth required (admin actions use the configured admin key from localStorage).

Tabs:
- **Queue** — live SSE queue view with Drop/Drain/Cancel actions
- **History** — last 200 completed requests with status badges
- **Logs** — live structured app logs with full-message toggle
- **Audit** — live audit/request records with formatted body viewer
- **Stats** — DB-backed ops and quality summary
- **Search** — persisted app-log, request, and semantic search
- **Keys** — list/create/revoke runtime API keys
- **Config** — admin key input (stored in `localStorage`)

SSE stream: `GET /ui/api/stream` — emits queue snapshots as `data: <json>\n\n`.
Status JSON: `GET /ui/api/status`
History JSON: `GET /ui/api/history`

## Multi-Model Routing

Requests are routed to the upstream that matches the requested `model` field. Matching is case-insensitive and supports glob patterns (`*`).

Config:

```yaml
upstreams:
  - name: gpu-fast
    url: http://127.0.0.1:11435
    models: ["llama*", "qwen*"]
    max_queue_depth: 10
  - name: gpu-slow
    url: http://127.0.0.1:11436
    models: []   # empty = fallback for unmatched models
    max_queue_depth: 5
```

Each upstream has its own independent queue. `/broker/status` reports per-upstream state.

If no upstream list is configured, behavior is backward-compatible (single upstream from `ollama_url`).

## Request Deduplication and Caching

Configured per API key:

```yaml
api_keys:
  - name: augr
    key: "..."
    deduplicate: true
    cache_ttl_secs: 300
    cache_max_entries: 200
```

Global fallback: `cache_max_entries` in root config.

**Deduplication**: If two identical requests (same model + messages hash) arrive while one is in-flight, the second waits and receives the same result without re-queuing.

**Cache**: Successful non-streaming responses are cached by request hash. Subsequent identical requests return immediately with `X-Llama-Line-Cache: HIT`. Cache is LRU with TTL.

Cache stats are exposed in `/broker/status` under `cache_stats` and in `/metrics`.

Dedup and caching are skipped for streaming requests.

## Response Headers

| Header | Value | Present on |
|--------|-------|------------|
| `X-Ollama-Broker` | `true` | All responses |
| `X-Llama-Line-Cache` | `HIT` | Cache hit responses |

## HTTP Status Codes

| Code | Meaning | When |
|------|---------|------|
| `200` | OK | Successful inference or pass-through |
| `201` | Created | New API key created |
| `401` | Unauthorized | Missing or invalid API key |
| `403` | Forbidden | Missing or invalid admin key |
| `404` | Not Found | Queue item not found |
| `502` | Bad Gateway | Ollama unreachable |
| `503` | Service Unavailable | Queue full |
| `504` | Gateway Timeout | Request or queue wait timeout exceeded |

## Example: Non-Streaming Inference

```bash
curl -N \
  -H "Authorization: Bearer secret-1" \
  -H "Content-Type: application/json" \
  http://localhost:11434/v1/chat/completions \
  -d '{"model":"qwen3.5:latest","messages":[{"role":"user","content":"Say hello"}],"stream":false}'
```

Example output:

```text
data: {"request_id":"f4d7b7095d8e255cfa8cb53a80493fe7","wait_seconds":5,"status":"queued"}

data: {"id":"chatcmpl-123","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"Hello!"}}],"usage":{...}}
```

## Example: Streaming Inference

```bash
curl -N \
  -H "Authorization: Bearer secret-1" \
  -H "Content-Type: application/json" \
  http://localhost:11434/v1/chat/completions \
  -d '{"model":"qwen3.5:latest","messages":[{"role":"user","content":"Say hello"}],"stream":true}'
```

Example output:

```text
data: {"request_id":"f4d7b7095d8e255cfa8cb53a80493fe7","wait_seconds":5,"status":"queued"}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","choices":[{"delta":{"role":"assistant","content":""}}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","choices":[{"delta":{"content":"Hello"}}]}

data: [DONE]
```

## Example: Create API Key at Runtime

```bash
curl -X POST http://localhost:11434/admin/keys \
  -H "X-Admin-Key: my-admin-key" \
  -H "Content-Type: application/json" \
  -d '{"name":"new-client","priority":5}'
```

## Example: Drain Queue

```bash
curl -X DELETE http://localhost:11434/admin/queue \
  -H "X-Admin-Key: my-admin-key"
```

## Example: Check Status

```bash
curl http://localhost:11434/broker/status
```
