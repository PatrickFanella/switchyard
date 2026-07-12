package controlplane

import (
	"context"
	"strings"
	"testing"
	"time"
)

type scriptedGiteaMerger struct {
	mergeCalls  int
	closeCalls  int
	mergeResult GiteaMergeResult
	closeIssue  GiteaIssue
	mergeErr    error
	closeErr    error
}

func (s *scriptedGiteaMerger) MergePullRequest(_ context.Context, _ string, _ int) (GiteaMergeResult, error) {
	s.mergeCalls++
	if s.mergeErr != nil {
		return GiteaMergeResult{}, s.mergeErr
	}
	return s.mergeResult, nil
}

func (s *scriptedGiteaMerger) CloseIssue(_ context.Context, _ string, _ string) (GiteaIssue, error) {
	s.closeCalls++
	if s.closeErr != nil {
		return GiteaIssue{}, s.closeErr
	}
	return s.closeIssue, nil
}

func seedOpenPilotStateAtPhase(t *testing.T, store Store, state OpenPilotIssueState, target OpenPilotPhase) {
	t.Helper()
	if state.Repo == "" || state.Issue == "" {
		t.Fatal("seed state requires repo and issue")
	}
	state.Phase = OpenPilotPhaseRequested
	if _, err := store.UpsertOpenPilotIssueState(state); err != nil {
		t.Fatalf("seed requested state: %v", err)
	}
	chain := []OpenPilotPhase{OpenPilotPhaseValidated, OpenPilotPhaseBranchPrepared, OpenPilotPhaseAgentCompleted, OpenPilotPhaseVerified, OpenPilotPhasePushed, OpenPilotPhasePRCreated, OpenPilotPhaseAIReviewed, OpenPilotPhaseCIPassed, OpenPilotPhaseMergeReady, OpenPilotPhaseMerged, OpenPilotPhaseIssueClosed}
	for _, next := range chain {
		if next == target {
			if _, err := store.TransitionOpenPilotIssueState(state.Repo, state.Issue, next); err != nil {
				t.Fatalf("transition to %s: %v", next, err)
			}
			return
		}
		if _, err := store.TransitionOpenPilotIssueState(state.Repo, state.Issue, next); err != nil {
			t.Fatalf("transition to %s: %v", next, err)
		}
	}
}

