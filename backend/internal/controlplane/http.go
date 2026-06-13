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
	store         Store
	runner        *Runner
	adapterProbe  AdapterProbe
	canceler      CancelDispatcher
	scheduler     *Scheduler
	workerPool    *WorkerPool
	asyncDispatch bool
}

func NewAPI(store Store, runner *Runner) *API {
	return &API{store: store, runner: runner, adapterProbe: NewEnvAdapterProbe(), canceler: NewEnvCancelDispatcher()}
}

func (a *API) SetCancelDispatcher(canceler CancelDispatcher) {
	a.canceler = canceler
}

func (a *API) SetScheduler(scheduler *Scheduler) {
	a.scheduler = scheduler
}

func (a *API) SetWorkerPool(workerPool *WorkerPool) {
	a.workerPool = workerPool
}

func (a *API) SetAsyncDispatch(enabled bool) {
	a.asyncDispatch = enabled
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]bool{"ok": true}) })
	mux.HandleFunc("GET /api/adapters", a.adapters)
	mux.HandleFunc("GET /api/scheduler", a.schedulerStatus)
	mux.HandleFunc("GET /api/workers", a.workerStatus)
	mux.HandleFunc("GET /api/pipelines", a.pipelines)
	mux.HandleFunc("GET /api/agent-profiles", a.agentProfiles)
	mux.HandleFunc("GET /api/workflows", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"workflows": workflowsOrEmpty(a.store.ListWorkflows())})
	})
	mux.HandleFunc("POST /api/workflows/open-pilot/run", a.runOpenPilotWorkflow)
	mux.HandleFunc("GET /api/triggers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"triggers": triggersOrEmpty(a.store.ListTriggers())})
	})
	mux.HandleFunc("GET /api/runs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"runs": runsOrEmpty(a.store.ListRuns())})
	})
	mux.HandleFunc("POST /api/runs", a.createRun)
	mux.HandleFunc("GET /api/runs/", a.runAction)
	mux.HandleFunc("POST /api/runs/", a.runAction)
	mux.HandleFunc("POST /api/triggers/", a.triggerAction)
	mux.HandleFunc("PATCH /api/triggers/", a.triggerAction)
	return withCORS(mux)
}

func (a *API) pipelines(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"pipelines": pipelinesOrEmpty(a.runner.Pipelines())})
}

func (a *API) agentProfiles(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"profiles": agentProfilesOrEmpty(a.runner.AgentProfiles())})
}

func (a *API) adapters(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, a.adapterProbe.Payload(ctx))
}

func (a *API) schedulerStatus(w http.ResponseWriter, r *http.Request) {
	if a.scheduler == nil {
		writeJSON(w, http.StatusOK, SchedulerStatus{Enabled: false, Running: false})
		return
	}
	writeJSON(w, http.StatusOK, a.scheduler.Status())
}

func (a *API) workerStatus(w http.ResponseWriter, r *http.Request) {
	if a.workerPool == nil {
		writeJSON(w, http.StatusOK, WorkerPoolStatus{Enabled: false, Running: false})
		return
	}
	writeJSON(w, http.StatusOK, a.workerPool.Status())
}

func (a *API) createRun(w http.ResponseWriter, r *http.Request) {
	var input CreateRunInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	run, err := a.dispatchRun(r.Context(), input)
	if err != nil {
		writeControlError(w, err)
		return
	}
	status := http.StatusCreated
	if a.asyncDispatch {
		w.Header().Set("Location", "/api/runs/"+run.ID)
		status = http.StatusAccepted
	}
	writeJSON(w, status, map[string]Run{"run": run})
}

