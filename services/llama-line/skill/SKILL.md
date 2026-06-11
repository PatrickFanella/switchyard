---
name: llama-line-client
description: >
  Build, integrate, or debug applications that talk to the llama-line ollama broker.
  llama-line is a local HTTP gateway that serialises GPU inference requests into a priority queue,
  streams SSE queue-position status to waiting clients, proxies ollama responses transparently,
  and provides an admin API, Prometheus metrics, web UI, multi-model routing, and request
  deduplication/caching. v0.4.0.
  Use this skill when: writing client code to call llama-line, handling SSE status events,
  implementing auth (Bearer token), using the admin API, checking broker health via /broker/status,
  configuring multi-model routing or per-client caching, or understanding how llama-line differs
  from calling ollama directly.
---

# llama-line Client Skill

llama-line sits in front of ollama at `http://<host>:11434` (default). Clients talk to it exactly
like ollama, with additions: required `Authorization` header, SSE status events prepended to
inference responses, per-client priority, and optional deduplication/caching.

## Quick Reference

| What | Value |
|------|-------|
| Default listen port | `11434` (replaces ollama's port) |
| Auth header | `Authorization: Bearer <key>` |
| Auth-free endpoints | `GET /broker/status`, `GET /metrics`, `GET /ui/` |
| Admin endpoints | `/admin/*` — `X-Admin-Key` header |
| Inference endpoints (queued) | `/api/generate`, `/api/chat`, `/api/embed`, `/v1/chat/completions`, `/v1/completions`, `/v1/embeddings` |
| All other paths | Proxied immediately, no queue |
| Web UI | `http://<host>:11434/ui/` |

## Authentication

Every inference/passthrough request must include:

```http
Authorization: Bearer <key>
```

Missing or invalid key → `401 {"error": "unauthorized"}`.

Keys have a `name` (for logs/status) and a `priority` (higher = dequeues first). Keys are defined
in config or created at runtime via `POST /admin/keys`.

Admin endpoints use:

```http
X-Admin-Key: <admin_key>
```

## Inference Requests — Response Format

All inference responses use `Content-Type: text/event-stream` regardless of `stream:true/false`.
The stream is structured as:

1. **Zero or more broker status events** (heartbeats while queued)
2. **The ollama response** — format depends on `stream`:
   - `stream:false`: one `data: <full JSON>\n\n` event
   - `stream:true`: ollama's SSE chunks pass through as-is → `data: [DONE]\n\n`

### Status event

```text
data: {"request_id":"f4d7b7095d8e255cfa8cb53a80493fe7","position":1,"wait_seconds":5,"status":"queued"}
```

**StatusUpdate fields:**
- `request_id` — correlates all events for a single request; use for logging
- `position` — 1-based queue position; omitted once in-flight
- `wait_seconds` — seconds since the request was enqueued
- `status` — `"queued"` | `"ollama_unavailable"` | `"dropped_by_admin"`

### Error event (terminal — no response follows)

```text
data: {"request_id":"f4d7b7095d8e255cfa8cb53a80493fe7","status":"ollama_unavailable","message":"connection refused"}
```

`status` is always the normalized string — never a raw Go error string. `message` carries the detail.

### Dropped event (terminal — request removed by admin drain/drop)

```text
data: {"request_id":"f4d7b7095d8e255cfa8cb53a80493fe7","status":"dropped_by_admin"}
```

### Parsing strategy

```
for each SSE line:
  if line starts with "data: ":
    payload = parse JSON
    if payload.status == "queued":
      handle/display queue position, continue
    if payload.status in ("ollama_unavailable", "dropped_by_admin"):
      raise error, stop
    else:
      # this is the actual response (non-streaming)
      handle as ollama JSON response, stop
  elif line == "" or line == "data: [DONE]":
    continue  # SSE separator or stream end
```

For **streaming** requests, after stripping broker status events, the remaining `data:` lines are
standard ollama SSE chunks — hand them to any OpenAI-compatible SSE parser.

## Cache Hits

When response caching is enabled for the client key and a cached result exists:

- Request returns immediately (no queue wait)
- Response header: `X-Llama-Line-Cache: HIT`
- Body: same SSE format (`data: <json>\n\n` for non-streaming)

Cache is keyed by SHA256 of `model + messages`. Streaming requests are never cached.

## Error Responses

All broker errors use proper HTTP status codes + `X-Ollama-Broker: true` header:

| HTTP | Body | When |
|------|------|------|
| 401 | `{"error":"unauthorized"}` | Bad/missing API key |
| 403 | `{"error":"forbidden"}` | Bad/missing admin key |
| 503 | `{"error":"queue full","queue_depth":N,"max_depth":N}` | Queue at capacity |
| 504 | `{"error":"request timeout"}` | Per-request timeout exceeded |
| 504 | `{"error":"queue wait timeout"}` | Waited too long in queue |
| 502 | `{"error":"..."}` | Ollama unreachable after retries |

Mid-stream errors (after SSE headers committed) arrive as terminal SSE events with
`status:"ollama_unavailable"` — not as HTTP error codes.

## Broker Status

```http
GET /broker/status
```

No auth required. Returns:

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
  "upstreams": [...],
  "cache_stats": [{"client":"my-app","hits":14,"misses":3,"size":10}]
}
```

Use for health checks, monitoring, and backpressure decisions.

## Admin API

Requires `X-Admin-Key: <admin_key>` or `Authorization: Bearer <admin_key>`.

### Queue endpoints

| Method | Path | Action |
|--------|------|--------|
| `GET` | `/admin/queue` | List queued requests |
| `DELETE` | `/admin/queue` | Drain entire queue |
| `DELETE` | `/admin/queue/{id}` | Drop single request |
| `GET` | `/admin/inflight` | Get current in-flight request |
| `POST` | `/admin/inflight/cancel` | Cancel in-flight request |

### Key management endpoints

| Method | Path | Action |
|--------|------|--------|
| `GET` | `/admin/keys` | List keys (no values exposed) |
| `POST` | `/admin/keys` | Create key — returns value once (201) |
| `DELETE` | `/admin/keys/{name}` | Revoke key |
| `PATCH` | `/admin/keys/{name}` | Update name or priority |

Create key body: `{"name":"client","priority":5}`  
Update key body: `{"name":"new-name","priority":3}` (both fields optional)

## Multi-Model Routing

The broker extracts the `model` field from each request body and routes to the matching upstream.
Matching is case-insensitive glob (`*` wildcard). First match wins. Unmatched → fallback upstream.

Each upstream has an independent queue with its own depth limit. `/broker/status` reports per-upstream state.

## Web UI

`GET /ui/` — embedded single-page dashboard (htmx + vanilla JS, no build step).

Tabs: Queue (SSE live), History (last 200), Keys (create/revoke), Config (admin key → localStorage).

The UI reads the admin key from localStorage key `llama_line_admin_key` for admin actions.

## Client Implementation Patterns

See `references/patterns.md` for:
- Python example (requests + SSE parsing)
- Go example (bufio.Scanner SSE loop)
- JavaScript/fetch example
- OpenAI SDK integration (Go/Python)
- Backpressure: checking `/broker/status` before sending
- Retry logic for 503/504 responses
- Streaming vs non-streaming inference

## Key Differences from Direct ollama

1. **Auth required** — add `Authorization: Bearer <key>` to every request
2. **Always SSE** — inference responses are always `text/event-stream`; broker status events precede the ollama body
3. **Consistent format** — non-streaming responses are wrapped in `data: <json>\n\n`; streaming chunks pass through as-is
4. **Normalized errors** — mid-stream errors use `status:"ollama_unavailable"` + `message`, never raw Go strings
5. **`request_id`** — every status event includes a `request_id` for correlation
6. **Priority queue** — higher-priority API keys dequeue first; equal-priority is FIFO
7. **Queue errors** — may receive 503 (queue full) or 504 (timeout) that ollama never sends
8. **Port** — broker listens on `11434`; ollama moved to `11435` (or wherever configured)
9. **`/broker/status`** — broker-only endpoint, not proxied to ollama
10. **Multi-model routing** — requests routed to different upstream queues by model
11. **Dedup/cache** — per-client in-flight deduplication and LRU response caching
12. **Admin API** — runtime key management, queue drain/drop/cancel
13. **Metrics** — Prometheus at `/metrics`, alerting webhooks
14. **Web UI** — embedded dashboard at `/ui/`
