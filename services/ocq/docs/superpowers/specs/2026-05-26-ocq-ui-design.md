# ocq UI Design

Date: 2026-05-26
Status: approved design, pending implementation plan

## Goal

Build a separate Vite React TypeScript Tailwind UI for `ocq` that acts primarily as an ops-first observability console, with an integrated chat/session experience for the existing OpenAI-compatible gateway.

The UI is for one owner, exposed publicly only behind existing proxy auth. It still uses `OCQ_GATEWAY_KEY` API keys for gateway requests. MVP stores the key in browser `localStorage`. A future hardening pass may replace this with a server-side session/cookie model, but that is outside this design.

## Non-goals

- No multi-user account system in MVP.
- No public unauthenticated UI.
- No remote-session mutation during attach/watch unless the user explicitly sends a message.
- No secrets in logs, metrics, request history, UI state exports, or docs.

## Deployment shape

- Add `apps/ui/` as a separate Vite React TypeScript Tailwind app.
- Deploy UI as its own service/container.
- UI calls `ocq serve` over HTTP using the saved gateway API key.
- Production route is protected by existing reverse-proxy auth.
- Gateway keeps OpenAI-compatible routes and adds `ocq`-specific admin/session APIs.

## UX direction

Use an ops-first console layout.

Primary navigation:

- **Overview**: gateway health, uptime, active/default model, request rate, p95 latency, error rate, active streams, OpenCode upstream state.
- **Requests**: recent completions table with timestamp, route, status, model, duration, stream/non-stream, session ID, and error summary.
- **Logs**: filterable gateway log history with SSE updates when available and polling fallback otherwise.
- **Sessions**: all OpenCode sessions, including sessions created by CLI, API, UI, or remote actors.
- **Chat**: secondary drawer/pane opened from a session or quick action; same chat UI for local and remote-created sessions.
- **Settings**: gateway base URL, API key saved in `localStorage`, theme, refresh interval, feature flags for streaming/polling.

## Sessions behavior

Sessions are central and must include all OpenCode sessions visible to the gateway, not just UI-created sessions.

When a session is selected:

1. Render it in the normal chat window, not a separate raw/debug view.
2. Show transcript/messages in chat format.
3. Watch for near-live updates using session events if available.
4. Fall back to polling with a visible `last updated` timestamp when event streaming is unavailable.
5. If safe continuation is supported, allow sending a new message into that session.
6. If safe continuation is not supported, mark the session `observe only` and disable input.

Attach/watch must be non-invasive:

- Never close, cancel, interrupt, or steal a remote run.
- Do not mutate session state while observing.
- Only mutate when the user explicitly sends a message.
- Prefer polling over risky attach semantics if OpenCode does not provide a safe observe API.

## Gateway API additions

Keep existing routes:

- `GET /health`
- `POST /v1/chat/completions`

Extend gateway with:

- `GET /ocq/health` — gateway + upstream health summary.
- `GET /ocq/metrics` — JSON metrics summary for UI.
- `GET /metrics` — Prometheus-compatible scrape endpoint for NUC monitoring.
- `GET /ocq/requests` — recent request ring buffer.
- `GET /ocq/logs` — recent gateway log ring buffer.
- `GET /ocq/sessions` — all OpenCode sessions visible to gateway.
- `GET /ocq/sessions/:id` — session detail/transcript.
- `POST /ocq/sessions/:id/messages` — explicit user send into existing session.
- `GET /ocq/sessions/:id/events` — SSE session events only if OpenCode provides safe non-invasive observation; otherwise omit this endpoint and use polling.

`POST /v1/chat/completions` must add OpenAI-compatible SSE streaming for `stream: true`; non-streaming behavior remains supported.

## Metrics and NUC integration

Gateway exposes Prometheus-compatible metrics at `GET /metrics`.

Initial metrics:

- request count by `route`, `model`, `status`, `stream`
- error count by `route`, `type`, `status`
- latency histogram by `route`, `model`, `stream`
- active streams gauge
- OpenCode upstream failure count
- session attach/watch count
- session poll/event count

Implementation plan must verify the existing NUC metrics stack before wiring scrape config. Current assumption: Grafana Alloy or Prometheus-compatible remote-write path may be present. The deploy doc should include NUC scrape/route notes without printing secrets.

## Frontend architecture

Use focused modules:

- `apps/ui/src/lib/api.ts`: authenticated gateway client.
- `apps/ui/src/lib/sse.ts`: SSE helper for completions/session events.
- `apps/ui/src/lib/storage.ts`: localStorage key/settings handling.
- `apps/ui/src/pages/Overview.tsx`
- `apps/ui/src/pages/Requests.tsx`
- `apps/ui/src/pages/Logs.tsx`
- `apps/ui/src/pages/Sessions.tsx`
- `apps/ui/src/components/ChatPane.tsx`
- `apps/ui/src/components/Shell.tsx`

Use React Query or SWR-style data fetching for polling/cache. Use SSE where available. Polling is the fallback everywhere streaming is unavailable or unsafe.

## Backend architecture

Extend current Node `http` gateway without replacing the CLI.

- Keep OpenCode HTTP details isolated in `src/opencode.js`.
- Add session listing/detail helpers to `src/opencode.js` after verifying OpenCode API shape.
- Add OpenAI SSE response mapping to `src/openai.js` or a new focused module if it grows.
- Add observability state as small in-memory ring buffers first.
- Keep metrics code isolated from route handlers.
- Keep API-key validation centralized and constant-time.

## Error handling

- Gateway returns concise JSON errors for API routes.
- UI shows short toast plus expandable detail drawer.
- Session watch states must distinguish `live`, `polling`, `stale`, and `observe only`.
- Streaming failures should leave partial transcript visible and show retry/continue options.

## Security

- Assume reverse-proxy auth protects the UI route.
- Browser stores gateway key in `localStorage` for MVP.
- Never log authorization headers or key values.
- Metrics and logs must not expose prompt text by default; request history may show IDs, timings, statuses, model, and short error summaries.
- Prompt/message previews are out of scope for MVP; if added in a future design, they must be opt-in.

## Testing and validation

Backend:

- route smoke tests for health, auth rejection, request logging, metrics output
- SSE chat completion test
- sessions list/detail tests against mocked OpenCode responses
- non-invasive session watch behavior documented and manually verified

Frontend:

- API client tests for auth headers and error parsing
- component tests for session states and settings storage
- manual Vite dev smoke against local gateway

Homelab/deploy:

- verify UI behind proxy auth
- verify gateway key required by UI requests
- verify `/metrics` scrape path against NUC metrics stack
- verify no secrets appear in logs/metrics

## Open questions for implementation planning

- Exact OpenCode API endpoints for listing sessions and reading transcripts.
- Whether OpenCode offers safe event/watch APIs for active sessions.
- Exact NUC metrics stack and scrape/remote-write path.
- Whether to use React Query or SWR; default recommendation is React Query if dependencies are acceptable.