func TestOpenPilotMergeAndCloseHappyPath(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	model := &captureModelExecutor{response: ModelResponse{Content: "APPROVED: looks good"}}
	reader := &scriptedCIGiteaReader{
		findPR:    GiteaPullRequest{ID: 99, Number: 7, HTMLURL: "https://git/OWNER/REPO/pulls/7", Head: GiteaPullRequestRef{Ref: "open-pilot/issue-123", SHA: "abc123"}},
		findFound: true,
		checks: []GiteaCheckSummary{{
			State:    "success",
			SHA:      "abc123",
			Statuses: []GiteaCommitStatus{{Context: "verify", Status: "success"}},
			Checks:   []GiteaCheckRun{{Name: "lint", Status: "completed", Conclusion: "success"}, {Name: "test", Status: "completed", Conclusion: "success"}},
		}},
	}
	merger := &scriptedGiteaMerger{
		mergeResult: GiteaMergeResult{Merged: true, SHA: "deadbeef", Message: "Merged PR #7"},
		closeIssue:  GiteaIssue{ID: 10, Number: 123, State: "closed", HTMLURL: "https://git/OWNER/REPO/issues/123"},
	}
	env := newOpenPilotAIReviewTestEnv(store)
	env.modelExecutor = model
	env.giteaReader = reader
	env.giteaMerger = merger
	env.openPilotCICheckMode = "require-pass"
	env.openPilotCIRequiredChecks = []string{"verify", "lint", "test"}
	env.openPilotMergeMode = "allow"
	env.openPilotCloseIssueMode = "allow"
	runner := &Runner{store: store}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{
		"repo":   "OWNER/REPO",
		"issue":  "123",
		"branch": "open-pilot/issue-123",
		"resume.open-pilot.pr-plan.pull_request_url":    "https://git/OWNER/REPO/pulls/7",
		"resume.open-pilot.pr-plan.pull_request_id":     "99",
		"resume.open-pilot.pr-plan.pull_request_number": "7",
		"resume.open-pilot.validate-issue.summary":      "Fetched issue: Fix switchyard (open)",
		"resume.open-pilot.agent-work.response":         "agent response",
	}})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	pipeline := Pipeline{ID: "test", WorkflowID: string(WorkflowOpenPilot), Steps: []PipelineStep{{ID: "ai-review", Position: 8, Name: "open-pilot.ai-review", Executor: "model", Profile: "operator-reviewer", Model: "${model}"}, {ID: "ci-checks", Position: 9, Name: "open-pilot.ci-checks", Executor: "gitea"}, {ID: "merge-pr", Position: 10, Name: "open-pilot.merge-pr", Executor: "gitea"}, {ID: "close-issue", Position: 11, Name: "open-pilot.close-issue", Executor: "gitea"}}}
	execution, err := runner.executePipeline(t.Context(), run, pipeline, openPilotExecutor{env: env}, openPilotExecutor{env: env})
	if err != nil {
		t.Fatalf("executePipeline() error = %v", err)
	}
	run, err = runner.finishExecution(run, "", execution, err)
	if err != nil {
		t.Fatalf("finishExecution() error = %v", err)
	}
	if run.Summary != "Open Pilot completed OWNER/REPO#123: PR merged and issue closed" {
		t.Fatalf("run.Summary = %q", run.Summary)
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if state.Phase != OpenPilotPhaseIssueClosed || !state.IssueClosed || state.MergeCommitSHA != "deadbeef" || state.GateStatus["merge"] != "merged" || state.GateStatus["issue_close"] != "closed" {
		t.Fatalf("state = %+v", state)
	}
	if merger.mergeCalls != 1 || merger.closeCalls != 1 {
		t.Fatalf("merger calls = merge:%d close:%d", merger.mergeCalls, merger.closeCalls)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if len(detail.Steps) != 4 || detail.Steps[2].Name != "open-pilot.merge-pr" || detail.Steps[2].OutputSummary != "Merged pull request" || detail.Steps[3].Name != "open-pilot.close-issue" || detail.Steps[3].OutputSummary != "Open Pilot completed OWNER/REPO#123: PR merged and issue closed" {
		t.Fatalf("steps = %+v", detail.Steps)
	}
	foundMerge := false
	foundClose := false
	for _, artifact := range detail.Artifacts {
		switch artifact.Name {
		case "open-pilot-merge.json":
			foundMerge = true
		case "open-pilot-issue-close.json":
			foundClose = true
		}
	}
	if !foundMerge || !foundClose {
		t.Fatalf("artifacts = %+v", detail.Artifacts)
	}
}

func TestOpenPilotMergeBlockedWhenPrerequisitesMissing(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	merger := &scriptedGiteaMerger{mergeResult: GiteaMergeResult{Merged: true, SHA: "deadbeef"}}
	env := newOpenPilotAIReviewTestEnv(store)
	env.giteaMerger = merger
	env.openPilotMergeMode = "allow"
	executor := openPilotExecutor{env: env}
	run := Run{ID: "run-1", WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}}
	state := map[string]any{"open-pilot.pull-request": GiteaPullRequest{ID: 99, Number: 7, HTMLURL: "https://git/OWNER/REPO/pulls/7"}, "open-pilot.gate-status": map[string]string{"ai_review": "changes_requested", "ci_checks": "passed"}}
	if _, err := executor.executeOpenPilotMerge(t.Context(), run, state); err == nil || !strings.Contains(err.Error(), "merge prerequisites not met") {
		t.Fatalf("expected merge prerequisites error, got %v", err)
	}
	saved, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if saved.Phase != OpenPilotPhaseFailed || saved.GateStatus["merge"] != "failed" || saved.FailingGate != "merge" || saved.RecommendedNextAction == "" || saved.OperatorSummary == "" {
		t.Fatalf("saved = %+v", saved)
	}
	if merger.mergeCalls != 0 {
		t.Fatalf("expected merger not to be called, got %d", merger.mergeCalls)
	}

	state = map[string]any{"open-pilot.pull-request": GiteaPullRequest{ID: 99, Number: 7, HTMLURL: "https://git/OWNER/REPO/pulls/7"}, "open-pilot.gate-status": map[string]string{"ai_review": "approved", "ci_checks": "failed"}}
	if _, err := store.TransitionOpenPilotIssueState("OWNER/REPO", "123", OpenPilotPhaseRequested); err != nil {
		t.Fatalf("reset transition: %v", err)
	}
	if _, err := executor.executeOpenPilotMerge(t.Context(), run, state); err == nil || !strings.Contains(err.Error(), "merge prerequisites not met") {
		t.Fatalf("expected merge prerequisites error for CI failure, got %v", err)
	}
	saved, err = store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if saved.Phase != OpenPilotPhaseFailed || saved.GateStatus["merge"] != "failed" || saved.FailingGate != "merge" || saved.RecommendedNextAction == "" || saved.OperatorSummary == "" {
		t.Fatalf("saved = %+v", saved)
	}
}

