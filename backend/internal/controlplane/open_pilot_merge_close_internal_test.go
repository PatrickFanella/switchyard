package controlplane

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type scriptedGiteaMerger struct {
	mergeCalls       int
	closeCalls       int
	mergeResult      GiteaMergeResult
	closeIssue       GiteaIssue
	mergeErr         error
	mergeErrs        []error
	closeErr         error
	expectedIdentity GiteaPullRequest
}

func (s *scriptedGiteaMerger) MergePullRequest(_ context.Context, _ string, expected GiteaPullRequest) (GiteaMergeResult, error) {
	s.mergeCalls++
	s.expectedIdentity = expected
	if len(s.mergeErrs) > 0 {
		err := s.mergeErrs[0]
		s.mergeErrs = s.mergeErrs[1:]
		if err != nil {
			return GiteaMergeResult{}, err
		}
	}
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
	seedOpenPilotStateAtPhase(t, store, OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "123", Base: "main"}, OpenPilotPhasePRCreated)
	model := &captureModelExecutor{response: ModelResponse{Content: "APPROVED: looks good"}}
	reader := &scriptedCIGiteaReader{
		findPR:    testedPullRequestIdentity(),
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
	if restored, ok := openPilotCIPassedPullRequestFromResume(state.ResumeInputs); !ok || !sameGiteaPullRequestMergeIdentity(restored, testedPullRequestIdentity()) {
		t.Fatalf("persisted CI-tested identity = %+v, ok=%t", restored, ok)
	}
	if merger.mergeCalls != 1 || merger.closeCalls != 1 {
		t.Fatalf("merger calls = merge:%d close:%d", merger.mergeCalls, merger.closeCalls)
	}
	if !sameGiteaPullRequestMergeIdentity(merger.expectedIdentity, testedPullRequestIdentity()) {
		t.Fatalf("merge identity = %+v", merger.expectedIdentity)
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

func TestOpenPilotIssueClosedPersistsMergeCommitSHA(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	seedOpenPilotStateAtPhase(t, store, OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "123", GateStatus: OpenPilotGateStatus{"merge": "merged"}}, OpenPilotPhaseMerged)
	executor := openPilotExecutor{env: newOpenPilotAIReviewTestEnv(store)}
	state := map[string]any{
		"open-pilot.merge-result":       GiteaMergeResult{Merged: true, SHA: "deadbeef"},
		"open-pilot.issue-close-result": GiteaIssue{State: "closed"},
		"open-pilot.gate-status":        map[string]string{"merge": "merged", "issue_close": "closed"},
	}
	if err := executor.persistOpenPilotIssueState(Run{ID: "run-1", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}}, OpenPilotPhaseIssueClosed, "open-pilot.close-issue", "", state); err != nil {
		t.Fatalf("persistOpenPilotIssueState() error = %v", err)
	}

	saved, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if saved.Phase != OpenPilotPhaseIssueClosed || !saved.IssueClosed || saved.MergeCommitSHA != "deadbeef" {
		t.Fatalf("saved = %+v", saved)
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
	checkedPR := testedPullRequestIdentity()
	resume := map[string]string{}
	persistOpenPilotCIPassedPullRequest(resume, checkedPR)
	seedOpenPilotStateAtPhase(t, store, OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "123", RunID: "run-1", Base: "main", GateStatus: OpenPilotGateStatus{"ai_review": "approved", "ci_checks": "passed"}, PullRequestURL: "https://git/OWNER/REPO/pulls/7", PullRequestID: 99, PullRequestNum: 7, ResumeInputs: resume}, OpenPilotPhaseCIPassed)
	state := map[string]any{"open-pilot.pull-request": checkedPR, "open-pilot.ci-passed-pull-request": checkedPR, "open-pilot.gate-status": map[string]string{"ai_review": "approved", "ci_checks": "passed"}}
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

func TestOpenPilotMergeRequiresCIPassedHeadAndDurableBase(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*GiteaPullRequest)
	}{
		{name: "head", mutate: func(pr *GiteaPullRequest) { pr.Head.SHA = "" }},
		{name: "base", mutate: func(pr *GiteaPullRequest) { pr.Base.SHA = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewStore(func() time.Time { return time.Unix(100, 0) })
			if err := SeedDefaults(store); err != nil {
				t.Fatal(err)
			}
			pr := testedPullRequestIdentity()
			test.mutate(&pr)
			resume := map[string]string{}
			persistOpenPilotCIPassedPullRequest(resume, pr)
			seedOpenPilotStateAtPhase(t, store, OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "123", RunID: "run-1", Base: "main", GateStatus: OpenPilotGateStatus{"ai_review": "approved", "ci_checks": "passed"}, PullRequestID: pr.ID, PullRequestNum: pr.Number, ResumeInputs: resume}, OpenPilotPhaseCIPassed)
			merger := &scriptedGiteaMerger{mergeResult: GiteaMergeResult{Merged: true}}
			env := newOpenPilotAIReviewTestEnv(store)
			env.giteaMerger = merger
			env.openPilotMergeMode = "allow"
			state := map[string]any{"open-pilot.pull-request": pr, "open-pilot.ci-passed-pull-request": pr, "open-pilot.gate-status": map[string]string{"ai_review": "approved", "ci_checks": "passed"}}
			_, err := (openPilotExecutor{env: env}).executeOpenPilotMerge(t.Context(), Run{ID: "run-1", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}}, state)
			if err == nil || merger.mergeCalls != 0 {
				t.Fatalf("error/calls = %v/%d", err, merger.mergeCalls)
			}
		})
	}
}

