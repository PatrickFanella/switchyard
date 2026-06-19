package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type llamaLineCompatTracker struct {
	mu       sync.Mutex
	requests map[string]*llamaLineCompatTrackedRequest
	maxDepth int
	store    Store
}

type llamaLineCompatTrackedRequest struct {
	ID        string
	Model     string
	Path      string
	Client    string
	Priority  int
	Status    string
	CreatedAt time.Time
	StartedAt time.Time
	UpdatedAt time.Time
	Cancel    context.CancelFunc
}

func newLlamaLineCompatTracker(store Store) *llamaLineCompatTracker {
	tracker := &llamaLineCompatTracker{requests: map[string]*llamaLineCompatTrackedRequest{}, maxDepth: 100, store: store}
	tracker.recoverQueuedAsync()
	return tracker
}

func (t *llamaLineCompatTracker) begin(r *http.Request, model string, requestBody string) (string, context.Context, func(string, string, string, map[string]string)) {
	id := firstNonEmpty(r.Header.Get("X-Request-ID"), r.Header.Get("X-Llama-Line-Request-ID"), "llama-"+shortRandomID())
	ctx, cancel := context.WithCancel(r.Context())
	now := time.Now().UTC()
	client := modelBrokerClientIdentityFromRequest(r)
	t.mu.Lock()
	t.requests[id] = &llamaLineCompatTrackedRequest{ID: id, Model: model, Path: r.URL.Path, Client: client.Name, Priority: client.Priority, Status: "queued", CreatedAt: now, UpdatedAt: now, Cancel: cancel}
	t.mu.Unlock()
	_, _ = t.store.CreateModelBrokerRequest(CreateModelBrokerRequestInput{ID: id, Model: model, Path: r.URL.Path, Client: client.Name, Priority: client.Priority, RequestBody: requestBody})
	t.markRunning(id)
	return id, ctx, func(status string, responseBody string, errorText string, correlation map[string]string) {
		t.finish(id, status, responseBody, errorText, correlation)
	}
}

func (t *llamaLineCompatTracker) submitAsync(r *http.Request, model string, messages []ModelMessage, requestBody string) (ModelBrokerRequest, bool) {
	id := firstNonEmpty(r.Header.Get("X-Request-ID"), r.Header.Get("X-Llama-Line-Request-ID"), "llama-"+shortRandomID())
	client := modelBrokerClientIdentityFromRequest(r)
	req, _ := t.store.CreateModelBrokerRequest(CreateModelBrokerRequestInput{ID: id, Model: model, Path: r.URL.Path, Client: client.Name, Priority: client.Priority, RequestBody: requestBody})
	if isTerminalModelBrokerRequest(req.Status) || req.Status == ModelBrokerRequestRunning {
		return req, false
	}
	go t.runAsync(req.ID, model, messages)
	return req, true
}

func (t *llamaLineCompatTracker) recoverQueuedAsync() {
	running, err := t.store.ListModelBrokerRequests(ListModelBrokerRequestsFilter{Statuses: []string{ModelBrokerRequestRunning}, Limit: t.maxDepth})
	if err == nil {
		staleAfter := modelBrokerRecoveryStaleAfter()
		now := time.Now().UTC()
		for _, req := range running {
			if now.Sub(req.UpdatedAt) < staleAfter {
				continue
			}
			_, _ = t.store.FinishModelBrokerRequest(req.ID, FinishModelBrokerRequestInput{Status: ModelBrokerRequestFailed, Error: "recovered stale running request after API restart", CorrelationIDs: req.CorrelationIDs})
		}
	}
	requests, err := t.store.ListModelBrokerRequests(ListModelBrokerRequestsFilter{Statuses: []string{ModelBrokerRequestQueued}, Limit: t.maxDepth})
	if err != nil {
		return
	}
	for _, req := range requests {
		model, messages, ok := decodeModelBrokerRequestPayload(req)
		if !ok {
			continue
		}
		go t.runAsync(req.ID, model, messages)
	}
}

func modelBrokerRecoveryStaleAfter() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("MODEL_BROKER_RECOVERY_STALE_AFTER")); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			return parsed
		}
	}
	return 15 * time.Minute
}

func (t *llamaLineCompatTracker) runAsync(id string, model string, messages []ModelMessage) {
	row, err := t.store.ModelBrokerRequest(id)
	if err != nil || isTerminalModelBrokerRequest(row.Status) || row.Status == ModelBrokerRequestRunning {
		return
	}
	if row.Status == ModelBrokerRequestCancelRequested {
		_, _ = t.store.FinishModelBrokerRequest(id, FinishModelBrokerRequestInput{Status: ModelBrokerRequestCanceled, Error: "request canceled before execution", CorrelationIDs: map[string]string{"llama_line_request_id": id}})
		return
	}
	_, _ = t.store.StartModelBrokerRequest(id)
	response, err := NewEnvModelBroker().Generate(context.Background(), ModelRequest{Model: model, Messages: messages, RequestID: id})
	if err != nil {
		_, _ = t.store.FinishModelBrokerRequest(id, FinishModelBrokerRequestInput{Status: ModelBrokerRequestFailed, ResponseBody: `{}`, Error: err.Error(), CorrelationIDs: map[string]string{"llama_line_request_id": id}})
		return
	}
	if response.CorrelationIDs == nil {
		response.CorrelationIDs = map[string]string{}
	}
	response.CorrelationIDs["llama_line_request_id"] = id
	responseBody, _ := json.Marshal(map[string]any{"content": response.Content, "correlation_ids": response.CorrelationIDs})
	_, _ = t.store.FinishModelBrokerRequest(id, FinishModelBrokerRequestInput{Status: ModelBrokerRequestCompleted, ResponseBody: string(responseBody), CorrelationIDs: response.CorrelationIDs})
}

