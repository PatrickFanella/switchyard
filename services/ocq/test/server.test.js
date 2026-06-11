const test = require("node:test")
const assert = require("node:assert/strict")
const { createGatewayServer } = require("../src/server")
const { listen, close, jsonFetch } = require("./helpers")

function createAbortError() {
  return Object.assign(new Error("request aborted"), { name: "AbortError" })
}

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

test("GET /healthz returns ok for llama-line probes", async () => {
  const { server, url } = await listen(createGatewayServer({ gatewayKey: "secret" }))
  try {
    const { response, body } = await jsonFetch(`${url}/healthz`)
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

test("POST /v1/chat/completions supports stream true", async () => {
  let authHeaderCalls = 0
  let sendPromptCalls = 0
  const { server, url } = await listen(createGatewayServer({
    gatewayKey: "secret",
    authHeader() {
      authHeaderCalls += 1
      return "Basic injected"
    },
    async sendPrompt(input) {
      sendPromptCalls += 1
      assert.equal(input.authorization, "Basic injected")
      assert.equal(input.modelID, "gpt-5.4-mini")
      assert.equal(input.providerID, "openai")
      assert.equal(input.prompt, "user: hi there")
      assert.equal(input.sessionID, "session-incoming")
      assert.equal(input.requestID, "req-stream")
      assert.equal(input.signal.aborted, false)
      return { sessionID: "session-123", text: "hello world" }
    },
  }))
  try {
    const response = await fetch(`${url}/v1/chat/completions`, {
      method: "POST",
      headers: {
        authorization: "Bearer secret",
        "content-type": "application/json",
        "x-request-id": "req-stream",
      },
      body: JSON.stringify({
        model: "openai/gpt-5.4-mini",
        stream: true,
        ocq_session_id: "session-incoming",
        messages: [{ role: "user", content: "hi there" }],
      }),
    })
    const text = await response.text()
    assert.equal(response.status, 200)
    assert.match(response.headers.get("content-type"), /text\/event-stream/)
    assert.equal(response.headers.get("x-ocq-session"), "session-123")
    assert.equal(response.headers.get("x-ocq-request-id"), "req-stream")
    assert.equal(response.headers.get("x-ocq-status"), "completed")
    assert.equal(response.headers.get("x-ocq-streaming-mode"), "buffered")
    assert.equal(authHeaderCalls, 1)
    assert.equal(sendPromptCalls, 1)

    const events = text.trim().split(/\n\n/).map((block) => block.replace(/^data: /, ""))
    const chunks = events.slice(0, -1).map((event) => JSON.parse(event))
    assert.equal(events.at(-1), "[DONE]")
    assert.equal(chunks[0].object, "chat.completion.chunk")
    assert.equal(chunks[0].choices[0].delta.role, "assistant")
    assert.equal(chunks[1].choices[0].delta.content, "hello")
    assert.equal(chunks[2].choices[0].delta.content, " ")
    assert.equal(chunks[3].choices[0].delta.content, "world")
    assert.equal(chunks.at(-1).choices[0].finish_reason, "stop")
  } finally {
    await close(server)
  }
})

test("POST /v1/chat/completions forwards session id for non-streaming requests", async () => {
  let sendPromptCalls = 0
  const { server, url } = await listen(createGatewayServer({
    gatewayKey: "secret",
    authHeader: () => "Basic injected",
    async sendPrompt(input) {
      sendPromptCalls += 1
      assert.equal(input.authorization, "Basic injected")
      assert.equal(input.sessionID, "session-incoming")
      assert.equal(input.requestID, "req-nonstream")
      assert.equal(input.signal.aborted, false)
      return { sessionID: "session-456", text: "nonstream ok" }
    },
  }))
  try {
    const { response, body } = await jsonFetch(`${url}/v1/chat/completions`, {
      method: "POST",
      headers: {
        authorization: "Bearer secret",
        "content-type": "application/json",
        "x-llama-line-request-id": "req-nonstream",
      },
      body: JSON.stringify({
        model: "openai/gpt-5.4-mini",
        stream: false,
        ocq_session_id: "session-incoming",
        messages: [{ role: "user", content: "hi" }],
      }),
    })

    assert.equal(response.status, 200)
    assert.equal(response.headers.get("x-ocq-session"), "session-456")
    assert.equal(response.headers.get("x-ocq-request-id"), "req-nonstream")
    assert.equal(response.headers.get("x-ocq-status"), "completed")
    assert.equal(body.ocq_session_id, "session-456")
    assert.equal(body.choices[0].message.content, "nonstream ok")
    assert.equal(sendPromptCalls, 1)
  } finally {
    await close(server)
  }
})

test("POST /ocq/requests/:requestID/abort aborts in-flight chat completions", async () => {
  let signal
  let startedResolve
  const started = new Promise((resolve) => {
    startedResolve = resolve
  })
  const { server, url } = await listen(createGatewayServer({
    gatewayKey: "secret",
    authHeader: () => "Basic injected",
    async sendPrompt(input) {
      signal = input.signal
      assert.equal(input.requestID, "req-abort")
      startedResolve()
      await new Promise((_, reject) => {
        input.signal.addEventListener("abort", () => reject(createAbortError()), { once: true })
      })
      return { sessionID: "session-aborted", text: "never reached" }
    },
  }))
  try {
    const request = fetch(`${url}/v1/chat/completions`, {
      method: "POST",
      headers: {
        authorization: "Bearer secret",
        "content-type": "application/json",
        "x-request-id": "req-abort",
      },
      body: JSON.stringify({
        model: "openai/gpt-5.4-mini",
        stream: false,
        messages: [{ role: "user", content: "hi" }],
      }),
    })

    await started
    const { response: abortResponse, body: abortBody } = await jsonFetch(`${url}/ocq/requests/req-abort/abort`, {
      method: "POST",
      headers: { authorization: "Bearer secret" },
    })
    assert.equal(abortResponse.status, 202)
    assert.deepEqual(abortBody, { requestID: "req-abort", aborted: true })
    assert.equal(signal.aborted, true)

    const response = await request
    assert.equal(response.status, 500)
  } finally {
    await close(server)
  }
})

test("POST /ocq/sessions/:sessionID/messages/:requestID/abort aborts session messages", async () => {
  let signal
  let startedResolve
  const started = new Promise((resolve) => {
    startedResolve = resolve
  })
  const { server, url } = await listen(createGatewayServer({
    gatewayKey: "secret",
    authHeader: () => "Basic injected",
    async sendPrompt(input) {
      signal = input.signal
      assert.equal(input.sessionID, "ses_remote")
      assert.equal(input.requestID, "req-session-abort")
      startedResolve()
      await new Promise((_, reject) => {
        input.signal.addEventListener("abort", () => reject(createAbortError()), { once: true })
      })
      return { sessionID: "ses_remote", text: "never reached" }
    },
  }))
  try {
    const request = fetch(`${url}/ocq/sessions/ses_remote/messages`, {
      method: "POST",
      headers: {
        authorization: "Bearer secret",
        "content-type": "application/json",
        "x-request-id": "req-session-abort",
      },
      body: JSON.stringify({ prompt: "continue" }),
    })

    await started
    const { response: abortResponse, body: abortBody } = await jsonFetch(`${url}/ocq/sessions/ses_remote/messages/req-session-abort/abort`, {
      method: "POST",
      headers: { authorization: "Bearer secret" },
    })
    assert.equal(abortResponse.status, 202)
    assert.deepEqual(abortBody, { requestID: "req-session-abort", aborted: true })
    assert.equal(signal.aborted, true)

    const response = await request
    assert.equal(response.status, 500)
  } finally {
    await close(server)
  }
})

test("POST /ocq/requests/:requestID/abort returns 404 for unknown requests", async () => {
  const { server, url } = await listen(createGatewayServer({ gatewayKey: "secret" }))
  try {
    const { response, body } = await jsonFetch(`${url}/ocq/requests/missing-request/abort`, {
      method: "POST",
      headers: { authorization: "Bearer secret" },
    })
    assert.equal(response.status, 404)
    assert.equal(body.error.code, "not_found")
  } finally {
    await close(server)
  }
})

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

test("GET /ocq/sessions lists all visible sessions", async () => {
  const { server, url } = await listen(createGatewayServer({
    gatewayKey: "secret",
    authHeader: () => "Basic test",
    listSessions: async () => [{ id: "ses_remote", title: "Remote", createdAt: 1, updatedAt: 2, observeOnly: true }],
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
    getSession: async () => ({ id: "ses_remote", title: "Remote", observeOnly: true, messages: [{ role: "assistant", content: "hi" }] }),
    sendPrompt: async () => { sent = true },
  }))
  try {
    const { response, body } = await jsonFetch(`${url}/ocq/sessions/ses_remote`, { headers: { authorization: "Bearer secret" } })
    assert.equal(response.status, 200)
    assert.equal(body.session.messages[0].content, "hi")
    assert.equal(body.mode, "observe_only")
    assert.equal(sent, false)
  } finally {
    await close(server)
  }
})

test("POST /ocq/sessions/:id is not a mutating alias", async () => {
  let sent = false
  const { server, url } = await listen(createGatewayServer({
    gatewayKey: "secret",
    authHeader: () => "Basic test",
    sendPrompt: async () => { sent = true },
  }))
  try {
    const { response } = await jsonFetch(`${url}/ocq/sessions/ses_remote`, {
      method: "POST",
      headers: {
        authorization: "Bearer secret",
        "content-type": "application/json",
      },
      body: JSON.stringify({ prompt: "continue" }),
    })
    assert.equal(response.status >= 200 && response.status < 300, false)
    assert.equal(sent, false)
  } finally {
    await close(server)
  }
})

test("POST /ocq/sessions/:id/messages forwards prompt", async () => {
  let sent = false
  const { server, url } = await listen(createGatewayServer({
    gatewayKey: "secret",
    authHeader: () => "Basic test",
    sendPrompt: async (input) => {
      sent = true
      assert.equal(input.sessionID, "ses_remote")
      assert.equal(input.prompt, "continue")
      return { sessionID: "ses_remote", text: "ok" }
    },
  }))
  try {
    const { response, body } = await jsonFetch(`${url}/ocq/sessions/ses_remote/messages`, {
      method: "POST",
      headers: {
        authorization: "Bearer secret",
        "content-type": "application/json",
      },
      body: JSON.stringify({ prompt: "continue" }),
    })
    assert.equal(response.status, 200)
    assert.equal(body.sessionID, "ses_remote")
    assert.equal(sent, true)
  } finally {
    await close(server)
  }
})

test("POST /ocq/sessions/:id/messages rejects missing bearer", async () => {
  const { server, url } = await listen(createGatewayServer({
    gatewayKey: "secret",
    authHeader: () => "Basic test",
  }))
  try {
    const { response, body } = await jsonFetch(`${url}/ocq/sessions/ses_remote/messages`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ prompt: "continue" }),
    })
    assert.equal(response.status, 401)
    assert.equal(body.error.code, "unauthorized")
  } finally {
    await close(server)
  }
})
