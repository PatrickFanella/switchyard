package controlplane

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func (a *API) ocqCompatSessions(w http.ResponseWriter, r *http.Request) {
	status := a.openCode.Status(r.Context())
	sessions := make([]map[string]any, 0, len(status.Sessions))
	for _, session := range status.Sessions {
		sessions = append(sessions, ocqSessionMap(session))
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions, "count": len(sessions), "backend": "switchyard-native"})
}

func (a *API) ocqCompatSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	status := a.openCode.Status(r.Context())
	for _, session := range status.Sessions {
		if session.ID == id {
			writeJSON(w, http.StatusOK, ocqSessionMap(session))
			return
		}
	}
	writeError(w, http.StatusNotFound, "session not found")
}

func ocqSessionMap(session OpenCodeExecutorStatusSession) map[string]any {
	return map[string]any{"id": session.ID, "name": session.Title, "title": session.Title, "status": session.Status, "user": session.User, "created_at": session.CreatedAt, "updated_at": session.UpdatedAt}
}

func (a *API) ocqCompatRequests(w http.ResponseWriter, r *http.Request) {
	limit := compatLimit(r, 100)
	query := firstNonEmpty(r.URL.Query().Get("q"), r.URL.Query().Get("query"))
	requests, err := a.store.ListModelBrokerRequests(ListModelBrokerRequestsFilter{Query: query, Limit: limit})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(requests))
	for _, req := range requests {
		out = append(out, ocqRequestMap(req))
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": out, "count": len(out), "backend": "switchyard-native"})
}

func (a *API) ocqCompatRequest(w http.ResponseWriter, r *http.Request) {
	req, err := a.store.ModelBrokerRequest(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "request not found")
		return
	}
	writeJSON(w, http.StatusOK, ocqRequestMap(req))
}

func ocqRequestMap(req ModelBrokerRequest) map[string]any {
	return map[string]any{
		"id":                  firstNonEmpty(req.CorrelationIDs["ocq_request_id"], req.CorrelationIDs["opencode_request_id"], req.ID),
		"request_id":          req.ID,
		"ocq_request_id":      firstNonEmpty(req.CorrelationIDs["ocq_request_id"], req.CorrelationIDs["opencode_request_id"], req.ID),
		"session_id":          firstNonEmpty(req.CorrelationIDs["ocq_session_id"], req.CorrelationIDs["opencode_session_id"]),
		"ocq_session_id":      firstNonEmpty(req.CorrelationIDs["ocq_session_id"], req.CorrelationIDs["opencode_session_id"]),
		"status":              req.Status,
		"model":               req.Model,
		"path":                req.Path,
		"client":              req.Client,
		"priority":            req.ClientPriority,
		"request_body":        req.RequestBody,
		"response_body":       req.ResponseBody,
		"error":               req.Error,
		"correlation_ids":     req.CorrelationIDs,
		"created_at":          req.CreatedAt,
		"started_at":          req.StartedAt,
		"finished_at":         req.FinishedAt,
		"updated_at":          req.UpdatedAt,
		"cancel_requested_at": req.CancelRequestedAt,
	}
}

func (a *API) ocqCompatAbort(w http.ResponseWriter, r *http.Request) {
	id := firstNonEmpty(r.PathValue("id"), r.URL.Query().Get("id"), r.URL.Query().Get("request_id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "request id required")
		return
	}
	if err := a.compatBroker.cancel(r.Context(), id); err == nil {
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "aborted", "id": id, "request_id": id, "backend": "switchyard-native"})
		return
	} else {
		writeCompatCancelError(w, id, err)
	}
}

func (a *API) ocqCompatSettings(w http.ResponseWriter, r *http.Request) {
	status := a.openCode.Status(context.WithoutCancel(r.Context()))
	writeJSON(w, http.StatusOK, map[string]any{
		"provider":               "switchyard-opencode-executor",
		"backend":                "switchyard-native",
		"workspace":              firstNonEmpty(os.Getenv("MODEL_BROKER_WORKDIR"), os.Getenv("OCQ_DEFAULT_DIRECTORY"), "/workspace"),
		"opencode_server_url":    firstNonEmpty(os.Getenv("OPENCODE_SERVER_URL"), "http://opencode:4096"),
		"executor_status":        status.Status,
		"session_count":          status.SessionCount,
		"active_session_count":   status.ActiveSessionCount,
		"legacy_ocq_compatible":  true,
		"checked_at":             time.Now().UTC(),
		"streaming_mode_default": "buffered-or-native",
	})
}

func (a *API) ocqCompatAudit(w http.ResponseWriter, r *http.Request) {
	a.llamaLineCompatAudit(w, r)
}

func (a *API) ocqCompatLogs(w http.ResponseWriter, r *http.Request) {
	a.llamaLineCompatLogs(w, r)
}

func compatLimit(r *http.Request, fallback int) int {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = fallback
	}
	return limit
}

func isTerminalModelBrokerRequest(status string) bool {
	switch strings.TrimSpace(status) {
	case ModelBrokerRequestCompleted, ModelBrokerRequestFailed, ModelBrokerRequestCanceled, ModelBrokerRequestDropped:
		return true
	default:
		return false
	}
}
