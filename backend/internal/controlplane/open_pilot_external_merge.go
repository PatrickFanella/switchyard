package controlplane

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var externalMergeSHAPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Reconciliation observes a merge performed outside this lifecycle. It does
// not assert that the failed AI review or CI gates passed, or rewrite runs.
func (a *API) reconcileOpenPilotState(w http.ResponseWriter, r *http.Request) {
	state, ok := a.openPilotStateFromQuery(w, r)
	if !ok {
		return
	}
	var input struct {
		ExpectedRunID     string    `json:"expected_run_id"`
		ExpectedUpdatedAt time.Time `json:"expected_updated_at"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeJSONDecodeError(w, err)
		return
	}
	if input.ExpectedRunID == "" || input.ExpectedRunID != state.RunID || !input.ExpectedUpdatedAt.Equal(state.UpdatedAt) {
		writeControlError(w, ErrConflict)
		return
	}
	env, ok := a.runner.executors.(*EnvWorkflowExecutors)
	if !ok || env.giteaPullRequestGetter == nil || env.issueFetcher == nil || state.PullRequestNum < 1 {
		writeControlError(w, ErrInvalid)
		return
	}
	repo := firstNonEmpty(state.CanonicalRepo, state.Repo)
	pr, err := env.giteaPullRequestGetter.GetPullRequest(r.Context(), repo, state.PullRequestNum)
	if err != nil {
		writeControlError(w, err)
		return
	}
	issue, err := env.issueFetcher.FetchIssue(r.Context(), repo, firstNonEmpty(state.CanonicalIssue, state.Issue))
	if err != nil {
		writeControlError(w, err)
		return
	}
	saved, err := a.store.WithContext(r.Context()).ReconcileOpenPilotExternalMerge(state, pr, issue)
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, openPilotStateResponse{State: saved, NextAction: openPilotNextAction(saved)})
}

func reconciledOpenPilotExternalMerge(current, expected OpenPilotIssueState, run Run, pr GiteaPullRequest, issue GiteaIssue, now time.Time) (OpenPilotIssueState, string, error) {
	if current.ID != expected.ID || current.RunID == "" || current.RunID != expected.RunID || !current.UpdatedAt.Equal(expected.UpdatedAt) {
		return OpenPilotIssueState{}, "", ErrConflict
	}
	if current.Phase != OpenPilotPhaseFailed && current.Phase != OpenPilotPhasePaused && current.Phase != OpenPilotPhaseMerged && current.Phase != OpenPilotPhaseIssueClosed {
		return OpenPilotIssueState{}, "", ErrConflict
	}
	if run.ID != current.RunID || run.WorkflowID != string(WorkflowOpenPilot) || run.Status == RunQueued || run.Status == RunRunning {
		return OpenPilotIssueState{}, "", ErrConflict
	}
	repo := firstNonEmpty(current.CanonicalRepo, current.Repo)
	issueNumber, err := strconv.Atoi(firstNonEmpty(current.CanonicalIssue, current.Issue))
	if err != nil || issue.Number != issueNumber || (issue.State != "open" && issue.State != "closed") || (issue.Repository != "" && !strings.EqualFold(issue.Repository, repo)) {
		return OpenPilotIssueState{}, "", ErrConflict
	}
	if !pr.Merged || pr.State != "closed" || !externalMergeSHAPattern.MatchString(pr.MergeCommitSHA) || !externalMergeSHAPattern.MatchString(pr.Head.SHA) || pr.ID != current.PullRequestID || pr.Number != current.PullRequestNum || current.PullRequestID < 1 || current.Branch == "" || pr.Head.Ref != current.Branch || current.Base == "" || pr.Base.Ref != current.Base || pr.Base.Repo == nil || !strings.EqualFold(pr.Base.Repo.FullName, repo) || pr.Head.Repo == nil || !strings.EqualFold(pr.Head.Repo.FullName, repo) {
		return OpenPilotIssueState{}, "", ErrConflict
	}
	if current.Phase == OpenPilotPhaseIssueClosed && issue.State != "closed" {
		return OpenPilotIssueState{}, "", ErrConflict
	}
	if (current.Phase == OpenPilotPhaseMerged || current.Phase == OpenPilotPhaseIssueClosed) && current.MergeCommitSHA == pr.MergeCommitSHA && current.IssueClosed == (issue.State == "closed") && current.LastError == "" && current.GateStatus["merge"] == "merged" {
		return cloneOpenPilotIssueState(current), "", nil
	}
	message, err := json.Marshal(map[string]any{"source": "gitea_external_merge", "previous_phase": current.Phase, "previous_error": current.LastError, "previous_summary": current.OperatorSummary, "pull_request_id": pr.ID, "pull_request_number": pr.Number, "head_sha": pr.Head.SHA, "merge_commit_sha": pr.MergeCommitSHA, "issue_state": issue.State})
	if err != nil {
		return OpenPilotIssueState{}, "", err
	}
	state := cloneOpenPilotIssueState(current)
	state.Phase, state.MergeCommitSHA, state.IssueClosed = OpenPilotPhaseMerged, pr.MergeCommitSHA, issue.State == "closed"
	state.LastError, state.FailingGate = "", ""
	state.OperatorSummary = fmt.Sprintf("Observed externally merged PR #%d; issue %s", pr.Number, issue.State)
	state.RecommendedNextAction = "manual_review"
	if state.GateStatus == nil {
		state.GateStatus = OpenPilotGateStatus{}
	}
	state.GateStatus["merge"] = "merged"
	if state.ResumeInputs == nil {
		state.ResumeInputs = map[string]string{}
	}
	state.ResumeInputs["resume.open-pilot.merge-pr.merge_commit_sha"] = pr.MergeCommitSHA
	if state.IssueClosed {
		state.Phase = OpenPilotPhaseIssueClosed
		state.GateStatus["issue_close"] = "closed"
		state.RecommendedNextAction = "complete"
		state.ResumeInputs["resume.open-pilot.close-issue.issue_closed"] = "true"
	}
	state.UpdatedAt = now
	return state, string(message), nil
}

func (s *MemoryStore) ReconcileOpenPilotExternalMerge(expected OpenPilotIssueState, pr GiteaPullRequest, issue GiteaIssue) (OpenPilotIssueState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := openPilotIssueStateKey(expected.Repo, expected.Issue)
	current, ok := s.openPilotIssueStates[key]
	if !ok {
		return OpenPilotIssueState{}, ErrNotFound
	}
	run, ok := s.runs[current.RunID]
	if !ok {
		return OpenPilotIssueState{}, ErrNotFound
	}
	for _, candidate := range s.runs {
		if candidate.WorkflowID == string(WorkflowOpenPilot) && strings.EqualFold(candidate.Inputs["repo"], current.Repo) && candidate.Inputs["issue"] == current.Issue && (candidate.Status == RunQueued || candidate.Status == RunRunning) {
			return OpenPilotIssueState{}, ErrConflict
		}
	}
	now := s.now().UTC()
	state, message, err := reconciledOpenPilotExternalMerge(current, expected, run, pr, issue, now)
	if err != nil {
		return OpenPilotIssueState{}, err
	}
	if message == "" {
		return cloneOpenPilotIssueState(state), nil
	}
	s.openPilotIssueStates[key] = state
	s.events[run.ID] = append(s.events[run.ID], RunEvent{ID: newID("evt"), RunID: run.ID, Type: "open-pilot.external-merge.reconciled", Message: message, CreatedAt: now})
	return cloneOpenPilotIssueState(state), nil
}
