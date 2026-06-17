package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type API struct {
	store         Store
	runner        *Runner
	adapterProbe  AdapterProbe
	modelBroker   ModelBrokerStatusProvider
	compatBroker  *llamaLineCompatTracker
	openCode      OpenCodeExecutorStatusProvider
	canceler      CancelDispatcher
	scheduler     *Scheduler
	workerPool    *WorkerPool
	asyncDispatch bool
}

func NewAPI(store Store, runner *Runner) *API {
	return &API{store: store, runner: runner, adapterProbe: NewEnvAdapterProbe(), modelBroker: NewEnvModelBrokerStatusProvider(), compatBroker: newLlamaLineCompatTracker(store), openCode: NewEnvOpenCodeExecutorStatusProvider(), canceler: NewEnvCancelDispatcher()}
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
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, http.StatusOK, map[string]bool{"ok": true}) })
	mux.HandleFunc("GET /broker/status", a.llamaLineCompatStatus)
	mux.HandleFunc("POST /v1/chat/completions", a.llamaLineCompatChatCompletions)
	mux.HandleFunc("POST /v1/completions", a.llamaLineCompatCompletions)
	mux.HandleFunc("POST /api/chat", a.llamaLineCompatChat)
	mux.HandleFunc("POST /api/generate", a.llamaLineCompatGenerate)
	mux.HandleFunc("POST /api/embed", a.llamaLineCompatEmbeddings)
	mux.HandleFunc("POST /api/embeddings", a.llamaLineCompatEmbeddings)
	mux.HandleFunc("POST /v1/embeddings", a.llamaLineCompatEmbeddings)
	mux.HandleFunc("GET /api/tags", a.llamaLineCompatTags)
	mux.HandleFunc("GET /v1/models", a.llamaLineCompatModels)
	mux.HandleFunc("GET /admin/queue", a.llamaLineCompatQueue)
	mux.HandleFunc("DELETE /admin/queue", a.llamaLineCompatQueue)
	mux.HandleFunc("DELETE /admin/queue/{id}", a.llamaLineCompatQueueItem)
	mux.HandleFunc("GET /admin/inflight", a.llamaLineCompatInflight)
	mux.HandleFunc("POST /admin/inflight/cancel", a.llamaLineCompatInflightCancel)
	mux.HandleFunc("GET /admin/logs", a.llamaLineCompatLogs)
	mux.HandleFunc("GET /admin/logs/recent", a.llamaLineCompatLogs)
	mux.HandleFunc("GET /admin/audit", a.llamaLineCompatAudit)
	mux.HandleFunc("GET /admin/audit/recent", a.llamaLineCompatAudit)
	mux.HandleFunc("GET /admin/search", a.llamaLineCompatSearch)
	mux.HandleFunc("GET /admin/stats/summary", a.llamaLineCompatStatsSummary)
	mux.HandleFunc("GET /admin/feedback", a.llamaLineCompatFeedback)
	mux.HandleFunc("POST /admin/feedback", a.llamaLineCompatFeedback)
	mux.HandleFunc("GET /ui/", a.llamaLineCompatUI)
	mux.HandleFunc("GET /ui/api/status", a.llamaLineCompatStatus)
	mux.HandleFunc("GET /ui/api/history", a.llamaLineCompatHistory)
	mux.HandleFunc("GET /ui/api/logs", a.llamaLineCompatLogs)
	mux.HandleFunc("GET /ui/api/search", a.llamaLineCompatSearch)
	mux.HandleFunc("GET /ui/api/config", a.llamaLineCompatConfig)
	mux.HandleFunc("GET /api/history", a.llamaLineCompatHistory)
	mux.HandleFunc("GET /api/history/{id}", a.llamaLineCompatHistoryItem)
	mux.HandleFunc("GET /api/search", a.llamaLineCompatSearch)
	mux.HandleFunc("GET /api/logs", a.llamaLineCompatLogs)
	mux.HandleFunc("GET /api/config", a.llamaLineCompatConfig)
	mux.HandleFunc("GET /metrics", a.llamaLineCompatMetrics)
	mux.HandleFunc("GET /api/adapters", a.adapters)
	mux.HandleFunc("GET /api/model-broker/status", a.modelBrokerStatus)
	mux.HandleFunc("GET /api/opencode-executor/status", a.openCodeExecutorStatus)
	mux.HandleFunc("GET /api/open-pilot/states", a.openPilotStates)
	mux.HandleFunc("GET /api/open-pilot/state", a.openPilotState)
	mux.HandleFunc("POST /api/open-pilot/state/pause", a.pauseOpenPilotState)
	mux.HandleFunc("POST /api/open-pilot/state/resume", a.resumeOpenPilotState)
	mux.HandleFunc("GET /api/scheduler", a.schedulerStatus)
	mux.HandleFunc("GET /api/workers", a.workerStatus)
	mux.HandleFunc("GET /api/operator-safety", a.operatorSafety)
	mux.HandleFunc("GET /api/pipelines", a.pipelines)
	mux.HandleFunc("GET /api/agent-profiles", a.agentProfiles)
	mux.HandleFunc("GET /api/workflow-definitions", a.workflowDefinitions)
	mux.HandleFunc("GET /api/workflow-definitions/{id}", a.workflowDefinitionAction)
	mux.HandleFunc("PUT /api/workflow-definitions/{id}", a.workflowDefinitionAction)
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

