package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"git.subcult.tv/PatrickFanella/llama-line/internal/persistence"
)

type adminSearchLogsResponse struct {
	Entries []persistence.AppLogSearchRow `json:"entries"`
}

type adminSearchRequestsResponse struct {
	Entries []persistence.RequestEventSearchRow `json:"entries"`
}

type adminSemanticSearchRequest struct {
	Q      string `json:"q"`
	Client string `json:"client"`
	Model  string `json:"model"`
	Status string `json:"status"`
	Path   string `json:"path"`
	Limit  int    `json:"limit"`
}

type adminSemanticSearchResponse struct {
	Entries []persistence.SemanticSearchRow `json:"entries"`
}

// HandleAdminSearchLogs GET /admin/search/logs
func (s *Server) HandleAdminSearchLogs(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "persistence store not configured", 0, 0)
		return
	}
	params := persistence.AppLogSearchParams{Limit: parseLimit(r.URL.Query().Get("limit"), 100)}
	params.Q = r.URL.Query().Get("q")
	params.Level = r.URL.Query().Get("level")
	params.Client = r.URL.Query().Get("client")
	params.RequestID = r.URL.Query().Get("request_id")
	params.Since = parseRFC3339Ptr(r.URL.Query().Get("since"))
	params.Until = parseRFC3339Ptr(r.URL.Query().Get("until"))
	entries, err := s.store.SearchAppLogs(r.Context(), params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), 0, 0)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(adminSearchLogsResponse{Entries: entries})
}

// HandleAdminSearchRequests GET /admin/search/requests
func (s *Server) HandleAdminSearchRequests(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "persistence store not configured", 0, 0)
		return
	}
	params := persistence.RequestEventSearchParams{Limit: parseLimit(r.URL.Query().Get("limit"), 100)}
	params.Q = r.URL.Query().Get("q")
	params.Client = r.URL.Query().Get("client")
	params.Model = r.URL.Query().Get("model")
	params.Status = r.URL.Query().Get("status")
	params.Path = r.URL.Query().Get("path")
	params.Since = parseRFC3339Ptr(r.URL.Query().Get("since"))
	params.Until = parseRFC3339Ptr(r.URL.Query().Get("until"))
	params.ErrorsOnly = parseBool(r.URL.Query().Get("errors_only"))
	entries, err := s.store.SearchRequestEvents(r.Context(), params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), 0, 0)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(adminSearchRequestsResponse{Entries: entries})
}

// HandleAdminSemanticSearch POST /admin/search/semantic
func (s *Server) HandleAdminSemanticSearch(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "persistence store not configured", 0, 0)
		return
	}
	if s.embedder == nil || !s.cfg.Embeddings.Enabled {
		writeError(w, http.StatusServiceUnavailable, "semantic search not configured", 0, 0)
		return
	}
	var req adminSemanticSearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body", 0, 0)
		return
	}
	if req.Q == "" {
		writeError(w, http.StatusBadRequest, "q is required", 0, 0)
		return
	}
	vec, err := s.embedder.Embed(r.Context(), s.cfg.Embeddings.Model, req.Q)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error(), 0, 0)
		return
	}
	if s.cfg.Embeddings.Dimension > 0 && len(vec) != s.cfg.Embeddings.Dimension {
		writeError(w, http.StatusBadGateway, "embedding dimension mismatch", 0, 0)
		return
	}
	entries, err := s.store.SemanticSearch(r.Context(), vec, persistence.SemanticSearchParams{
		Client: req.Client,
		Model:  req.Model,
		Status: req.Status,
		Path:   req.Path,
		Limit:  req.Limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), 0, 0)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(adminSemanticSearchResponse{Entries: entries})
}

func parseLimit(v string, def int) int {
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	if n < 1 {
		return 1
	}
	if n > 500 {
		return 500
	}
	return n
}

func parseRFC3339Ptr(v string) *time.Time {
	if v == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return nil
	}
	return &t
}

func parseBool(v string) bool {
	if v == "" {
		return false
	}
	b, _ := strconv.ParseBool(v)
	return b
}