func (a *API) runOpenPilotWorkflow(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Repo  string `json:"repo"`
		Issue string `json:"issue"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if input.Repo == "" || input.Issue == "" {
		writeError(w, http.StatusBadRequest, "repo and issue are required")
		return
	}
	run, err := a.dispatchRun(r.Context(), CreateRunInput{WorkflowID: string(WorkflowOpenPilot), TriggerID: "manual-open-pilot", Inputs: map[string]string{"repo": input.Repo, "issue": input.Issue}})
	if err != nil {
		writeControlError(w, err)
		return
	}
	status := http.StatusCreated
	if a.asyncDispatch {
		w.Header().Set("Location", "/api/runs/"+run.ID)
		status = http.StatusAccepted
	}
	writeJSON(w, status, map[string]Run{"run": run})
}

func (a *API) dispatchRun(ctx context.Context, input CreateRunInput) (Run, error) {
	if a.asyncDispatch {
		return a.runner.Enqueue(input)
	}
	return a.runner.RunOnce(ctx, input)
}

func (a *API) runAction(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/runs/")
	if id == "" {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	if actionRunID, action, ok := strings.Cut(id, "/"); ok {
		if action == "steps" || strings.HasPrefix(action, "steps/") {
			a.stepAction(w, r, actionRunID, strings.TrimPrefix(action, "steps"))
			return
		}
		switch {
		case r.Method == http.MethodPost && action == "retry":
			a.retryRun(w, r, actionRunID)
			return
		case r.Method == http.MethodPost && action == "cancel":
			a.cancelRun(w, actionRunID)
			return
		default:
			writeError(w, http.StatusNotFound, "run action not found")
			return
		}
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusNotFound, "run action not found")
		return
	}
	detail, err := a.store.RunDetail(id)
	if err != nil {
		writeControlError(w, err)
		return
	}
	detail.Events = eventsOrEmpty(detail.Events)
	detail.Artifacts = artifactsOrEmpty(detail.Artifacts)
	detail.Steps = stepsOrEmpty(detail.Steps)
	writeJSON(w, http.StatusOK, detail)
}

func (a *API) stepAction(w http.ResponseWriter, r *http.Request, runID string, path string) {
	if path == "" || path == "/" {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusNotFound, "step action not found")
			return
		}
		writeJSON(w, http.StatusOK, map[string][]RunStep{"steps": stepsOrEmpty(a.store.ListRunSteps(runID))})
		return
	}
	path = strings.TrimPrefix(path, "/")
	stepID, action, ok := strings.Cut(path, "/")
	if !ok || stepID == "" || r.Method != http.MethodPost {
		writeError(w, http.StatusNotFound, "step action not found")
		return
	}
	switch action {
	case "retry":
		step, err := a.store.RetryRunStep(stepID)
		if err != nil {
			writeControlError(w, err)
			return
		}
		detail, _ := a.store.RunDetail(runID)
		writeJSON(w, http.StatusOK, map[string]any{"run": detail.Run, "step": step})
	case "cancel":
		step, err := a.store.CancelRunStep(stepID)
		if err != nil {
			writeControlError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]RunStep{"step": step})
	default:
		writeError(w, http.StatusNotFound, "step action not found")
	}
}

func (a *API) retryRun(w http.ResponseWriter, r *http.Request, id string) {
	detail, err := a.store.RunDetail(id)
	if err != nil {
		writeControlError(w, err)
		return
	}
	run, err := a.dispatchRun(r.Context(), CreateRunInput{WorkflowID: detail.Run.WorkflowID, TriggerID: detail.Run.TriggerID, Inputs: detail.Run.Inputs})
	if err != nil {
		writeControlError(w, err)
		return
	}
	_, _ = a.store.AddEvent(run.ID, "run.retry", "Retried from "+id)
	status := http.StatusCreated
	if a.asyncDispatch {
		w.Header().Set("Location", "/api/runs/"+run.ID)
		status = http.StatusAccepted
	}
	writeJSON(w, status, map[string]Run{"run": run})
}

func (a *API) cancelRun(w http.ResponseWriter, id string) {
	detail, err := a.store.RunDetail(id)
	if err != nil {
		writeControlError(w, err)
		return
	}
	if detail.Run.Status != RunQueued && detail.Run.Status != RunRunning {
		writeError(w, http.StatusBadRequest, "only queued or running runs can be canceled")
		return
	}
	_, _ = a.store.AddEvent(id, "run.cancel.requested", "Operator requested cancellation")
	if a.canceler != nil {
		for _, event := range cancelRunDetail(context.Background(), a.canceler, detail).Events {
			_, _ = a.store.AddEvent(id, event.Type, event.Message)
		}
	}
	run, err := a.store.FinishRun(id, RunCanceled, "Canceled by operator")
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]Run{"run": run})
}

func workflowsOrEmpty(items []Workflow) []Workflow {
	if items == nil {
		return []Workflow{}
	}
	return items
}

func pipelinesOrEmpty(items []Pipeline) []Pipeline {
	if items == nil {
		return []Pipeline{}
	}
	return items
}

func agentProfilesOrEmpty(items []AgentProfile) []AgentProfile {
	if items == nil {
		return []AgentProfile{}
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

func stepsOrEmpty(items []RunStep) []RunStep {
	if items == nil {
		return []RunStep{}
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

func (a *API) triggerAction(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/triggers/")
	triggerID, action, hasAction := strings.Cut(path, "/")
	if triggerID == "" {
		writeError(w, http.StatusNotFound, "trigger not found")
		return
	}
	if r.Method == http.MethodPatch && !hasAction {
		a.updateTrigger(w, r, triggerID)
		return
	}
	if r.Method != http.MethodPost || !hasAction || action != "run" {
		writeError(w, http.StatusNotFound, "trigger action not found")
		return
	}
	var run Run
	var err error
	if a.asyncDispatch {
		run, err = a.runner.EnqueueTrigger(triggerID)
	} else {
		run, err = a.runner.RunTrigger(r.Context(), triggerID)
	}
	if err != nil {
		writeControlError(w, err)
		return
	}
	status := http.StatusCreated
	if a.asyncDispatch {
		w.Header().Set("Location", "/api/runs/"+run.ID)
		status = http.StatusAccepted
	}
	writeJSON(w, status, map[string]Run{"run": run})
}

func (a *API) updateTrigger(w http.ResponseWriter, r *http.Request, triggerID string) {
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if input.Enabled == nil {
		writeError(w, http.StatusBadRequest, "enabled is required")
		return
	}
	trigger, err := a.store.SetTriggerEnabled(triggerID, *input.Enabled)
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]Trigger{"trigger": trigger})
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
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
