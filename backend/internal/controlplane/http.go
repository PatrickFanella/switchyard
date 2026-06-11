package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

type API struct {
	store        Store
	runner       *Runner
	adapterProbe AdapterProbe
}

func NewAPI(store Store, runner *Runner) *API {
	return &API{store: store, runner: runner, adapterProbe: NewEnvAdapterProbe()}
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]bool{"ok": true}) })
	mux.HandleFunc("GET /api/adapters", a.adapters)
	mux.HandleFunc("GET /api/workflows", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"workflows": workflowsOrEmpty(a.store.ListWorkflows())})
	})
	mux.HandleFunc("GET /api/triggers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"triggers": triggersOrEmpty(a.store.ListTriggers())})
	})
	mux.HandleFunc("GET /api/runs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"runs": runsOrEmpty(a.store.ListRuns())})
	})
	mux.HandleFunc("POST /api/runs", a.createRun)
	mux.HandleFunc("GET /api/runs/", a.runDetail)
	mux.HandleFunc("POST /api/triggers/", a.runTrigger)
	return withCORS(mux)
}

func (a *API) adapters(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, a.adapterProbe.Payload(ctx))
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
	detail.Events = eventsOrEmpty(detail.Events)
	detail.Artifacts = artifactsOrEmpty(detail.Artifacts)
	writeJSON(w, http.StatusOK, detail)
}

func workflowsOrEmpty(items []Workflow) []Workflow {
	if items == nil {
		return []Workflow{}
	}
	return items
}

func triggersOrEmpty(items []Trigger) []Trigger {
	if items == nil {
		return []Trigger{}
	}
	return items
}

func runsOrEmpty(items []Run) []Run {
	if items == nil {
		return []Run{}
	}
	return items
}

func eventsOrEmpty(items []RunEvent) []RunEvent {
	if items == nil {
		return []RunEvent{}
	}
	return items
}

func artifactsOrEmpty(items []Artifact) []Artifact {
	if items == nil {
		return []Artifact{}
	}
	return items
}