func testedPullRequestIdentity() GiteaPullRequest {
	return GiteaPullRequest{
		ID: 99, Number: 7, State: "open", HTMLURL: "https://git/OWNER/REPO/pulls/7",
		Head: GiteaPullRequestRef{Repo: pullRequestRepoIdentity("OWNER/REPO"), Ref: "open-pilot/issue-123", SHA: "abc123"},
		Base: GiteaPullRequestRef{Repo: pullRequestRepoIdentity("OWNER/REPO"), Ref: "main", SHA: "base123"},
	}
}

func TestOpenPilotMergeRecoversDurableCITestedIdentity(t *testing.T) {
	for _, phase := range []OpenPilotPhase{OpenPilotPhaseCIPassed, OpenPilotPhaseMergeReady} {
		t.Run(string(phase), func(t *testing.T) {
			store := NewStore(func() time.Time { return time.Unix(100, 0) })
			if err := SeedDefaults(store); err != nil {
				t.Fatal(err)
			}
			run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}})
			if err != nil {
				t.Fatal(err)
			}
			pr := testedPullRequestIdentity()
			resume := map[string]string{}
			persistOpenPilotCIPassedPullRequest(resume, pr)
			seedOpenPilotStateAtPhase(t, store, OpenPilotIssueState{
				Repo: "OWNER/REPO", Issue: "123", RunID: run.ID, Base: "main", Branch: pr.Head.Ref,
				PullRequestURL: pr.HTMLURL, PullRequestID: pr.ID, PullRequestNum: pr.Number,
				GateStatus: OpenPilotGateStatus{"ai_review": "approved", "ci_checks": "passed"}, ResumeInputs: resume,
			}, phase)
			merger := &scriptedGiteaMerger{mergeResult: GiteaMergeResult{Merged: true, SHA: "deadbeef"}}
			env := newOpenPilotAIReviewTestEnv(store)
			env.giteaMerger = merger
			env.openPilotMergeMode = "allow"
			hydrated := map[string]any{}
			(openPilotExecutor{env: env}).HydratePipelineResumeState(run, Pipeline{}, hydrated)
			if !openPilotMergeStepReady(run, hydrated) {
				t.Fatalf("merge state not restored: %+v", hydrated)
			}
			execution, err := (openPilotExecutor{env: env}).executeOpenPilotMerge(t.Context(), run, hydrated)
			if err != nil || execution.Summary != "Merged pull request" {
				t.Fatalf("execution = %+v, error = %v", execution, err)
			}
			if merger.mergeCalls != 1 || !sameGiteaPullRequestMergeIdentity(merger.expectedIdentity, pr) {
				t.Fatalf("merge calls/identity = %d/%+v", merger.mergeCalls, merger.expectedIdentity)
			}
			saved, err := store.OpenPilotIssueState("OWNER/REPO", "123")
			if err != nil || saved.Phase != OpenPilotPhaseMerged || saved.GateStatus["merge"] != "merged" {
				t.Fatalf("saved = %+v, error = %v", saved, err)
			}
		})
	}
}