func TestOpenPilotMergeDisabledOrDryRunDoNotAdvance(t *testing.T) {
	for _, mergeMode := range []string{"disabled", "dry-run"} {
		t.Run(mergeMode, func(t *testing.T) {
			store := NewStore(func() time.Time { return time.Unix(100, 0) })
			if err := SeedDefaults(store); err != nil {
				t.Fatalf("SeedDefaults() error = %v", err)
			}
			merger := &scriptedGiteaMerger{mergeResult: GiteaMergeResult{Merged: true, SHA: "deadbeef"}}
			env := newOpenPilotAIReviewTestEnv(store)
			env.giteaMerger = merger
			env.openPilotMergeMode = mergeMode
			executor := openPilotExecutor{env: env}
			seedOpenPilotStateAtPhase(t, store, OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "123", GateStatus: OpenPilotGateStatus{"ai_review": "approved", "ci_checks": "passed"}, PullRequestURL: "https://git/OWNER/REPO/pulls/7", PullRequestID: 99, PullRequestNum: 7}, OpenPilotPhaseCIPassed)
			state := map[string]any{"open-pilot.pull-request": GiteaPullRequest{ID: 99, Number: 7, HTMLURL: "https://git/OWNER/REPO/pulls/7"}, "open-pilot.gate-status": map[string]string{"ai_review": "approved", "ci_checks": "passed"}}
			if _, err := executor.executeOpenPilotMerge(t.Context(), Run{ID: "run-1", WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}}, state); err != nil {
				t.Fatalf("executeOpenPilotMerge() error = %v", err)
			}
			if merger.mergeCalls != 0 {
				t.Fatalf("merge calls = %d", merger.mergeCalls)
			}
			saved, err := store.OpenPilotIssueState("OWNER/REPO", "123")
			if err != nil {
				t.Fatalf("OpenPilotIssueState() error = %v", err)
			}
			wantMergeGate := "skipped"
			if mergeMode == "dry-run" {
				wantMergeGate = "dry-run"
			}
			if saved.Phase != OpenPilotPhaseCIPassed || saved.MergeCommitSHA != "" || saved.GateStatus["merge"] != wantMergeGate {
				t.Fatalf("saved = %+v", saved)
			}
		})
	}
}

func TestOpenPilotMergeRejectsUnconfirmedMergeResult(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	merger := &scriptedGiteaMerger{mergeResult: GiteaMergeResult{Merged: false, Message: "not merged"}}
	env := newOpenPilotAIReviewTestEnv(store)
	env.giteaMerger = merger
	env.openPilotMergeMode = "allow"
	executor := openPilotExecutor{env: env}
	seedOpenPilotStateAtPhase(t, store, OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "123", GateStatus: OpenPilotGateStatus{"ai_review": "approved", "ci_checks": "passed"}, PullRequestURL: "https://git/OWNER/REPO/pulls/7", PullRequestID: 99, PullRequestNum: 7}, OpenPilotPhaseCIPassed)
	state := map[string]any{"open-pilot.pull-request": GiteaPullRequest{ID: 99, Number: 7, HTMLURL: "https://git/OWNER/REPO/pulls/7"}, "open-pilot.gate-status": map[string]string{"ai_review": "approved", "ci_checks": "passed"}}
	_, err := executor.executeOpenPilotMerge(t.Context(), Run{ID: "run-1", WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}}, state)
	if err == nil || !strings.Contains(err.Error(), "did not confirm merged") {
		t.Fatalf("expected unconfirmed merge error, got %v", err)
	}
	saved, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if saved.Phase != OpenPilotPhaseFailed || saved.GateStatus["merge"] != "failed" || saved.MergeCommitSHA != "" || saved.FailingGate != "merge" || saved.RecommendedNextAction == "" || saved.OperatorSummary == "" {
		t.Fatalf("saved = %+v", saved)
	}
}

func TestOpenPilotCloseDisabledOrDryRunDoNotMarkClosed(t *testing.T) {
	for _, closeMode := range []string{"disabled", "dry-run"} {
		t.Run(closeMode, func(t *testing.T) {
			store := NewStore(func() time.Time { return time.Unix(100, 0) })
			if err := SeedDefaults(store); err != nil {
				t.Fatalf("SeedDefaults() error = %v", err)
			}
			merger := &scriptedGiteaMerger{closeIssue: GiteaIssue{ID: 10, Number: 123, State: "closed"}}
			env := newOpenPilotAIReviewTestEnv(store)
			env.giteaMerger = merger
			env.openPilotCloseIssueMode = closeMode
			executor := openPilotExecutor{env: env}
			seedOpenPilotStateAtPhase(t, store, OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "123", GateStatus: OpenPilotGateStatus{"ai_review": "approved", "ci_checks": "passed", "merge": "merged"}, PullRequestURL: "https://git/OWNER/REPO/pulls/7", PullRequestID: 99, PullRequestNum: 7, MergeCommitSHA: "deadbeef"}, OpenPilotPhaseMerged)
			state := map[string]any{"open-pilot.gate-status": map[string]string{"merge": "merged"}, "open-pilot.merge-commit-sha": "deadbeef"}
			if _, err := executor.executeOpenPilotCloseIssue(t.Context(), Run{ID: "run-1", WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}}, state); err != nil {
				t.Fatalf("executeOpenPilotCloseIssue() error = %v", err)
			}
			if merger.closeCalls != 0 {
				t.Fatalf("close calls = %d", merger.closeCalls)
			}
			saved, err := store.OpenPilotIssueState("OWNER/REPO", "123")
			if err != nil {
				t.Fatalf("OpenPilotIssueState() error = %v", err)
			}
			wantCloseGate := "skipped"
			if closeMode == "dry-run" {
				wantCloseGate = "dry-run"
			}
			if saved.Phase != OpenPilotPhaseMerged || saved.IssueClosed || saved.GateStatus["issue_close"] != wantCloseGate {
				t.Fatalf("saved = %+v", saved)
			}
		})
	}
}