func staticAdapterPayload() AdapterPayload {
	return AdapterPayload{
		Queues: []AdapterQueue{{
			ID:        "primary",
			Name:      "Primary Control Queue",
			Status:    "healthy",
			Depth:     0,
			Capacity:  100,
			Workers:   1,
			UpdatedAt: adapterSampleTime,
		}},
		Sessions: []AdapterSession{{
			ID:         "session-1",
			Name:       "Operator Session",
			Surface:    "sessions",
			Status:     "active",
			WorkflowID: string(WorkflowOpenPilot),
			StartedAt:  adapterSampleTime,
			UpdatedAt:  adapterSampleTime,
		}},
		Logs: []AdapterLog{{
			ID:        "log-1",
			Level:     "info",
			Source:    "controlplane",
			Message:   "Adapter surface placeholder loaded",
			CreatedAt: adapterSampleTime,
		}},
		Prompts: []AdapterPrompt{{
			ID:          "prompt-1",
			Name:        "Default Operator Prompt",
			Surface:     "prompts",
			Template:    "You are operating Switchyard control surfaces.",
			Variables:   []string{"workflow", "status"},
			Description: "Placeholder prompt contract for UI wiring.",
		}},
		Config: AdapterConfig{
			Environment: "development",
			Version:     "0.0.0-placeholder",
			BaseURL:     "/api",
			Features:    []string{"queues", "sessions", "logs", "prompts", "config", "agents", "skills", "health"},
			Limits: map[string]int{
				"max_concurrent_runs": 1,
				"max_queue_depth":     100,
			},
		},
		Agents: []AdapterAgent{{
			ID:           "agent-1",
			Name:         "Control Plane Agent",
			Role:         "operator",
			Status:       "online",
			Model:        "placeholder",
			Capabilities: []string{"run dispatch", "queue inspection", "surface rendering"},
		}},
		Skills: []AdapterSkill{{
			ID:          "skill-1",
			Name:        "Queue Inspector",
			Category:    "control-plane",
			Status:      "enabled",
			Description: "Placeholder skill contract for future UI surfaces.",
		}},
		Health: AdapterHealth{
			Status: "healthy",
			Services: []AdapterService{{
				Name:    "api",
				Status:  "healthy",
				Details: "static placeholder payload",
			}},
			CheckedAt: adapterSampleTime,
		},
		LlamaLine: AdapterLlamaLine{
			Requests: []AdapterLlamaRequest{{
				ID:         "llama-request-1",
				Model:      "llama3.1",
				Route:      "chat/completions",
				Status:     "completed",
				LatencyMS:  184,
				QueueDepth: 0,
				CreatedAt:  adapterSampleTime,
			}},
			Stats: AdapterLlamaStats{
				ActiveRequests: 2,
				QueuedRequests: 1,
				AvgLatencyMS:   210,
				SuccessRate:    0.98,
			},
			Search: []AdapterLlamaSearchResult{{
				Query:  "operator prompts",
				Match:  "Default Operator Prompt",
				Source: "prompts",
				Score:  0.93,
			}},
			Upstreams: []AdapterLlamaUpstream{{
				Name:       "ollama-local",
				URL:        "http://127.0.0.1:11434",
				Status:     "healthy",
				Pending:    0,
				LastSeenAt: adapterSampleTime,
			}},
		},
		OCQ: AdapterOCQ{
			Requests: []AdapterOCQRequest{{
				ID:        "ocq-request-1",
				Workflow:  string(WorkflowOpenPilot),
				Trigger:   "manual-open-pilot",
				Status:    "running",
				Actor:     "operator",
				StartedAt: adapterSampleTime,
				UpdatedAt: adapterSampleTime,
			}},
			Logs: []AdapterOCQLog{{
				ID:        "ocq-log-1",
				Level:     "info",
				Channel:   "adapter",
				Message:   "OCQ surface placeholder loaded",
				CreatedAt: adapterSampleTime,
			}},
			Sessions: []AdapterOCQSession{{
				ID:        "ocq-session-1",
				Title:     "Primary Operator Session",
				Status:    "active",
				User:      "operator",
				StartedAt: adapterSampleTime,
				UpdatedAt: adapterSampleTime,
			}},
			Settings: AdapterOCQSettings{
				Workspace:     "switchyard",
				Provider:      "local",
				RetentionDays: 7,
				CORSAllowed:   []string{"*"},
			},
		},
		Blacktower: AdapterBlacktower{
			Tasks: []AdapterBlacktowerTask{{
				ID:        "bt-task-1",
				Name:      "Sync operator skills",
				Status:    "running",
				Owner:     "blacktower",
				Progress:  72,
				UpdatedAt: adapterSampleTime,
			}},
			Reports: []AdapterBlacktowerReport{{
				ID:          "bt-report-1",
				Title:       "Scheduler health summary",
				Type:        "health",
				Status:      "ready",
				Findings:    []string{"queue lag within limits", "no failed tasks"},
				GeneratedAt: adapterSampleTime,
			}},
			SchedulerHealth: AdapterSchedulerHealth{
				Status:      "healthy",
				NextRunAt:   adapterSampleTime.Add(30 * time.Minute),
				LastTickAt:  adapterSampleTime,
				SkewSeconds: 0,
			},
		},
		Changemaker: AdapterChangemaker{
			Posts: []AdapterChangemakerPost{{
				ID:          "cm-post-1",
				Title:       "Launch checklist",
				Status:      "published",
				Tags:        []string{"open-pilot", "release"},
				PublishedAt: adapterSampleTime,
			}},
			Service: AdapterChangemakerService{
				Name:      "changemaker",
				Status:    "healthy",
				Version:   "static-placeholder",
				Healthy:   true,
				UpdatedAt: adapterSampleTime,
			},
			RunControl: AdapterChangemakerRunControl{
				Enabled:     true,
				Mode:        "manual",
				ActiveRunID: "run-1",
				QueuedRuns:  1,
			},
		},
	}
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
