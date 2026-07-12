package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type llamaLineCompatTracker struct {
	mu        sync.Mutex
	requests  map[string]*llamaLineCompatTrackedRequest
	maxDepth  int
	store     Store
	notify    chan struct{}
	workers   int
	enqueueMu sync.Mutex
	ctx       context.Context
	cancelCtx context.CancelFunc
	wg        sync.WaitGroup
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
	workers := modelBrokerQueueWorkers()
	ctx, cancel := context.WithCancel(context.Background())
	tracker := &llamaLineCompatTracker{requests: map[string]*llamaLineCompatTrackedRequest{}, maxDepth: modelBrokerQueueDepth(), workers: workers, store: store, notify: make(chan struct{}, workers), ctx: ctx, cancelCtx: cancel}
	tracker.recoverQueuedAsync()
	for i := 0; i < tracker.workers; i++ {
		tracker.wg.Add(1)
		go tracker.queueWorker()
	}
	tracker.signalQueue()
	return tracker
}

func modelBrokerQueueDepth() int {
	if value, err := strconv.Atoi(strings.TrimSpace(os.Getenv("MODEL_BROKER_QUEUE_MAX_DEPTH"))); err == nil && value > 0 && value <= 200 {
		return value
	}
	return 100
}

func modelBrokerQueueWorkers() int {
	if value, err := strconv.Atoi(strings.TrimSpace(os.Getenv("MODEL_BROKER_QUEUE_WORKERS"))); err == nil && value > 0 && value <= 64 {
		return value
	}
	return 1
}

func (t *llamaLineCompatTracker) begin(r *http.Request, model string, requestBody string) (string, context.Context, func(string, string, string, map[string]string)) {
	id := firstNonEmpty(r.Header.Get("X-Request-ID"), r.Header.Get("X-Llama-Line-Request-ID"), "llama-"+shortRandomID())
	ctx, cancel := context.WithCancel(r.Context())
	now := time.Now().UTC()
	client := modelBrokerClientIdentityFromRequest(r)
	t.mu.Lock()
	t.requests[id] = &llamaLineCompatTrackedRequest{ID: id, Model: model, Path: r.URL.Path, Client: client.Name, Priority: client.Priority, Status: "queued", CreatedAt: now, UpdatedAt: now, Cancel: cancel}
	t.mu.Unlock()
	store := t.store.WithContext(r.Context())
	_, _ = store.CreateModelBrokerRequest(CreateModelBrokerRequestInput{ID: id, Model: model, Path: r.URL.Path, Client: client.Name, Priority: client.Priority, RequestBody: requestBody})
	t.markRunning(store, id)
	return id, ctx, func(status string, responseBody string, errorText string, correlation map[string]string) {
		t.finish(id, status, responseBody, errorText, correlation)
	}
}

