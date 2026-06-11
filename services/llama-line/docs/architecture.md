# llama-line Architecture

## 1. Overview
llama-line is a single Go binary. No external dependencies at runtime. All concurrency is handled with goroutines, channels, and a mutex-protected queue.

## 2. Package Layout
```text
cmd/app/          — binary entrypoint, CLI flags, signal handling, component wiring
internal/config/  — Config and APIKey structs, YAML load, env/CLI overlay
internal/logger/  — slog-based structured JSON logger constructor
internal/queue/   — Queue, Request, Dispatcher, StatusUpdate, Result
internal/proxy/   — Forwarder (HTTP client + retry), PipeResponse
internal/server/  — HTTP server, auth middleware, routing, SSE, handlers, status
```

## 3. Request Lifecycle
1. Client sends `POST /api/generate` with `Authorization: Bearer <key>`
2. `AuthMiddleware` validates key, attaches `ClientName` to context
3. `Server.ListenAndServe` mux routes to `HandleInference`
4. `HandleInference` checks queue depth via `Queue.Snapshot()`; returns 503 if full
5. Creates `queue.Request` with UUID, channels, context
6. `Queue.Enqueue` adds request, signals `notify` channel to wake Dispatcher
7. `HandleInference` enters SSE loop: reads from `StatusCh`, heartbeat ticker, `ResponseCh`, or client disconnect
8. `Dispatcher.Run` wakes, calls `Queue.Dequeue`, checks queue wait timeout
9. Dispatcher calls `Forwarder.Forward` with per-request timeout context
10. `Forwarder.Forward` clones request, strips auth, sends to ollama, retries on failure
11. On success, Dispatcher sends `Result{Resp}` to `ResponseCh`
12. `HandleInference` receives result, calls `proxy.PipeResponse` to stream ollama response to client
13. `Queue.Complete` clears in-flight, `broadcastPositions` updates all waiting clients

```text
client -> auth -> handle inference -> queue -> dispatcher -> forwarder -> ollama
   ^                                                           |
   |---------------------- SSE / response ----------------------|
```

## 4. Concurrency Model
- One `Dispatcher.Run` goroutine — serialises all inference requests
- One goroutine per connected client (the HTTP handler goroutine)
- `Queue` protected by `sync.Mutex`
- `Queue.notify` is a buffered channel(1) used to wake the Dispatcher
- `StatusCh` is buffered(8) per request — non-blocking sends, drops if full
- `connectedClients` is `atomic.Int32`
- Graceful shutdown: `context.WithTimeout(30s)` on `http.Server.Shutdown`

## 5. Retry Logic
- `Forwarder.Forward` retries on connection error only (not HTTP errors)
- Backoff: starts at 1s, doubles each attempt, capped at `OllamaRetryMaxSecs`
- On each retry: sends `StatusUpdate{Status: "ollama_unavailable"}` to all waiting clients via `Queue.broadcastPositions`-style broadcast through the request's `StatusCh`
- Retry loop respects `ctx` cancellation (client disconnect or request timeout)

## 6. Timeout Enforcement
Two independent timeouts, both enforced in `Dispatcher.Run`:
- **Queue wait timeout**: checked before dequeue via `time.Since(r.EnqueuedAt)`; if exceeded, sends `ErrQueueWaitTimeout` to `ResponseCh` and skips forwarding
- **Per-request timeout**: `context.WithTimeout(r.Ctx, requestTimeout)` wraps the `Forwarder.Forward` call; cancels upstream ollama connection if exceeded

## 7. Disconnect Handling
- Client disconnect cancels `r.Ctx` (derived from `r.Context()`)
- `HandleInference` selects on `r.Context().Done()` and returns immediately
- `defer s.queue.Drop(req.ID)` removes the request from the queue
- If in-flight: `r.Ctx` cancellation propagates to `Forwarder.Forward` via the child context, cancelling the upstream HTTP request

## 8. Configuration Priority
CLI flags > environment variables > YAML config file > hardcoded defaults. Implemented in `internal/config/load.go`: `Load()` → `ApplyEnv()` → `ApplyCLI()`.
