# OpenCode gateway plan

## Goal

Evolve `ocq` from a one-shot CLI into a small private inference gateway for `almaz`.

Target shape:

```text
client/tool
  -> OpenAI-compatible HTTP API
  -> ocq gateway on almaz
  -> OpenCode server API
  -> provider auth from almaz OpenCode login
```

Keep the existing CLI. Add a server mode around the same OpenCode client flow.

## Non-goals

- Do not pretend ChatGPT/Codex auth is OpenAI Platform API auth.
- Do not make this a public multi-tenant API.
- Do not add complex durable queueing in the first version.
- Do not replace `llama-line`; local Ollama/GPU queueing remains there.

## MVP

Add `ocq serve`:

```sh
ocq serve --host 127.0.0.1 --port 8088
```

Expose:

- `GET /health`
- `POST /v1/chat/completions`

Support first:

- non-streaming responses only
- `Authorization: Bearer <gateway key>`
- model names as `provider/model`, e.g. `openai/gpt-5.4-mini`
- same OpenCode server auth env file already used by CLI
- concise JSON errors

## Request mapping

Input:

```json
{
  "model": "openai/gpt-5.4-mini",
  "messages": [
    { "role": "system", "content": "Answer concisely." },
    { "role": "user", "content": "Ping?" }
  ],
  "stream": false
}
```

Gateway behavior:

1. Parse `model` into `providerID` and `modelID`.
2. Convert messages into one OpenCode prompt.
3. Create or reuse an OpenCode session.
4. Send `POST /session/:id/message` with:

```json
{
  "parts": [{ "type": "text", "text": "..." }],
  "model": { "providerID": "openai", "modelID": "gpt-5.4-mini" }
}
```

5. Flatten OpenCode response `parts` into assistant text.
6. Return OpenAI-compatible response:

```json
{
  "id": "chatcmpl-...",
  "object": "chat.completion",
  "created": 1770000000,
  "model": "openai/gpt-5.4-mini",
  "choices": [
    {
      "index": 0,
      "message": { "role": "assistant", "content": "pong" },
      "finish_reason": "stop"
    }
  ]
}
```

## Config

Prefer env first, optional config file later.

```sh
OCQ_GATEWAY_HOST=127.0.0.1
OCQ_GATEWAY_PORT=8088
OCQ_GATEWAY_KEY=...
OPENCODE_SERVER_URL=http://127.0.0.1:4096
OCQ_ENV_FILE=$HOME/.config/opencode/server.env
OCQ_DEFAULT_MODEL=openai/gpt-5.4-mini
OCQ_DEFAULT_DIRECTORY=$HOME
```

Keep OpenCode server credentials separate:

```sh
OPENCODE_SERVER_USERNAME=...
OPENCODE_SERVER_PASSWORD=...
```

## Almaz deployment

Run privately on `almaz`:

```text
127.0.0.1:8088 -> ocq gateway
127.0.0.1:4096 -> opencode serve
```

Expose only via one of:

- Tailscale/WireGuard-only URL
- Caddy route with Authelia plus gateway bearer key
- localhost-only for other almaz services

Avoid unauthenticated public exposure.

## Later phases

### Phase 2: streaming

Add `stream: true` and emit OpenAI-style SSE:

```text
data: {"choices":[{"delta":{"content":"..."}}]}
data: [DONE]
```

If OpenCode server cannot stream cleanly, keep non-streaming as default.

### Phase 3: sessions

Allow optional session control:

- `X-OCQ-Session: ses_...`
- response header `X-OCQ-Session`
- configurable session mode: `ephemeral`, `per-client`, `explicit`

Default should be `ephemeral` to avoid accidental context bleed.

### Phase 4: model routing

Optional route table:

```yaml
models:
  "openai/*": opencode
  "anthropic/*": opencode
  "llama/*": http://127.0.0.1:11434
```

If local Ollama routing grows, forward to `llama-line` instead of reimplementing queueing.

### Phase 5: ops polish

- systemd unit
- Caddy snippet
- structured request logs without prompt bodies by default
- `/metrics` if needed
- rate limits / simple in-memory concurrency cap

## Reuse from current code

Current `bin/ocq` already has the core pieces:

- `parseArgs` for `provider/model`
- `parseEnvFile` and `authHeader`
- `request`, `createSession`, `sendPrompt`
- `textFromParts`

Refactor target:

```text
bin/ocq              CLI entrypoint
src/opencode.js      OpenCode server client
src/openai.js        OpenAI-compatible schema mapping
src/server.js        HTTP server
```

No framework required initially; Node `http` is enough.

## Verification

Minimum checks:

```sh
npm run check
ocq "reply ok"
ocq serve --port 8088
curl -s http://127.0.0.1:8088/health
curl -s -H "Authorization: Bearer $OCQ_GATEWAY_KEY" \
  -H "Content-Type: application/json" \
  http://127.0.0.1:8088/v1/chat/completions \
  -d '{"model":"openai/gpt-5.4-mini","messages":[{"role":"user","content":"reply ok"}]}'
```
