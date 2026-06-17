package controlplane

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"time"
)

type llamaLineCompatTracker struct {
	mu       sync.Mutex
	requests map[string]*llamaLineCompatTrackedRequest
	maxDepth int
}

type llamaLineCompatTrackedRequest struct {
	ID        string
	Model     string
	Path      string
	Client    string
	Status    string
	CreatedAt time.Time
	StartedAt time.Time
	UpdatedAt time.Time
	Cancel    context.CancelFunc
}

func newLlamaLineCompatTracker() *llamaLineCompatTracker {
	return &llamaLineCompatTracker{requests: map[string]*llamaLineCompatTrackedRequest{}, maxDepth: 100}
}

func (t *llamaLineCompatTracker) begin(r *http.Request, model string) (string, context.Context, func(string)) {
	id := firstNonEmpty(r.Header.Get("X-Request-ID"), r.Header.Get("X-Llama-Line-Request-ID"), "llama-"+shortRandomID())
	ctx, cancel := context.WithCancel(r.Context())
	now := time.Now().UTC()
	client := r.RemoteAddr
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		client = forwarded
	}
	t.mu.Lock()
	t.requests[id] = &llamaLineCompatTrackedRequest{ID: id, Model: model, Path: r.URL.Path, Client: client, Status: "queued", CreatedAt: now, UpdatedAt: now, Cancel: cancel}
	t.mu.Unlock()
	t.markRunning(id)
	return id, ctx, func(status string) { t.finish(id, status) }
}

func (t *llamaLineCompatTracker) markRunning(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if item := t.requests[id]; item != nil {
		item.Status = "running"
		item.StartedAt = time.Now().UTC()
		item.UpdatedAt = item.StartedAt
	}
}

func (t *llamaLineCompatTracker) finish(id string, status string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if item := t.requests[id]; item != nil {
		item.Status = status
		item.UpdatedAt = time.Now().UTC()
		delete(t.requests, id)
	}
}

func (t *llamaLineCompatTracker) snapshot() (queued []map[string]any, inflight []map[string]any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, item := range t.requests {
		entry := map[string]any{
			"id":         item.ID,
			"model":      item.Model,
			"path":       item.Path,
			"client":     item.Client,
			"status":     item.Status,
			"created_at": item.CreatedAt,
			"updated_at": item.UpdatedAt,
		}
		if !item.StartedAt.IsZero() {
			entry["started_at"] = item.StartedAt
		}
		switch item.Status {
		case "queued":
			queued = append(queued, entry)
		case "running":
			inflight = append(inflight, entry)
		}
	}
	sort.Slice(queued, func(i, j int) bool {
		return queued[i]["created_at"].(time.Time).Before(queued[j]["created_at"].(time.Time))
	})
	sort.Slice(inflight, func(i, j int) bool {
		return inflight[i]["created_at"].(time.Time).Before(inflight[j]["created_at"].(time.Time))
	})
	return queued, inflight
}

func (t *llamaLineCompatTracker) cancel(id string) bool {
	t.mu.Lock()
	item := t.requests[id]
	t.mu.Unlock()
	if item == nil {
		return false
	}
	item.Cancel()
	t.finish(id, "canceled")
	return true
}

func (t *llamaLineCompatTracker) cancelAll(status string) int {
	t.mu.Lock()
	items := make([]*llamaLineCompatTrackedRequest, 0, len(t.requests))
	for _, item := range t.requests {
		items = append(items, item)
	}
	t.mu.Unlock()
	for _, item := range items {
		item.Cancel()
		t.finish(item.ID, status)
	}
	return len(items)
}

func (t *llamaLineCompatTracker) inFlightIDs() []string {
	_, inflight := t.snapshot()
	ids := make([]string, 0, len(inflight))
	for _, item := range inflight {
		if id, ok := item["id"].(string); ok {
			ids = append(ids, id)
		}
	}
	return ids
}
