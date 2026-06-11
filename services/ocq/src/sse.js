function startSse(res) {
  res.writeHead(200, {
    "content-type": "text/event-stream; charset=utf-8",
    "cache-control": "no-cache, no-transform",
    connection: "keep-alive",
    "x-accel-buffering": "no",
  })
}

function writeSse(res, data) {
  const payload = typeof data === "string" ? data : JSON.stringify(data)
  res.write(`data: ${payload}\n\n`)
}

function endSse(res) {
  res.end()
}

module.exports = { startSse, writeSse, endSse }
