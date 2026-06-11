const test = require("node:test")
const assert = require("node:assert/strict")
const { listSessions, getSession } = require("../src/opencode")
const { listen, close, createMockOpenCode } = require("./helpers")

test("listSessions normalizes OpenCode sessions", async () => {
  const mock = createMockOpenCode((req, res) => {
    assert.equal(req.method, "GET")
    const url = new URL(req.url, "http://x")
    assert.equal(url.pathname, "/session")
    assert.equal(url.searchParams.get("directory"), "/tmp")
    assert.equal(req.headers.authorization, "Basic x")
    res.writeHead(200, { "content-type": "application/json" })
    res.end(JSON.stringify([{ id: "ses_1", title: "Remote", time: { created: 1, updated: 2 } }]))
  })

  const { server, url } = await listen(mock)
  try {
    const sessions = await listSessions({ baseUrl: url, directory: "/tmp", authorization: "Basic x" })
    assert.deepEqual(sessions, [{ id: "ses_1", title: "Remote", createdAt: 1, updatedAt: 2, observeOnly: true }])
  } finally {
    await close(server)
  }
})

test("getSession normalizes transcript parts", async () => {
  const mock = createMockOpenCode((req, res) => {
    assert.equal(req.method, "GET")
    const url = new URL(req.url, "http://x")
    assert.equal(url.pathname, "/session/ses_1")
    assert.equal(url.searchParams.get("directory"), "/tmp")
    res.writeHead(200, { "content-type": "application/json" })
    res.end(JSON.stringify({ id: "ses_1", title: "Remote", messages: [{ role: "assistant", parts: [{ type: "text", text: "hi" }] }] }))
  })

  const { server, url } = await listen(mock)
  try {
    const session = await getSession({ baseUrl: url, directory: "/tmp", authorization: "Basic x" }, "ses_1")
    assert.equal(session.id, "ses_1")
    assert.equal(session.observeOnly, true)
    assert.equal(session.messages[0].content, "hi")
  } finally {
    await close(server)
  }
})
