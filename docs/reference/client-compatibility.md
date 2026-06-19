# Client Compatibility Reference

Switchyard owns the native model-broker runtime and exposes llama-line/Ollama/OpenAI-compatible ingress for existing clients.

## Base URLs

- Local host: `http://127.0.0.1:11434`
- LAN clients: `http://10.0.0.50:11434`
- Compose-internal API: `http://api:8080`

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

## Subcorp note

Subcorp should point `OLLAMA_BASE_URL` and embedding URLs at `http://10.0.0.50:11434`. Its `/api/chat` parser must accept plain JSON and not require SSE.

## Model inventory and pulls

- `GET /api/models` reports installed Ollama models from the real upstream Ollama `/api/tags` endpoint and OpenCode provider/model metadata from the configured OpenCode server.
- `POST /api/models/pull` pulls Ollama models by calling upstream Ollama `/api/pull` with `stream:false`.
- OpenCode metadata can be refreshed with `opencode models --refresh`; OpenCode does not pull provider model weights.
