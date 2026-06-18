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

## OpenAI-compatible endpoints

| Endpoint | Response mode | Notes |
| --- | --- | --- |
| `POST /v1/chat/completions` | JSON or SSE when `stream: true` | Emits OpenAI-compatible chunks plus Switchyard/OCQ correlation headers. |
| `POST /v1/completions` | JSON | Completion compatibility. |
| `POST /v1/embeddings` | JSON | Embedding compatibility. |
| `GET /v1/models` | JSON | Model list compatibility. |

## Compatibility and admin endpoints

- `GET /broker/status`
- `GET /api/history`
- `GET /api/history/{id}`
- `GET /api/search`
- `GET /api/logs`
- `GET /api/config`
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
