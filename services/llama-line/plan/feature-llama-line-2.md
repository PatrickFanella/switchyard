---
goal: llama-line v2 — Admin API, runtime key management, priority queue, web UI, observability, multi-model routing, and request deduplication
version: 2.0
date_created: 2026-05-22
last_updated: 2026-05-22
owner: PatrickFanella
status: Planned
tags: [feature, architecture, infrastructure, v2]
---

# llama-line v2

## Overview

v1 delivered a strict FIFO queue, SSE status streaming, API key auth, ollama proxy, heartbeat, disconnect handling, and a broker status endpoint.

v2 focuses on operability, intelligence, and extensibility: operators get runtime control over the queue and keys, clients get priority-aware scheduling, and the broker gains observability and multi-backend routing.

---

## Epic 1 — Per-client priority queue (Issue #33)

### Goal
Allow API keys to be assigned a priority tier. Higher-priority clients jump ahead of lower-priority clients in the queue. Same-priority requests remain FIFO.

### Motivation
Background batch jobs (e.g. sentiment scoring, discovery) should not block interactive or time-sensitive requests. A priority queue lets foreground clients get GPU time without requiring separate ollama instances.

### Requirements
- REQ-P01: Each API key in config gets an optional `priority` field (integer, default 0; higher = more urgent)
- REQ-P02: Queue ordering: highest priority first; ties broken by enqueue time (FIFO)
- REQ-P03: `GET /broker/status` includes per-client priority in queue snapshot
- REQ-P04: SSE status events include the client's effective priority
- REQ-P05: Existing clients with no priority field behave identically to v1 (priority 0)

### Tasks
- [ ] Add `Priority int` to `config.APIKey`
- [ ] Refactor `Queue` from slice to priority-heap (`container/heap`)
- [ ] Update `broadcastPositions` to reflect new ordering
- [ ] Update `/broker/status` response with priority info
- [ ] Update config docs and `config.example.yaml`
- [ ] Tests: verify high-priority request jumps ahead of queued low-priority request

---

## Epic 2 — Admin API (Issue #34)

### Goal
Expose a set of authenticated admin endpoints for inspecting and managing the queue at runtime without restarting the broker.

### Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | /admin/queue | Full queue snapshot: all waiting requests with ID, client, priority, wait time, position |
| DELETE | /admin/queue/{request_id} | Drop a specific queued request (404 if not found or in-flight) |
| DELETE | /admin/queue | Drain entire queue (drops all waiting, does not cancel in-flight) |
| GET | /admin/inflight | Current in-flight request details |
| POST | /admin/inflight/cancel | Cancel the current in-flight request and advance the queue |

### Requirements
- REQ-A01: Admin endpoints require a separate `admin_key` in config (distinct from client API keys)
- REQ-A02: Admin key passed as `Authorization: Bearer <admin_key>` or `X-Admin-Key` header
- REQ-A03: All admin actions logged at INFO level with operator identity
- REQ-A04: Dropped requests receive a final SSE event: `{"status":"dropped_by_admin"}`

### Tasks
- [ ] Add `AdminKey string` to config
- [ ] Implement `internal/server/admin.go` with all endpoints
- [ ] Add admin auth middleware (separate from client auth)
- [ ] Expose `Cancel()` on in-flight request from Queue
- [ ] Tests for all admin endpoints
- [ ] Docs: update `docs/api.md` with admin endpoint reference

---

## Epic 3 — Runtime API key management (Issue #35)

### Goal
Allow API keys to be added, revoked, and listed at runtime via the Admin API without restarting the broker or editing the config file.

### Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | /admin/keys | List all active keys (names only, never expose key values) |
| POST | /admin/keys | Create a new key: `{"name": "...", "priority": 0}` — returns generated key value once |
| DELETE | /admin/keys/{name} | Revoke a key by name; in-flight requests from that client complete, queued requests are dropped |
| PATCH | /admin/keys/{name} | Update key metadata (priority, name) |

### Requirements
- REQ-K01: Keys loaded from config file at startup seed the runtime key store
- REQ-K02: Runtime changes held in memory only (restart resets to config)
- REQ-K03: Optional `--persist-keys` flag writes runtime key changes back to config file atomically
- REQ-K04: Revoked keys rejected immediately for new requests; in-flight request from that key completes
- REQ-K05: Key values generated as UUID v4 or configurable-length random hex

### Tasks
- [ ] Implement in-memory key store with RWMutex
- [ ] Seed from config on startup
- [ ] Implement CRUD admin endpoints
- [ ] Update auth middleware to consult key store (not static config slice)
- [ ] Optional: `--persist-keys` flag + atomic config file write
- [ ] Tests
- [ ] Docs

---

## Epic 4 — Web UI (Issue #36)

### Goal
A lightweight embedded web UI served by the broker for real-time queue monitoring and admin operations.

### Features
- **Queue dashboard**: live queue depth, in-flight client + model + elapsed time, waiting clients with position and wait time — updates via SSE
- **Client list**: connected clients, their priority tier, request counts
- **Admin actions**: drop request, drain queue, cancel in-flight — with confirmation dialogs
- **Key management**: list keys, create key (shows value once), revoke key
- **Request history**: last N completed requests with client, model, duration, status (success/error/timeout/dropped)
- **Config viewer**: read-only view of current effective config

