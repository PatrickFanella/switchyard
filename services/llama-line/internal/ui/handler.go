// Package ui provides the embedded web dashboard for llama-line.
// It is served at /ui/ and protected by the admin key.
package ui

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"time"
)

//go:embed static
var staticFiles embed.FS

// Querier is the subset of queue.Queue the UI needs.
type Querier interface {
	Snapshot() QueueSnapshot
}

// QueueSnapshot mirrors queue.QueueSnapshot to avoid a circular import.
type QueueSnapshot struct {
	Depth          int
	MaxDepth       int
	InFlightClient string
	InFlightID     string
	Items          []QueueItem
}

// QueueItem mirrors queue.QueueItem.
type QueueItem struct {
	ID         string    `json:"id"`
	ClientName string    `json:"client"`
	Priority   int       `json:"priority"`
	Position   int       `json:"position"`
	EnqueuedAt time.Time `json:"enqueued_at"`
}

// KeyLister is the subset of keystore.Store the UI needs.
type KeyLister interface {
	List() []KeyMeta
}

// KeyMeta mirrors keystore.KeyMeta.
type KeyMeta struct {
	Name      string    `json:"name"`
	Priority  int       `json:"priority"`
	CreatedAt time.Time `json:"created_at"`
}

// Handler serves the embedded web UI and its API endpoints.
type Handler struct {
	queue   Querier
	keys    KeyLister
	history *RingBuffer
	static  http.Handler
}

// New creates a Handler. history may be nil (no history shown).
// It prefers the built SPA at static/dist; if missing (dev), falls back to static/.
func New(q Querier, k KeyLister, history *RingBuffer) *Handler {
	var sub fs.FS
	if dist, err := fs.Sub(staticFiles, "static/dist"); err == nil {
		if _, statErr := fs.Stat(dist, "index.html"); statErr == nil {
			sub = dist
		}
	}
	if sub == nil {
		sub, _ = fs.Sub(staticFiles, "static")
	}
	return &Handler{
		queue:   q,
		keys:    k,
		history: history,
		static:  spaFileServer(sub),
	}
}

// spaFileServer serves files from fsys and falls back to index.html for
// unknown paths so the React HashRouter (and any future BrowserRouter) work.
func spaFileServer(fsys fs.FS) http.Handler {
	fileSrv := http.FileServer(http.FS(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "" || p == "/" {
			fileSrv.ServeHTTP(w, r)
			return
		}
		// Try the file. If it doesn't exist, serve index.html.
		clean := p
		if len(clean) > 0 && clean[0] == '/' {
			clean = clean[1:]
		}
		if _, err := fs.Stat(fsys, clean); err == nil {
			fileSrv.ServeHTTP(w, r)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		fileSrv.ServeHTTP(w, r2)
	})
}

// ServeHTTP routes /ui/* requests.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/ui/api/status" || r.URL.Path == "/ui/api/status/":
		h.handleStatus(w, r)
	case r.URL.Path == "/ui/api/history" || r.URL.Path == "/ui/api/history/":
		h.handleHistory(w, r)
	case r.URL.Path == "/ui/api/stream" || r.URL.Path == "/ui/api/stream/":
		h.handleStream(w, r)
	default:
		// Strip /ui prefix and serve static files.
		r2 := r.Clone(r.Context())
		r2.URL.Path = r.URL.Path[len("/ui"):]
		if r2.URL.Path == "" {
			r2.URL.Path = "/"
		}
		h.static.ServeHTTP(w, r2)
	}
}

// handleStatus returns a JSON snapshot of the current queue state.
func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	snap := h.queue.Snapshot()
	items := make([]QueueItem, len(snap.Items))
	for i, it := range snap.Items {
		items[i] = it
	}
	resp := map[string]any{
		"queue_depth":      snap.Depth,
		"max_depth":        snap.MaxDepth,
		"in_flight_client": snap.InFlightClient,
		"in_flight_id":     snap.InFlightID,
		"queue":            items,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleHistory returns the request history ring buffer as JSON.
func (h *Handler) handleHistory(w http.ResponseWriter, r *http.Request) {
	var entries []HistoryEntry
	if h.history != nil {
		entries = h.history.Snapshot()
	}
	if entries == nil {
		entries = []HistoryEntry{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"entries": entries})
}

// handleStream pushes queue state as SSE events every 2 seconds.
func (h *Handler) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	send := func() {
		snap := h.queue.Snapshot()
		items := make([]QueueItem, len(snap.Items))
		for i, it := range snap.Items {
			items[i] = it
		}
		payload := map[string]any{
			"queue_depth":      snap.Depth,
			"max_depth":        snap.MaxDepth,
			"in_flight_client": snap.InFlightClient,
			"in_flight_id":     snap.InFlightID,
			"queue":            items,
		}
		b, _ := json.Marshal(payload)
		w.Write([]byte("data: "))
		w.Write(b)
		w.Write([]byte("\n\n"))
		flusher.Flush()
	}

	send() // immediate first push
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			send()
		}
	}
}
