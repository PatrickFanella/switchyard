package controlplane

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (a *API) listRuns(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 100")
			return
		}
		limit = parsed
	}
	input := ListRunsPageInput{Limit: limit}
	if raw := strings.TrimSpace(r.URL.Query().Get("cursor")); raw != "" {
		created, id, err := decodeRunCursor(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid cursor")
			return
		}
		input.BeforeCreated = &created
		input.BeforeID = id
	}
	page, err := a.store.ListRunsPageContext(r.Context(), input)
	if err != nil {
		writeControlError(w, err)
		return
	}
	nextCursor := ""
	if page.HasMore && len(page.Runs) > 0 {
		last := page.Runs[len(page.Runs)-1]
		nextCursor = encodeRunCursor(last.CreatedAt, last.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runsOrEmpty(page.Runs), "next_cursor": nextCursor})
}

func encodeRunCursor(created time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(created.UTC().Format(time.RFC3339Nano) + "\n" + id))
}

func decodeRunCursor(cursor string) (time.Time, string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", err
	}
	createdText, id, ok := strings.Cut(string(decoded), "\n")
	if !ok || strings.TrimSpace(id) == "" {
		return time.Time{}, "", fmt.Errorf("malformed cursor")
	}
	created, err := time.Parse(time.RFC3339Nano, createdText)
	return created, id, err
}

func (a *API) createRun(w http.ResponseWriter, r *http.Request) {
	var input CreateRunInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSONDecodeError(w, err)
		return
	}
	run, err := a.dispatchRun(r.Context(), input)
	if err != nil {
		writeControlError(w, err)
		return
	}
	_, _ = a.store.AddEvent(run.ID, "audit.run.created", "Run created by "+requestActor(r))
	status := http.StatusCreated
	if a.asyncDispatch {
		w.Header().Set("Location", "/api/runs/"+run.ID)
		status = http.StatusAccepted
	}
	writeJSON(w, status, map[string]Run{"run": run})
}

func (a *API) dispatchRun(ctx context.Context, input CreateRunInput) (Run, error) {
	if a.asyncDispatch {
		return a.runner.EnqueueContext(ctx, input)
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
			a.cancelRun(w, r, actionRunID)
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
	detail, err := a.store.RunDetailContext(r.Context(), id)
	if err != nil {
		writeControlError(w, err)
		return
	}
	detail.Events = eventsOrEmpty(detail.Events)
	detail.Artifacts = artifactsOrEmpty(detail.Artifacts)
	detail.Steps = stepsOrEmpty(detail.Steps)
	truncated := map[string]bool{}
	if len(detail.Events) > 500 {
		detail.Events = detail.Events[len(detail.Events)-500:]
		truncated["events"] = true
	}
	if len(detail.Artifacts) > 200 {
		detail.Artifacts = detail.Artifacts[len(detail.Artifacts)-200:]
		truncated["artifacts"] = true
	}
	if len(detail.Steps) > 200 {
		detail.Steps = detail.Steps[len(detail.Steps)-200:]
		truncated["steps"] = true
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": detail.Run, "steps": detail.Steps, "events": detail.Events, "artifacts": detail.Artifacts, "truncated": truncated})
}

func (a *API) stepAction(w http.ResponseWriter, r *http.Request, runID string, path string) {
	if path == "" || path == "/" {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusNotFound, "step action not found")
			return
		}
		steps := stepsOrEmpty(a.store.ListRunSteps(runID))
		if len(steps) > 200 {
			steps = steps[len(steps)-200:]
		}
		writeJSON(w, http.StatusOK, map[string][]RunStep{"steps": steps})
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
		if step.RunID != runID {
			writeError(w, http.StatusNotFound, "step not found for run")
			return
		}
		detail, err := a.store.RunDetailContext(r.Context(), runID)
		if err != nil {
			writeControlError(w, err)
			return
		}
		inputs := retryRunInputsFromStep(detail, step)
		applyOpenPilotRetryInputsFromState(a.store, detail, step, inputs)
		run, err := a.dispatchRun(r.Context(), CreateRunInput{WorkflowID: detail.Run.WorkflowID, TriggerID: detail.Run.TriggerID, Inputs: inputs, PipelineSnapshot: detail.Run.PipelineSnapshot})
		if err != nil {
			writeControlError(w, err)
			return
		}
		_, _ = a.store.AddEvent(run.ID, "run.step.retry", "Retried "+step.Name+" from "+runID+" by "+requestActor(r))
		status := http.StatusCreated
		if a.asyncDispatch {
			w.Header().Set("Location", "/api/runs/"+run.ID)
			status = http.StatusAccepted
		}
		writeJSON(w, status, map[string]any{"run": run, "step": step})
	case "cancel":
		step, err := a.store.RunStep(stepID)
		if err != nil {
			writeControlError(w, err)
			return
		}
		if step.RunID != runID {
			writeError(w, http.StatusNotFound, "step not found for run")
			return
		}
		if step.Status == StepCanceled {
			writeJSON(w, http.StatusOK, map[string]RunStep{"step": step})
			return
		}
		detail, err := a.store.RunDetailContext(r.Context(), runID)
		if err != nil {
			writeControlError(w, err)
			return
		}
		step, err = a.store.CancelRunStep(stepID)
		if err != nil {
			writeControlError(w, err)
			return
		}
		a.runner.CancelRun(runID)
		if a.canceler != nil {
			for _, event := range cancelRunDetail(r.Context(), a.canceler, detail).Events {
				_, _ = a.store.AddEvent(runID, event.Type, event.Message)
			}
		}
		if detail.Run.Status == RunQueued || detail.Run.Status == RunRunning {
			_, _ = a.store.FinishRun(runID, RunCanceled, "Canceled by operator")
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

func (a *API) retryRun(w http.ResponseWriter, r *http.Request, id string) {
	detail, err := a.store.RunDetailContext(r.Context(), id)
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
	run, err := a.dispatchRun(r.Context(), CreateRunInput{WorkflowID: detail.Run.WorkflowID, TriggerID: detail.Run.TriggerID, Inputs: detail.Run.Inputs, PipelineSnapshot: detail.Run.PipelineSnapshot})
	if err != nil {
		writeControlError(w, err)
		return
	}
	_, _ = a.store.AddEvent(run.ID, "run.retry", "Retried from "+id+" by "+requestActor(r))
	status := http.StatusCreated
	if a.asyncDispatch {
		w.Header().Set("Location", "/api/runs/"+run.ID)
		status = http.StatusAccepted
	}
	writeJSON(w, status, map[string]Run{"run": run})
}

func (a *API) cancelRun(w http.ResponseWriter, r *http.Request, id string) {
	detail, err := a.store.RunDetailContext(r.Context(), id)
	if err != nil {
		writeControlError(w, err)
		return
	}
	if detail.Run.Status == RunCanceled {
		writeJSON(w, http.StatusOK, map[string]Run{"run": detail.Run})
		return
	}
	if detail.Run.Status != RunQueued && detail.Run.Status != RunRunning {
		writeError(w, http.StatusBadRequest, "only queued or running runs can be canceled")
		return
	}
	_, _ = a.store.AddEvent(id, "run.cancel.requested", "Cancellation requested by "+requestActor(r))
	a.runner.CancelRun(id)
	if a.canceler != nil {
		for _, event := range cancelRunDetail(r.Context(), a.canceler, detail).Events {
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
		writeJSONDecodeError(w, err)
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
