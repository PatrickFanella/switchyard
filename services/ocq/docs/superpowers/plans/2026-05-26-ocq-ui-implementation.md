# ocq UI Implementation Plan

> **For agentic workers:** Execute this plan task-by-task. Recommended path:
> dispatch a fresh subagent per task, review each result with `review-quality`,
> then continue. For complex multi-agent splits, use
> `parallel-feature-development`, `team-composition-patterns`, and
> `team-communication-protocols`. Steps use checkbox (`- [ ]`) syntax for
> tracking.

**Goal:** Build an ops-first Vite React TypeScript Tailwind UI for `ocq`, plus the gateway APIs needed for chat, sessions, logs, metrics, and safe session observation.

**Architecture:** Keep `ocq` CLI/gateway in the root Node package and add a separate `apps/ui` Vite app. Extend the gateway with small focused modules for auth, observability state, Prometheus metrics, streaming, and OpenCode session adapters. The UI talks to the gateway with a saved API key, uses polling by default, and upgrades to SSE where safe.

**Tech Stack:** Node 18 `http`, Vite, React, TypeScript, Tailwind CSS, Vitest, React Testing Library, Prometheus text exposition, Server-Sent Events.

---

## File map

Root backend:

- Modify `package.json`: add workspace-ish scripts for backend check/test and UI commands.
- Modify `README.md`: document UI dev/deploy, `/metrics`, and session safety.
- Modify `src/server.js`: route orchestration only; delegate auth, observability, metrics, SSE, and session handlers.
- Modify `src/opencode.js`: add safe session list/detail helpers after endpoint discovery.
- Modify `src/openai.js`: add streaming chunk helpers.
- Create `src/auth.js`: bearer validation and reusable auth guard helpers.
- Create `src/observability.js`: in-memory request/error/log/session-watch state.
- Create `src/metrics.js`: Prometheus text output and UI JSON summary.
- Create `src/sse.js`: SSE response helpers.
- Create `src/session-api.js`: gateway handlers for `/ocq/sessions*`.
- Create `test/server.test.js`: backend route tests using Node's test runner and mocked OpenCode server.
- Create `test/opencode-session-discovery.test.js`: locks discovered OpenCode session endpoint assumptions.

Frontend app:

- Create `apps/ui/package.json`, `apps/ui/vite.config.ts`, `apps/ui/tsconfig.json`, `apps/ui/index.html`, `apps/ui/postcss.config.js`, `apps/ui/tailwind.config.js`.
- Create `apps/ui/src/main.tsx`, `apps/ui/src/App.tsx`, `apps/ui/src/styles.css`.
- Create `apps/ui/src/lib/types.ts`: shared UI types matching gateway responses.
- Create `apps/ui/src/lib/storage.ts`: localStorage settings/key helpers.
- Create `apps/ui/src/lib/api.ts`: authenticated gateway client.
- Create `apps/ui/src/lib/sse.ts`: SSE client helpers.
- Create `apps/ui/src/components/Shell.tsx`, `MetricCard.tsx`, `StatusBadge.tsx`, `ChatPane.tsx`, `SettingsPanel.tsx`.
- Create `apps/ui/src/pages/Overview.tsx`, `Requests.tsx`, `Logs.tsx`, `Sessions.tsx`.
- Create `apps/ui/src/test/*.test.ts(x)`: storage, API, and component tests.

Homelab docs:

- Create `docs/ocq-ui-deploy.md`: separate UI service/container notes, reverse proxy auth assumption, API-key handling, NUC metrics verification checklist.

---

### Task 1: Backend test harness and scripts

**Files:**
- Modify: `package.json`
- Create: `test/helpers.js`
- Create: `test/server.test.js`

- [ ] **Step 1: Add Node test scripts**

Update `package.json` scripts to:

```json
{
  "scripts": {
    "check": "node --check bin/ocq && node --check src/*.js && node --check test/*.js",
    "test": "node --test test/*.test.js"
  }
}
```

- [ ] **Step 2: Create backend test helper**

Create `test/helpers.js`:

```js
const http = require("node:http")

function listen(server) {
  return new Promise((resolve) => {
    server.listen(0, "127.0.0.1", () => {
      const address = server.address()
      resolve({ server, url: `http://127.0.0.1:${address.port}` })
    })
  })
}

async function close(server) {
  await new Promise((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
}

async function jsonFetch(url, options = {}) {
  const response = await fetch(url, options)
  const text = await response.text()
  return { response, body: text ? JSON.parse(text) : undefined, text }
}

function createMockOpenCode(handler) {
  return http.createServer(async (req, res) => {
    await handler(req, res)
  })
}

module.exports = { listen, close, jsonFetch, createMockOpenCode }
```

- [ ] **Step 3: Add failing smoke tests for existing routes**

Create `test/server.test.js`:

```js
const test = require("node:test")
const assert = require("node:assert/strict")
const { createGatewayServer } = require("../src/server")
const { listen, close, jsonFetch } = require("./helpers")

test("GET /health returns ok", async () => {
  const { server, url } = await listen(createGatewayServer({ gatewayKey: "secret" }))
  try {
    const { response, body } = await jsonFetch(`${url}/health`)
    assert.equal(response.status, 200)
    assert.deepEqual(body, { ok: true })
  } finally {
    await close(server)
  }
})

test("POST /v1/chat/completions rejects missing bearer", async () => {
  const { server, url } = await listen(createGatewayServer({ gatewayKey: "secret" }))
  try {
    const { response, body } = await jsonFetch(`${url}/v1/chat/completions`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ model: "openai/gpt-5.4-mini", messages: [{ role: "user", content: "hi" }] }),
    })
    assert.equal(response.status, 401)
    assert.equal(body.error.code, "unauthorized")
  } finally {
    await close(server)
  }
})
```

- [ ] **Step 4: Run tests**

Run: `npm test`

Expected: PASS for both tests before further changes.

- [ ] **Step 5: Commit**

Run:

```bash
git add package.json test/helpers.js test/server.test.js
git commit -m "test: add backend gateway harness"
```

---

### Task 2: Auth, observability, and metrics modules

**Files:**
- Create: `src/auth.js`
- Create: `src/observability.js`
- Create: `src/metrics.js`
- Modify: `src/server.js`
- Modify: `test/server.test.js`

- [ ] **Step 1: Add failing tests for observability APIs**

Append to `test/server.test.js`:

```js
test("GET /ocq/metrics returns UI metrics summary", async () => {
  const { server, url } = await listen(createGatewayServer({ gatewayKey: "secret" }))
  try {
    const { response, body } = await jsonFetch(`${url}/ocq/metrics`, {
      headers: { authorization: "Bearer secret" },
    })
    assert.equal(response.status, 200)
    assert.equal(typeof body.uptimeSeconds, "number")
    assert.equal(body.activeStreams, 0)
    assert.ok(Array.isArray(body.latencyBuckets))
  } finally {
    await close(server)
  }
})

