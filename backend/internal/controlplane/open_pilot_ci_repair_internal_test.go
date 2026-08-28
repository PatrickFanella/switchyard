package controlplane

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type ciRepairHandoffExecutor struct {
	store Store
}

func (e ciRepairHandoffExecutor) Execute(context.Context, Run) (WorkflowExecution, error) {
	return WorkflowExecution{}, nil
}

func (e ciRepairHandoffExecutor) ExecutePipelineStep(_ context.Context, run Run, step PipelineStep, _ map[string]any) (PipelineStepExecution, error) {
	if step.ID != "ci-checks" {
		return PipelineStepExecution{}, nil
	}
	request := testOpenPilotCIRepairRequest(run, run.ClaimedBy, "verify failed")
	result, err := e.store.HandleOpenPilotCIFailure(request)
	if err != nil {
		return PipelineStepExecution{}, err
	}
	return PipelineStepExecution{Summary: result.ParentRun.Summary, Handoff: true}, nil
}

func TestBuildOpenPilotCIRepairFeedbackRequiresNamedCurrentHeadFailure(t *testing.T) {
	required := []string{"verify"}
	snapshot := GiteaCheckSummary{SHA: "current", State: "failure", Statuses: []GiteaCommitStatus{{Context: "verify", Status: "failure", Description: "secret log", TargetURL: "https://user:pass@ci.example/job/1?token=secret#log"}}}
	feedback, ok := buildOpenPilotCIRepairFeedback(required, snapshot, "current")
	if !ok || !strings.Contains(feedback, "verify") || !strings.Contains(feedback, "https://ci.example/job/1") {
		t.Fatalf("feedback = %q, %t", feedback, ok)
	}
	for _, forbidden := range []string{"user", "pass", "token", "secret", "#log"} {
		if strings.Contains(feedback, forbidden) {
			t.Fatalf("feedback contains %q: %q", forbidden, feedback)
		}
	}
	for name, candidate := range map[string]GiteaCheckSummary{
		"missing sha": {State: "failure", Statuses: snapshot.Statuses},
		"stale sha":   {SHA: "stale", State: "failure", Statuses: snapshot.Statuses},
		"aggregate":   {SHA: "current", State: "failure"},
		"pending":     {SHA: "current", Statuses: []GiteaCommitStatus{{Context: "verify", Status: "pending"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if got, concrete := buildOpenPilotCIRepairFeedback(required, candidate, "current"); concrete || got != "" {
				t.Fatalf("feedback = %q, %t", got, concrete)
			}
		})
	}
}

func TestBuildOpenPilotCIRepairFeedbackIsBounded(t *testing.T) {
	required := make([]string, 20)
	statuses := make([]GiteaCommitStatus, 20)
	for i := range required {
		required[i] = strings.Repeat("x", 200) + string(rune('a'+i))
		statuses[i] = GiteaCommitStatus{Context: required[i], Status: "failure", TargetURL: "https://ci.example/" + strings.Repeat("p", 800)}
	}
	feedback, ok := buildOpenPilotCIRepairFeedback(required, GiteaCheckSummary{SHA: "sha", Statuses: statuses}, "sha")
	if !ok || len(feedback) > openPilotCIFeedbackMaxTotal || strings.Count(feedback, "\n- ") > openPilotCIFeedbackMaxChecks {
		t.Fatalf("feedback length/checks = %d/%d", len(feedback), strings.Count(feedback, "\n- "))
	}
}

func TestBuildOpenPilotCIRepairFeedbackIncludesAllTerminalFailures(t *testing.T) {
	for _, status := range []string{"failure", "error", "cancelled", "timed_out", "action_required"} {
		t.Run(status, func(t *testing.T) {
			feedback, ok := buildOpenPilotCIRepairFeedback([]string{"verify"}, GiteaCheckSummary{SHA: "head", Statuses: []GiteaCommitStatus{{Context: "verify", Status: status}}}, "head")
			if !ok || !strings.Contains(feedback, "verify") {
				t.Fatalf("feedback = %q, %t", feedback, ok)
			}
		})
	}
}

func TestValidateCreateRunInputRejectsCIRepairKeys(t *testing.T) {
	for _, key := range []string{openPilotCIRepairCountKey, openPilotCIRepairFeedbackKey, openPilotCIRepairOriginalPromptKey} {
		err := validateCreateRunInput(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "1", key: "injected"}})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("key %q error = %v", key, err)
		}
	}
}

