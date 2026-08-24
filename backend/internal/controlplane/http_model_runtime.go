package controlplane

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (a *API) modelBrokerQueue(w http.ResponseWriter, r *http.Request) {
	queue, err := a.modelRuntime.QueueStatus(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"queue": queue})
}

func (a *API) modelBrokerRequests(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 200")
			return
		}
		limit = parsed
	}
	filter := ListModelBrokerRequestsFilter{Limit: limit, Statuses: splitCSV(r.URL.Query().Get("status"))}
	if cursor := strings.TrimSpace(r.URL.Query().Get("before")); cursor != "" {
		parts := strings.SplitN(cursor, ",", 2)
		if len(parts) != 2 {
			writeError(w, http.StatusBadRequest, "before must be RFC3339 timestamp,id")
			return
		}
		created, err := time.Parse(time.RFC3339Nano, parts[0])
		if err != nil || strings.TrimSpace(parts[1]) == "" {
			writeError(w, http.StatusBadRequest, "before must be RFC3339 timestamp,id")
			return
		}
		filter.BeforeCreated, filter.BeforeID = &created, parts[1]
	}
	rows, err := a.modelRuntime.List(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": rows})
}

func (a *API) modelBrokerRequest(w http.ResponseWriter, r *http.Request) {
	req, err := a.modelRuntime.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "model request not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"request": req})
}

func (a *API) cancelModelBrokerRequest(w http.ResponseWriter, r *http.Request) {
	actor := boundedAuditValue(requestActor(r))
	req, err := a.modelRuntime.CancelWithEvidence(r.Context(), r.PathValue("id"), ModelBrokerMutationEvidence{Actor: actor, Reason: "canceled by operator"})
	writeModelRuntimeMutation(w, req, actor, err)
}

func (a *API) dropModelBrokerRequest(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Reason string `json:"reason"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	reason := boundedAuditValue(firstNonEmpty(strings.TrimSpace(input.Reason), "dropped by operator"))
	actor := boundedAuditValue(requestActor(r))
	req, err := a.modelRuntime.DropWithEvidence(r.Context(), r.PathValue("id"), ModelBrokerMutationEvidence{Actor: actor, Reason: reason})
	writeModelRuntimeMutation(w, req, actor, err)
}

func (a *API) cancelModelBrokerRequests(w http.ResponseWriter, r *http.Request) {
	actor := boundedAuditValue(requestActor(r))
	rows, err := a.modelRuntime.CancelAllWithEvidence(r.Context(), ModelBrokerMutationEvidence{Actor: actor, Reason: "bulk canceled by operator"})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"actor": actor, "canceled": len(rows), "requests": rows})
}

func writeModelRuntimeMutation(w http.ResponseWriter, req ModelBrokerRequest, actor string, err error) {
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "model request not found")
		return
	}
	if errors.Is(err, ErrConflict) {
		writeError(w, http.StatusConflict, "model request state changed")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"actor": actor, "request": req})
}