func decodeModelBrokerRequestPayload(req ModelBrokerRequest) (string, []ModelMessage, bool) {
	var input struct {
		Model    string         `json:"model"`
		Messages []ModelMessage `json:"messages"`
	}
	if err := json.Unmarshal([]byte(req.RequestBody), &input); err != nil {
		return "", nil, false
	}
	model := firstNonEmpty(strings.TrimSpace(input.Model), req.Model)
	return model, input.Messages, model != "" && len(input.Messages) > 0
}

func (t *llamaLineCompatTracker) markRunning(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if item := t.requests[id]; item != nil {
		item.Status = "running"
		item.StartedAt = time.Now().UTC()
		item.UpdatedAt = item.StartedAt
	}
	_, _ = t.store.StartModelBrokerRequest(id)
}

func (t *llamaLineCompatTracker) finish(id string, status string, responseBody string, errorText string, correlation map[string]string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if item := t.requests[id]; item != nil {
		item.Status = status
		item.UpdatedAt = time.Now().UTC()
		delete(t.requests, id)
	}
	_, _ = t.store.FinishModelBrokerRequest(id, FinishModelBrokerRequestInput{Status: status, ResponseBody: responseBody, Error: errorText, CorrelationIDs: correlation})
}

func (t *llamaLineCompatTracker) snapshot() (queued []map[string]any, inflight []map[string]any) {
	queuedRows, _ := t.store.ListModelBrokerRequests(ListModelBrokerRequestsFilter{Statuses: []string{ModelBrokerRequestQueued, ModelBrokerRequestCancelRequested}, Limit: t.maxDepth})
	inflightRows, _ := t.store.ListModelBrokerRequests(ListModelBrokerRequestsFilter{Statuses: []string{ModelBrokerRequestRunning}, Limit: t.maxDepth})
	if len(queuedRows) > 0 || len(inflightRows) > 0 {
		return modelBrokerRequestsAsMaps(queuedRows), modelBrokerRequestsAsMaps(inflightRows)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, item := range t.requests {
		entry := map[string]any{
			"id":         item.ID,
			"model":      item.Model,
			"path":       item.Path,
			"client":     item.Client,
			"priority":   item.Priority,
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
	sort.Slice(queued, func(i, j int) bool { return compatQueueItemLess(queued[i], queued[j]) })
	sort.Slice(inflight, func(i, j int) bool {
		return inflight[i]["created_at"].(time.Time).Before(inflight[j]["created_at"].(time.Time))
	})
	return queued, inflight
}

func (t *llamaLineCompatTracker) cancel(id string) bool {
	if _, err := t.store.CancelModelBrokerRequest(id); err != nil {
		return false
	}
	t.mu.Lock()
	item := t.requests[id]
	t.mu.Unlock()
	if item != nil {
		item.Cancel()
	}
	return true
}

func (t *llamaLineCompatTracker) cancelAll(status string) int {
	t.mu.Lock()
	items := make([]*llamaLineCompatTrackedRequest, 0, len(t.requests))
	for _, item := range t.requests {
		items = append(items, item)
	}
	t.mu.Unlock()
	count, _ := t.store.CancelAllModelBrokerRequests()
	for _, item := range items {
		item.Cancel()
	}
	if count > 0 {
		return count
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

func modelBrokerRequestsAsMaps(requests []ModelBrokerRequest) []map[string]any {
	out := make([]map[string]any, 0, len(requests))
	for _, req := range requests {
		entry := map[string]any{"id": req.ID, "model": req.Model, "path": req.Path, "client": req.Client, "priority": req.ClientPriority, "status": req.Status, "created_at": req.CreatedAt, "updated_at": req.UpdatedAt}
		if req.StartedAt != nil {
			entry["started_at"] = *req.StartedAt
		}
		if req.FinishedAt != nil {
			entry["finished_at"] = *req.FinishedAt
		}
		if req.CancelRequestedAt != nil {
			entry["cancel_requested_at"] = *req.CancelRequestedAt
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return compatQueueItemLess(out[i], out[j]) })
	return out
}

func compatQueueItemLess(left, right map[string]any) bool {
	leftPriority, _ := left["priority"].(int)
	rightPriority, _ := right["priority"].(int)
	if leftPriority != rightPriority {
		return leftPriority > rightPriority
	}
	leftCreated, leftOK := left["created_at"].(time.Time)
	rightCreated, rightOK := right["created_at"].(time.Time)
	if leftOK && rightOK {
		return leftCreated.Before(rightCreated)
	}
	return false
}
