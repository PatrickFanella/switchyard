---
goal: Build llama-line — an ollama broker/gateway that serialises inference requests into a strict FIFO queue, streams SSE status updates to waiting clients, and proxies responses transparently
version: 1.0
date_created: 2026-05-22
last_updated: 2026-05-22
owner: PatrickFanella
status: 'Planned'
tags: [feature, architecture, infrastructure]
---

# Introduction

![Status: Planned](https://img.shields.io/badge/status-Planned-blue)

`llama-line` is a Go service that runs on the same machine as ollama and acts as a transparent broker/gateway. Clients (speaking native ollama API or OpenAI-compatible API) connect to llama-line instead of ollama directly. The broker serialises all GPU inference requests into a strict FIFO queue (one in-flight at a time), streams queue-position and elapsed-wait status updates to waiting clients via SSE on their existing connection, then pipes the ollama response back when the request is dequeued. Non-inference endpoints are proxied immediately without queuing.

## 1. Requirements & Constraints

- **REQ-001**: The broker MUST accept both native ollama API requests and OpenAI-compatible API requests on the same listener
- **REQ-002**: Exactly one inference request MUST be in-flight with ollama at any time (strict serial queue)
- **REQ-003**: Queued clients MUST receive SSE status updates on their open connection containing `{"position": N, "wait_seconds": N}` while waiting
- **REQ-004**: SSE status updates MUST be sent on every queue change event AND on a configurable heartbeat interval
- **REQ-005**: When a request is dequeued, the broker MUST pipe the ollama response stream directly to the client on the same connection
- **REQ-006**: All requests MUST include a valid API key (`Authorization: Bearer <key>`) or be rejected with HTTP 401
- **REQ-007**: API keys MUST be defined as a static list of `{name, key}` pairs in the config file
- **REQ-008**: The broker MUST enforce a configurable maximum queue depth; requests arriving when full MUST be rejected with HTTP 503
- **REQ-009**: Error responses MUST match ollama's `{"error": "..."}` format, extended with `queue_depth` and `max_depth` fields where applicable
- **REQ-010**: When a client disconnects while queued, the request MUST be dropped from the queue immediately
- **REQ-011**: When a client disconnects while in-flight, the upstream ollama request MUST be cancelled immediately
- **REQ-012**: When ollama is unreachable, the broker MUST hold the front-of-queue request, retry with exponential backoff, and send `{"status": "ollama_unavailable", "position": 1}` SSE updates to all waiting clients
- **REQ-013**: A configurable per-request timeout MUST cancel the in-flight ollama request and advance the queue if exceeded
- **REQ-014**: A configurable max queue wait timeout MUST drop a queued request with a timeout error if exceeded
- **REQ-015**: `GET /broker/status` MUST return current queue depth, in-flight request client identity, and connected client count
- **REQ-016**: All responses MUST include the header `X-Ollama-Broker: true`
- **REQ-017**: The broker MUST emit structured JSON logs with a configurable log level (debug/info/warn/error)
- **REQ-018**: The broker MUST be configurable via YAML config file, with env var and CLI flag overrides (priority: CLI > env > config > defaults)
- **CON-001**: Written in Go, single static binary, no runtime dependencies
- **CON-002**: Deployed as a binary with a sample systemd unit file; no Docker required
- **CON-003**: The broker listen port and upstream ollama URL are fully configurable with no hardcoded defaults
- **CON-004**: Module path: `git.subcult.tv/PatrickFanella/llama-line`
- **GUD-001**: Non-inference endpoints (`/api/tags`, `/api/pull`, `/api/delete`, `/api/ps`, `/api/show`, `/api/version`, etc.) MUST be proxied immediately without queuing
- **GUD-002**: `POST /api/pull` MUST bypass the queue even though it is a POST request
- **GUD-003**: Queue ordering is strict FIFO for v1; priority queue is planned for v2
- **GUD-004**: Model loading time is transparent to the broker — in-flight is in-flight
- **PAT-001**: Follow existing template structure: `cmd/app/main.go` as entrypoint, `internal/` for all packages

## 2. Implementation Steps

### Implementation Phase 1 — Project Bootstrap

- GOAL-001: Rename the Go template to llama-line, establish package structure, wire config loading

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-001 | Update `go.mod` module path from `git.subcult.tv/subculture-collective/project-template-go` to `git.subcult.tv/PatrickFanella/llama-line` | | |
| TASK-002 | Rename `cmd/app/main.go` entrypoint: replace template boilerplate with CLI flag parsing using `flag` stdlib. Flags: `--config <path>`, `--log-level <level>`. Print version and exit on `--version` | | |
| TASK-003 | Create `internal/config/config.go`: define `Config` struct with fields: `ListenAddr string`, `OllamaURL string`, `MaxQueueDepth int`, `RequestTimeoutSecs int`, `QueueWaitTimeoutSecs int`, `HeartbeatIntervalSecs int`, `OllamaRetryMaxSecs int`, `LogLevel string`, `Keys []APIKey` where `APIKey` is `{Name string, Key string}` | | |
| TASK-004 | Implement `internal/config/load.go`: load YAML config file via `gopkg.in/yaml.v3`, then overlay env vars (`LLAMA_LINE_LISTEN_ADDR`, `LLAMA_LINE_OLLAMA_URL`, `LLAMA_LINE_MAX_QUEUE_DEPTH`, `LLAMA_LINE_REQUEST_TIMEOUT_SECS`, `LLAMA_LINE_QUEUE_WAIT_TIMEOUT_SECS`, `LLAMA_LINE_HEARTBEAT_INTERVAL_SECS`, `LLAMA_LINE_LOG_LEVEL`), then overlay CLI flags | | |
| TASK-005 | Create `config.example.yaml` at repo root with all fields documented and example API keys | | |
| TASK-006 | Create `internal/logger/logger.go`: structured JSON logger wrapping `log/slog` with configurable level. Export `New(level string) *slog.Logger` | | |
| TASK-007 | Run `go mod tidy` to pull `gopkg.in/yaml.v3` dependency | | |

### Implementation Phase 2 — Queue Engine

- GOAL-002: Implement the core FIFO queue with concurrency-safe operations, timeout enforcement, and disconnect handling

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-008 | Create `internal/queue/queue.go`: define `Request` struct with fields: `ID string` (UUID), `ClientName string`, `EnqueuedAt time.Time`, `Ctx context.Context`, `Cancel context.CancelFunc`, `ResponseCh chan Result`, `StatusCh chan StatusUpdate`. Define `StatusUpdate` struct: `Position int`, `WaitSeconds int`, `Status string`. Define `Result` struct: `Resp *http.Response`, `Err error` | | |
| TASK-009 | Implement `Queue` struct in `internal/queue/queue.go`: fields `mu sync.Mutex`, `items []*Request`, `maxDepth int`, `inFlight *Request`. Methods: `Enqueue(r *Request) error` (returns error if full), `Dequeue() *Request`, `Drop(id string)`, `Depth() int`, `InFlight() *Request`, `Snapshot() QueueSnapshot` where `QueueSnapshot` contains depth, in-flight client name, and connected count | | |
| TASK-010 | Implement `internal/queue/dispatcher.go`: `Dispatcher` struct with a `Run(ctx context.Context)` goroutine that blocks on a channel, dequeues the next request, calls the ollama forwarder, and signals completion. Uses `notify chan struct{}` to wake on enqueue | | |
| TASK-011 | Implement queue wait timeout in `Dispatcher.Run`: for each queued request, check `time.Since(r.EnqueuedAt)` against `Config.QueueWaitTimeoutSecs`; if exceeded, send timeout error to `r.ResponseCh` and drop the request before forwarding | | |
| TASK-012 | Implement per-request timeout in `Dispatcher.Run`: wrap the ollama HTTP call with a `context.WithTimeout` derived from `r.Ctx` using `Config.RequestTimeoutSecs`; on timeout, cancel upstream and send error to `r.ResponseCh` | | |

### Implementation Phase 3 — Ollama Proxy

- GOAL-003: Implement the HTTP forwarder that proxies requests to ollama, handles streaming, and retries on unavailability

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-013 | Create `internal/proxy/forwarder.go`: `Forwarder` struct with field `OllamaURL string`. Method `Forward(ctx context.Context, r *http.Request) (*http.Response, error)` that clones the incoming request, sets the upstream URL, copies all headers except `Authorization`, and executes via `http.Client` with no response buffering | | |
| TASK-014 | Implement ollama unavailability retry in `Forwarder.Forward`: on connection error, retry with exponential backoff starting at 1s, doubling up to `Config.OllamaRetryMaxSecs`; send `StatusUpdate{Status: "ollama_unavailable"}` to all waiting clients' `StatusCh` on each retry attempt | | |
| TASK-015 | Implement streaming pipe in `internal/proxy/pipe.go`: `PipeResponse(w http.ResponseWriter, resp *http.Response)` copies all response headers from ollama to the client, sets `X-Ollama-Broker: true`, then `io.Copy`s the response body. Flush after each write if `w` implements `http.Flusher` | | |

### Implementation Phase 4 — HTTP Server & Routing

- GOAL-004: Implement the HTTP listener, request routing, auth middleware, SSE status streaming, and broker status endpoint

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-016 | Create `internal/server/server.go`: `Server` struct wiring `Config`, `Queue`, `Dispatcher`, `Forwarder`, `Logger`. Method `ListenAndServe(ctx context.Context) error` using `net/http` stdlib. Register routes via `http.ServeMux` | | |
| TASK-017 | Implement auth middleware in `internal/server/middleware.go`: `AuthMiddleware(keys []config.APIKey, next http.Handler) http.Handler`. Extract `Authorization: Bearer <key>` header, look up in keys map, attach `ClientName` to request context. Return `{"error": "unauthorized"}` HTTP 401 on failure | | |
| TASK-018 | Define inference endpoints constant in `internal/server/routes.go`: `var InferenceEndpoints = []string{"/api/generate", "/api/chat", "/api/embed", "/v1/chat/completions", "/v1/completions", "/v1/embeddings"}`. Implement `IsInferenceEndpoint(path string) bool` | | |
| TASK-019 | Implement `internal/server/handler.go` `HandleInference(w http.ResponseWriter, r *http.Request)`: (1) check queue depth, return 503 if full with `{"error":"queue_full","queue_depth":N,"max_depth":N}`; (2) create `Request` with UUID, client name from context, enqueue; (3) start SSE loop writing `StatusUpdate` events from `r.StatusCh` until `r.ResponseCh` receives result; (4) on result, call `PipeResponse`; (5) on client disconnect (`r.Context().Done()`), call `queue.Drop(id)` | | |
| TASK-020 | Implement SSE status writer in `internal/server/sse.go`: `WriteStatusEvent(w http.ResponseWriter, u StatusUpdate)` writes `data: <json>\n\n` and flushes. Set response headers `Content-Type: text/event-stream`, `Cache-Control: no-cache`, `Connection: keep-alive` before first write | | |
| TASK-021 | Implement heartbeat goroutine in `HandleInference`: ticker at `Config.HeartbeatIntervalSecs`; on tick, send current position and elapsed wait to `StatusCh` if request is still queued | | |
| TASK-022 | Implement `HandlePassThrough(w http.ResponseWriter, r *http.Request)`: forward immediately via `Forwarder.Forward`, pipe response, add `X-Ollama-Broker: true` header | | |
| TASK-023 | Implement `GET /broker/status` handler in `internal/server/status.go`: return JSON `{"queue_depth": N, "max_depth": N, "in_flight_client": "<name or null>", "connected_clients": N}`. This endpoint does NOT require auth | | |
| TASK-024 | Wire all routes in `server.go`: auth middleware wraps all routes except `/broker/status`; inference endpoints route to `HandleInference`; all other paths route to `HandlePassThrough` | | |

### Implementation Phase 5 — Entrypoint & Deployment Artifacts

- GOAL-005: Wire everything into the binary entrypoint and produce deployment artifacts

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-025 | Update `cmd/app/main.go`: parse CLI flags, load config, construct logger, construct queue, forwarder, dispatcher, server; call `dispatcher.Run` in a goroutine; call `server.ListenAndServe`; handle `SIGINT`/`SIGTERM` for graceful shutdown (drain in-flight request, reject new requests, exit) | | |
| TASK-026 | Create `deploy/llama-line.service`: systemd unit file with `ExecStart=/usr/local/bin/llama-line --config /etc/llama-line/config.yaml`, `Restart=on-failure`, `RestartSec=5`, `EnvironmentFile=-/etc/llama-line/env` | | |
| TASK-027 | Update `Makefile`: add `build` target (`go build -o bin/llama-line ./cmd/app`), `install` target (`cp bin/llama-line /usr/local/bin/llama-line`), `run` target (`go run ./cmd/app --config config.example.yaml`) | | |
| TASK-028 | Update `README.md` with: overview, requirements, install steps, config reference table (all fields, types, env var names, defaults), API key setup, systemd deployment instructions, `GET /broker/status` response example | | |

### Implementation Phase 6 — Tests

- GOAL-006: Implement unit and integration tests covering queue logic, auth, routing, and SSE behaviour

| Task | Description | Completed | Date |
|------|-------------|-----------|------|
| TASK-029 | `internal/queue/queue_test.go`: test `Enqueue` returns error when `maxDepth` reached; test `Drop` removes correct item; test `Depth()` accuracy; test `Dequeue` returns nil on empty queue | | |
| TASK-030 | `internal/server/middleware_test.go`: test valid key passes through with correct `ClientName` in context; test missing header returns 401; test invalid key returns 401 | | |
| TASK-031 | `internal/server/routes_test.go`: test `IsInferenceEndpoint` returns true for all 6 endpoints and false for `/api/tags`, `/api/pull`, `/broker/status` | | |
| TASK-032 | `internal/config/load_test.go`: test YAML load populates all fields; test env var overlay overrides YAML value; test missing config file returns descriptive error | | |
| TASK-033 | `internal/server/handler_test.go`: integration test using `httptest.Server` as fake ollama; test that two simultaneous inference requests result in one being queued (SSE position=1 received); test that client disconnect drops request from queue | | |

## 3. Alternatives

- **ALT-001**: Use `fastify` or `express` (Node.js) — rejected in favour of Go for single static binary deployment without runtime dependency
- **ALT-002**: Bounded concurrency (N in-flight) instead of strict serial — rejected for v1; ollama's GPU contention is the root cause, serial is safest default
- **ALT-003**: Async job API (client gets job ID, polls status endpoint) — rejected in favour of SSE on the same connection; minimises client code changes
- **ALT-004**: Docker deployment — rejected; ollama typically runs as a binary/systemd service, broker fits naturally alongside it without container networking complexity
- **ALT-005**: Reconnect-and-resume for dropped clients — rejected; if client is gone, work is wasted GPU time; drop-on-disconnect is correct
- **ALT-006**: Estimated wait time in status updates — rejected; model load times vary too widely, wrong estimates are worse than none; elapsed wait is honest

## 4. Dependencies

- **DEP-001**: `gopkg.in/yaml.v3` — YAML config file parsing
- **DEP-002**: Go stdlib `net/http` — HTTP server and client (no third-party HTTP framework)
- **DEP-003**: Go stdlib `log/slog` — structured JSON logging (Go 1.21+)
- **DEP-004**: Go stdlib `crypto/rand` or `github.com/google/uuid` — UUID generation for request IDs
- **DEP-005**: Go 1.26 (as specified in `go.mod`)

## 5. Files

- **FILE-001**: `go.mod` — update module path to `git.subcult.tv/PatrickFanella/llama-line`
- **FILE-002**: `cmd/app/main.go` — binary entrypoint, CLI flags, signal handling, component wiring
- **FILE-003**: `internal/config/config.go` — `Config` and `APIKey` structs
- **FILE-004**: `internal/config/load.go` — YAML load + env var + CLI flag overlay
- **FILE-005**: `internal/logger/logger.go` — structured JSON logger constructor
- **FILE-006**: `internal/queue/queue.go` — `Request`, `StatusUpdate`, `Result`, `Queue` structs and methods
- **FILE-007**: `internal/queue/dispatcher.go` — `Dispatcher` goroutine, timeout enforcement
- **FILE-008**: `internal/proxy/forwarder.go` — ollama HTTP forwarder with retry logic
- **FILE-009**: `internal/proxy/pipe.go` — streaming response pipe with `X-Ollama-Broker` header injection
- **FILE-010**: `internal/server/server.go` — `Server` struct, `ListenAndServe`, route wiring
- **FILE-011**: `internal/server/middleware.go` — API key auth middleware
- **FILE-012**: `internal/server/routes.go` — inference endpoint list and `IsInferenceEndpoint`
- **FILE-013**: `internal/server/handler.go` — `HandleInference` with SSE loop and queue integration
- **FILE-014**: `internal/server/sse.go` — SSE event writer
- **FILE-015**: `internal/server/status.go` — `GET /broker/status` handler
- **FILE-016**: `config.example.yaml` — documented example config with all fields
- **FILE-017**: `deploy/llama-line.service` — systemd unit file
- **FILE-018**: `Makefile` — build, install, run, verify targets
- **FILE-019**: `README.md` — usage, config reference, deployment guide
- **FILE-020**: `internal/queue/queue_test.go` — queue unit tests
- **FILE-021**: `internal/server/middleware_test.go` — auth middleware tests
- **FILE-022**: `internal/server/routes_test.go` — endpoint routing tests
- **FILE-023**: `internal/config/load_test.go` — config loading tests
- **FILE-024**: `internal/server/handler_test.go` — integration tests

## 6. Testing

- **TEST-001**: Queue rejects enqueue when `maxDepth` is reached and returns correct error
- **TEST-002**: Queue `Drop` removes the correct request by ID without affecting other items
- **TEST-003**: Auth middleware rejects requests with missing `Authorization` header with HTTP 401
- **TEST-004**: Auth middleware rejects requests with invalid API key with HTTP 401
- **TEST-005**: Auth middleware attaches correct `ClientName` to context for valid key
- **TEST-006**: `IsInferenceEndpoint` returns true for all 6 queued endpoints
- **TEST-007**: `IsInferenceEndpoint` returns false for `/api/tags`, `/api/pull`, `/broker/status`
- **TEST-008**: Config YAML load populates all struct fields correctly
- **TEST-009**: Env var overlay overrides YAML-loaded value
- **TEST-010**: Two simultaneous inference requests result in second receiving SSE `position=1` before first completes
- **TEST-011**: Client disconnect while queued results in request being removed from queue
- **TEST-012**: `GET /broker/status` returns correct queue depth and in-flight client name without requiring auth

## 7. Risks & Assumptions

- **RISK-001**: ollama's SSE streaming format may vary between versions — the broker pipes bytes blindly, so format changes in ollama won't break the broker, but SSE preamble events must not corrupt the ollama stream format
- **RISK-002**: Very long-running requests (e.g. large context generation) may cause queue wait times that exceed client HTTP timeouts at the load balancer or reverse proxy layer — mitigated by configurable heartbeat keeping the connection alive
- **RISK-003**: If ollama is restarted mid-request, the in-flight request will fail; the broker will cancel it and advance the queue, but the client will receive an error rather than a retry
- **ASSUMPTION-001**: ollama is running on the same host as the broker and is reachable at a configurable local URL
- **ASSUMPTION-002**: Clients are trusted to handle SSE `data:` events before the actual ollama response — clients that don't understand SSE will receive the status events as part of the response body, which may confuse non-streaming clients
- **ASSUMPTION-003**: API keys are low-rotation; static config reload via service restart is acceptable for v1
- **ASSUMPTION-004**: The Go template's existing `cmd/app` and `internal/app` structure will be replaced, not extended

## 8. Related Specifications / Further Reading

- [CONTEXT.md](../CONTEXT.md) — domain glossary for llama-line
- [ollama API reference](https://github.com/ollama/ollama/blob/main/docs/api.md)
- [ollama OpenAI compatibility](https://github.com/ollama/ollama/blob/main/docs/openai.md)
- [Server-Sent Events spec](https://html.spec.whatwg.org/multipage/server-sent-events.html)
- [Go slog structured logging](https://pkg.go.dev/log/slog)
