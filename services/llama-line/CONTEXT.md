# llama-line — Domain Glossary

## Broker
A Go service that runs on the same machine as ollama. Clients connect to the Broker instead of ollama directly. The Broker serialises requests into a strict serial queue, streams queue-position status to waiting clients via SSE, then pipes the ollama response back when the request is dequeued.

## Client
An application (on any server) that makes inference requests to the Broker. Each Client is identified by an API key. Clients speak either the native ollama HTTP API or the OpenAI-compatible API.

## Queue
The ordered list of pending requests held by the Broker. At most one request is in-flight with ollama at any time. All others wait in the Queue.

## Queue Position
A 1-based integer indicating where a request sits in the Queue. Position 1 means next to run.

## Elapsed Wait
The number of seconds a request has been waiting in the Queue. Reported alongside Queue Position in status updates.

## Status Update
An SSE event sent to a waiting Client on its open connection. Carries Queue Position and Elapsed Wait. Sent periodically while the request is queued.

## In-Flight Request
The single request currently being processed by ollama. While a request is in-flight, all other requests are queued.

## API Key
A secret token that identifies a Client. Required on every request. Used to track which Client owns which queued or in-flight request. Defined as a static list in the config file for v1; runtime management via admin API planned for v2.

## Status Update
An SSE event sent to a waiting Client on its open connection. Carries Queue Position and Elapsed Wait while queued. Carries a distinct `ollama_unavailable` status when ollama is unreachable.

## Broker Status
A read-only HTTP endpoint (`GET /broker/status`) returning current queue depth, in-flight request identity, and connected client count.

## Inference Endpoint
One of the six HTTP endpoints that consume GPU resources and are subject to queuing: `POST /api/generate`, `POST /api/chat`, `POST /api/embed`, `POST /v1/chat/completions`, `POST /v1/completions`, `POST /v1/embeddings`. All other endpoints are proxied immediately without queuing.

## Pass-Through
A request to a non-Inference Endpoint that the Broker forwards to ollama immediately, bypassing the Queue entirely. Includes model management endpoints (`/api/tags`, `/api/pull`, `/api/delete`, `/api/ps`, etc.).

## Queue Depth
The number of requests currently waiting in the Queue. The Broker enforces a configurable maximum Queue Depth; requests arriving when the Queue is full are rejected with HTTP 503.

## Heartbeat
A periodic SSE event sent to waiting Clients to keep the connection alive through proxies and load balancers. Interval is configurable.

## Queue Full
The state when Queue Depth has reached the configured maximum. New requests are rejected with an error response matching ollama's `{"error": "..."}` format, extended with `queue_depth` and `max_depth` fields.

## Ollama Unavailable
A state where the Broker cannot reach ollama. The Broker holds the front-of-queue request, retries with backoff, and sends `ollama_unavailable` Status Updates to all waiting Clients.

## Request Timeout
A configurable duration. If the in-flight request has not completed within this time, the Broker cancels the upstream ollama connection and advances the Queue.

## Queue Wait Timeout
A configurable duration. If a request has been waiting in the Queue longer than this time, the Broker drops it and returns a timeout error to the Client.

## Dispatcher
The internal goroutine loop that dequeues requests one at a time, enforces queue wait timeout and per-request timeout, calls the Forwarder, and sends the Result back to the waiting handler via ResponseCh.

## Forwarder
The component that clones the incoming HTTP request, strips the Authorization header, sets the upstream ollama URL, executes the request, and retries with exponential backoff on connection failure.

## SSE (Server-Sent Events)
The protocol used to stream status updates to waiting clients. Events are sent as `data: <json>\n\n` on the same HTTP connection the client used to make the inference request.

## ResponseCh
A Go channel on each queued Request. The Dispatcher sends the Result (ollama response or error) to this channel when the request completes.

## StatusCh
A Go channel on each queued Request. The Dispatcher and Queue broadcast StatusUpdate events to this channel while the request is waiting.

## X-Ollama-Broker
HTTP response header set to `true` on all responses from llama-line, allowing clients to detect they are talking to the broker rather than ollama directly.

## Connected Clients
The count of clients currently in the SSE wait loop (i.e. queued or in-flight). Reported by `GET /broker/status`.