func TestOpenPilotMergeGenericRetryPreservesMergeReadyIdentity(t *testing.T) {
	t.Setenv("SWITCHYARD_RUN_RETRY_BACKOFF", "1s")
	now := time.Unix(100, 0).UTC()
	store := NewStore(func() time.Time { return now })
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	pr := testedPullRequestIdentity()
	pipeline := Pipeline{ID: "merge-retry", WorkflowID: string(WorkflowOpenPilot), Steps: []PipelineStep{{ID: "merge-pr", Position: 11, Name: "open-pilot.merge-pr", Executor: "gitea"}}}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), MaxAttempts: 2, PipelineSnapshot: &pipeline, Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}})
	if err != nil {
		t.Fatal(err)
	}
	resume := map[string]string{}
	persistOpenPilotCIPassedPullRequest(resume, pr)
	seedOpenPilotStateAtPhase(t, store, OpenPilotIssueState{
		Repo: "OWNER/REPO", Issue: "123", RunID: run.ID, Base: "main", Branch: pr.Head.Ref,
		PullRequestURL: pr.HTMLURL, PullRequestID: pr.ID, PullRequestNum: pr.Number,
		GateStatus: OpenPilotGateStatus{"ai_review": "approved", "ci_checks": "passed"}, ResumeInputs: resume,
	}, OpenPilotPhaseCIPassed)
	merger := &scriptedGiteaMerger{mergeErrs: []error{errors.New(strings.Repeat("temporary gitea error ", 300)), nil}, mergeResult: GiteaMergeResult{Merged: true, SHA: "deadbeef"}}
	env := newOpenPilotAIReviewTestEnv(store)
	env.giteaMerger = merger
	env.openPilotMergeMode = "allow"
	runner := NewRunner(store, MapExecutorRegistry{string(WorkflowOpenPilot): openPilotExecutor{env: env}})
	pool := NewWorkerPool(store, runner, 1, time.Minute, time.Hour, func() time.Time { return now })

	claimed, err := store.ClaimNextRun("worker-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	pool.execute(t.Context(), "worker-1", claimed)
	saved, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil || saved.Phase != OpenPilotPhaseMergeReady || saved.GateStatus["merge"] != "merge_ready" || saved.FailingGate != "merge" || saved.LastError == "" || len(saved.LastError) > openPilotCIRepairMaxFeedbackBytes {
		t.Fatalf("retry state = %+v, %v", saved, err)
	}
	restored, ok := openPilotCIPassedPullRequestFromResume(saved.ResumeInputs)
	if !ok || !sameGiteaPullRequestMergeIdentity(restored, pr) {
		t.Fatalf("retry identity = %+v, ok=%t", restored, ok)
	}

	now = now.Add(time.Second)
	claimed, err = store.ClaimNextRun("worker-2", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	pool.execute(t.Context(), "worker-2", claimed)
	saved, err = store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil || saved.Phase != OpenPilotPhaseMerged || saved.GateStatus["merge"] != "merged" || merger.mergeCalls != 2 {
		t.Fatalf("merged state/calls = %+v/%d, %v", saved, merger.mergeCalls, err)
	}
}

func TestHydratePipelineResumeStateRejectsDifferentRun(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	pr := testedPullRequestIdentity()
	resume := map[string]string{}
	persistOpenPilotCIPassedPullRequest(resume, pr)
	seedOpenPilotStateAtPhase(t, store, OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "123", RunID: "other-run", PullRequestID: pr.ID, PullRequestNum: pr.Number, GateStatus: OpenPilotGateStatus{"ai_review": "approved", "ci_checks": "passed"}, ResumeInputs: resume}, OpenPilotPhaseCIPassed)
	state := map[string]any{}
	(openPilotExecutor{env: newOpenPilotAIReviewTestEnv(store)}).HydratePipelineResumeState(Run{ID: "current-run", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}}, Pipeline{}, state)
	if _, ok := state["open-pilot.ci-passed-pull-request"]; ok || len(openPilotGateStatusFromState(state)) != 0 {
		t.Fatalf("hydrated foreign durable state: %+v", state)
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
