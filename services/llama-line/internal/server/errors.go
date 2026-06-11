package server

import (
	"encoding/json"
	"net/http"
)

type errorResponse struct {
	Error      string `json:"error"`
	QueueDepth int    `json:"queue_depth,omitempty"`
	MaxDepth   int    `json:"max_depth,omitempty"`
}

func writeError(w http.ResponseWriter, status int, msg string, queueDepth, maxDepth int) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Ollama-Broker", "true")
	w.WriteHeader(status)
	resp := errorResponse{Error: msg}
	if queueDepth > 0 || maxDepth > 0 {
		resp.QueueDepth = queueDepth
		resp.MaxDepth = maxDepth
	}
	json.NewEncoder(w).Encode(resp)
}