func (a *API) operatorSafety(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, NewEnvOperatorSafetyStatus())
}

func (a *API) pipelines(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"pipelines": pipelinesOrEmpty(a.runner.Pipelines())})
}

func (a *API) agentProfiles(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"profiles": agentProfilesOrEmpty(a.runner.AgentProfiles())})
}

func (a *API) workflowDefinitions(w http.ResponseWriter, r *http.Request) {
	definitions, err := a.store.ListWorkflowDefinitions()
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflow_definitions": workflowDefinitionsOrEmpty(definitions)})
}

func (a *API) workflowDefinitionAction(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusNotFound, "workflow definition not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		a.getWorkflowDefinition(w, r, id)
	case http.MethodPut:
		a.updateWorkflowDefinition(w, r, id)
	default:
		writeError(w, http.StatusNotFound, "workflow definition not found")
	}
}

func (a *API) getWorkflowDefinition(w http.ResponseWriter, r *http.Request, id string) {
	definition, err := a.store.WorkflowDefinition(id)
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflow_definition": definition})
}

func (a *API) updateWorkflowDefinition(w http.ResponseWriter, r *http.Request, id string) {
	var definition WorkflowDefinition
	if err := json.NewDecoder(r.Body).Decode(&definition); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	definition.ID = id
	definition.Pipeline.WorkflowID = id
	if err := validateWorkflowDefinitionWrite(definition); err != nil {
		writeControlError(w, err)
		return
	}
	definition.Status = "draft"
	saved, err := a.store.UpsertWorkflowDefinition(definition)
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflow_definition": saved})
}

func validateWorkflowDefinitionWrite(definition WorkflowDefinition) error {
	if definition.Status != "" && !strings.EqualFold(definition.Status, "draft") {
		return ErrInvalid
	}
	seenIDs := map[string]bool{}
	seenPositions := map[int]bool{}
	for _, step := range definition.Pipeline.Steps {
		id := strings.TrimSpace(step.ID)
		if id == "" || seenIDs[id] {
			return ErrInvalid
		}
		seenIDs[id] = true
		if step.Position < 1 || seenPositions[step.Position] {
			return ErrInvalid
		}
		seenPositions[step.Position] = true
		switch strings.TrimSpace(step.Executor) {
		case "internal", "model", "command":
		case "http":
			return ErrInvalid
		default:
			return ErrInvalid
		}
	}
	return nil
}

func (a *API) adapters(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, a.adapterProbe.Payload(ctx))
}

