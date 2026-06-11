package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"git.subcult.tv/PatrickFanella/llama-line/internal/config"
	"git.subcult.tv/PatrickFanella/llama-line/internal/keystore"
	"git.subcult.tv/PatrickFanella/llama-line/internal/queue"
)

// AdminHandler returns an http.Handler for all /admin/* routes, protected by admin key auth.
// Mount it at /admin/ in your mux.
func (s *Server) AdminHandler() http.Handler {
	return s.adminAuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/admin/queue":
			s.HandleAdminGetQueue(w, r)
		case r.Method == http.MethodDelete && r.URL.Path == "/admin/queue":
			s.HandleAdminDrainQueue(w, r)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/admin/queue/"):
			s.HandleAdminDropRequest(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/admin/inflight":
			s.HandleAdminGetInflight(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/admin/inflight/cancel":
			s.HandleAdminCancelInflight(w, r)
		// Key management
		case r.Method == http.MethodGet && r.URL.Path == "/admin/keys":
			s.HandleAdminListKeys(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/admin/keys":
			s.HandleAdminCreateKey(w, r)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/admin/keys/"):
			s.HandleAdminRevokeKey(w, r)
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/admin/keys/"):
			s.HandleAdminUpdateKey(w, r)
		// Logs
		case r.Method == http.MethodGet && r.URL.Path == "/admin/logs/recent":
			s.HandleAdminLogsRecent(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/admin/logs/stream":
			s.HandleAdminLogsStream(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/admin/audit/recent":
			s.HandleAdminAuditRecent(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/admin/audit/stream":
			s.HandleAdminAuditStream(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/admin/search/logs":
			s.HandleAdminSearchLogs(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/admin/search/requests":
			s.HandleAdminSearchRequests(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/admin/search/semantic":
			s.HandleAdminSemanticSearch(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/admin/stats/summary":
			s.HandleAdminStatsSummary(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/admin/feedback":
			s.HandleAdminFeedback(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
}

// adminAuthMiddleware validates the admin key from Authorization or X-Admin-Key header.
func (s *Server) adminAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.AdminKey == "" {
			writeError(w, http.StatusForbidden, "admin API not configured", 0, 0)
			return
		}
		token := r.Header.Get("X-Admin-Key")
		if token == "" {
			auth := r.Header.Get("Authorization")
			token, _ = strings.CutPrefix(auth, "Bearer ")
		}
		// EventSource cannot set custom headers; allow ?key= query param for
		// SSE-only endpoints. Only honoured for GET requests so write methods
		// still require a header.
		if token == "" && r.Method == http.MethodGet {
			token = r.URL.Query().Get("key")
		}
		if token != s.cfg.AdminKey {
			writeError(w, http.StatusUnauthorized, "unauthorized", 0, 0)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- response types ---

type adminQueueItem struct {
	ID          string    `json:"id"`
	Client      string    `json:"client"`
	Priority    int       `json:"priority"`
	Position    int       `json:"position"`
	EnqueuedAt  time.Time `json:"enqueued_at"`
	WaitSeconds int       `json:"wait_seconds"`
}

type adminQueueResponse struct {
	Depth    int              `json:"depth"`
	MaxDepth int              `json:"max_depth"`
	Items    []adminQueueItem `json:"items"`
}

type adminInflightResponse struct {
	ID           string     `json:"id,omitempty"`
	Client       string     `json:"client,omitempty"`
	Priority     *int       `json:"priority,omitempty"`
	EnqueuedAt   *time.Time `json:"enqueued_at,omitempty"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	JobSeconds   int        `json:"job_seconds,omitempty"`
	TotalSeconds int        `json:"total_seconds,omitempty"`
	InFlight     bool       `json:"in_flight"`
}

func (s *Server) findQueueEntryForRequestID(id string) (*upstreamEntry, bool) {
	for i := range s.upstreamEntries {
		e := &s.upstreamEntries[i]
		snap := e.queue.Snapshot()
		for _, it := range snap.Items {
			if it.ID == id {
				return e, true
			}
		}
	}
	return nil, false
}

func (s *Server) firstInflightEntry() (*upstreamEntry, queue.QueueSnapshot, bool) {
	for i := range s.upstreamEntries {
		e := &s.upstreamEntries[i]
		snap := e.queue.Snapshot()
		if snap.InFlightClient != "" {
			return e, snap, true
		}
	}
	return nil, queue.QueueSnapshot{}, false
}

// HandleAdminGetQueue returns the current queue state.
// GET /admin/queue
func (s *Server) HandleAdminGetQueue(w http.ResponseWriter, r *http.Request) {
	snap := s.queue.Snapshot()
	items := make([]adminQueueItem, len(snap.Items))
	for i, it := range snap.Items {
		items[i] = adminQueueItem{
			ID:          it.ID,
			Client:      it.ClientName,
			Priority:    it.Priority,
			Position:    it.Position,
			EnqueuedAt:  it.EnqueuedAt,
			WaitSeconds: int(time.Since(it.EnqueuedAt).Seconds()),
		}
	}
	resp := adminQueueResponse{
		Depth:    snap.Depth,
		MaxDepth: snap.MaxDepth,
		Items:    items,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// HandleAdminDropRequest drops a specific queued request by ID.
// DELETE /admin/queue/{id}
func (s *Server) HandleAdminDropRequest(w http.ResponseWriter, r *http.Request) {
	// Extract ID from path: /admin/queue/{id}
	id := strings.TrimPrefix(r.URL.Path, "/admin/queue/")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing request id", 0, 0)
		return
	}

	entry, found := s.findQueueEntryForRequestID(id)
	if !found {
		writeError(w, http.StatusNotFound, "request not found (may be in-flight or already completed)", 0, 0)
		return
	}

	if req := entry.queue.Drop(id); req != nil {
		select {
		case req.StatusCh <- queue.StatusUpdate{
			RequestID: id,
			Status:    "dropped_by_admin",
			Message:   "queue item dropped by admin",
		}:
		default:
		}
	}
	s.log.Info("admin dropped queued request", "id", id, "upstream", entry.cfg.Name)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "dropped", "id": id})
}

// HandleAdminDrainQueue drops all queued requests.
// DELETE /admin/queue
func (s *Server) HandleAdminDrainQueue(w http.ResponseWriter, r *http.Request) {
	total := 0
	for i := range s.upstreamEntries {
		e := &s.upstreamEntries[i]
		dropped := e.queue.Drain()
		for _, req := range dropped {
			select {
			case req.StatusCh <- queue.StatusUpdate{
				RequestID: req.ID,
				Status:    "dropped_by_admin",
				Message:   "queue drained by admin",
			}:
			default:
			}
		}
		if len(dropped) > 0 {
			s.log.Info("admin drained upstream queue", "upstream", e.cfg.Name, "count", len(dropped))
		}
		total += len(dropped)
	}
	s.log.Info("admin drained queues", "count", total)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"status": "drained", "dropped": total})
}

// HandleAdminGetInflight returns the current in-flight request.
// GET /admin/inflight
func (s *Server) HandleAdminGetInflight(w http.ResponseWriter, r *http.Request) {
	_, snap, ok := s.firstInflightEntry()
	resp := adminInflightResponse{InFlight: ok}
	if ok {
		resp.ID = snap.InFlightID
		resp.Client = snap.InFlightClient
		resp.Priority = &snap.InFlightPriority
		if !snap.InFlightEnqueuedAt.IsZero() {
			resp.EnqueuedAt = &snap.InFlightEnqueuedAt
			resp.TotalSeconds = int(time.Since(snap.InFlightEnqueuedAt).Seconds())
		}
		if !snap.InFlightStartedAt.IsZero() {
			resp.StartedAt = &snap.InFlightStartedAt
			resp.JobSeconds = int(time.Since(snap.InFlightStartedAt).Seconds())
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// HandleAdminCancelInflight cancels the current in-flight request.
// POST /admin/inflight/cancel
func (s *Server) HandleAdminCancelInflight(w http.ResponseWriter, r *http.Request) {
	count := 0
	upstreams := make([]string, 0, len(s.upstreamEntries))
	for i := range s.upstreamEntries {
		e := &s.upstreamEntries[i]
		if e.queue.CancelInFlight() {
			count++
			upstreams = append(upstreams, e.cfg.Name)
		}
	}
	if count == 0 {
		writeError(w, http.StatusNotFound, "no in-flight request", 0, 0)
		return
	}
	s.log.Info("admin cancelled in-flight request", "count", count, "upstreams", upstreams)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "cancelled"})
}

// --- Key management ---

// HandleAdminListKeys lists all active key names (never exposes values).
// GET /admin/keys
func (s *Server) HandleAdminListKeys(w http.ResponseWriter, r *http.Request) {
	keys := s.keys.List()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"keys": keys})
}

// HandleAdminCreateKey creates a new API key.
// POST /admin/keys  body: {"name":"...","priority":0}
// Returns the key value exactly once.
func (s *Server) HandleAdminCreateKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		Priority int    `json:"priority"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeError(w, http.StatusBadRequest, "invalid request body: name required", 0, 0)
		return
	}
	k, err := s.keys.Create(req.Name, req.Priority)
	if errors.Is(err, keystore.ErrKeyExists) {
		writeError(w, http.StatusConflict, "key name already exists", 0, 0)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), 0, 0)
		return
	}
	if err := s.persistCreatedKey(k); err != nil {
		_ = s.keys.Revoke(k.Name)
		writeError(w, http.StatusInternalServerError, err.Error(), 0, 0)
		return
	}
	s.log.Info("admin created key", "name", k.Name)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{
		"name":       k.Name,
		"key":        k.Value, // returned exactly once
		"priority":   k.Priority,
		"created_at": k.CreatedAt,
	})
}

// HandleAdminRevokeKey revokes a key by name.
// DELETE /admin/keys/{name}
func (s *Server) HandleAdminRevokeKey(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/admin/keys/")
	if name == "" {
		writeError(w, http.StatusBadRequest, "missing key name", 0, 0)
		return
	}
	if s.keys.Get(name) == nil {
		writeError(w, http.StatusNotFound, "key not found", 0, 0)
		return
	}
	if err := s.persistRevokedKey(name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error(), 0, 0)
		return
	}
	if err := s.keys.Revoke(name); errors.Is(err, keystore.ErrKeyNotFound) {
		writeError(w, http.StatusNotFound, "key not found", 0, 0)
		return
	}
	s.log.Info("admin revoked key", "name", name)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "revoked", "name": name})
}

// HandleAdminUpdateKey updates a key's name and/or priority.
// PATCH /admin/keys/{name}  body: {"name":"...","priority":0}
func (s *Server) HandleAdminUpdateKey(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/admin/keys/")
	if name == "" {
		writeError(w, http.StatusBadRequest, "missing key name", 0, 0)
		return
	}
	var req struct {
		Name     string `json:"name"`
		Priority int    `json:"priority"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", 0, 0)
		return
	}
	old := s.keys.Get(name)
	if old == nil {
		writeError(w, http.StatusNotFound, "key not found", 0, 0)
		return
	}
	err := s.keys.Update(name, req.Name, req.Priority)
	switch {
	case errors.Is(err, keystore.ErrKeyNotFound):
		writeError(w, http.StatusNotFound, "key not found", 0, 0)
	case errors.Is(err, keystore.ErrKeyExists):
		writeError(w, http.StatusConflict, "key name already exists", 0, 0)
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error(), 0, 0)
	default:
		newName := req.Name
		if newName == "" {
			newName = name
		}
		updated := s.keys.Get(newName)
		if updated == nil {
			writeError(w, http.StatusInternalServerError, "updated key not found", 0, 0)
			return
		}
		if err := s.persistUpdatedKey(name, updated); err != nil {
			_ = s.keys.Update(newName, old.Name, old.Priority)
			writeError(w, http.StatusInternalServerError, err.Error(), 0, 0)
			return
		}
		s.log.Info("admin updated key", "name", name)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
	}
}

func (s *Server) persistCreatedKey(k *keystore.Key) error {
	if s.cfg.ConfigPath == "" {
		return nil
	}
	return config.UpsertAPIKeyInFile(s.cfg.ConfigPath, apiKeyFromStore(k))
}

func (s *Server) persistUpdatedKey(oldName string, k *keystore.Key) error {
	if s.cfg.ConfigPath == "" {
		return nil
	}
	return config.RenameAPIKeyInFile(s.cfg.ConfigPath, oldName, apiKeyFromStore(k))
}

func (s *Server) persistRevokedKey(name string) error {
	if s.cfg.ConfigPath == "" {
		return nil
	}
	return config.RemoveAPIKeyFromFile(s.cfg.ConfigPath, name)
}

func apiKeyFromStore(k *keystore.Key) config.APIKey {
	return config.APIKey{
		Name:            k.Name,
		Key:             k.Value,
		Priority:        k.Priority,
		Deduplicate:     k.Deduplicate,
		CacheTTLSecs:    k.CacheTTLSecs,
		CacheMaxEntries: k.CacheMaxEntries,
	}
}
