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
	"time"
)

type llamaLineCompatTracker struct {
	maxDepth int
	runtime  modelCompatibilityRuntime
}

type modelCompatibilityRuntime interface {
	ModelRuntimeControl
	Begin(context.Context, CreateModelBrokerRequestInput) (ModelBrokerRequest, context.Context, error)
	Finish(context.Context, string, string, string, string, map[string]string) (ModelBrokerRequest, error)
	Generate(context.Context, ModelRequest) (ModelResponse, error)
	Stream(context.Context, ModelRequest, func(ModelStreamEvent) error) (ModelResponse, error)
	Emit(context.Context, string, ModelStreamEvent, func(ModelStreamEvent) error) error
	MergeCorrelation(context.Context, string, map[string]string) (ModelBrokerRequest, error)
	StreamingMode(string) string
	ExecutionDecision(string) BackendDecision
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

func (t *llamaLineCompatTracker) begin(r *http.Request, model string, requestBody string) (ModelBrokerRequest, context.Context, func(string, string, string, map[string]string), error) {
	id := firstNonEmpty(r.Header.Get("X-Request-ID"), r.Header.Get("X-Llama-Line-Request-ID"), "llama-"+shortRandomID())
	client := modelBrokerClientIdentityFromRequest(r)
	req, ctx, err := t.runtime.Begin(r.Context(), CreateModelBrokerRequestInput{ID: id, Model: model, Path: r.URL.Path, Client: client.Name, Priority: client.Priority, RequestBody: requestBody})
	if err == nil {
		_, _ = t.runtime.MergeCorrelation(r.Context(), id, map[string]string{"llama_line_request_id": id})
	}
	if err != nil {
		return req, nil, nil, err
	}
	return req, ctx, func(status string, responseBody string, errorText string, correlation map[string]string) {
		t.finish(id, status, responseBody, errorText, correlation)
	}, nil
}

func (t *llamaLineCompatTracker) submitAsync(r *http.Request, model string, messages []ModelMessage, requestBody string) (ModelBrokerRequest, bool, error) {
	id := firstNonEmpty(r.Header.Get("X-Request-ID"), r.Header.Get("X-Llama-Line-Request-ID"), "llama-"+shortRandomID())
	client := modelBrokerClientIdentityFromRequest(r)
	req, admitted, err := t.runtime.Admit(r.Context(), CreateModelBrokerRequestInput{ID: id, Model: model, Path: r.URL.Path, Client: client.Name, Priority: client.Priority, RequestBody: requestBody})
	if err == nil || errors.Is(err, ErrQueueFull) {
		req, _ = t.runtime.MergeCorrelation(r.Context(), req.ID, map[string]string{"llama_line_request_id": req.ID})
	}
	return req, admitted, err
}

func modelBrokerRecoveryStaleAfter() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("MODEL_BROKER_RECOVERY_STALE_AFTER")); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			return parsed
		}
	}
	return 15 * time.Minute
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

func (t *llamaLineCompatTracker) finish(id string, status string, responseBody string, errorText string, correlation map[string]string) {
	_, _ = t.runtime.Finish(context.Background(), id, status, responseBody, errorText, correlation)
}

func mergeStringMaps(base, updates map[string]string) map[string]string {
	merged := cloneMap(base)
	if merged == nil {
		merged = map[string]string{}
	}
	for key, value := range updates {
		if value != "" {
			merged[key] = value
		}
	}
	return merged
}

func (t *llamaLineCompatTracker) snapshot(ctx context.Context) (queued []map[string]any, inflight []map[string]any, status ModelQueueStatus, err error) {
	status, err = t.runtime.QueueStatus(ctx)
	if err != nil {
		return nil, nil, ModelQueueStatus{}, err
	}
	queuedRows, err := t.runtime.List(ctx, ListModelBrokerRequestsFilter{Statuses: []string{ModelBrokerRequestQueued, ModelBrokerRequestCancelRequested}, Limit: t.maxDepth})
	if err != nil {
		return nil, nil, ModelQueueStatus{}, err
	}
	inflightRows, err := t.runtime.List(ctx, ListModelBrokerRequestsFilter{Statuses: []string{ModelBrokerRequestRunning}, Limit: t.maxDepth})
	if err != nil {
		return nil, nil, ModelQueueStatus{}, err
	}
	return modelBrokerRequestsAsMaps(queuedRows), modelBrokerRequestsAsMaps(inflightRows), status, nil
}

func (t *llamaLineCompatTracker) cancel(ctx context.Context, id string) error {
	_, err := t.runtime.CancelWithEvidence(ctx, id, ModelBrokerMutationEvidence{Actor: contextActor(ctx), Reason: "compatibility cancel"})
	return err
}

func (t *llamaLineCompatTracker) cancelAll(ctx context.Context, status string) (int, error) {
	rows, err := t.runtime.CancelAllWithEvidence(ctx, ModelBrokerMutationEvidence{Actor: contextActor(ctx), Reason: status})
	return len(rows), err
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
