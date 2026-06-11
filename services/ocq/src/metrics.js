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
