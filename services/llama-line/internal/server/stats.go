package server

import (
	"encoding/json"
	"net/http"
	"time"
)

// HandleAdminStatsSummary GET /admin/stats/summary
func (s *Server) HandleAdminStatsSummary(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "persistence store not configured", 0, 0)
		return
	}
	now := time.Now().UTC()
	since := now.Add(-time.Hour)
	until := now
	if parsed := parseRFC3339Ptr(r.URL.Query().Get("since")); parsed != nil {
		since = parsed.UTC()
	}
	if parsed := parseRFC3339Ptr(r.URL.Query().Get("until")); parsed != nil {
		until = parsed.UTC()
	}
	if until.Before(since) {
		writeError(w, http.StatusBadRequest, "until must be after since", 0, 0)
		return
	}
	summary, err := s.store.StatsSummary(r.Context(), since, until)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), 0, 0)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(summary)
}