test("GET /metrics exposes Prometheus text", async () => {
  const { server, url } = await listen(createGatewayServer({ gatewayKey: "secret" }))
  try {
    const response = await fetch(`${url}/metrics`, { headers: { authorization: "Bearer secret" } })
    const text = await response.text()
    assert.equal(response.status, 200)
    assert.match(response.headers.get("content-type"), /text\/plain/)
    assert.match(text, /# HELP ocq_gateway_requests_total/)
    assert.match(text, /ocq_gateway_active_streams/)
  } finally {
    await close(server)
  }
})
```

Expected now: FAIL because routes do not exist.

- [ ] **Step 2: Create `src/auth.js`**

```js
const { timingSafeEqual } = require("node:crypto")

function hasValidBearer(req, gatewayKey) {
  if (!gatewayKey) return false
  const prefix = "Bearer "
  const header = req.headers.authorization || ""
  if (!header.startsWith(prefix)) return false
  const actual = Buffer.from(header.slice(prefix.length))
  const expected = Buffer.from(gatewayKey)
  return actual.length === expected.length && timingSafeEqual(actual, expected)
}

function redactHeaders(headers = {}) {
  const copy = { ...headers }
  if (copy.authorization) copy.authorization = "[redacted]"
  if (copy.Authorization) copy.Authorization = "[redacted]"
  return copy
}

module.exports = { hasValidBearer, redactHeaders }
```

- [ ] **Step 3: Create `src/observability.js`**

```js
const DEFAULT_RING_SIZE = 200

function createRingBuffer(limit = DEFAULT_RING_SIZE) {
  const items = []
  return {
    push(item) {
      items.push(item)
      while (items.length > limit) items.shift()
    },
    list() {
      return [...items].reverse()
    },
    clear() {
      items.length = 0
    },
  }
}

function createObservabilityState(now = () => Date.now()) {
  const startedAt = now()
  const requests = createRingBuffer()
  const logs = createRingBuffer()
  const counters = {
    requestsTotal: 0,
    errorsTotal: 0,
    upstreamFailuresTotal: 0,
    sessionWatchesTotal: 0,
    sessionPollsTotal: 0,
    activeStreams: 0,
  }
  const latencies = []

  function log(level, message, meta = {}) {
    logs.push({ ts: new Date(now()).toISOString(), level, message, meta })
  }

  function recordRequest(entry) {
    counters.requestsTotal += 1
    if (entry.status >= 400) counters.errorsTotal += 1
    if (typeof entry.durationMs === "number") latencies.push(entry.durationMs)
    while (latencies.length > 200) latencies.shift()
    requests.push({ ts: new Date(now()).toISOString(), ...entry })
  }

  function summary() {
    const sorted = [...latencies].sort((a, b) => a - b)
    const p95 = sorted.length ? sorted[Math.max(0, Math.ceil(sorted.length * 0.95) - 1)] : 0
    return {
      uptimeSeconds: Math.floor((now() - startedAt) / 1000),
      requestsTotal: counters.requestsTotal,
      errorsTotal: counters.errorsTotal,
      upstreamFailuresTotal: counters.upstreamFailuresTotal,
      activeStreams: counters.activeStreams,
      sessionWatchesTotal: counters.sessionWatchesTotal,
      sessionPollsTotal: counters.sessionPollsTotal,
      p95LatencyMs: p95,
      latencyBuckets: [50, 100, 250, 500, 1000, 2500, 5000],
    }
  }

  return { requests, logs, counters, log, recordRequest, summary }
}

module.exports = { createRingBuffer, createObservabilityState }
```

- [ ] **Step 4: Create `src/metrics.js`**

```js
function prometheusMetrics(state) {
  const summary = state.summary()
  return [
    "# HELP ocq_gateway_requests_total Total gateway requests.",
    "# TYPE ocq_gateway_requests_total counter",
    `ocq_gateway_requests_total ${summary.requestsTotal}`,
    "# HELP ocq_gateway_errors_total Total gateway errors.",
    "# TYPE ocq_gateway_errors_total counter",
    `ocq_gateway_errors_total ${summary.errorsTotal}`,
    "# HELP ocq_gateway_active_streams Active streaming responses.",
    "# TYPE ocq_gateway_active_streams gauge",
    `ocq_gateway_active_streams ${summary.activeStreams}`,
    "# HELP ocq_gateway_upstream_failures_total OpenCode upstream failures.",
    "# TYPE ocq_gateway_upstream_failures_total counter",
    `ocq_gateway_upstream_failures_total ${summary.upstreamFailuresTotal}`,
    "# HELP ocq_gateway_session_watches_total Session watch/attach attempts.",
    "# TYPE ocq_gateway_session_watches_total counter",
    `ocq_gateway_session_watches_total ${summary.sessionWatchesTotal}`,
    "# HELP ocq_gateway_p95_latency_ms Gateway p95 latency in milliseconds.",
    "# TYPE ocq_gateway_p95_latency_ms gauge",
    `ocq_gateway_p95_latency_ms ${summary.p95LatencyMs}`,
    "",
  ].join("\n")
}

module.exports = { prometheusMetrics }
```

- [ ] **Step 5: Wire routes in `src/server.js`**

Move bearer helper import to `src/auth.js`, create default `opts.observability`, and add routes:

```js
const { hasValidBearer } = require("./auth")
const { createObservabilityState } = require("./observability")
const { prometheusMetrics } = require("./metrics")

function requireBearer(req, res, opts) {
  if (hasValidBearer(req, opts.gatewayKey)) return true
  json(res, 401, errorBody("missing or invalid bearer token", "authentication_error", "unauthorized"), {
    "www-authenticate": "Bearer",
  })
  return false
}
```

Inside `createGatewayServer` after `const opts = getServerOptions(options)`:

```js
opts.observability = options.observability || createObservabilityState()
opts.authHeader = options.authHeader
opts.sendPrompt = options.sendPrompt
opts.listSessions = options.listSessions
opts.getSession = options.getSession
```

Add route handling before 404:

```js
if (url.pathname.startsWith("/ocq/") || url.pathname === "/metrics") {
  if (!requireBearer(req, res, opts)) return
}

if (req.method === "GET" && url.pathname === "/ocq/metrics") {
  json(res, 200, opts.observability.summary())
  return
}

if (req.method === "GET" && url.pathname === "/ocq/requests") {
  json(res, 200, { requests: opts.observability.requests.list() })
  return
}

if (req.method === "GET" && url.pathname === "/ocq/logs") {
  json(res, 200, { logs: opts.observability.logs.list() })
  return
}

if (req.method === "GET" && url.pathname === "/metrics") {
  res.writeHead(200, { "content-type": "text/plain; version=0.0.4; charset=utf-8" })
  res.end(prometheusMetrics(opts.observability))
  return
}
```

- [ ] **Step 6: Run checks**

Run: `npm run check && npm test`

Expected: PASS.

- [ ] **Step 7: Commit**

Run:

```bash
git add src/auth.js src/observability.js src/metrics.js src/server.js test/server.test.js package.json
git commit -m "feat: add gateway observability metrics"
```

---

### Task 3: OpenAI-compatible SSE streaming

**Files:**
- Create: `src/sse.js`
- Modify: `src/openai.js`
- Modify: `src/server.js`
- Modify: `test/server.test.js`

- [ ] **Step 1: Add failing SSE test**

Append to `test/server.test.js`:

```js
test("POST /v1/chat/completions supports stream true", async () => {
  const { server, url } = await listen(createGatewayServer({
    gatewayKey: "secret",
    authHeader: () => "Basic test",
    sendPrompt: async () => ({ sessionID: "ses_1", messageID: "msg_1", text: "hello world" }),
  }))
  try {
    const response = await fetch(`${url}/v1/chat/completions`, {
      method: "POST",
      headers: { authorization: "Bearer secret", "content-type": "application/json" },
      body: JSON.stringify({ stream: true, model: "openai/gpt-5.4-mini", messages: [{ role: "user", content: "hi" }] }),
    })
    const text = await response.text()
    assert.equal(response.status, 200)
    assert.match(response.headers.get("content-type"), /text\/event-stream/)
    assert.match(text, /data: .*chat.completion.chunk/)
    assert.match(text, /data: \[DONE\]/)
  } finally {
    await close(server)
  }
})
```

Expected now: FAIL because streaming is rejected.

- [ ] **Step 2: Create `src/sse.js`**

```js
function startSse(res) {
  res.writeHead(200, {
    "content-type": "text/event-stream; charset=utf-8",
    "cache-control": "no-cache, no-transform",
    connection: "keep-alive",
  })
}

function writeSse(res, data) {
  res.write(`data: ${typeof data === "string" ? data : JSON.stringify(data)}\n\n`)
}

function endSse(res) {
  writeSse(res, "[DONE]")
  res.end()
}

module.exports = { startSse, writeSse, endSse }
```

- [ ] **Step 3: Add stream chunk helpers to `src/openai.js`**

Add:

```js
function chatCompletionChunk({ id, model, delta = {}, finishReason = null }) {
  return {
    id,
    object: "chat.completion.chunk",
    created: Math.floor(Date.now() / 1000),
    model,
    choices: [{ index: 0, delta, finish_reason: finishReason }],
  }
}

function streamTextChunks({ model, text }) {
  const id = `chatcmpl-${crypto.randomUUID()}`
  const chunks = [chatCompletionChunk({ id, model, delta: { role: "assistant" } })]
  for (const token of text.match(/\S+\s*/g) || []) {
    chunks.push(chatCompletionChunk({ id, model, delta: { content: token } }))
  }
  chunks.push(chatCompletionChunk({ id, model, delta: {}, finishReason: "stop" }))
  return chunks
}
```

Export both. Ensure `crypto` is already imported or add `const crypto = require("node:crypto")`.

- [ ] **Step 4: Use dependency injection in `src/server.js`**

In `handleChatCompletions`, use:

```js
const getAuthHeader = opts.authHeader || authHeader
const send = opts.sendPrompt || sendPrompt
```

Replace direct calls with `getAuthHeader(opts)` and `send({...})`.

- [ ] **Step 5: Implement SSE branch**

Replace the current `stream === true` rejection with:

```js
if (body.stream === true) {
  opts.observability.counters.activeStreams += 1
  try {
    const authorization = getAuthHeader(opts)
    const result = await send({
      baseUrl: opts.baseUrl,
      directory: opts.directory,
      envFile: opts.envFile,
      authorization,
      providerID: model.providerID,
      modelID: model.modelID,
      prompt: prompt.prompt,
      system: prompt.system,
      sessionID: body.ocq_session_id,
    })
    res.setHeader("x-ocq-session", result.sessionID)
    startSse(res)
    for (const chunk of streamTextChunks({ model: model.model, text: result.text })) writeSse(res, chunk)
    endSse(res)
  } catch (error) {
    if (!res.headersSent) json(res, 500, errorBody(error.message, "server_error"))
  } finally {
    opts.observability.counters.activeStreams -= 1
  }
  return
}
```

Place this after model/prompt parsing. Import `startSse`, `writeSse`, `endSse`, `streamTextChunks`.

- [ ] **Step 6: Run checks**

Run: `npm run check && npm test`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add src/sse.js src/openai.js src/server.js test/server.test.js
git commit -m "feat: add chat completions streaming"
```

---

### Task 4: Session discovery and safe session APIs

**Files:**
- Modify: `src/opencode.js`
- Create: `src/session-api.js`
- Modify: `src/server.js`
- Create: `test/opencode-session-discovery.test.js`
- Modify: `test/server.test.js`

- [ ] **Step 1: Discover OpenCode session endpoints without mutating sessions**

Run read-only probes against local OpenCode server:

```bash
curl -s -u "$OPENCODE_SERVER_USERNAME:$OPENCODE_SERVER_PASSWORD" "http://127.0.0.1:4096/session?directory=$HOME"
curl -s -u "$OPENCODE_SERVER_USERNAME:$OPENCODE_SERVER_PASSWORD" "http://127.0.0.1:4096/session/list?directory=$HOME"
```

Expected: identify the correct list endpoint. Do not POST. Do not attach to active sessions.

- [ ] **Step 2: Lock endpoint shape in a test**

Create `test/opencode-session-discovery.test.js` with mocked assumptions:

```js
const test = require("node:test")
const assert = require("node:assert/strict")
const { listSessions, getSession } = require("../src/opencode")
const { listen, close, createMockOpenCode } = require("./helpers")

test("listSessions normalizes OpenCode sessions", async () => {
  const mock = createMockOpenCode((req, res) => {
    assert.equal(req.method, "GET")
    assert.equal(new URL(req.url, "http://x").pathname, "/session")
    res.writeHead(200, { "content-type": "application/json" })
    res.end(JSON.stringify([{ id: "ses_1", title: "Remote", time: { created: 1, updated: 2 } }]))
  })
  const { server, url } = await listen(mock)
  try {
    const sessions = await listSessions({ baseUrl: url, directory: "/tmp", authorization: "Basic x" })
    assert.deepEqual(sessions, [{ id: "ses_1", title: "Remote", createdAt: 1, updatedAt: 2 }])
  } finally {
    await close(server)
  }
})

test("getSession normalizes transcript parts", async () => {
  const mock = createMockOpenCode((req, res) => {
    assert.equal(req.method, "GET")
    assert.equal(new URL(req.url, "http://x").pathname, "/session/ses_1")
    res.writeHead(200, { "content-type": "application/json" })
    res.end(JSON.stringify({ id: "ses_1", title: "Remote", messages: [{ role: "assistant", parts: [{ type: "text", text: "hi" }] }] }))
  })
  const { server, url } = await listen(mock)
  try {
    const session = await getSession({ baseUrl: url, directory: "/tmp", authorization: "Basic x" }, "ses_1")
    assert.equal(session.id, "ses_1")
    assert.equal(session.messages[0].content, "hi")
  } finally {
    await close(server)
  }
})
```

If discovery shows different paths or shapes, update this exact test to match verified read-only endpoints before implementing.

- [ ] **Step 3: Add session helpers in `src/opencode.js`**

Add:

```js
function normalizeSession(session) {
  return {
    id: session.id,
    title: session.title || session.id,
    createdAt: session.time?.created || session.createdAt || null,
    updatedAt: session.time?.updated || session.updatedAt || null,
  }
}

function normalizeMessage(message) {
  return {
    id: message.id || message.info?.id || null,
    role: message.role || message.info?.role || "assistant",
    content: textFromParts(message.parts),
    raw: message,
  }
}

async function listSessions(opts) {
  const sessions = await request(opts, "GET", "/session")
  return (Array.isArray(sessions) ? sessions : sessions?.sessions || []).map(normalizeSession)
}

async function getSession(opts, sessionID) {
  const session = await request(opts, "GET", `/session/${encodeURIComponent(sessionID)}`)
  return {
    ...normalizeSession(session),
    observeOnly: false,
    messages: (session.messages || session.children || []).map(normalizeMessage),
  }
}
```

Export `listSessions`, `getSession`, `normalizeSession`, `normalizeMessage`.

- [ ] **Step 4: Create `src/session-api.js`**

```js
const { authHeader, listSessions, getSession, sendPrompt } = require("./opencode")

async function handleSessions(req, res, opts, helpers) {
  const authorization = (opts.authHeader || authHeader)(opts)
  const sessions = await (opts.listSessions || listSessions)({ ...opts, authorization })
  opts.observability.counters.sessionPollsTotal += 1
  helpers.json(res, 200, { sessions })
}

async function handleSessionDetail(req, res, opts, helpers, sessionID) {
  const authorization = (opts.authHeader || authHeader)(opts)
  const session = await (opts.getSession || getSession)({ ...opts, authorization }, sessionID)
  opts.observability.counters.sessionWatchesTotal += 1
  helpers.json(res, 200, { session, mode: session.observeOnly ? "observe_only" : "interactive", transport: "polling" })
}

async function handleSessionMessage(req, res, opts, helpers, sessionID) {
  const body = await helpers.readJson(req)
  if (!body.prompt || typeof body.prompt !== "string") {
    helpers.json(res, 400, helpers.errorBody("prompt is required"))
    return
  }
  const authorization = (opts.authHeader || authHeader)(opts)
  const result = await (opts.sendPrompt || sendPrompt)({
    ...opts,
    authorization,
    sessionID,
    prompt: body.prompt,
    providerID: body.providerID || opts.defaultProviderID,
    modelID: body.modelID || opts.defaultModelID,
    system: body.system,
  })
  helpers.json(res, 200, result, { "x-ocq-session": result.sessionID })
}

module.exports = { handleSessions, handleSessionDetail, handleSessionMessage }
```

- [ ] **Step 5: Add route tests**

Append to `test/server.test.js`:

```js
test("GET /ocq/sessions lists all visible sessions", async () => {
  const { server, url } = await listen(createGatewayServer({
    gatewayKey: "secret",
    authHeader: () => "Basic test",
    listSessions: async () => [{ id: "ses_remote", title: "Remote", createdAt: 1, updatedAt: 2 }],
  }))
  try {
    const { response, body } = await jsonFetch(`${url}/ocq/sessions`, { headers: { authorization: "Bearer secret" } })
    assert.equal(response.status, 200)
    assert.equal(body.sessions[0].id, "ses_remote")
  } finally {
    await close(server)
  }
})

test("GET /ocq/sessions/:id observes without mutation", async () => {
  let sent = false
  const { server, url } = await listen(createGatewayServer({
    gatewayKey: "secret",
    authHeader: () => "Basic test",
    getSession: async () => ({ id: "ses_remote", title: "Remote", observeOnly: false, messages: [{ role: "assistant", content: "hi" }] }),
    sendPrompt: async () => { sent = true },
  }))
  try {
    const { response, body } = await jsonFetch(`${url}/ocq/sessions/ses_remote`, { headers: { authorization: "Bearer secret" } })
    assert.equal(response.status, 200)
    assert.equal(body.session.messages[0].content, "hi")
    assert.equal(sent, false)
  } finally {
    await close(server)
  }
})
```

- [ ] **Step 6: Wire session routes in `src/server.js`**

Import handlers and route:

```js
const { handleSessions, handleSessionDetail, handleSessionMessage } = require("./session-api")
```

Add before 404:

```js
const sessionMatch = url.pathname.match(/^\/ocq\/sessions\/([^/]+)$/)
if (req.method === "GET" && url.pathname === "/ocq/sessions") {
  await handleSessions(req, res, opts, { json, readJson, errorBody })
  return
}
if (sessionMatch && req.method === "GET") {
  await handleSessionDetail(req, res, opts, { json, readJson, errorBody }, decodeURIComponent(sessionMatch[1]))
  return
}
if (sessionMatch && req.method === "POST") {
  await handleSessionMessage(req, res, opts, { json, readJson, errorBody }, decodeURIComponent(sessionMatch[1]))
  return
}
```

- [ ] **Step 7: Run checks**

Run: `npm run check && npm test`

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add src/opencode.js src/session-api.js src/server.js test/opencode-session-discovery.test.js test/server.test.js
git commit -m "feat: add safe session APIs"
```

---

### Task 5: Scaffold Vite React TypeScript Tailwind UI

**Files:**
- Modify: `package.json`
- Create: `apps/ui/*`

- [ ] **Step 1: Add root UI scripts**

Update root `package.json` scripts:

```json
{
  "scripts": {
    "check": "node --check bin/ocq && node --check src/*.js && node --check test/*.js",
    "test": "node --test test/*.test.js",
    "ui:dev": "npm --prefix apps/ui run dev",
    "ui:build": "npm --prefix apps/ui run build",
    "ui:test": "npm --prefix apps/ui run test -- --run",
    "check:all": "npm run check && npm test && npm run ui:build && npm run ui:test"
  }
}
```

- [ ] **Step 2: Create `apps/ui/package.json`**

```json
{
  "name": "@patrickfanella/ocq-ui",
  "version": "0.1.0",
  "private": true,
  "type": "module",
  "scripts": {
    "dev": "vite --host 127.0.0.1 --port 5178",
    "build": "tsc -b && vite build",
    "test": "vitest",
    "preview": "vite preview --host 127.0.0.1 --port 4178"
  },
  "dependencies": {
    "@vitejs/plugin-react": "latest",
    "vite": "latest",
    "typescript": "latest",
    "react": "latest",
    "react-dom": "latest",
    "lucide-react": "latest"
  },
  "devDependencies": {
    "@testing-library/jest-dom": "latest",
    "@testing-library/react": "latest",
    "@testing-library/user-event": "latest",
    "@types/react": "latest",
    "@types/react-dom": "latest",
    "@vitest/ui": "latest",
    "autoprefixer": "latest",
    "jsdom": "latest",
    "postcss": "latest",
    "tailwindcss": "latest",
    "vitest": "latest"
  }
}
```

- [ ] **Step 3: Add Vite/Tailwind config files**

Create `apps/ui/vite.config.ts`:

```ts
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/ocq': 'http://127.0.0.1:8088',
      '/v1': 'http://127.0.0.1:8088',
      '/metrics': 'http://127.0.0.1:8088',
    },
  },
  test: { environment: 'jsdom', setupFiles: './src/test/setup.ts' },
})
```

Create `apps/ui/tailwind.config.js`:

```js
export default {
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  theme: { extend: {} },
  plugins: [],
}
```

Create `apps/ui/postcss.config.js`:

```js
export default { plugins: { tailwindcss: {}, autoprefixer: {} } }
```

Create `apps/ui/tsconfig.json`:

```json
{
  "compilerOptions": {
    "target": "ES2022",
    "useDefineForClassFields": true,
    "lib": ["ES2022", "DOM", "DOM.Iterable"],
    "allowJs": false,
    "skipLibCheck": true,
    "esModuleInterop": true,
    "allowSyntheticDefaultImports": true,
    "strict": true,
    "forceConsistentCasingInFileNames": true,
    "module": "ESNext",
    "moduleResolution": "Node",
    "resolveJsonModule": true,
    "isolatedModules": true,
    "noEmit": true,
    "jsx": "react-jsx"
  },
  "include": ["src"],
  "references": []
}
```

- [ ] **Step 4: Add app shell entry files**

Create `apps/ui/index.html`:

```html
<div id="root"></div><script type="module" src="/src/main.tsx"></script>
```

Create `apps/ui/src/main.tsx`:

```tsx
import React from 'react'
import ReactDOM from 'react-dom/client'
import { App } from './App'
import './styles.css'

ReactDOM.createRoot(document.getElementById('root')!).render(<React.StrictMode><App /></React.StrictMode>)
```

Create `apps/ui/src/styles.css`:

```css
@tailwind base;
@tailwind components;
@tailwind utilities;

body { @apply bg-slate-950 text-slate-100 antialiased; }
button, input, select, textarea { @apply outline-none; }
```

Create `apps/ui/src/App.tsx`:

```tsx
export function App() {
  return <div className="min-h-screen bg-slate-950 p-6 text-slate-100">ocq console</div>
}
```

Create `apps/ui/src/test/setup.ts`:

```ts
import '@testing-library/jest-dom/vitest'
```

- [ ] **Step 5: Install and build**

Run:

```bash
npm --prefix apps/ui install
npm run ui:build
```

Expected: Vite build succeeds and creates `apps/ui/dist`.

- [ ] **Step 6: Commit**

```bash
git add package.json apps/ui
git commit -m "feat: scaffold ocq ui app"
```

---

### Task 6: UI gateway client, storage, and tests

**Files:**
- Create: `apps/ui/src/lib/types.ts`
- Create: `apps/ui/src/lib/storage.ts`
- Create: `apps/ui/src/lib/api.ts`
- Create: `apps/ui/src/lib/sse.ts`
- Create: `apps/ui/src/test/storage.test.ts`
- Create: `apps/ui/src/test/api.test.ts`

- [ ] **Step 1: Create shared types**

Create `apps/ui/src/lib/types.ts`:

```ts
export type Settings = { baseUrl: string; apiKey: string; refreshMs: number; theme: 'dark' }
export type MetricsSummary = { uptimeSeconds: number; requestsTotal: number; errorsTotal: number; activeStreams: number; p95LatencyMs: number; latencyBuckets: number[] }
export type GatewayRequest = { ts: string; route: string; status: number; model?: string; durationMs?: number; stream?: boolean; sessionID?: string; error?: string }
export type GatewayLog = { ts: string; level: 'debug' | 'info' | 'warn' | 'error'; message: string; meta?: Record<string, unknown> }
export type SessionSummary = { id: string; title: string; createdAt: number | null; updatedAt: number | null }
export type ChatMessage = { id?: string | null; role: string; content: string }
export type SessionDetail = SessionSummary & { observeOnly: boolean; messages: ChatMessage[] }
```

- [ ] **Step 2: Implement storage**

Create `apps/ui/src/lib/storage.ts`:

```ts
import type { Settings } from './types'

const KEY = 'ocq.ui.settings.v1'
export const DEFAULT_SETTINGS: Settings = { baseUrl: '', apiKey: '', refreshMs: 3000, theme: 'dark' }

export function loadSettings(storage: Storage = localStorage): Settings {
  const raw = storage.getItem(KEY)
  if (!raw) return DEFAULT_SETTINGS
  try { return { ...DEFAULT_SETTINGS, ...JSON.parse(raw) } } catch { return DEFAULT_SETTINGS }
}

export function saveSettings(settings: Settings, storage: Storage = localStorage) {
  storage.setItem(KEY, JSON.stringify(settings))
}
```

- [ ] **Step 3: Test storage**

Create `apps/ui/src/test/storage.test.ts`:

```ts
import { describe, expect, it } from 'vitest'
import { loadSettings, saveSettings } from '../lib/storage'

it('saves and loads settings', () => {
  localStorage.clear()
  saveSettings({ baseUrl: 'http://x', apiKey: 'secret', refreshMs: 1000, theme: 'dark' })
  expect(loadSettings()).toEqual({ baseUrl: 'http://x', apiKey: 'secret', refreshMs: 1000, theme: 'dark' })
})

it('falls back on invalid JSON', () => {
  localStorage.setItem('ocq.ui.settings.v1', '{')
  expect(loadSettings().apiKey).toBe('')
})
```

- [ ] **Step 4: Implement API client**

Create `apps/ui/src/lib/api.ts`:

```ts
import type { GatewayLog, GatewayRequest, MetricsSummary, SessionDetail, SessionSummary } from './types'

export class GatewayError extends Error { constructor(message: string, public status: number) { super(message) } }

export type GatewayClient = ReturnType<typeof createGatewayClient>

export function createGatewayClient(baseUrl: string, apiKey: string) {
  const root = baseUrl.replace(/\/+$/, '')
  async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
    const response = await fetch(`${root}${path}`, { ...init, headers: { authorization: `Bearer ${apiKey}`, 'content-type': 'application/json', ...(init.headers || {}) } })
    const text = await response.text()
    const data = text ? JSON.parse(text) : undefined
    if (!response.ok) throw new GatewayError(data?.error?.message || `HTTP ${response.status}`, response.status)
    return data as T
  }
  return {
    metrics: () => request<MetricsSummary>('/ocq/metrics'),
    requests: () => request<{ requests: GatewayRequest[] }>('/ocq/requests'),
    logs: () => request<{ logs: GatewayLog[] }>('/ocq/logs'),
    sessions: () => request<{ sessions: SessionSummary[] }>('/ocq/sessions'),
    session: (id: string) => request<{ session: SessionDetail; mode: string; transport: string }>(`/ocq/sessions/${encodeURIComponent(id)}`),
    sendSessionMessage: (id: string, prompt: string) => request<{ sessionID: string; messageID?: string; text: string }>(`/ocq/sessions/${encodeURIComponent(id)}/messages`, { method: 'POST', body: JSON.stringify({ prompt }) }),
  }
}
```

- [ ] **Step 5: Implement SSE helper**

Create `apps/ui/src/lib/sse.ts`:

```ts
export async function streamChatCompletion(baseUrl: string, apiKey: string, body: unknown, onToken: (token: string) => void) {
  const response = await fetch(`${baseUrl.replace(/\/+$/, '')}/v1/chat/completions`, {
    method: 'POST',
    headers: { authorization: `Bearer ${apiKey}`, 'content-type': 'application/json' },
    body: JSON.stringify({ ...(body as object), stream: true }),
  })
  if (!response.ok || !response.body) throw new Error(`stream failed: HTTP ${response.status}`)
  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  while (true) {
    const { value, done } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })
    const events = buffer.split('\n\n')
    buffer = events.pop() || ''
    for (const event of events) {
      const line = event.split('\n').find((part) => part.startsWith('data: '))
      if (!line) continue
      const data = line.slice(6)
      if (data === '[DONE]') return
      const parsed = JSON.parse(data)
      const token = parsed.choices?.[0]?.delta?.content
      if (token) onToken(token)
    }
  }
}
```

- [ ] **Step 6: Test API auth header**

Create `apps/ui/src/test/api.test.ts`:

```ts
import { expect, it, vi } from 'vitest'
import { createGatewayClient } from '../lib/api'

it('sends bearer auth to metrics endpoint', async () => {
  const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({ uptimeSeconds: 1, requestsTotal: 0, errorsTotal: 0, activeStreams: 0, p95LatencyMs: 0, latencyBuckets: [] }), { status: 200 }))
  const client = createGatewayClient('http://gateway/', 'secret')
  await client.metrics()
  expect(fetchMock).toHaveBeenCalledWith('http://gateway/ocq/metrics', expect.objectContaining({ headers: expect.objectContaining({ authorization: 'Bearer secret' }) }))
  fetchMock.mockRestore()
})
```

- [ ] **Step 7: Run UI tests**

Run: `npm run ui:test && npm run ui:build`

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add apps/ui/src/lib apps/ui/src/test
git commit -m "feat: add ui gateway client"
```

---

### Task 7: Ops console UI pages

**Files:**
- Modify: `apps/ui/src/App.tsx`
- Create: `apps/ui/src/components/Shell.tsx`
- Create: `apps/ui/src/components/MetricCard.tsx`
- Create: `apps/ui/src/components/StatusBadge.tsx`
- Create: `apps/ui/src/components/SettingsPanel.tsx`
- Create: `apps/ui/src/pages/Overview.tsx`
- Create: `apps/ui/src/pages/Requests.tsx`
- Create: `apps/ui/src/pages/Logs.tsx`
- Create: `apps/ui/src/test/App.test.tsx`

- [ ] **Step 1: Create reusable components**

Create `apps/ui/src/components/MetricCard.tsx`:

```tsx
export function MetricCard({ label, value }: { label: string; value: string | number }) {
  return <div className="rounded-xl border border-slate-800 bg-slate-900 p-4"><p className="text-sm text-slate-400">{label}</p><p className="mt-2 text-2xl font-semibold">{value}</p></div>
}
```

Create `apps/ui/src/components/StatusBadge.tsx`:

```tsx
export function StatusBadge({ ok, children }: { ok: boolean; children: React.ReactNode }) {
  return <span className={ok ? 'rounded-full bg-emerald-500/10 px-2 py-1 text-emerald-300' : 'rounded-full bg-red-500/10 px-2 py-1 text-red-300'}>{children}</span>
}
```

- [ ] **Step 2: Create shell and settings**

Create `apps/ui/src/components/Shell.tsx`:

```tsx
const tabs = ['Overview', 'Requests', 'Logs', 'Sessions', 'Settings'] as const
export type Tab = typeof tabs[number]

export function Shell({ tab, onTab, children }: { tab: Tab; onTab: (tab: Tab) => void; children: React.ReactNode }) {
  return <div className="min-h-screen bg-slate-950 text-slate-100"><aside className="fixed inset-y-0 left-0 w-64 border-r border-slate-800 bg-slate-950 p-4"><h1 className="text-xl font-bold">ocq console</h1><nav className="mt-8 space-y-2">{tabs.map((item) => <button key={item} onClick={() => onTab(item)} className={`block w-full rounded-lg px-3 py-2 text-left ${tab === item ? 'bg-sky-500/20 text-sky-200' : 'text-slate-300 hover:bg-slate-900'}`}>{item}</button>)}</nav></aside><main className="ml-64 p-6">{children}</main></div>
}
```

Create `apps/ui/src/components/SettingsPanel.tsx`:

```tsx
import type { Settings } from '../lib/types'
export function SettingsPanel({ settings, onChange }: { settings: Settings; onChange: (settings: Settings) => void }) {
  return <div className="max-w-2xl space-y-4"><label className="block"><span className="text-sm text-slate-400">Gateway URL</span><input className="mt-1 w-full rounded bg-slate-900 p-2" value={settings.baseUrl} onChange={(e) => onChange({ ...settings, baseUrl: e.target.value })} placeholder="http://127.0.0.1:8088" /></label><label className="block"><span className="text-sm text-slate-400">API key</span><input className="mt-1 w-full rounded bg-slate-900 p-2" type="password" value={settings.apiKey} onChange={(e) => onChange({ ...settings, apiKey: e.target.value })} /></label><label className="block"><span className="text-sm text-slate-400">Refresh ms</span><input className="mt-1 w-full rounded bg-slate-900 p-2" type="number" value={settings.refreshMs} onChange={(e) => onChange({ ...settings, refreshMs: Number(e.target.value) || 3000 })} /></label></div>
}
```

- [ ] **Step 3: Create pages**

Create `apps/ui/src/pages/Overview.tsx`:

```tsx
import type { MetricsSummary } from '../lib/types'
import { MetricCard } from '../components/MetricCard'
export function Overview({ metrics }: { metrics?: MetricsSummary }) {
  return <section><h2 className="text-2xl font-semibold">Overview</h2><div className="mt-6 grid grid-cols-4 gap-4"><MetricCard label="Uptime" value={`${metrics?.uptimeSeconds ?? 0}s`} /><MetricCard label="Requests" value={metrics?.requestsTotal ?? 0} /><MetricCard label="Errors" value={metrics?.errorsTotal ?? 0} /><MetricCard label="p95 latency" value={`${metrics?.p95LatencyMs ?? 0}ms`} /></div></section>
}
```

Create `apps/ui/src/pages/Requests.tsx`:

```tsx
import type { GatewayRequest } from '../lib/types'
export function Requests({ requests = [] }: { requests?: GatewayRequest[] }) {
  return <section><h2 className="text-2xl font-semibold">Requests</h2><table className="mt-6 w-full text-sm"><tbody>{requests.map((req, i) => <tr key={`${req.ts}-${i}`} className="border-b border-slate-800"><td className="py-2">{req.ts}</td><td>{req.route}</td><td>{req.status}</td><td>{req.model}</td><td>{req.durationMs}ms</td></tr>)}</tbody></table></section>
}
```

Create `apps/ui/src/pages/Logs.tsx`:

```tsx
import type { GatewayLog } from '../lib/types'
export function Logs({ logs = [] }: { logs?: GatewayLog[] }) {
  return <section><h2 className="text-2xl font-semibold">Logs</h2><div className="mt-6 space-y-2 font-mono text-sm">{logs.map((log, i) => <div key={`${log.ts}-${i}`} className="rounded bg-slate-900 p-3"><span className="text-slate-500">{log.ts}</span> <span className="text-sky-300">{log.level}</span> {log.message}</div>)}</div></section>
}
```

- [ ] **Step 4: Wire `App.tsx`**

Create polling with `setInterval` and no React Query dependency yet:

```tsx
import { useEffect, useMemo, useState } from 'react'
import { Shell, type Tab } from './components/Shell'
import { SettingsPanel } from './components/SettingsPanel'
import { createGatewayClient } from './lib/api'
import { loadSettings, saveSettings } from './lib/storage'
import type { GatewayLog, GatewayRequest, MetricsSummary } from './lib/types'
import { Logs } from './pages/Logs'
import { Overview } from './pages/Overview'
import { Requests } from './pages/Requests'

export function App() {
  const [tab, setTab] = useState<Tab>('Overview')
  const [settings, setSettings] = useState(loadSettings)
  const [metrics, setMetrics] = useState<MetricsSummary>()
  const [requests, setRequests] = useState<GatewayRequest[]>([])
  const [logs, setLogs] = useState<GatewayLog[]>([])
  const client = useMemo(() => settings.apiKey ? createGatewayClient(settings.baseUrl || window.location.origin, settings.apiKey) : null, [settings])
  useEffect(() => saveSettings(settings), [settings])
  useEffect(() => {
    if (!client) return
    let cancelled = false
    async function refresh() {
      const [m, r, l] = await Promise.all([client!.metrics(), client!.requests(), client!.logs()])
      if (!cancelled) { setMetrics(m); setRequests(r.requests); setLogs(l.logs) }
    }
    refresh().catch(() => undefined)
    const id = setInterval(() => refresh().catch(() => undefined), settings.refreshMs)
    return () => { cancelled = true; clearInterval(id) }
  }, [client, settings.refreshMs])
  return <Shell tab={tab} onTab={setTab}>{tab === 'Overview' && <Overview metrics={metrics} />}{tab === 'Requests' && <Requests requests={requests} />}{tab === 'Logs' && <Logs logs={logs} />}{tab === 'Sessions' && <div>Sessions</div>}{tab === 'Settings' && <SettingsPanel settings={settings} onChange={setSettings} />}</Shell>
}
```

- [ ] **Step 5: Add app smoke test**

Create `apps/ui/src/test/App.test.tsx`:

```tsx
import { render, screen } from '@testing-library/react'
import { expect, it } from 'vitest'
import { App } from '../App'

it('renders ops console navigation', () => {
  render(<App />)
  expect(screen.getByText('ocq console')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Overview' })).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Sessions' })).toBeInTheDocument()
})
```

- [ ] **Step 6: Run UI checks**

Run: `npm run ui:test && npm run ui:build`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add apps/ui/src
git commit -m "feat: build ops console shell"
```

---

### Task 8: Sessions page and chat pane

**Files:**
- Create: `apps/ui/src/components/ChatPane.tsx`
- Create: `apps/ui/src/pages/Sessions.tsx`
- Modify: `apps/ui/src/App.tsx`
- Create: `apps/ui/src/test/Sessions.test.tsx`

- [ ] **Step 1: Create chat pane**

Create `apps/ui/src/components/ChatPane.tsx`:

```tsx
import { useState } from 'react'
import type { ChatMessage, SessionDetail } from '../lib/types'

export function ChatPane({ session, onSend }: { session?: SessionDetail; onSend: (prompt: string) => Promise<ChatMessage | void> }) {
  const [prompt, setPrompt] = useState('')
  const [sending, setSending] = useState(false)
  if (!session) return <div className="rounded-xl border border-slate-800 p-6 text-slate-400">Select a session to watch it like chat.</div>
  async function submit() {
    if (!prompt.trim() || session!.observeOnly) return
    setSending(true)
    try { await onSend(prompt); setPrompt('') } finally { setSending(false) }
  }
  return <div className="flex h-[calc(100vh-8rem)] flex-col rounded-xl border border-slate-800 bg-slate-950"><div className="border-b border-slate-800 p-4"><h3 className="font-semibold">{session.title}</h3><p className="text-sm text-slate-400">{session.observeOnly ? 'observe only' : 'interactive'} · polling safe watch</p></div><div className="flex-1 space-y-4 overflow-auto p-4">{session.messages.map((msg, i) => <div key={msg.id || i} className={msg.role === 'user' ? 'ml-auto max-w-3xl rounded-xl bg-sky-600 p-3' : 'max-w-3xl rounded-xl bg-slate-800 p-3'}><p className="text-xs uppercase text-slate-300">{msg.role}</p><p className="whitespace-pre-wrap">{msg.content}</p></div>)}</div><div className="border-t border-slate-800 p-4"><textarea disabled={session.observeOnly || sending} className="h-24 w-full rounded bg-slate-900 p-3" value={prompt} onChange={(e) => setPrompt(e.target.value)} placeholder={session.observeOnly ? 'Observe-only session' : 'Send into this session'} /><button disabled={session.observeOnly || sending || !prompt.trim()} onClick={submit} className="mt-2 rounded bg-sky-600 px-4 py-2 disabled:opacity-50">Send</button></div></div>
}
```

- [ ] **Step 2: Create sessions page**

Create `apps/ui/src/pages/Sessions.tsx`:

```tsx
import type { GatewayClient } from '../lib/api'
import type { ChatMessage, SessionDetail, SessionSummary } from '../lib/types'
import { ChatPane } from '../components/ChatPane'

export function Sessions({ sessions, selected, onSelect, client, refreshSelected }: { sessions: SessionSummary[]; selected?: SessionDetail; onSelect: (id: string) => void; client: GatewayClient | null; refreshSelected: () => Promise<void> }) {
  async function send(prompt: string): Promise<ChatMessage | void> {
    if (!client || !selected) return
    const result = await client.sendSessionMessage(selected.id, prompt)
    await refreshSelected()
    return { id: result.messageID, role: 'assistant', content: result.text }
  }
  return <section className="grid grid-cols-[20rem_1fr] gap-4"><aside className="rounded-xl border border-slate-800 bg-slate-900 p-3"><h2 className="mb-3 text-xl font-semibold">Sessions</h2>{sessions.map((session) => <button key={session.id} onClick={() => onSelect(session.id)} className="mb-2 block w-full rounded p-3 text-left hover:bg-slate-800"><div className="font-medium">{session.title}</div><div className="text-xs text-slate-500">{session.id}</div></button>)}</aside><ChatPane session={selected} onSend={send} /></section>
}
```

- [ ] **Step 3: Wire sessions into App**

Add state:

```tsx
const [sessions, setSessions] = useState<SessionSummary[]>([])
const [selectedSession, setSelectedSession] = useState<SessionDetail>()
```

Extend refresh:

```tsx
const [m, r, l, s] = await Promise.all([client!.metrics(), client!.requests(), client!.logs(), client!.sessions()])
if (!cancelled) { setMetrics(m); setRequests(r.requests); setLogs(l.logs); setSessions(s.sessions) }
```

Add helper:

```tsx
async function selectSession(id: string) {
  if (!client) return
  const detail = await client.session(id)
  setSelectedSession(detail.session)
}
async function refreshSelected() {
  if (selectedSession) await selectSession(selectedSession.id)
}
```

Render sessions:

```tsx
{tab === 'Sessions' && <Sessions sessions={sessions} selected={selectedSession} onSelect={selectSession} client={client} refreshSelected={refreshSelected} />}
```

- [ ] **Step 4: Test observe-only disables input**

Create `apps/ui/src/test/Sessions.test.tsx`:

```tsx
import { render, screen } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import { ChatPane } from '../components/ChatPane'

it('marks observe-only sessions and disables sending', () => {
  render(<ChatPane session={{ id: 'ses_1', title: 'Remote', createdAt: null, updatedAt: null, observeOnly: true, messages: [{ role: 'assistant', content: 'working' }] }} onSend={vi.fn()} />)
  expect(screen.getByText(/observe only/i)).toBeInTheDocument()
  expect(screen.getByPlaceholderText('Observe-only session')).toBeDisabled()
})
```

- [ ] **Step 5: Run UI checks**

Run: `npm run ui:test && npm run ui:build`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add apps/ui/src
git commit -m "feat: add session chat UI"
```

---

### Task 9: Docs and homelab metrics checklist

**Files:**
- Modify: `README.md`
- Create: `docs/ocq-ui-deploy.md`

- [ ] **Step 1: Update README UI section**

Add:

```md
## UI console

`ocq` includes a separate Vite React UI under `apps/ui`. It is designed to run as its own private service behind reverse-proxy auth and to call `ocq serve` with an API key.

Development:

```sh
npm --prefix apps/ui install
OCQ_GATEWAY_KEY=secret ocq serve --port 8088
npm run ui:dev
```

The UI stores the gateway key in browser `localStorage` for MVP. Do not expose the UI without proxy auth.
```

- [ ] **Step 2: Create deploy doc**

Create `docs/ocq-ui-deploy.md`:

```md
# ocq UI deploy notes

## Shape

- `ocq serve` remains the gateway/API process.
- `apps/ui` builds a separate static app/container.
- Public access must be protected by existing proxy auth.
- UI requests still require `Authorization: Bearer <OCQ_GATEWAY_KEY>`.

## Metrics

Gateway exposes Prometheus-compatible metrics at `/metrics`.

Before wiring NUC monitoring, verify the active NUC metrics path. Expected possibilities:

- Grafana Alloy scraping `/metrics` and remote-writing.
- Prometheus-compatible scraper.

Do not put secrets in scrape labels, logs, or docs.

## Safety checks

- Selecting a remote session only reads session detail.
- Sending a message is the only UI action that mutates a session.
- Session event streaming may be omitted if OpenCode lacks safe non-invasive watch.
- Request logs do not include prompt text by default.
```

- [ ] **Step 3: Run all checks**

Run: `npm run check:all`

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add README.md docs/ocq-ui-deploy.md
git commit -m "docs: document ocq ui deployment"
```

---

### Task 10: End-to-end manual validation

**Files:**
- No required file changes unless bugs are found.

- [ ] **Step 1: Start gateway**

Run:

```bash
OCQ_GATEWAY_KEY=secret ./bin/ocq serve --port 8088
```

Expected: `ocq gateway listening on http://127.0.0.1:8088`.

- [ ] **Step 2: Verify API key rejection**

Run:

```bash
curl -i http://127.0.0.1:8088/ocq/metrics
```

Expected: HTTP 401.

- [ ] **Step 3: Verify metrics scrape**

Run:

```bash
curl -s -H 'Authorization: Bearer secret' http://127.0.0.1:8088/metrics
```

Expected output includes `ocq_gateway_requests_total` and no secret values.

- [ ] **Step 4: Start UI**

Run:

```bash
npm run ui:dev
```

Expected: Vite dev server on `http://127.0.0.1:5178`.

- [ ] **Step 5: Browser smoke**

Open UI, set gateway URL `http://127.0.0.1:8088`, save API key `secret`, verify Overview/Requests/Logs load.

- [ ] **Step 6: Session safety smoke**

Open Sessions, select a remote/CLI-created session, verify transcript appears in chat UI. Do not send a message during observation. Confirm gateway request log shows only `GET /ocq/sessions/:id` for selection.

- [ ] **Step 7: Final status**

Run:

```bash
git status --short
```

Expected: clean working tree.

---

## Self-review notes

- Spec coverage: UI app, separate service, proxy+API key, ops dashboard, all-session session view, non-invasive watch, streaming, metrics, NUC integration docs, tests, and deploy checks are covered.
- Known implementation discovery point: exact OpenCode read-only session endpoints are intentionally verified in Task 4 before implementing session helpers.
- No prompt previews are implemented; this matches the security requirement.