func TestOpenPilotCIChecksRejectInvalidAutoRepairMode(t *testing.T) {
	env := newOpenPilotCICheckTestEnv(nil)
	env.openPilotCICheckMode = "disabled"
	env.openPilotCIAutoRepairMode = "invalid"
	_, err := (openPilotExecutor{env: env}).executeOpenPilotCIChecks(t.Context(), Run{}, map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "OPEN_PILOT_CI_AUTO_REPAIR_MODE") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateOpenPilotCIPullRequestIdentityRejectsMismatch(t *testing.T) {
	repo := struct {
		Name     string `json:"name,omitempty"`
		FullName string `json:"full_name,omitempty"`
	}{Name: "REPO", FullName: "OWNER/REPO"}
	expectedSHA := strings.Repeat("a", 40)
	persisted := GiteaPullRequest{ID: 9, Number: 4, HTMLURL: "https://git.example/OWNER/REPO/pulls/4"}
	fresh := GiteaPullRequest{ID: 9, Number: 4, State: "open", HTMLURL: persisted.HTMLURL, Head: GiteaPullRequestRef{Ref: "open-pilot/issue-1", SHA: expectedSHA, Repo: &repo}, Base: GiteaPullRequestRef{Ref: "main", Repo: &repo}}
	if err := validateOpenPilotCIPullRequestIdentity("OWNER/REPO", "open-pilot/issue-1", "main", expectedSHA, persisted, fresh); err != nil {
		t.Fatalf("valid identity error = %v", err)
	}
	fresh.Number = 5
	if err := validateOpenPilotCIPullRequestIdentity("OWNER/REPO", "open-pilot/issue-1", "main", expectedSHA, persisted, fresh); err == nil {
		t.Fatal("expected pull request number mismatch")
	}
	fresh.Number, fresh.State = 4, ""
	if err := validateOpenPilotCIPullRequestIdentity("OWNER/REPO", "open-pilot/issue-1", "main", expectedSHA, persisted, fresh); err == nil {
		t.Fatal("expected missing open state rejection")
	}
	fresh.State, fresh.Base.Ref = "open", "release"
	if err := validateOpenPilotCIPullRequestIdentity("OWNER/REPO", "open-pilot/issue-1", "main", expectedSHA, persisted, fresh); err == nil {
		t.Fatal("expected wrong base branch rejection")
	}
	fresh.Base.Ref = "main"
	otherRepo := repo
	otherRepo.FullName = "OTHER/REPO"
	fresh.Base.Repo = &otherRepo
	if err := validateOpenPilotCIPullRequestIdentity("OWNER/REPO", "open-pilot/issue-1", "main", expectedSHA, persisted, fresh); err == nil {
		t.Fatal("expected wrong base repository rejection")
	}
	fresh.Base.Repo = &repo
	fresh.Head.SHA = strings.Repeat("b", 40)
	if err := validateOpenPilotCIPullRequestIdentity("OWNER/REPO", "open-pilot/issue-1", "main", expectedSHA, persisted, fresh); err == nil {
		t.Fatal("expected persisted commit SHA mismatch rejection")
	}
}

func TestOpenPilotCIRepairRevalidatesPRAndWorktreeBeforeOpenCode(t *testing.T) {
	for _, test := range []struct {
		name       string
		change     string
		wantPrompt bool
	}{
		{name: "exact identity", wantPrompt: true},
		{name: "remote head changed", change: "remote"},
		{name: "local head changed", change: "local"},
		{name: "base branch changed", change: "base"},
		{name: "dirty tracked worktree", change: "dirty"},
		{name: "untracked worktree", change: "untracked"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repoPath := initWorktreeTestRepo(t)
			manager := NewGitOpenPilotWorktreeManager(OpenPilotWorktreeAllow, repoPath, t.TempDir())
			plan, err := manager.Prepare(t.Context(), Run{ID: "repair-child"}, "owner/repo", "main", "open-pilot/issue-1")
			if err != nil {
				t.Fatal(err)
			}
			localSHA := strings.TrimSpace(runGitOutput(t, plan.WorktreePath, "rev-parse", "HEAD"))
			markerSHA, remoteSHA := localSHA, localSHA
			switch test.change {
			case "remote":
				remoteSHA = "remote-changed"
			case "local":
				markerSHA, remoteSHA = "expected-other", "expected-other"
			case "dirty":
				if err := os.WriteFile(filepath.Join(plan.WorktreePath, "README.md"), []byte("dirty\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "untracked":
				if err := os.WriteFile(filepath.Join(plan.WorktreePath, "untracked.txt"), []byte("dirty\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			pr := ciRepairTestPullRequest("owner/repo", plan.Branch, remoteSHA)
			if test.change == "base" {
				pr.Base.Ref = "release"
			}
			reader := &scriptedCIGiteaReader{findPR: pr, findFound: true}
			openCode := &captureOpenCodeExecutor{response: OpenCodeResponse{Content: "repaired"}}
			env := newOpenPilotAIReviewTestEnv(nil)
			env.giteaReader, env.openCodeExecutor = reader, openCode
			env.issueFetcher = &stubIssueFetcher{issue: GiteaIssue{ID: 1, Number: 1, Repository: "owner/repo", State: "open"}}
			run := Run{ID: "repair-child", Inputs: map[string]string{
				"repo": "owner/repo", "issue": "1", openPilotCIRepairParentRunIDKey: "parent",
				openPilotCIRepairRepoKey: "owner/repo", openPilotCIRepairPRIDKey: "90", openPilotCIRepairPRNumberKey: "9",
				openPilotCIRepairHeadBranchKey: plan.Branch, openPilotCIRepairFailedHeadSHAKey: markerSHA,
				openPilotCIRepairBaseRepoKey: "owner/repo", openPilotCIRepairBaseBranchKey: plan.BaseBranch,
			}}
			result, err := (openPilotExecutor{env: env}).ExecutePipelineStep(t.Context(), run, PipelineStep{ID: "agent-work", Name: "open-pilot.agent-work"}, map[string]any{"open-pilot.prompt": "repair", "open-pilot.branch-plan": plan})
			if err != nil {
				t.Fatalf("ExecutePipelineStep() error = %v", err)
			}
			if got := len(openCode.requests); got != 0 && !test.wantPrompt {
				t.Fatalf("OpenCode requests = %d", got)
			}
			if test.wantPrompt && len(openCode.requests) != 1 {
				t.Fatalf("OpenCode requests = %d", len(openCode.requests))
			}
			if !test.wantPrompt && !result.Halt {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func ciRepairTestPullRequest(repo, branch, sha string) GiteaPullRequest {
	refRepo := &struct {
		Name     string `json:"name,omitempty"`
		FullName string `json:"full_name,omitempty"`
	}{Name: "repo", FullName: repo}
	return GiteaPullRequest{ID: 90, Number: 9, State: "open", HTMLURL: "https://git.example/owner/repo/pulls/9", Head: GiteaPullRequestRef{Ref: branch, SHA: sha, Repo: refRepo}, Base: GiteaPullRequestRef{Ref: "main", Repo: refRepo}}
}

func TestMemoryStoreHandlesOpenPilotCIFailureAtomically(t *testing.T) {
	store, claimed, pipeline := seedOpenPilotCIRepairParent(t)
	request := testOpenPilotCIRepairRequest(claimed, "worker-1", "Required checks failed:\n- verify: https://ci.example/1")
	request.ChildRun = CreateRunInput{MaxAttempts: 3, Inputs: map[string]string{"resume.open-pilot.prompt": "repair verify"}}

	first, err := store.HandleOpenPilotCIFailure(request)
	if err != nil {
		t.Fatalf("HandleOpenPilotCIFailure() error = %v", err)
	}
	if !first.Queued || first.Attempt != 1 || first.ChildRun == nil || first.ChildRun.MaxAttempts != 1 || first.ParentRun.Status != RunFailed || first.ParentRun.MaxAttempts != first.ParentRun.AttemptCount {
		t.Fatalf("first = %+v", first)
	}
	child := *first.ChildRun
	if !reflect.DeepEqual(child.PipelineSnapshot, &pipeline) || child.Inputs["retry_from_step_position"] != "7" || child.Inputs[openPilotCIRepairParentRunIDKey] != claimed.ID || child.Inputs[openPilotCIRepairCountKey] != "1" {
		t.Fatalf("child = %+v", child)
	}
	if child.Inputs["resume.open-pilot.prompt"] != "repair verify" || child.Inputs[openPilotCIRepairOriginalPromptKey] != "original issue prompt" || child.Inputs["opencode_session_id"] != "session-1" {
		t.Fatalf("child inputs = %+v", child.Inputs)
	}
	if child.Inputs[openPilotCIRepairRepoKey] != "OWNER/REPO" || child.Inputs[openPilotCIRepairPRIDKey] != "90" || child.Inputs[openPilotCIRepairPRNumberKey] != "9" || child.Inputs[openPilotCIRepairHeadBranchKey] != "open-pilot/issue-1" || child.Inputs[openPilotCIRepairFailedHeadSHAKey] != "failed-head" {
		t.Fatalf("protected child inputs = %+v", child.Inputs)
	}
	detail, err := store.RunDetail(claimed.ID)
	if err != nil || len(detail.Artifacts) != 1 || detail.Artifacts[0].Name != "open-pilot-ci-checks.json" || !strings.Contains(detail.Artifacts[0].URI, "verify") {
		t.Fatalf("parent artifacts = %+v, %v", detail.Artifacts, err)
	}
	replay, err := store.HandleOpenPilotCIFailure(request)
	if err != nil || !replay.Replayed || replay.ChildRun == nil || replay.ChildRun.ID != child.ID {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if detail, err = store.RunDetail(claimed.ID); err != nil || len(detail.Artifacts) != 1 {
		t.Fatalf("replay duplicated artifact: %+v, %v", detail.Artifacts, err)
	}

	secondClaim, err := store.ClaimNextRun("worker-2", time.Minute)
	if err != nil {
		t.Fatalf("ClaimNextRun(child) error = %v", err)
	}
	secondRequest := testOpenPilotCIRepairRequest(secondClaim, "worker-2", "verify failed again")
	secondRequest.ChildRun = CreateRunInput{Inputs: map[string]string{"resume.open-pilot.prompt": "repair again"}}
	second, err := store.HandleOpenPilotCIFailure(secondRequest)
	if err != nil || !second.Queued || second.Attempt != 2 || second.ChildRun == nil || second.ChildRun.Inputs[openPilotCIRepairOriginalPromptKey] != "original issue prompt" {
		t.Fatalf("second = %+v, %v", second, err)
	}
	thirdClaim, err := store.ClaimNextRun("worker-3", time.Minute)
	if err != nil {
		t.Fatalf("ClaimNextRun(second child) error = %v", err)
	}
	third, err := store.HandleOpenPilotCIFailure(testOpenPilotCIRepairRequest(thirdClaim, "worker-3", "verify failed third time"))
	if err != nil || !third.Exhausted || third.Queued || third.ChildRun != nil || third.State.Phase != OpenPilotPhaseFailed || third.ParentRun.MaxAttempts != third.ParentRun.AttemptCount {
		t.Fatalf("third = %+v, %v", third, err)
	}
	replayedExhausted, err := store.HandleOpenPilotCIFailure(testOpenPilotCIRepairRequest(thirdClaim, "worker-3", "verify failed third time"))
	if err != nil || !replayedExhausted.Replayed || !replayedExhausted.Exhausted || replayedExhausted.Attempt != openPilotCIRepairMaxAttempts {
		t.Fatalf("exhausted replay = %+v, %v", replayedExhausted, err)
	}
	if detail, err := store.RunDetail(thirdClaim.ID); err != nil || len(detail.Artifacts) != 1 {
		t.Fatalf("exhausted replay duplicated artifact: %+v, %v", detail.Artifacts, err)
	}
}

func TestMemoryStoreRejectsStaleCIRepairClaim(t *testing.T) {
	store, claimed, _ := seedOpenPilotCIRepairParent(t)
	for name, request := range map[string]OpenPilotCIRepairRequest{
		"worker":  testOpenPilotCIRepairRequest(claimed, "stale", "failed"),
		"attempt": testOpenPilotCIRepairRequest(claimed, "worker-1", "failed"),
	} {
		t.Run(name, func(t *testing.T) {
			if name == "attempt" {
				request.ExpectedAttemptCount++
			}
			if _, err := store.HandleOpenPilotCIFailure(request); !errors.Is(err, ErrConflict) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	detail, err := store.RunDetail(claimed.ID)
	if err != nil || detail.Run.Status != RunRunning {
		t.Fatalf("parent changed after rejected handoff: %+v, %v", detail.Run, err)
	}
}

func TestMemoryStoreRejectsExpiredCIRepairLease(t *testing.T) {
	store, claimed, _ := seedOpenPilotCIRepairParent(t)
	store.now = func() time.Time { return time.Unix(161, 0) }
	if _, err := store.HandleOpenPilotCIFailure(testOpenPilotCIRepairRequest(claimed, "worker-1", "failed")); !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v", err)
	}
	detail, err := store.RunDetail(claimed.ID)
	if err != nil || detail.Run.Status != RunRunning {
		t.Fatalf("parent changed after expired lease: %+v, %v", detail.Run, err)
	}
}

func TestMemoryStoreRejectsStaleCIRepairAffinityBeforeMutation(t *testing.T) {
	store, claimed, _ := seedOpenPilotCIRepairParent(t)
	request := testOpenPilotCIRepairRequest(claimed, "worker-1", "failed")
	request.Target.ExecutorGeneration = "stale-generation"
	before := len(store.ListRuns())
	if _, err := store.HandleOpenPilotCIFailure(request); !errors.Is(err, ErrConflict) {
		t.Fatalf("HandleOpenPilotCIFailure() error = %v", err)
	}
	detail, err := store.RunDetail(claimed.ID)
	if err != nil || detail.Run.Status != RunRunning || len(store.ListRuns()) != before {
		t.Fatalf("parent=%+v runs=%d err=%v", detail.Run, len(store.ListRuns()), err)
	}
}

func TestMemoryStoreRejectsStaleCIRepairReplayAffinity(t *testing.T) {
	store, claimed, _ := seedOpenPilotCIRepairParent(t)
	request := testOpenPilotCIRepairRequest(claimed, "worker-1", "failed")
	first, err := store.HandleOpenPilotCIFailure(request)
	if err != nil || first.ChildRun == nil {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	request.Target.ExecutorGeneration = "stale-generation"
	if _, err := store.HandleOpenPilotCIFailure(request); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale replay error = %v", err)
	}
}

func TestMemoryStoreRejectsForeignWorkflowCIRepairParent(t *testing.T) {
	store, claimed, _ := seedOpenPilotCIRepairParent(t)
	store.mu.Lock()
	parent := store.runs[claimed.ID]
	parent.WorkflowID = "generic"
	store.runs[claimed.ID] = parent
	store.mu.Unlock()
	request := testOpenPilotCIRepairRequest(claimed, "worker-1", "failed")
	if _, err := store.HandleOpenPilotCIFailure(request); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign workflow error = %v", err)
	}
}

func TestMemoryExternalResumeStripsCIRepairControlInputs(t *testing.T) {
	store, claimed, _ := seedOpenPilotCIRepairParent(t)
	first, err := store.HandleOpenPilotCIFailure(testOpenPilotCIRepairRequest(claimed, "worker-1", "failed"))
	if err != nil {
		t.Fatal(err)
	}
	child := *first.ChildRun
	if _, err := store.FinishRun(child.ID, RunFailed, "repair failed"); err != nil {
		t.Fatal(err)
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "1")
	if err != nil {
		t.Fatal(err)
	}
	state.Phase, state.LastStepName = OpenPilotPhaseFailed, "open-pilot.ci-checks"
	if _, err := store.UpsertOpenPilotIssueState(state); err != nil {
		t.Fatal(err)
	}
	_, resumed, err := store.ClaimAndCreateOpenPilotResumeRun("OWNER/REPO", "1", trustedOpenPilotInput("OWNER/REPO", "1", CreateRunInput{WorkflowID: string(WorkflowOpenPilot), MaxAttempts: 2, Inputs: map[string]string{openPilotCIRepairCountKey: "0", openPilotCIRepairFeedbackKey: "forged", openPilotCIRepairOriginalPromptKey: "forged"}}))
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Inputs["retry_from_step"] != "open-pilot.ci-checks" || resumed.Inputs[openPilotCIRepairCountKey] != "1" || resumed.Inputs[openPilotCIRepairFeedbackKey] != "failed" || resumed.Inputs[openPilotCIRepairOriginalPromptKey] != "original issue prompt" {
		t.Fatalf("resume cursor/context = %+v", resumed.Inputs)
	}
	for key := range resumed.Inputs {
		if strings.HasPrefix(key, "resume.open-pilot.ci-repair.") && !openPilotCIRepairExternalResumeInputAllowed(key) {
			t.Fatalf("external resume retained protected input %q", key)
		}
	}
}

func TestMemoryStoreSerializesDuplicateCIRepairHandoff(t *testing.T) {
	store, claimed, _ := seedOpenPilotCIRepairParent(t)
	request := testOpenPilotCIRepairRequest(claimed, "worker-1", "verify failed")
	request.ChildRun = CreateRunInput{Inputs: map[string]string{"resume.open-pilot.prompt": "repair"}}
	results := make(chan OpenPilotCIRepairResult, 2)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			result, err := store.HandleOpenPilotCIFailure(request)
			results <- result
			errs <- err
		}()
	}
	ids := map[string]bool{}
	replays := 0
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("HandleOpenPilotCIFailure() error = %v", err)
		}
		result := <-results
		if result.ChildRun == nil {
			t.Fatalf("result = %+v", result)
		}
		ids[result.ChildRun.ID] = true
		if result.Replayed {
			replays++
		}
	}
	if len(ids) != 1 || replays != 1 {
		t.Fatalf("child IDs/replays = %+v/%d", ids, replays)
	}
}

func TestRunnerReturnsTerminalParentAfterCIRepairHandoff(t *testing.T) {
	store, claimed, _ := seedOpenPilotCIRepairParent(t)
	claimed.Inputs["retry_from_step"] = "open-pilot.ci-checks"
	claimed.Inputs["retry_from_step_position"] = "12"
	store.mu.Lock()
	store.runs[claimed.ID] = claimed
	store.mu.Unlock()
	runner := NewRunner(store, MapExecutorRegistry{string(WorkflowOpenPilot): ciRepairHandoffExecutor{store: store}})
	finished, err := runner.ExecuteClaimedRun(t.Context(), claimed, "worker-1")
	if err != nil {
		t.Fatalf("ExecuteClaimedRun() error = %v", err)
	}
	if finished.Status != RunFailed || finished.MaxAttempts != finished.AttemptCount {
		t.Fatalf("finished = %+v", finished)
	}
	if _, err := store.RequeueFailedRun(finished.ID, time.Now(), "must not retry parent"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("RequeueFailedRun() error = %v", err)
	}
}

func TestPostgresStoreHandlesOpenPilotCIFailureAtomically(t *testing.T) {
	databaseURL := os.Getenv("SWITCHYARD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set SWITCHYARD_TEST_DATABASE_URL to run PostgreSQL CI repair integration test")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	db, err := OpenPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := NewPostgresStore(db, func() time.Time { return time.Unix(200, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	resetIsolatedPostgresRuns(t, db)
	repo := "OWNER/CI-REPAIR-" + newID("test")
	issue := "1"
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `delete from open_pilot_issue_states where repo=$1`, repo)
		_, _ = db.ExecContext(context.Background(), `delete from runs where inputs->>'repo'=$1`, repo)
	})
	pipeline := Pipeline{ID: "snapshot", WorkflowID: string(WorkflowOpenPilot), Steps: []PipelineStep{{ID: "agent-work", Position: 5, Name: "open-pilot.agent-work", Executor: "opencode"}, {ID: "ci-checks", Position: 9, Name: "open-pilot.ci-checks", Executor: "gitea"}}}
	state, parent, err := store.ClaimAndCreateOpenPilotInitialRun(repo, issue, trustedOpenPilotInput(repo, issue, CreateRunInput{WorkflowID: string(WorkflowOpenPilot), MaxAttempts: 3, PipelineSnapshot: &pipeline, Inputs: map[string]string{"repo": repo, "issue": issue, "resume.open-pilot.prompt": "original", "opencode_session_id": "session"}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `update runs set status=$2,attempt_count=1,claimed_by=$3,claimed_at=current_timestamp,lease_expires_at=current_timestamp + interval '1 minute' where id=$1`, parent.ID, RunRunning, "pg-worker"); err != nil {
		t.Fatal(err)
	}
	state.Phase, state.RunID, state.Branch, state.Base, state.WorktreePath = OpenPilotPhaseFailed, parent.ID, "open-pilot/issue-1", "main", "/worktree"
	state.PullRequestURL, state.PullRequestID, state.PullRequestNum = "https://git.example/pulls/1", 1, 1
	state.ResumeInputs = map[string]string{"resume.open-pilot.prompt": "original", "resume.open-pilot.branch-plan.branch": state.Branch, "resume.open-pilot.branch-plan.worktree_path": state.WorktreePath, "opencode_session_id": "session"}
	if _, err := store.UpsertOpenPilotIssueState(state); err != nil {
		t.Fatal(err)
	}
	step, err := store.CreateRunStep(CreateRunStepInput{RunID: parent.ID, Position: 9, Name: "open-pilot.ci-checks", Executor: "gitea"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartRunStep(step.ID); err != nil {
		t.Fatal(err)
	}
	request := OpenPilotCIRepairRequest{Target: *trustedOpenPilotInput(repo, issue, CreateRunInput{}).OpenPilotTarget, Repo: repo, Issue: issue, ParentRunID: parent.ID, ExpectedWorkerID: "pg-worker", ExpectedAttemptCount: 1, PullRequestID: 1, PullRequestNumber: 1, HeadBranch: state.Branch, FailedHeadSHA: "failed-head", Feedback: "verify failed", ChildRun: CreateRunInput{MaxAttempts: 9, Inputs: map[string]string{"resume.open-pilot.prompt": "repair"}}}
	result, err := store.HandleOpenPilotCIFailure(request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Queued || result.ChildRun == nil || result.ChildRun.MaxAttempts != 1 || result.ParentRun.Status != RunFailed || result.ParentRun.MaxAttempts != 1 || result.ChildRun.Inputs["retry_from_step_position"] != "5" {
		t.Fatalf("result = %+v", result)
	}
	replayed, err := store.HandleOpenPilotCIFailure(request)
	if err != nil || !replayed.Replayed || replayed.ChildRun == nil || replayed.ChildRun.ID != result.ChildRun.ID {
		t.Fatalf("replay = %+v, %v", replayed, err)
	}
	parentDetail, err := store.RunDetail(parent.ID)
	if err != nil || len(parentDetail.Artifacts) != 1 || parentDetail.Artifacts[0].Name != "open-pilot-ci-checks.json" {
		t.Fatalf("parent artifacts = %+v, %v", parentDetail.Artifacts, err)
	}
	store.now = time.Now
	secondClaim, err := store.ClaimNextRun("pg-worker-2", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	secondRequest := request
	secondRequest.ParentRunID, secondRequest.ExpectedWorkerID, secondRequest.ExpectedAttemptCount = secondClaim.ID, "pg-worker-2", secondClaim.AttemptCount
	secondRequest.Feedback = "verify failed again"
	second, err := store.HandleOpenPilotCIFailure(secondRequest)
	if err != nil || !second.Queued || second.Attempt != 2 || second.ChildRun == nil {
		t.Fatalf("second = %+v, %v", second, err)
	}
	thirdClaim, err := store.ClaimNextRun("pg-worker-3", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	thirdRequest := request
	thirdRequest.ParentRunID, thirdRequest.ExpectedWorkerID, thirdRequest.ExpectedAttemptCount = thirdClaim.ID, "pg-worker-3", thirdClaim.AttemptCount
	thirdRequest.Feedback = "verify failed third time"
	third, err := store.HandleOpenPilotCIFailure(thirdRequest)
	if err != nil || !third.Exhausted || third.Attempt != openPilotCIRepairMaxAttempts {
		t.Fatalf("third = %+v, %v", third, err)
	}
	exhaustedReplay, err := store.HandleOpenPilotCIFailure(thirdRequest)
	if err != nil || !exhaustedReplay.Replayed || !exhaustedReplay.Exhausted {
		t.Fatalf("exhausted replay = %+v, %v", exhaustedReplay, err)
	}
	thirdDetail, err := store.RunDetail(thirdClaim.ID)
	if err != nil || len(thirdDetail.Artifacts) != 1 {
		t.Fatalf("third artifacts = %+v, %v", thirdDetail.Artifacts, err)
	}
	resumable := third.State
	if resumable.Phase != OpenPilotPhaseFailed {
		t.Fatalf("resumable state = %+v", resumable)
	}
	if _, err := store.UpsertOpenPilotIssueState(resumable); err != nil {
		t.Fatal(err)
	}
	_, resumed, err := store.ClaimAndCreateOpenPilotResumeRun(repo, issue, trustedOpenPilotInput(repo, issue, CreateRunInput{WorkflowID: string(WorkflowOpenPilot), MaxAttempts: 2, Inputs: map[string]string{openPilotCIRepairCountKey: "0", openPilotCIRepairFeedbackKey: "forged", openPilotCIRepairOriginalPromptKey: "forged"}}))
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Inputs["retry_from_step"] != "open-pilot.ci-checks" || resumed.Inputs[openPilotCIRepairCountKey] != "2" || resumed.Inputs[openPilotCIRepairFeedbackKey] != "verify failed again" || resumed.Inputs[openPilotCIRepairOriginalPromptKey] != "original" {
		t.Fatalf("postgres resume cursor/context = %+v", resumed.Inputs)
	}
	for key := range resumed.Inputs {
		if strings.HasPrefix(key, "resume.open-pilot.ci-repair.") && !openPilotCIRepairExternalResumeInputAllowed(key) {
			t.Fatalf("postgres external resume retained protected input %q", key)
		}
	}
	reconcileState, reconcileRun, err := store.ClaimAndCreateOpenPilotInitialRun(repo, "2", trustedOpenPilotInput(repo, "2", CreateRunInput{WorkflowID: string(WorkflowOpenPilot), PipelineSnapshot: &pipeline, Inputs: map[string]string{"repo": repo, "issue": "2"}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `update runs set status=$2,summary='repair child failed',inputs=inputs || jsonb_build_object($3::text,$4::text) where id=$1`, reconcileRun.ID, RunFailed, openPilotCIRepairParentRunIDKey, "parent"); err != nil {
		t.Fatal(err)
	}
	reconcileState.Phase, reconcileState.RunID = OpenPilotPhaseValidated, reconcileRun.ID
	reconcileState, err = store.UpsertOpenPilotIssueState(reconcileState)
	if err != nil {
		t.Fatal(err)
	}
	reconcileState.Phase = OpenPilotPhaseBranchPrepared
	if _, err := store.UpsertOpenPilotIssueState(reconcileState); err != nil {
		t.Fatal(err)
	}
	if count, err := store.ReconcileOpenPilotCIRepairTerminals(100, openPilotAdmissionGateForTarget(t, *trustedOpenPilotInput(repo, "2", CreateRunInput{}).OpenPilotTarget)); err != nil || count != 1 {
		t.Fatalf("postgres reconciled = %d, %v", count, err)
	}
	if reconciled, err := store.OpenPilotIssueState(repo, "2"); err != nil || reconciled.Phase != OpenPilotPhaseFailed || reconciled.RecommendedNextAction != "manual_review" {
		t.Fatalf("postgres reconciled state = %+v, %v", reconciled, err)
	}
}

func TestPostgresStoreRejectsExpiredCIRepairLease(t *testing.T) {
	databaseURL := os.Getenv("SWITCHYARD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set SWITCHYARD_TEST_DATABASE_URL to run PostgreSQL CI repair integration test")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	db, err := OpenPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := NewPostgresStore(db, time.Now)
	repo := "OWNER/CI-EXPIRED-" + newID("test")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `delete from open_pilot_issue_states where repo=$1`, repo)
		_, _ = db.ExecContext(context.Background(), `delete from runs where inputs->>'repo'=$1`, repo)
	})
	pipeline := Pipeline{ID: "snapshot", WorkflowID: string(WorkflowOpenPilot), Steps: []PipelineStep{{ID: "agent-work", Position: 5, Name: "open-pilot.agent-work"}}}
	state, parent, err := store.ClaimAndCreateOpenPilotInitialRun(repo, "1", trustedOpenPilotInput(repo, "1", CreateRunInput{WorkflowID: string(WorkflowOpenPilot), PipelineSnapshot: &pipeline, Inputs: map[string]string{"repo": repo, "issue": "1"}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `update runs set status=$2,attempt_count=1,claimed_by='worker',claimed_at=current_timestamp - interval '2 minutes',lease_expires_at=current_timestamp - interval '1 minute' where id=$1`, parent.ID, RunRunning); err != nil {
		t.Fatal(err)
	}
	state.Phase, state.Branch, state.WorktreePath, state.PullRequestURL, state.PullRequestID, state.PullRequestNum = OpenPilotPhaseFailed, "branch", "/worktree", "https://git/pulls/1", 1, 1
	if _, err := store.UpsertOpenPilotIssueState(state); err != nil {
		t.Fatal(err)
	}
	request := OpenPilotCIRepairRequest{Target: *trustedOpenPilotInput(repo, "1", CreateRunInput{}).OpenPilotTarget, Repo: repo, Issue: "1", ParentRunID: parent.ID, ExpectedWorkerID: "worker", ExpectedAttemptCount: 1, PullRequestID: 1, PullRequestNumber: 1, HeadBranch: "branch", FailedHeadSHA: "sha", Feedback: "failed"}
	if _, err := store.HandleOpenPilotCIFailure(request); !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v", err)
	}
}

func TestPostgresCIRepairLeaseUsesWallClockAfterLockWait(t *testing.T) {
	source, err := os.ReadFile("postgres_store_open_pilot.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "select clock_timestamp()") || strings.Contains(string(source), "select current_timestamp") {
		t.Fatal("CI repair lease handoff must compare against clock_timestamp()")
	}
}

func seedOpenPilotCIRepairParent(t *testing.T) (*MemoryStore, Run, Pipeline) {
	t.Helper()
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	pipeline := Pipeline{ID: "snapshot", WorkflowID: string(WorkflowOpenPilot), Steps: []PipelineStep{{ID: "agent-work", Position: 7, Name: "open-pilot.agent-work", Executor: "opencode"}, {ID: "ci-checks", Position: 12, Name: "open-pilot.ci-checks", Executor: "gitea"}}}
	state, run, err := store.ClaimAndCreateOpenPilotInitialRun("OWNER/REPO", "1", trustedOpenPilotInput("OWNER/REPO", "1", CreateRunInput{WorkflowID: string(WorkflowOpenPilot), MaxAttempts: 3, PipelineSnapshot: &pipeline, Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "1", "resume.open-pilot.prompt": "original issue prompt", "opencode_session_id": "session-1"}}))
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimNextRun("worker-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	state.Phase = OpenPilotPhaseFailed
	state.RunID = run.ID
	state.Branch = "open-pilot/issue-1"
	state.Base = "main"
	state.WorktreePath = "/worktrees/issue-1"
	state.PullRequestURL = "https://git.example/OWNER/REPO/pulls/9"
	state.PullRequestID = 90
	state.PullRequestNum = 9
	state.ResumeInputs = map[string]string{
		"resume.open-pilot.prompt":                      "original issue prompt",
		"resume.open-pilot.branch-plan.repo_path":       "/repo",
		"resume.open-pilot.branch-plan.worktree_path":   "/worktrees/issue-1",
		"resume.open-pilot.branch-plan.branch":          "open-pilot/issue-1",
		"resume.open-pilot.branch-plan.executed":        "true",
		"resume.open-pilot.pr-plan.pull_request_url":    state.PullRequestURL,
		"resume.open-pilot.pr-plan.pull_request_id":     "90",
		"resume.open-pilot.pr-plan.pull_request_number": "9",
		"opencode_session_id":                           "session-1",
	}
	if _, err := store.UpsertOpenPilotIssueState(state); err != nil {
		t.Fatal(err)
	}
	step, err := store.CreateRunStep(CreateRunStepInput{RunID: run.ID, Position: 12, Name: "open-pilot.ci-checks", Executor: "gitea"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartRunStep(step.ID); err != nil {
		t.Fatal(err)
	}
	return store, claimed, pipeline
}

func testOpenPilotCIRepairRequest(run Run, worker, feedback string) OpenPilotCIRepairRequest {
	return OpenPilotCIRepairRequest{
		Target: OpenPilotTarget{RepositoryID: "legacy", RepositorySlug: strings.ToLower(run.Inputs["repo"]), Issue: run.Inputs["issue"], ExecutorGeneration: "legacy"},
		Repo:   "OWNER/REPO", Issue: "1", ParentRunID: run.ID, ExpectedWorkerID: worker, ExpectedAttemptCount: run.AttemptCount,
		PullRequestID: 90, PullRequestNumber: 9, HeadBranch: "open-pilot/issue-1", FailedHeadSHA: "failed-head", Feedback: feedback,
	}
}