func (t *llamaLineCompatTracker) submitAsync(r *http.Request, model string, messages []ModelMessage, requestBody string) (ModelBrokerRequest, bool, error) {
	id := firstNonEmpty(r.Header.Get("X-Request-ID"), r.Header.Get("X-Llama-Line-Request-ID"), "llama-"+shortRandomID())
	client := modelBrokerClientIdentityFromRequest(r)
	store := t.store.WithContext(r.Context())
	t.enqueueMu.Lock()
	defer t.enqueueMu.Unlock()
	if existing, err := store.ModelBrokerRequest(id); err == nil {
		return existing, false, nil
	}
	active, err := store.ListModelBrokerRequests(ListModelBrokerRequestsFilter{Statuses: []string{ModelBrokerRequestQueued, ModelBrokerRequestRunning, ModelBrokerRequestCancelRequested}, Limit: t.maxDepth + 1})
	if err != nil {
		return ModelBrokerRequest{}, false, err
	}
	if len(active) >= t.maxDepth {
		return ModelBrokerRequest{}, false, ErrQueueFull
	}
	req, err := store.CreateModelBrokerRequest(CreateModelBrokerRequestInput{ID: id, Model: model, Path: r.URL.Path, Client: client.Name, Priority: client.Priority, RequestBody: requestBody})
	if err != nil {
		return ModelBrokerRequest{}, false, err
	}
	if isTerminalModelBrokerRequest(req.Status) || req.Status == ModelBrokerRequestRunning {
		return req, false, nil
	}
	t.signalQueue()
	return req, true, nil
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
		_, _, ok := decodeModelBrokerRequestPayload(req)
		if !ok {
			_, _ = t.store.FinishModelBrokerRequest(req.ID, FinishModelBrokerRequestInput{Status: ModelBrokerRequestFailed, Error: "invalid recovered request payload"})
		}
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

func (t *llamaLineCompatTracker) signalQueue() {
	for i := 0; i < t.workers; i++ {
		select {
		case t.notify <- struct{}{}:
		default:
			return
		}
	}
}

func (t *llamaLineCompatTracker) queueWorker() {
	defer t.wg.Done()
	for {
		select {
		case <-t.ctx.Done():
			return
		case <-t.notify:
		}
		for {
			row, err := t.store.ClaimNextModelBrokerRequest()
			if err != nil {
				break
			}
			t.runClaimedAsync(row)
		}
	}
}

func (t *llamaLineCompatTracker) close(ctx context.Context) error {
	t.cancelCtx()
	done := make(chan struct{})
	go func() {
		t.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *llamaLineCompatTracker) runClaimedAsync(row ModelBrokerRequest) {
	id := row.ID
	model, messages, ok := decodeModelBrokerRequestPayload(row)
	if !ok {
		_, _ = t.store.FinishModelBrokerRequest(id, FinishModelBrokerRequestInput{Status: ModelBrokerRequestFailed, Error: "invalid queued request payload"})
		return
	}
	ctx, cancel := context.WithCancel(t.ctx)
	now := time.Now().UTC()
	t.mu.Lock()
	t.requests[id] = &llamaLineCompatTrackedRequest{ID: id, Model: model, Path: row.Path, Client: row.Client, Priority: row.ClientPriority, Status: ModelBrokerRequestRunning, CreatedAt: row.CreatedAt, StartedAt: now, UpdatedAt: now, Cancel: cancel}
	t.mu.Unlock()
	defer cancel()
	response, err := NewEnvModelBroker().Generate(ctx, ModelRequest{Model: model, Messages: messages, RequestID: id})
	if err != nil {
		status := ModelBrokerRequestFailed
		if errors.Is(err, context.Canceled) {
			status = ModelBrokerRequestCanceled
		}
		t.finish(id, status, `{}`, err.Error(), map[string]string{"llama_line_request_id": id})
		return
	}
	if response.CorrelationIDs == nil {
		response.CorrelationIDs = map[string]string{}
	}
	response.CorrelationIDs["llama_line_request_id"] = id
	responseBody, _ := json.Marshal(map[string]any{"content": response.Content, "correlation_ids": response.CorrelationIDs})
	t.finish(id, ModelBrokerRequestCompleted, string(responseBody), "", response.CorrelationIDs)
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

func (t *llamaLineCompatTracker) markRunning(store Store, id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if item := t.requests[id]; item != nil {
		item.Status = "running"
		item.StartedAt = time.Now().UTC()
		item.UpdatedAt = item.StartedAt
	}
	_, _ = store.StartModelBrokerRequest(id)
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

func (t *llamaLineCompatTracker) snapshot(ctx context.Context) (queued []map[string]any, inflight []map[string]any) {
	store := t.store.WithContext(ctx)
	queuedRows, _ := store.ListModelBrokerRequests(ListModelBrokerRequestsFilter{Statuses: []string{ModelBrokerRequestQueued, ModelBrokerRequestCancelRequested}, Limit: t.maxDepth})
	inflightRows, _ := store.ListModelBrokerRequests(ListModelBrokerRequestsFilter{Statuses: []string{ModelBrokerRequestRunning}, Limit: t.maxDepth})
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

func (t *llamaLineCompatTracker) cancel(ctx context.Context, id string) bool {
	store := t.store.WithContext(ctx)
	before, err := store.ModelBrokerRequest(id)
	if err != nil {
		return false
	}
	if _, err := store.CancelModelBrokerRequest(id); err != nil {
		return false
	}
	if before.Status == ModelBrokerRequestQueued {
		_, _ = store.FinishModelBrokerRequest(id, FinishModelBrokerRequestInput{Status: ModelBrokerRequestCanceled, Error: "request canceled before execution", CorrelationIDs: map[string]string{"llama_line_request_id": id}})
	}
	t.mu.Lock()
	item := t.requests[id]
	t.mu.Unlock()
	if item != nil {
		item.Cancel()
	}
	return true
}

func (t *llamaLineCompatTracker) cancelAll(ctx context.Context, status string) int {
	t.mu.Lock()
	items := make([]*llamaLineCompatTrackedRequest, 0, len(t.requests))
	for _, item := range t.requests {
		items = append(items, item)
	}
	t.mu.Unlock()
	count, _ := t.store.WithContext(ctx).CancelAllModelBrokerRequests()
	for _, item := range items {
		item.Cancel()
	}
	if count > 0 {
		return count
	}
	return len(items)
}

func (t *llamaLineCompatTracker) inFlightIDs(ctx context.Context) []string {
	_, inflight := t.snapshot(ctx)
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
