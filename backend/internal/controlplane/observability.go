package controlplane

import (
	"net/http"
	"sort"
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
	rows, err := a.store.ListModelBrokerRequests(ListModelBrokerRequestsFilter{Limit: 100})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	totals := map[string]int{}
	recent := make([]modelBrokerObservabilityRequest, 0, len(rows))
	errors := []modelBrokerObservabilityRequest{}
	latency := modelBrokerLatencySummary{}
	for _, row := range rows {
		totals[row.Status]++
		item := modelBrokerObservabilityItem(row)
		recent = append(recent, item)
		if row.Error != "" || row.Status == ModelBrokerRequestFailed || row.Status == ModelBrokerRequestCanceled {
			errors = append(errors, item)
		}
		if item.DurationMS > 0 {
			latency.Count++
			latency.Avg += item.DurationMS
			if item.DurationMS > latency.Max {
				latency.Max = item.DurationMS
			}
		}
	}
	if latency.Count > 0 {
		latency.Avg = latency.Avg / int64(latency.Count)
	}

	sort.Slice(recent, func(i, j int) bool { return recent[i].CreatedAt.After(recent[j].CreatedAt) })
	sort.Slice(errors, func(i, j int) bool { return errors[i].CreatedAt.After(errors[j].CreatedAt) })
	if len(recent) > 20 {
		recent = recent[:20]
	}
	if len(errors) > 10 {
		errors = errors[:10]
	}

	writeJSON(w, http.StatusOK, modelBrokerObservabilityResponse{
		Status:         NewEnvModelBrokerStatusProvider().Status(r.Context()),
		Totals:         totals,
		RecentRequests: recent,
		RecentErrors:   errors,
		LatencyMS:      latency,
		CheckedAt:      time.Now().UTC(),
	})
}

func modelBrokerObservabilityItem(row ModelBrokerRequest) modelBrokerObservabilityRequest {
	item := modelBrokerObservabilityRequest{ID: row.ID, Status: row.Status, Model: row.Model, Path: row.Path, Client: row.Client, Error: row.Error, CorrelationIDs: row.CorrelationIDs, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	if row.StartedAt != nil && row.FinishedAt != nil {
		item.DurationMS = row.FinishedAt.Sub(*row.StartedAt).Milliseconds()
	}
	return item
}