func (a *API) modelBrokerStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, map[string]any{"model_broker": a.modelBroker.Status(ctx)})
}

func (a *API) openCodeExecutorStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, map[string]any{"opencode_executor": a.openCode.Status(ctx)})
}

func (a *API) openPilotStates(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"states": openPilotStatesOrEmpty(a.store.ListOpenPilotIssueStates())})
}

func (a *API) openPilotState(w http.ResponseWriter, r *http.Request) {
	repo, issue, ok := openPilotRepoIssueFromQuery(w, r)
	if !ok {
		return
	}
	state, err := a.store.OpenPilotIssueState(repo, issue)
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, openPilotStateResponse{State: state, NextAction: openPilotNextAction(state)})
}

func (a *API) pauseOpenPilotState(w http.ResponseWriter, r *http.Request) {
	repo, issue, ok := openPilotRepoIssueFromQuery(w, r)
	if !ok {
		return
	}
	state, err := a.store.OpenPilotIssueState(repo, issue)
	if err != nil {
		writeControlError(w, err)
		return
	}
	state.ResumeInputs = mergeOpenPilotResumeStateInputs(state)
	state.Phase = OpenPilotPhasePaused
	state.LastError = firstNonEmpty(state.LastError, "paused by operator")
	state, err = a.store.UpsertOpenPilotIssueState(state)
	if err != nil {
		writeControlError(w, err)
		return
	}
	a.cancelOpenPilotRuns(repo, issue, "Paused Open Pilot issue "+repo+"#"+issue)
	writeJSON(w, http.StatusOK, openPilotStateResponse{State: state, NextAction: openPilotNextAction(state)})
}

func (a *API) resumeOpenPilotState(w http.ResponseWriter, r *http.Request) {
	repo, issue, ok := openPilotRepoIssueFromQuery(w, r)
	if !ok {
		return
	}
	state, err := a.store.OpenPilotIssueState(repo, issue)
	if err != nil {
		writeControlError(w, err)
		return
	}
	if !isOpenPilotResumablePhase(state.Phase) {
		writeError(w, http.StatusBadRequest, "state is not resumable")
		return
	}
	originalPhase := state.Phase
	if openPilotHasActiveRun(a.store, repo, issue) {
		writeError(w, http.StatusConflict, "open-pilot issue already has an active run")
		return
	}
	inputs := mergeOpenPilotResumeStateInputs(state)
	if inputs == nil {
		inputs = map[string]string{}
	}
	inputs["repo"] = repo
	inputs["issue"] = issue
	state, err = a.store.ClaimOpenPilotIssueStateForResume(repo, issue, inputs)
	if err != nil {
		writeControlError(w, err)
		return
	}
	run, err := a.dispatchRun(r.Context(), CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: inputs})
	if err != nil {
		state.Phase = OpenPilotPhaseFailed
		if originalPhase == OpenPilotPhasePaused {
			state.Phase = OpenPilotPhasePaused
		}
		state.LastError = err.Error()
		state.FailingGate = "resume"
		state.OperatorSummary = "Resume dispatch failed: " + err.Error()
		state.RecommendedNextAction = "resume"
		_, _ = a.store.UpsertOpenPilotIssueState(state)
		writeControlError(w, err)
		return
	}
	if fresh, err := a.store.OpenPilotIssueState(repo, issue); err == nil {
		state = fresh
	}
	status := http.StatusCreated
	if a.asyncDispatch {
		w.Header().Set("Location", "/api/runs/"+run.ID)
		status = http.StatusAccepted
	}
	writeJSON(w, status, openPilotStateResponse{State: state, Run: &run, NextAction: openPilotNextAction(state)})
}

func openPilotHasActiveRun(store Store, repo, issue string) bool {
	for _, run := range store.ListRuns() {
		if run.WorkflowID == string(WorkflowOpenPilot) && run.Inputs["repo"] == repo && run.Inputs["issue"] == issue && (run.Status == RunQueued || run.Status == RunRunning) {
			return true
		}
	}
	return false
}

