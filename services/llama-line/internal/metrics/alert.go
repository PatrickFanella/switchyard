package metrics

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// AlertPayload is the JSON body sent to the webhook URL.
type AlertPayload struct {
	Event     string    `json:"event"`
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
	Detail    any       `json:"detail,omitempty"`
}

// Alerter sends webhook notifications for broker alert conditions.
type Alerter struct {
	webhookURL string
	client     *http.Client
	log        *slog.Logger
}

// NewAlerter creates an Alerter. webhookURL may be empty (no-op).
func NewAlerter(webhookURL string, log *slog.Logger) *Alerter {
	return &Alerter{
		webhookURL: webhookURL,
		client:     &http.Client{Timeout: 5 * time.Second},
		log:        log,
	}
}

// Send fires a webhook notification asynchronously.
// If webhookURL is empty, this is a no-op.
func (a *Alerter) Send(event, message string, detail any) {
	if a.webhookURL == "" {
		return
	}
	payload := AlertPayload{
		Event:     event,
		Message:   message,
		Timestamp: time.Now().UTC(),
		Detail:    detail,
	}
	go func() {
		body, err := json.Marshal(payload)
		if err != nil {
			a.log.Warn("alert: marshal failed", "err", err)
			return
		}
		resp, err := a.client.Post(a.webhookURL, "application/json", bytes.NewReader(body))
		if err != nil {
			a.log.Warn("alert: webhook failed", "event", event, "err", err)
			return
		}
		resp.Body.Close()
		a.log.Debug("alert: webhook sent", "event", event, "status", resp.StatusCode)
	}()
}

// AlertQueueFull fires a queue_full alert if depth >= threshold.
func (a *Alerter) AlertQueueFull(depth, maxDepth, threshold int) {
	if threshold <= 0 || depth < threshold {
		return
	}
	a.Send("queue_full", "queue depth reached alert threshold", map[string]int{
		"depth":     depth,
		"max_depth": maxDepth,
		"threshold": threshold,
	})
}

// AlertOllamaUnavailable fires an ollama_unavailable alert.
func (a *Alerter) AlertOllamaUnavailable() {
	a.Send("ollama_unavailable", "ollama is not reachable", nil)
}