func TestOpenPilotCloseRejectsNonClosedIssueResponse(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	merger := &scriptedGiteaMerger{closeIssue: GiteaIssue{ID: 10, Number: 123, State: "open"}}
	env := newOpenPilotAIReviewTestEnv(store)
	env.giteaMerger = merger
	env.openPilotCloseIssueMode = "allow"
	executor := openPilotExecutor{env: env}
	seedOpenPilotStateAtPhase(t, store, OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "123", GateStatus: OpenPilotGateStatus{"ai_review": "approved", "ci_checks": "passed", "merge": "merged"}, PullRequestURL: "https://git/OWNER/REPO/pulls/7", PullRequestID: 99, PullRequestNum: 7, MergeCommitSHA: "deadbeef"}, OpenPilotPhaseMerged)
	state := map[string]any{"open-pilot.gate-status": map[string]string{"merge": "merged"}, "open-pilot.merge-commit-sha": "deadbeef"}
	_, err := executor.executeOpenPilotCloseIssue(t.Context(), Run{ID: "run-1", WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}}, state)
	if err == nil || !strings.Contains(err.Error(), "response state") {
		t.Fatalf("expected non-closed issue error, got %v", err)
	}
	saved, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if saved.Phase != OpenPilotPhaseFailed || saved.GateStatus["issue_close"] != "failed" || saved.IssueClosed || saved.FailingGate != "issue_close" || saved.RecommendedNextAction == "" || saved.OperatorSummary == "" {
		t.Fatalf("saved = %+v", saved)
	}
}

func TestOpenPilotCloseIssueResumesAfterMergedState(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	seedOpenPilotStateAtPhase(t, store, OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "123", GateStatus: OpenPilotGateStatus{"ai_review": "approved", "ci_checks": "passed", "merge": "merged"}, PullRequestURL: "https://git/OWNER/REPO/pulls/7", PullRequestID: 99, PullRequestNum: 7, MergeCommitSHA: "deadbeef"}, OpenPilotPhaseMerged)
	merger := &scriptedGiteaMerger{closeIssue: GiteaIssue{ID: 10, Number: 123, State: "closed", HTMLURL: "https://git/OWNER/REPO/issues/123"}}
	env := newOpenPilotAIReviewTestEnv(store)
	env.giteaMerger = merger
	env.openPilotCloseIssueMode = "allow"
	runner := &Runner{store: store}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123", "retry_from_step": "open-pilot.close-issue", "retry_from_step_position": "12"}})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	pipeline := Pipeline{ID: "test", WorkflowID: string(WorkflowOpenPilot), Steps: []PipelineStep{{ID: "close-issue", Position: 12, Name: "open-pilot.close-issue", Executor: "gitea"}}}
	execution, err := runner.executePipeline(t.Context(), run, pipeline, openPilotExecutor{env: env}, openPilotExecutor{env: env})
	if err != nil {
		t.Fatalf("executePipeline() error = %v", err)
	}
	run, err = runner.finishExecution(run, "", execution, err)
	if err != nil {
		t.Fatalf("finishExecution() error = %v", err)
	}
	if run.Summary != "Open Pilot completed OWNER/REPO#123: PR merged and issue closed" {
		t.Fatalf("run.Summary = %q", run.Summary)
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if state.Phase != OpenPilotPhaseIssueClosed || !state.IssueClosed || state.MergeCommitSHA != "deadbeef" {
		t.Fatalf("state = %+v", state)
	}
	if merger.closeCalls != 1 {
		t.Fatalf("close calls = %d", merger.closeCalls)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if len(detail.Steps) != 1 || detail.Steps[0].OutputSummary != "Open Pilot completed OWNER/REPO#123: PR merged and issue closed" {
		t.Fatalf("detail = %+v", detail.Steps)
	}
}
