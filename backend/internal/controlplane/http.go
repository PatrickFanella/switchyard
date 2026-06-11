package controlplane

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

type API struct {
	store  *Store
	runner *Runner
}

func NewAPI(store *Store, runner *Runner) *API {
	return &API{store: store, runner: runner}
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]bool{"ok": true}) })
	mux.HandleFunc("GET /api/workflows", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"workflows": a.store.ListWorkflows()})
	})
	mux.HandleFunc("GET /api/triggers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"triggers": a.store.ListTriggers()})
	})
	mux.HandleFunc("GET /api/runs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"runs": a.store.ListRuns()})
	})
	mux.HandleFunc("POST /api/runs", a.createRun)
	mux.HandleFunc("GET /api/runs/", a.runDetail)
	mux.HandleFunc("POST /api/triggers/", a.runTrigger)
	return withCORS(mux)
}

func (a *API) createRun(w http.ResponseWriter, r *http.Request) {
	var input CreateRunInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	run, err := a.runner.RunOnce(r.Context(), input)
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]Run{"run": run})
}

func (a *API) runDetail(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/runs/")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	detail, err := a.store.RunDetail(id)
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (a *API) runTrigger(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/triggers/")
	triggerID, action, ok := strings.Cut(path, "/")
	if !ok || action != "run" || triggerID == "" {
		writeError(w, http.StatusNotFound, "trigger action not found")
		return
	}
	run, err := a.runner.RunTrigger(r.Context(), triggerID)
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]Run{"run": run})
}

func writeControlError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]map[string]string{"error": {"message": message}})
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "content-type, authorization")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
