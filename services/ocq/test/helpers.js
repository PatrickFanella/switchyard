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
  await new Promise((resolve, reject) => server.close((error) => (error ? reject(error) : resolve())))
}

async function jsonFetch(url, options = {}) {
  const response = await fetch(url, options)
  const text = await response.text()
  return { response, body: text ? JSON.parse(text) : undefined, text }
}

function createMockOpenCode(handler) {
  return http.createServer((req, res) => {
    Promise.resolve(handler(req, res)).catch((error) => {
      if (res.headersSent || res.writableEnded) {
        res.destroy(error)
        return
      }
      res.writeHead(500, { "content-type": "application/json; charset=utf-8" })
      res.end(JSON.stringify({ error: { message: error.message } }))
    })
  })
}

module.exports = { listen, close, jsonFetch, createMockOpenCode }
