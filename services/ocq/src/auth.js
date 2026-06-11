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
  for (const key of Object.keys(copy)) {
    if (key.toLowerCase() === "authorization") copy[key] = "[redacted]"
  }
  return copy
}

module.exports = { hasValidBearer, redactHeaders }
