const { redactHeaders } = require("./auth")

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
    if (entry.upstreamFailure) counters.upstreamFailuresTotal += 1
    if (entry.sessionWatch) counters.sessionWatchesTotal += 1
    if (entry.sessionPoll) counters.sessionPollsTotal += 1
    if (typeof entry.durationMs === "number") {
      latencies.push(entry.durationMs)
      while (latencies.length > 200) latencies.shift()
    }
    requests.push({
      ts: new Date(now()).toISOString(),
      ...entry,
      headers: entry.headers ? redactHeaders(entry.headers) : entry.headers,
    })
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
