package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (a *API) openPilotStates(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"states": openPilotStatesOrEmpty(a.store.ListOpenPilotIssueStates())})
}

func (a *API) openPilotRepositoryAudit(w http.ResponseWriter, _ *http.Request) {
	if a.runner.openPilotGateError != nil || a.runner.openPilotGate == nil {
		writeControlError(w, firstNonNilError(a.runner.openPilotGateError, ErrInvalid))
		return
	}
	states, err := a.store.ListOpenPilotIssueStatesForAudit(maxOpenPilotAuditStates + 1)
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, buildOpenPilotRepositoryAudit(a.runner.openPilotGate, states))
}

type openPilotRollbackStatus struct {
	Gate                    OpenPilotRolloutStatus `json:"gate"`
	ActiveRegistryStates    int                    `json:"active_registry_states"`
	ResumableRegistryStates int                    `json:"resumable_registry_states"`
	CancellableRegistryRuns int                    `json:"cancellable_registry_runs"`
	SingletonRollbackSafe   bool                   `json:"singleton_rollback_safe"`
}

func (a *API) openPilotRollout(w http.ResponseWriter, r *http.Request) {
	if a.runner == nil || a.runner.openPilotGateError != nil || a.runner.openPilotGate == nil || a.runner.openPilotGate.registry == nil {
		writeControlError(w, ErrInvalid)
		return
	}
	if r.Method == http.MethodPost {
		if a.runner.openPilotGate.mode != "durable" {
			writeError(w, http.StatusConflict, "durable Open Pilot activation is not configured")
			return
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var input ConfigureOpenPilotRolloutInput
		if err := decoder.Decode(&input); err != nil {
			writeJSONDecodeError(w, err)
			return
		}
		manifest := a.runner.openPilotGate.registry.manifest
		if input.RegistryFingerprint != manifest.RegistryFingerprint {
			writeControlError(w, ErrConflict)
			return
		}
		for _, target := range input.ProofTargets {
			repository, ok := a.runner.openPilotGate.registry.RepositoryByID(target.RepositoryID)
			if !ok || repository.Slug != target.RepositorySlug || repository.ExecutorGeneration != target.ExecutorGeneration {
				writeControlError(w, ErrConflict)
				return
			}
		}
		if _, err := a.store.ConfigureOpenPilotRollout(input); err != nil {
			writeControlError(w, err)
			return
		}
	}
	status, err := a.openPilotRollbackEvidence()
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (a *API) openPilotRollbackEvidence() (openPilotRollbackStatus, error) {
	gate, err := a.store.OpenPilotRolloutStatus()
	if err != nil {
		return openPilotRollbackStatus{}, err
	}
	status := openPilotRollbackStatus{Gate: gate}
	for _, state := range a.store.ListOpenPilotIssueStates() {
		if state.RepositoryID == "" || state.ExecutorGeneration == "" {
			continue
		}
		switch state.Phase {
		case OpenPilotPhaseIssueClosed:
		case OpenPilotPhaseFailed, OpenPilotPhasePaused, OpenPilotPhaseMerged:
			status.ResumableRegistryStates++
		default:
			status.ActiveRegistryStates++
		}
	}
	for _, run := range a.store.ListRuns() {
		if run.WorkflowID == string(WorkflowOpenPilot) && run.Inputs[openPilotRepositoryIDInputKey] != "" && (run.Status == RunQueued || run.Status == RunRunning) {
			status.CancellableRegistryRuns++
		}
	}
	status.SingletonRollbackSafe = gate.Mode == "disabled" && status.ActiveRegistryStates == 0 && status.ResumableRegistryStates == 0 && status.CancellableRegistryRuns == 0
	return status, nil
}

func firstNonNilError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return ErrInvalid
}

func (a *API) openPilotState(w http.ResponseWriter, r *http.Request) {
	state, ok := a.openPilotStateFromQuery(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, openPilotStateResponse{State: state, NextAction: openPilotNextAction(state)})
}

func (a *API) pauseOpenPilotState(w http.ResponseWriter, r *http.Request) {
	state, ok := a.openPilotStateFromQuery(w, r)
	if !ok {
		return
	}
	state.ResumeInputs = mergeOpenPilotResumeStateInputs(state)
	state.Phase = OpenPilotPhasePaused
	state.LastError = firstNonEmpty(state.LastError, "paused by operator")
	state, err := a.store.UpsertOpenPilotIssueState(state)
	if err != nil {
		writeControlError(w, err)
		return
	}
	a.cancelOpenPilotRuns(state.Repo, state.Issue, "Paused Open Pilot issue "+state.Repo+"#"+state.Issue)
	writeJSON(w, http.StatusOK, openPilotStateResponse{State: state, NextAction: openPilotNextAction(state)})
}

func (a *API) resumeOpenPilotState(w http.ResponseWriter, r *http.Request) {
	state, ok := a.openPilotStateFromQuery(w, r)
	if !ok {
		return
	}
	inputs, err := safeOpenPilotResumeInputs(state)
	if err != nil {
		writeError(w, http.StatusBadRequest, "state lacks safe durable evidence for Open Pilot lifecycle resume")
		return
	}
	var correction struct {
		ExpectedRunID string `json:"expected_run_id"`
		Feedback      string `json:"feedback"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&correction); err != nil && !errors.Is(err, io.EOF) {
		writeJSONDecodeError(w, err)
		return
	}
	correction.Feedback = strings.TrimSpace(correction.Feedback)
	if err := validateOpenPilotOperatorFeedback(state, correction.ExpectedRunID, correction.Feedback, inputs); err != nil {
		writeControlError(w, err)
		return
	}
	removeOpenPilotCIRepairInputs(inputs)
	freshSessionRunID := r.URL.Query().Get("fresh_session_for_run")
	if freshSessionRunID != "" {
		if err := validateOpenPilotFreshSessionState(state, freshSessionRunID); err != nil {
			writeControlError(w, err)
			return
		}
		if err := validateOpenPilotFreshSessionWorktree(r.Context(), openPilotWorktreePlanFromResume(inputs), inputs); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
	}
	prepared, err := a.runner.prepareOpenPilotStateRunInput(state, CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: inputs, OpenPilotFreshSessionRunID: freshSessionRunID, OpenPilotFeedbackRunID: correction.ExpectedRunID, OpenPilotFeedback: correction.Feedback})
	if err != nil {
		writeControlError(w, err)
		return
	}
	state, run, err := a.store.ClaimAndCreateOpenPilotResumeRun(state.Repo, state.Issue, prepared)
	if err != nil {
		writeControlError(w, err)
		return
	}
	if !a.asyncDispatch {
		run, err = a.store.StartRun(run.ID)
		if err == nil {
			run, err = a.runner.ExecuteRun(r.Context(), run)
		}
		if err != nil {
			writeControlError(w, err)
			return
		}
	}
	if fresh, err := a.store.OpenPilotIssueState(state.Repo, state.Issue); err == nil {
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

func (a *API) runOpenPilotWorkflow(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Repo        string `json:"repo"`
		Issue       string `json:"issue"`
		TestCommand string `json:"test_command"`
		BaseBranch  string `json:"base_branch"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSONDecodeError(w, err)
		return
	}
	if input.Repo == "" || input.Issue == "" {
		writeError(w, http.StatusBadRequest, "repo and issue are required")
		return
	}
	inputs := map[string]string{"repo": input.Repo, "issue": input.Issue}
	if testCommand := strings.TrimSpace(input.TestCommand); testCommand != "" {
		inputs["test_command"] = testCommand
	}
	if baseBranch := strings.TrimSpace(input.BaseBranch); baseBranch != "" {
		inputs["base_branch"] = baseBranch
	}
	run, err := a.dispatchRun(r.Context(), CreateRunInput{WorkflowID: string(WorkflowOpenPilot), TriggerID: "manual-open-pilot", Inputs: inputs})
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

type openPilotStateResponse struct {
	State      OpenPilotIssueState `json:"state"`
	Run        *Run                `json:"run,omitempty"`
	NextAction string              `json:"next_action"`
}

func (a *API) openPilotStateFromQuery(w http.ResponseWriter, r *http.Request) (OpenPilotIssueState, bool) {
	if a.runner.openPilotGateError != nil {
		writeControlError(w, a.runner.openPilotGateError)
		return OpenPilotIssueState{}, false
	}
	repo := r.URL.Query().Get("repo")
	issue := r.URL.Query().Get("issue")
	if repo == "" || issue == "" {
		writeError(w, http.StatusBadRequest, "repo and issue are required")
		return OpenPilotIssueState{}, false
	}
	target, err := a.runner.openPilotGate.Resolve(repo, issue)
	if err != nil {
		writeControlError(w, err)
		return OpenPilotIssueState{}, false
	}
	state, err := a.store.OpenPilotIssueStateForTarget(target)
	if err != nil {
		writeControlError(w, err)
		return OpenPilotIssueState{}, false
	}
	if _, err := a.runner.openPilotGate.ValidateState(state); err != nil {
		writeControlError(w, err)
		return OpenPilotIssueState{}, false
	}
	return state, true
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
			if resolved := openPilotStepPosition(step); resolved != "" {
				return step, resolved
			}
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
		return "open-pilot.commit-changes", "6"
	case OpenPilotPhaseVerified:
		return "open-pilot.commit-changes", "6"
	case OpenPilotPhaseCommitted:
		return "open-pilot.push-branch", "7"
	case OpenPilotPhasePushed:
		return "open-pilot.pr-plan", "8"
	case OpenPilotPhasePRCreated:
		return "open-pilot.ai-review", "9"
	case OpenPilotPhaseAIReviewed:
		return "open-pilot.ci-checks", "10"
	case OpenPilotPhaseCIPassed, OpenPilotPhaseMergeReady:
		return "open-pilot.merge-pr", "11"
	case OpenPilotPhaseMerged:
		return "open-pilot.close-issue", "12"
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
	case "open-pilot.commit-changes":
		return "6"
	case "open-pilot.push-branch":
		return "7"
	case "open-pilot.pr-plan":
		return "8"
	case "open-pilot.ai-review":
		return "9"
	case "open-pilot.ci-checks":
		return "10"
	case "open-pilot.merge-pr":
		return "11"
	case "open-pilot.close-issue":
		return "12"
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

// openPilotReadiness is the bounded control-only readiness preflight. It
// always answers 200 with structured reason codes for configuration and
// prerequisite failures; only a malformed query is rejected.
func (a *API) openPilotReadiness(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	repo := strings.TrimSpace(query.Get("repo"))
	issue := strings.TrimSpace(query.Get("issue"))
	testCommand := strings.TrimSpace(query.Get("test_command"))
	if issue != "" && repo == "" {
		writeError(w, http.StatusBadRequest, "repo is required when issue is provided")
		return
	}
	if len(repo) > 256 || len(issue) > 32 || len(testCommand) > maxOpenPilotReadinessCommandBytes {
		writeError(w, http.StatusBadRequest, "repo, issue, or test_command exceeds its bounded length")
		return
	}
	writeJSON(w, http.StatusOK, buildOpenPilotReadiness(a.openPilotReadinessInput(r.Context(), repo, issue, testCommand)))
}

// openPilotReadinessValidate dry-runs one operator registry or runtime manifest
// document. It never clones, touches the filesystem, opens a network
// connection, or changes admission state.
func (a *API) openPilotReadinessValidate(w http.ResponseWriter, r *http.Request) {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxOpenPilotManifestBytes+(4<<10)))
	decoder.DisallowUnknownFields()
	var input OpenPilotConfigurationDryRunInput
	if err := decoder.Decode(&input); err != nil {
		writeJSONDecodeError(w, err)
		return
	}
	var gate *OpenPilotAdmissionGate
	if a.runner != nil && a.runner.openPilotGateError == nil {
		gate = a.runner.openPilotGate
	}
	writeJSON(w, http.StatusOK, dryRunOpenPilotConfiguration(input, gate, time.Now()))
}

// openPilotInbox is the bounded, read-only intake queue. It answers 200 with
// per-issue reason codes and upstream availability; only a malformed query is
// rejected. It never claims, dispatches, or writes to Gitea.
func (a *API) openPilotInbox(w http.ResponseWriter, r *http.Request) {
	filter, problem := parseOpenPilotInboxFilter(r.URL.Query())
	if problem != "" {
		writeError(w, http.StatusBadRequest, problem)
		return
	}
	input := openPilotInboxInput{Filter: filter, Issues: a.openPilotIssues, Store: a.store, PollRepos: a.openPilotPollRepos, Now: time.Now()}
	if a.runner != nil {
		input.Gate, input.GateError = a.runner.openPilotGate, a.runner.openPilotGateError
		if env, ok := a.runner.executors.(*EnvWorkflowExecutors); ok {
			input.Executors = env
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), openPilotInboxTimeout)
	defer cancel()
	writeJSON(w, http.StatusOK, buildOpenPilotInbox(ctx, input))
}
