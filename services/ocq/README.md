# ocq

Tiny CLI and private OpenAI-compatible gateway for a local OpenCode server.

`ocq` creates or continues an OpenCode session, sends one prompt, and prints the assistant response. It can also run `ocq serve`, a small private HTTP gateway for tools that speak the OpenAI chat completions API.

## Requirements

- Node.js 18+
- OpenCode server running locally, for example:

```sh
opencode serve --hostname 0.0.0.0 --port 4096
```

- Basic auth credentials in an env file, default:

```sh
~/.config/opencode/server.env
```

Expected keys:

```sh
OPENCODE_SERVER_USERNAME=...
OPENCODE_SERVER_PASSWORD=...
```

Do not commit this env file.

## Install

Clone and link locally:

```sh
git clone https://github.com/PatrickFanella/ocq.git
cd ocq
npm link
```

Or run directly:

```sh
./bin/ocq "reply with exactly: ok"
```

## Usage

```sh
ocq [options] <prompt>
ocq serve [options]
```

Options:

```text
  -s, --session <id>       Continue an existing session
  -m, --model <model>      Model ID or provider/model
  -p, --provider <id>      Provider ID
  -d, --directory <path>   OpenCode directory context
  -u, --url <url>          OpenCode server URL
      --env <path>         Server env file
      --system <text>      System prompt override
      --no-system          Do not send a system prompt
      --agent <name>       OpenCode agent name
      --title <title>      Title for newly-created session
      --json               Print JSON { sessionID, messageID, text }
  -h, --help               Show help
```

Serve options:

```text
      --host <host>        Gateway bind host
      --port <port>        Gateway bind port
      --key <key>          Gateway bearer key
  -m, --model <model>      Default gateway model provider/model
  -d, --directory <path>   OpenCode directory context
  -u, --url <url>          OpenCode server URL
      --env <path>         Server env file
```

Defaults:

- URL: `http://127.0.0.1:4096`
- provider/model: `openai/gpt-5.4-mini`
- directory: `$HOME`
- env file: `~/.config/opencode/server.env`
- gateway bind: `127.0.0.1:8088`

## Examples

```sh
ocq "what changed in OpenCode recently?"
ocq --json "start a quick conversation"
ocq --session ses_abc123 "follow up"
ocq --model openai/gpt-5.4-mini "quick answer"
OCQ_GATEWAY_KEY=secret ocq serve --port 8088
./bin/ocq serve --port 8088
curl -s http://127.0.0.1:8088/health
curl -s -H "Authorization: Bearer secret" \
  -H "Content-Type: application/json" \
  http://127.0.0.1:8088/v1/chat/completions \
  -d '{"model":"openai/gpt-5.4-mini","messages":[{"role":"user","content":"reply ok"}]}'
```

## llama-line integration contract

When OCQ is used behind llama-line, configure it as an OpenAI-compatible upstream:

- Health probes: `GET /health` and `GET /healthz` both return `{ "ok": true }`.
- Chat endpoint: `POST /v1/chat/completions` with `Authorization: Bearer $OCQ_GATEWAY_KEY`.
- Session continuation: pass `ocq_session_id` in streaming or non-streaming request bodies.
- Session propagation: OCQ returns `x-ocq-session` and includes `ocq_session_id` in non-streaming JSON responses.
- Request correlation: OCQ accepts `X-Request-ID` or `X-Llama-Line-Request-ID` and returns `x-ocq-request-id`.
- Status headers: successful responses include `x-ocq-status: completed`; streaming responses also include `x-ocq-streaming-mode: buffered`.
- Cancellation: if the client disconnects before completion, OCQ aborts the upstream OpenCode HTTP request.
- Streaming compatibility: `stream: true` returns OpenAI-style SSE chunks after OpenCode completes. This is buffered compatibility streaming, not live OpenCode token/log streaming.

## UI console

- The UI lives in a separate Vite app under `apps/ui`.
- Treat the UI as private service behind proxy auth; do not expose it directly.
- The UI still uses the gateway API key for requests.
- MVP storage is `localStorage` for the API key; assume the browser profile is trusted.

Setup / dev:

```sh
npm --prefix apps/ui install
OCQ_GATEWAY_KEY=secret ocq serve --port 8088
npm run ui:dev
```

Safety notes:

- Session selection is read-only; sending a prompt is the only mutation.
- Keep request logs prompt-free by default.
- Do not store secrets in metrics, logs, or docs.
- `/metrics` is for Prometheus-style scraping; keep session/metrics views limited to operational data.

## Environment overrides

```sh
OPENCODE_SERVER_URL=http://127.0.0.1:4096
OPENCODE_SERVER_USERNAME=...
OPENCODE_SERVER_PASSWORD=...
OCQ_PROVIDER=openai
OCQ_MODEL=gpt-5.4-mini
OCQ_DIRECTORY=$HOME
OCQ_ENV_FILE=$HOME/.config/opencode/server.env
OCQ_SYSTEM="Use browser tools when needed. Otherwise answer concisely."
OCQ_GATEWAY_HOST=127.0.0.1
OCQ_GATEWAY_PORT=8088
OCQ_GATEWAY_KEY=...
OCQ_DEFAULT_MODEL=openai/gpt-5.4-mini
OCQ_DEFAULT_DIRECTORY=$HOME
```

## License

MIT
