// Package metrics provides Prometheus instrumentation for llama-line.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics holds all registered Prometheus metrics for the broker.
type Metrics struct {
	// QueueDepth is the current number of waiting (not in-flight) requests.
	QueueDepth prometheus.Gauge

	// RequestsTotal counts completed requests by client and outcome status.
	// status labels: "success", "error", "timeout", "dropped", "queue_full"
	RequestsTotal *prometheus.CounterVec

	// QueueWaitSeconds tracks time from enqueue to dequeue.
	QueueWaitSeconds *prometheus.HistogramVec

	// InferenceDurationSeconds tracks ollama response time per client.
	InferenceDurationSeconds *prometheus.HistogramVec

	// OllamaUnavailableTotal counts ollama unavailable retry events.
	OllamaUnavailableTotal prometheus.Counter

	// ConnectedClients is the current number of active SSE connections.
	ConnectedClients prometheus.Gauge
}

// New registers and returns all broker metrics using the given registerer.
// Pass prometheus.DefaultRegisterer for production use.
func New(reg prometheus.Registerer) *Metrics {
	factory := promauto.With(reg)
	return &Metrics{
		QueueDepth: factory.NewGauge(prometheus.GaugeOpts{
			Name: "llama_line_queue_depth",
			Help: "Current number of requests waiting in the queue.",
		}),
		RequestsTotal: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "llama_line_requests_total",
			Help: "Total requests processed, by client and outcome status.",
		}, []string{"client", "status"}),
		QueueWaitSeconds: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "llama_line_queue_wait_seconds",
			Help:    "Time from enqueue to dequeue, by client.",
			Buckets: prometheus.DefBuckets,
		}, []string{"client"}),
		InferenceDurationSeconds: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "llama_line_inference_duration_seconds",
			Help:    "Ollama inference response time, by client.",
			Buckets: prometheus.DefBuckets,
		}, []string{"client"}),
		OllamaUnavailableTotal: factory.NewCounter(prometheus.CounterOpts{
			Name: "llama_line_ollama_unavailable_total",
			Help: "Total ollama unavailable events (retry triggers).",
		}),
		ConnectedClients: factory.NewGauge(prometheus.GaugeOpts{
			Name: "llama_line_connected_clients",
			Help: "Current number of active SSE client connections.",
		}),
	}
}

// RecordQueueWait records time a request spent waiting in the queue.
func (m *Metrics) RecordQueueWait(client string, secs float64) {
	m.QueueWaitSeconds.WithLabelValues(client).Observe(secs)
}

// RecordInferenceDuration records ollama inference response time.
func (m *Metrics) RecordInferenceDuration(client string, secs float64) {
	m.InferenceDurationSeconds.WithLabelValues(client).Observe(secs)
}

// RecordOllamaUnavailable increments the ollama unavailable counter.
func (m *Metrics) RecordOllamaUnavailable() {
	m.OllamaUnavailableTotal.Inc()
}

// SetQueueDepth sets the queue depth gauge.
func (m *Metrics) SetQueueDepth(depth float64) {
	m.QueueDepth.Set(depth)
}
