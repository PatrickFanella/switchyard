# Client Compatibility Reference

Switchyard owns the native model-broker runtime and exposes llama-line/Ollama/OpenAI-compatible ingress for existing clients.

## Base URLs

- Local host: `http://127.0.0.1:11434`
- LAN clients: `http://10.0.0.50:11434`
- Authenticated control API: `http://127.0.0.1:18080` (host) or `http://api:8080` (Compose)

The model base URLs serve only model-compatible routes and `/broker/status`. Use the authenticated control API for durable history, search, logs, configuration, queue/inflight administration, and OCQ compatibility administration.

## Ollama-compatible endpoints

| Endpoint | Response mode | Notes |
| --- | --- | --- |
| `GET /api/tags` | JSON | Proxies real upstream Ollama tags. |
| `POST /api/generate` | JSON | Non-stream response by default. |
| `POST /api/chat` | JSON | Returns plain Ollama JSON with `message.content`; clients expecting SSE must support JSON too. |
| `POST /api/embed` | JSON | Embedding passthrough. |

Ollama-routed `/api/chat` requests preserve `tools`, `tool_choice`, `options`, tool response messages, and native `message.tool_calls` when the upstream Ollama model returns structured tool calls.

## OpenAI-compatible endpoints

| Endpoint | Response mode | Notes |
| --- | --- | --- |
| `POST /v1/chat/completions` | JSON or SSE when `stream: true` | Emits OpenAI-compatible chunks plus Switchyard/OCQ correlation headers. |
| `POST /v1/completions` | JSON | Completion compatibility. |
| `POST /v1/embeddings` | JSON | Embedding compatibility. |
| `GET /v1/models` | JSON | Model list compatibility. |

For non-streaming `/v1/chat/completions`, Ollama native `message.tool_calls` are converted to OpenAI-compatible `message.tool_calls` with `finish_reason: "tool_calls"`.

Switchyard rejects llama-line/OpenAI-compatible chat completions that contain no assistant content and no `message.tool_calls`. Tool-call-only responses are valid and are preserved as `ModelResponse.ToolCalls` internally and as OpenAI-compatible `message.tool_calls` externally. Streaming OpenAI error payloads are terminal errors; streaming tool-call-only payloads should use the non-streaming tool path so structured calls are not silently accepted as empty content.

## Compatibility and admin endpoints

`GET /broker/status` is available on model ingress. Every other endpoint in this section is restricted to the authenticated control listener.

- `GET /broker/status`
- `GET /api/history`
- `GET /api/history/{id}`
- `GET /api/search`
- `GET /api/logs`
- `GET /api/config`
- `GET /api/models`
- `POST /api/models/pull`
- `GET /admin/queue`
- `DELETE /admin/queue`
- `DELETE /admin/queue/{id}`
- `GET /admin/inflight`
- `POST /admin/inflight/cancel`
- `GET /admin/logs`
- `GET /admin/audit`
- `GET /admin/search`
- `GET /admin/stats/summary`
- `GET /ocq/sessions`
- `GET /ocq/requests`
- `GET /ocq/settings`

## Correlation headers

- `X-Request-ID`
- `X-Llama-Line-Request-ID`
- `X-OCQ-Request-ID`
- `X-OCQ-Session`
- `X-OCQ-Streaming-Mode`
- `X-Switchyard-Backend` (deprecated external generation routes only)
- `X-Switchyard-Compatibility-Warning` (deprecated external generation routes only)

## Compatibility lifecycle

Compatibility ingress remains supported: existing llama-line/Ollama/OpenAI client endpoints continue to be served by Switchyard. The external selectors `MODEL_BROKER_BACKEND=llama-line` and `OPENCODE_EXECUTOR_BACKEND=ocq` are deprecated cutover choices; native routing remains the default and Ollama routing remains supported.

When a synchronous generation request selects a deprecated external route (`llama-line`, or nested `ocq` through the native broker), Switchyard sets `X-Switchyard-Backend` to that route and `X-Switchyard-Compatibility-Warning: deprecated-external-backend` before JSON or SSE response commitment, including upstream errors. Native, Ollama, and non-generation routes do not receive either header. Durable records use only `switchyard.backend`, `switchyard.deprecated_backend`, and `switchyard.compatibility_warning` for this decision; workflow attempts emit `compatibility.deprecated_backend_attempted`. `/broker/status` exposes `effective_selector` and optional raw `configured_selector`; `/api/config` exposes effective broker/executor selectors and optional raw configured values. Blank or unknown selectors resolve to `native`, and warnings appear only for deprecated llama-line or nested OCQ selection.

Selectors will not be removed before v1.0. Removal requires migration documentation, at least 90 days, and two consecutive releases with zero observed deprecated-backend correlations/events; it may then occur in v1.0 or a later major release. This policy does not remove supported ingress. External Changemaker adapter probes are migration visibility only and do not select the native Changemaker pipeline.

## Client identity and priority

Switchyard restores llama-line-style client attribution for compatible ingress:

- Preferred: configure bearer keys with `MODEL_BROKER_CLIENT_KEYS`, for example `subcorp=subcorp-secret:50,ci=ci-secret:10`. Requests with `Authorization: Bearer subcorp-secret` are recorded as client `subcorp` with priority `50`.
- Trusted header fallback: `X-Switchyard-Client` or `X-Llama-Line-Client`, with optional `X-Switchyard-Priority` or `X-Llama-Line-Priority`.
- Last fallback: `X-Forwarded-For`, then remote address.

The resolved `client` and `priority` appear in `/broker/status`, queue/inflight endpoints, `/api/history`, `/api/logs`, `/admin/audit`, and OCQ request compatibility responses. Higher priority sorts ahead of lower priority for queued compatibility views; synchronous compatible requests still start immediately in the current native broker path.

## Subcorp note

Subcorp should point `OLLAMA_BASE_URL` and embedding URLs at `http://10.0.0.50:11434`. Its `/api/chat` parser must accept plain JSON and not require SSE.

## Model inventory and pulls

- `GET /api/models` reports installed Ollama models from the real upstream Ollama `/api/tags` endpoint and OpenCode provider/model metadata from the configured OpenCode server.
- `POST /api/models/pull` pulls Ollama models by calling upstream Ollama `/api/pull` with `stream:false`.
- OpenCode metadata can be refreshed with `opencode models --refresh`; OpenCode does not pull provider model weights.
