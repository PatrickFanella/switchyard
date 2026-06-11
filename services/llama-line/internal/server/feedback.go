package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

type adminFeedbackRequest struct {
	SpanID      string   `json:"span_id"`
	Name        string   `json:"name"`
	Label       string   `json:"label,omitempty"`
	Score       *float64 `json:"score,omitempty"`
	Explanation string   `json:"explanation,omitempty"`
}

func phoenixBaseURL(endpoint string) string {
	return strings.TrimSuffix(endpoint, "/v1/traces")
}

func (s *Server) HandleAdminFeedback(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Phoenix.Enabled || strings.TrimSpace(s.cfg.Phoenix.OTLPEndpoint) == "" {
		writeError(w, http.StatusServiceUnavailable, "phoenix not configured", 0, 0)
		return
	}

	var req adminFeedbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body", 0, 0)
		return
	}
	if strings.TrimSpace(req.SpanID) == "" || strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, "span_id and name are required", 0, 0)
		return
	}

	base := phoenixBaseURL(strings.TrimSpace(s.cfg.Phoenix.OTLPEndpoint))
	if base == "" {
		writeError(w, http.StatusServiceUnavailable, "phoenix endpoint not configured", 0, 0)
		return
	}

	payload := map[string]any{
		"name":           req.Name,
		"annotator_kind": "HUMAN",
		"identifier":     req.SpanID,
		"span_id":        req.SpanID,
		"result":         map[string]any{},
	}
	if req.Label != "" {
		payload["result"].(map[string]any)["label"] = req.Label
	}
	if req.Score != nil {
		payload["result"].(map[string]any)["score"] = *req.Score
	}
	if req.Explanation != "" {
		payload["result"].(map[string]any)["explanation"] = req.Explanation
	}

	body, err := json.Marshal(payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode feedback", 0, 0)
		return
	}

	ctx := r.Context()
	if _, ok := ctx.Deadline(); !ok {
		var cancel func()
		ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
	}
	reqHTTP, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/span_annotations?sync=false", bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to create phoenix request", 0, 0)
		return
	}
	reqHTTP.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(reqHTTP)
	if err != nil {
		s.log.Warn("phoenix feedback request failed", "error", err)
		writeError(w, http.StatusBadGateway, "phoenix feedback request failed", 0, 0)
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeError(w, http.StatusBadGateway, "phoenix rejected feedback", 0, 0)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