func (a *API) cancelOpenPilotRuns(repo, issue, message string) {
	for _, run := range a.store.ListRuns() {
		if run.WorkflowID != string(WorkflowOpenPilot) || run.Inputs["repo"] != repo || run.Inputs["issue"] != issue {
			continue
		}
		if run.Status != RunQueued && run.Status != RunRunning {
			continue
		}
		_, _ = a.store.AddEvent(run.ID, "open-pilot.pause", message)
		if a.canceler != nil {
			if detail, err := a.store.RunDetail(run.ID); err == nil {
				for _, event := range cancelRunDetail(context.Background(), a.canceler, detail).Events {
					_, _ = a.store.AddEvent(run.ID, event.Type, event.Message)
				}
			}
		}
		_, _ = a.store.FinishRun(run.ID, RunCanceled, message)
	}
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
		step, err := a.store.RunStep(stepID)
		if err != nil {
			writeControlError(w, err)
			return
		}
		detail, err := a.store.RunDetail(runID)
		if err != nil {
			writeControlError(w, err)
			return
		}
		inputs := retryRunInputsFromStep(detail, step)
		applyOpenPilotRetryInputsFromState(a.store, detail, step, inputs)
		run, err := a.dispatchRun(r.Context(), CreateRunInput{WorkflowID: detail.Run.WorkflowID, TriggerID: detail.Run.TriggerID, Inputs: inputs})
		if err != nil {
			writeControlError(w, err)
			return
		}
		_, _ = a.store.AddEvent(run.ID, "run.step.retry", "Retried "+step.Name+" from "+runID)
		status := http.StatusCreated
		if a.asyncDispatch {
			w.Header().Set("Location", "/api/runs/"+run.ID)
			status = http.StatusAccepted
		}
		writeJSON(w, status, map[string]any{"run": run, "step": step})
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

func retryRunInputsFromStep(detail RunDetail, step RunStep) map[string]string {
	inputs := cloneMap(detail.Run.Inputs)
	if inputs == nil {
		inputs = map[string]string{}
	}
	inputs["retry_from_run"] = detail.Run.ID
	inputs["retry_from_step_id"] = step.ID
	inputs["retry_from_step"] = step.Name
	inputs["retry_from_step_position"] = strconv.Itoa(step.Position)
	for _, existing := range detail.Steps {
		prefix := "resume." + existing.Name + "."
		for key, value := range existing.Inputs {
			inputs[prefix+key] = value
		}
		if existing.OutputSummary != "" {
			inputs[prefix+"summary"] = existing.OutputSummary
		}
		for key, value := range existing.CorrelationIDs {
			inputs[prefix+"correlation."+key] = value
		}
	}
	return inputs
}

func applyOpenPilotRetryInputsFromState(store Store, detail RunDetail, step RunStep, inputs map[string]string) {
	if store == nil || detail.Run.WorkflowID != string(WorkflowOpenPilot) || step.Name != "open-pilot.ai-review" || inputs == nil {
		return
	}
	repo := detail.Run.Inputs["repo"]
	issue := detail.Run.Inputs["issue"]
	if repo == "" || issue == "" {
		return
	}
	state, err := store.OpenPilotIssueState(repo, issue)
	if err != nil {
		return
	}
	for key, value := range state.ResumeInputs {
		if value != "" {
			inputs[key] = value
		}
	}
	if retryStep := state.ResumeInputs["retry_from_step"]; retryStep != "" {
		inputs["retry_from_step"] = retryStep
	}
	if retryPosition := state.ResumeInputs["retry_from_step_position"]; retryPosition != "" {
		inputs["retry_from_step_position"] = retryPosition
	}
}

func (a *API) retryRun(w http.ResponseWriter, r *http.Request, id string) {
	detail, err := a.store.RunDetail(id)
	if err != nil {
		writeControlError(w, err)
		return
	}
	if detail.Run.WorkflowID == string(WorkflowOpenPilot) {
		if repo, issue := detail.Run.Inputs["repo"], detail.Run.Inputs["issue"]; repo != "" && issue != "" {
			if state, stateErr := a.store.OpenPilotIssueState(repo, issue); stateErr == nil && isOpenPilotResumablePhase(state.Phase) {
				writeError(w, http.StatusConflict, "use the Open Pilot lifecycle resume action for this issue")
				return
			}
		}
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

func workflowDefinitionsOrEmpty(items []WorkflowDefinition) []WorkflowDefinition {
	if items == nil {
		return []WorkflowDefinition{}
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

type openPilotStateResponse struct {
	State      OpenPilotIssueState `json:"state"`
	Run        *Run                `json:"run,omitempty"`
	NextAction string              `json:"next_action"`
}

func openPilotRepoIssueFromQuery(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	repo := strings.TrimSpace(r.URL.Query().Get("repo"))
	issue := strings.TrimSpace(r.URL.Query().Get("issue"))
	if repo == "" || issue == "" {
		writeError(w, http.StatusBadRequest, "repo and issue are required")
		return "", "", false
	}
	return repo, issue, true
}

func openPilotStatesOrEmpty(items []OpenPilotIssueState) []OpenPilotIssueState {
	if items == nil {
		return []OpenPilotIssueState{}
	}
	return items
}

func mergeOpenPilotResumeStateInputs(state OpenPilotIssueState) map[string]string {
	inputs := cloneMap(state.ResumeInputs)
	if inputs == nil {
		inputs = map[string]string{}
	}
	inputs["repo"] = state.Repo
	inputs["issue"] = state.Issue
	if state.Branch != "" {
		inputs["resume.open-pilot.branch-plan.branch"] = state.Branch
		inputs["resume.open-pilot.branch-plan.correlation.branch"] = state.Branch
	}
	if state.Base != "" {
		inputs["resume.open-pilot.branch-plan.base_branch"] = state.Base
	}
	if state.WorktreePath != "" {
		inputs["resume.open-pilot.branch-plan.worktree_path"] = state.WorktreePath
		inputs["resume.open-pilot.branch-plan.correlation.worktree_path"] = state.WorktreePath
	}
	if state.PullRequestURL != "" {
		inputs["resume.open-pilot.pr-plan.correlation.pull_request"] = state.PullRequestURL
	}
	if state.PullRequestNum > 0 {
		inputs["resume.open-pilot.pr-plan.pull_request_number"] = strconv.Itoa(state.PullRequestNum)
	}
	if state.MergeCommitSHA != "" {
		inputs["resume.open-pilot.merge-pr.merge_commit_sha"] = state.MergeCommitSHA
	}
	if state.IssueClosed {
		inputs["resume.open-pilot.close-issue.issue_closed"] = "true"
	}
	step, position := openPilotResumeCursorForState(state)
	if step != "" && position != "" {
		if state.Phase == OpenPilotPhaseFailed || state.Phase == OpenPilotPhasePaused {
			inputs["retry_from_step"] = step
			inputs["retry_from_step_position"] = position
		} else {
			inputs["retry_from_step"] = step
			inputs["retry_from_step_position"] = position
		}
	}
	return inputs
}

func openPilotResumeCursorForState(state OpenPilotIssueState) (string, string) {
	if (state.Phase == OpenPilotPhaseFailed || state.Phase == OpenPilotPhasePaused) && state.ResumeInputs != nil {
		if state.LastStepName != "" && !openPilotShouldUseStoredRepairCursor(state) {
			if pos := openPilotStepPosition(state.LastStepName); pos != "" {
				return state.LastStepName, pos
			}
		}
		if step, pos := state.ResumeInputs["retry_from_step"], state.ResumeInputs["retry_from_step_position"]; step != "" && pos != "" {
			return step, pos
		}
		if state.LastStepName != "" {
			if pos := openPilotStepPosition(state.LastStepName); pos != "" {
				return state.LastStepName, pos
			}
		}
	}
	switch state.Phase {
	case OpenPilotPhaseRequested:
		return "open-pilot.validate-issue", "1"
	case OpenPilotPhaseValidated:
		return "open-pilot.branch-plan", "2"
	case OpenPilotPhaseBranchPrepared:
		return "open-pilot.prepare-prompt", "3"
	case OpenPilotPhaseAgentCompleted:
		if state.ResumeInputs != nil && strings.TrimSpace(state.ResumeInputs["test_command"]) != "" {
			return "open-pilot.verify-command", "5"
		}
		return "open-pilot.push-branch", "6"
	case OpenPilotPhaseVerified:
		return "open-pilot.push-branch", "6"
	case OpenPilotPhasePushed:
		return "open-pilot.pr-plan", "7"
	case OpenPilotPhasePRCreated:
		return "open-pilot.ai-review", "8"
	case OpenPilotPhaseAIReviewed:
		return "open-pilot.ci-checks", "9"
	case OpenPilotPhaseCIPassed, OpenPilotPhaseMergeReady:
		return "open-pilot.merge-pr", "10"
	case OpenPilotPhaseMerged:
		return "open-pilot.close-issue", "11"
	case OpenPilotPhaseFailed, OpenPilotPhasePaused:
		return "open-pilot.agent-work", "4"
	default:
		return "", ""
	}
}

func openPilotShouldUseStoredRepairCursor(state OpenPilotIssueState) bool {
	if state.FailingGate == "ai_review" || state.GateStatus["ai_review"] == "changes_requested" {
		return true
	}
	return state.ResumeInputs != nil && strings.TrimSpace(state.ResumeInputs["ai_review.feedback"]) != ""
}

func openPilotStepPosition(name string) string {
	switch name {
	case "open-pilot.validate-issue":
		return "1"
	case "open-pilot.branch-plan":
		return "2"
	case "open-pilot.prepare-prompt":
		return "3"
	case "open-pilot.agent-work":
		return "4"
	case "open-pilot.verify-command":
		return "5"
	case "open-pilot.push-branch":
		return "6"
	case "open-pilot.pr-plan":
		return "7"
	case "open-pilot.ai-review":
		return "8"
	case "open-pilot.ci-checks":
		return "9"
	case "open-pilot.merge-pr":
		return "10"
	case "open-pilot.close-issue":
		return "11"
	default:
		return ""
	}
}

func openPilotNextAction(state OpenPilotIssueState) string {
	switch state.Phase {
	case OpenPilotPhaseFailed, OpenPilotPhasePaused:
		return "Resume"
	case OpenPilotPhaseIssueClosed:
		return "Complete"
	case OpenPilotPhaseMerged:
		if state.IssueClosed {
			return "Complete"
		}
		return "Close issue"
	case OpenPilotPhaseMergeReady, OpenPilotPhaseCIPassed:
		return "Merge PR"
	case OpenPilotPhaseAIReviewed:
		if state.GateStatus["ci_checks"] == "passed" {
			return "Merge PR"
		}
		return "Wait for CI"
	case OpenPilotPhasePRCreated:
		if state.GateStatus["ai_review"] == "approved" {
			if state.GateStatus["ci_checks"] == "passed" {
				return "Merge PR"
			}
			return "Wait for CI"
		}
		return "AI review"
	case OpenPilotPhasePushed:
		return "Create PR"
	case OpenPilotPhaseVerified:
		return "Push branch"
	case OpenPilotPhaseAgentCompleted:
		return "Verify"
	case OpenPilotPhaseBranchPrepared:
		return "Run agent"
	case OpenPilotPhaseValidated:
		return "Prepare branch"
	case OpenPilotPhaseRequested:
		return "Validate issue"
	default:
		return "Continue"
	}
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