### Requirements
- REQ-U01: UI served at `/ui/` from embedded `embed.FS` (no external file dependencies, no build step)
- REQ-U02: Auth: admin key required (same as Admin API)
- REQ-U03: Minimal JS — htmx or vanilla JS + SSE for live updates
- REQ-U04: Responsive, works on mobile
- REQ-U05: Dark mode

### Tasks
- [ ] Design UI layout and component list
- [ ] Implement `internal/ui/` package with `embed.FS`
- [ ] Wire `/ui/` route in server (admin-auth protected)
- [ ] Live queue widget via SSE feed from `/admin/queue/stream`
- [ ] Request history ring buffer in broker (configurable size, default 100)
- [ ] Admin action buttons wired to Admin API endpoints
- [ ] Key management panel
- [ ] Tests: embedded asset serving, SSE stream endpoint

---

## Epic 5 — Observability (Issue #37)

### Goal
Expose Prometheus metrics and structured per-request tracing so operators can monitor broker health, queue pressure, and client behaviour over time.

### Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `llama_line_queue_depth` | gauge | — | Current queue depth |
| `llama_line_requests_total` | counter | client, status | Total requests by outcome |
| `llama_line_queue_wait_seconds` | histogram | client | Time from enqueue to dequeue |
| `llama_line_inference_duration_seconds` | histogram | client | Ollama response time |
| `llama_line_ollama_unavailable_total` | counter | — | Retry events |
| `llama_line_connected_clients` | gauge | — | Current SSE connections |

### Alerting hooks
- Configurable webhook URL: broker POSTs JSON on queue full, ollama unavailable > N retries, request timeout

### Tasks
- [ ] Add `github.com/prometheus/client_golang` dependency
- [ ] Implement `internal/metrics/metrics.go`
- [ ] Instrument queue, dispatcher, and handler
- [ ] Expose `GET /metrics` endpoint
- [ ] Implement webhook notifier for alert conditions
- [ ] Config: `metrics_enabled`, `metrics_path`, `alert_webhook_url`, `alert_queue_full_threshold`
- [ ] Tests
- [ ] Docs

---

## Epic 6 — Multi-model routing (Issue #38)

### Goal
Support multiple upstream ollama instances and route requests to the appropriate instance based on the requested model.

### Requirements
- REQ-M01: Config `upstreams` list replaces single `ollama_url`; each upstream has `url`, `models` (list of model name patterns), `max_queue_depth`
- REQ-M02: Each upstream gets its own FIFO/priority queue
- REQ-M03: Requests routed to the upstream whose `models` list matches the requested model (glob or exact)
- REQ-M04: Fallback upstream for unmatched models
- REQ-M05: `GET /broker/status` returns per-upstream queue state
- REQ-M06: Backward compatible: single `ollama_url` config still works

### Tasks
- [ ] Redesign config: `upstreams` list with per-upstream queue config
- [ ] Implement `internal/router/` — model-to-upstream mapping
- [ ] Per-upstream `Queue` and `Dispatcher` instances
- [ ] Update handler to resolve upstream before enqueue
- [ ] Update `/broker/status` for multi-upstream
- [ ] Backward compat: single `ollama_url` auto-converts to single upstream
- [ ] Tests
- [ ] Docs

---

## Epic 7 — Request deduplication and caching (Issue #39)

### Goal
Optionally deduplicate identical in-flight requests and cache responses for repeated identical prompts.

### Requirements
- REQ-D01: If an identical request (same model + messages hash) is already in-flight or queued, the new request subscribes to the same result channel instead of enqueuing separately
- REQ-D02: Configurable TTL cache keyed on request hash; cache hits bypass the queue entirely
- REQ-D03: Both features opt-in per API key (`deduplicate: true`, `cache_ttl_secs: 60`)
- REQ-D04: Cache is in-memory only; Redis backend planned for later
- REQ-D05: Cache not applied to streaming requests

### Tasks
- [ ] Implement request hasher (model + messages → SHA256)
- [ ] Deduplication: shared result channel for matching in-flight/queued requests
- [ ] Cache: LRU with TTL, configurable max entries
- [ ] Per-key opt-in config
- [ ] `GET /broker/status` includes cache hit rate
- [ ] Tests
- [ ] Docs

---

## Suggested implementation order

1. **Priority queue** (#33) — foundational; Admin API and UI both benefit from it
2. **Admin API** (#34) — enables runtime control; prerequisite for UI
3. **Runtime key management** (#35) — builds on Admin API auth infrastructure
4. **Observability** (#37) — independent; can be parallelised with key management
5. **Web UI** (#36) — depends on Admin API + key management being stable
6. **Multi-model routing** (#38) — architectural change; do after core v2 features are stable
7. **Deduplication & caching** (#39) — optimization; last, after routing is settled

---

## Non-goals for v2

- Persistent request history (database-backed) — in-memory ring buffer only
- Redis cache backend — planned for v3
- OAuth / OIDC authentication — static admin key is sufficient for homelab
- Horizontal scaling / distributed queue — single-host design is intentional
