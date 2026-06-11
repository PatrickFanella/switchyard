package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"git.subcult.tv/PatrickFanella/llama-line/internal/queue"
)

// SetSSEHeaders sets required headers for an SSE response.
// Must be called before any WriteStatusEvent call.
func SetSSEHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Ollama-Broker", "true")
}

// WriteStatusEvent writes a single SSE data event and flushes.
func WriteStatusEvent(w http.ResponseWriter, update queue.StatusUpdate) error {
	b, err := json.Marshal(update)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
		return err
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}
