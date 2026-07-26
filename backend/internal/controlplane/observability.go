package controlplane

import (
	"context"
	"net/http"
	"time"
)

type modelBrokerObservabilityResponse struct {
	Status         ModelBrokerStatus                 `json:"status"`
	Totals         map[string]int                    `json:"totals"`
	RecentRequests []modelBrokerObservabilityRequest `json:"recent_requests"`
	RecentErrors   []modelBrokerObservabilityRequest `json:"recent_errors"`
	LatencyMS      modelBrokerLatencySummary         `json:"latency_ms"`
	CheckedAt      time.Time                         `json:"checked_at"`
}

type modelBrokerObservabilityRequest struct {
	ID             string            `json:"id"`
	Status         string            `json:"status"`
	Model          string            `json:"model"`
	Path           string            `json:"path"`
	Client         string            `json:"client"`
	Error          string            `json:"error,omitempty"`
	DurationMS     int64             `json:"duration_ms,omitempty"`
	CorrelationIDs map[string]string `json:"correlation_ids,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

type modelBrokerLatencySummary struct {
	Count int   `json:"count"`
	Avg   int64 `json:"avg"`
	Max   int64 `json:"max"`
}

func (a *API) modelBrokerObservability(w http.ResponseWriter, r *http.Request) {
	since := time.Now().UTC().Add(-24 * time.Hour)
	metrics, err := modelBrokerRuntimeMetrics(a.store, r.Context(), since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	recentRows, err := a.store.ListModelBrokerRequestsContext(r.Context(), ListModelBrokerRequestsFilter{SinceCreated: &since, Limit: 20})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	errorRows, err := a.store.ListModelBrokerRequestsContext(r.Context(), ListModelBrokerRequestsFilter{SinceCreated: &since, Statuses: []string{ModelBrokerRequestFailed, ModelBrokerRequestCanceled}, Limit: 10})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	recent, errors := make([]modelBrokerObservabilityRequest, 0, len(recentRows)), make([]modelBrokerObservabilityRequest, 0, len(errorRows))
	for _, row := range recentRows {
		recent = append(recent, modelBrokerObservabilityItem(row))
	}
	for _, row := range errorRows {
		errors = append(errors, modelBrokerObservabilityItem(row))
	}
	latency := modelBrokerLatencySummary{Count: metrics.ModelLatencyCount, Avg: int64(metrics.ModelLatencyMilliseconds), Max: int64(metrics.ModelLatencyMaxMilliseconds)}
	if latency.Count > 0 {
		latency.Avg /= int64(latency.Count)
	}

	writeJSON(w, http.StatusOK, modelBrokerObservabilityResponse{
		Status:         NewEnvModelBrokerStatusProvider().Status(r.Context()),
		Totals:         metrics.ModelRequestStatusCounts,
		RecentRequests: recent,
		RecentErrors:   errors,
		LatencyMS:      latency,
		CheckedAt:      time.Now().UTC(),
	})
}

type windowedRuntimeMetricsStore interface {
	RuntimeMetricsSince(context.Context, time.Time) (RuntimeMetrics, error)
}

func modelBrokerRuntimeMetrics(store Store, ctx context.Context, since time.Time) (RuntimeMetrics, error) {
	if source, ok := store.(windowedRuntimeMetricsStore); ok {
		return source.RuntimeMetricsSince(ctx, since)
	}
	return store.RuntimeMetrics()
}

func modelBrokerObservabilityItem(row ModelBrokerRequest) modelBrokerObservabilityRequest {
	item := modelBrokerObservabilityRequest{ID: row.ID, Status: row.Status, Model: row.Model, Path: row.Path, Client: row.Client, Error: row.Error, CorrelationIDs: row.CorrelationIDs, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	if row.StartedAt != nil && row.FinishedAt != nil {
		item.DurationMS = row.FinishedAt.Sub(*row.StartedAt).Milliseconds()
	}
	return item
}
