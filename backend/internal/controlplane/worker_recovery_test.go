package controlplane

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

type blockedAdoptionStore struct {
	Store
	entered chan struct{}
	release chan struct{}
}

func (s *blockedAdoptionStore) WithContext(ctx context.Context) Store {
	return &blockedAdoptionStore{Store: s.Store.WithContext(ctx), entered: s.entered, release: s.release}
}

func (s *blockedAdoptionStore) AdoptOpenPilotRunAffinity(runID, workerID string, attempt int, gate *OpenPilotAdmissionGate) (Run, OpenPilotIssueState, error) {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-s.release
	return s.Store.AdoptOpenPilotRunAffinity(runID, workerID, attempt, gate)
}

type countingOpenPilotExecutor struct{ calls atomic.Int32 }

func (e *countingOpenPilotExecutor) Execute(context.Context, Run) (WorkflowExecution, error) {
	e.calls.Add(1)
	return WorkflowExecution{Status: RunSucceeded}, nil
}

func (e *countingOpenPilotExecutor) ExecutePipelineStep(context.Context, Run, PipelineStep, map[string]any) (PipelineStepExecution, error) {
	e.calls.Add(1)
	return PipelineStepExecution{Summary: "executed"}, nil
}

func TestBlockedAffinityAdoptionCannotExecuteAfterLeaseReclaim(t *testing.T) {
	now := time.Unix(1_000, 0).UTC()
	base := NewStore(func() time.Time { return now })
	target := *trustedOpenPilotInput("OWNER/REPO", "1", CreateRunInput{}).OpenPilotTarget
	pipeline := Pipeline{ID: "adoption-race", WorkflowID: string(WorkflowOpenPilot), Steps: []PipelineStep{{ID: "execute", Name: "execute", Position: 1, Executor: "test"}}}
	_, run, err := base.ClaimAndCreateOpenPilotInitialRun("OWNER/REPO", "1", CreateRunInput{WorkflowID: string(WorkflowOpenPilot), OpenPilotTarget: &target, PipelineSnapshot: &pipeline, MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := base.ClaimNextRun("stale-worker", 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	blocked := &blockedAdoptionStore{Store: base, entered: make(chan struct{}, 1), release: make(chan struct{})}
	executor := &countingOpenPilotExecutor{}
	runner := NewRunner(blocked, MapExecutorRegistry{string(WorkflowOpenPilot): executor})
	runner.SetOpenPilotAdmissionGate(openPilotAdmissionGateForTarget(t, target))
	pool := NewWorkerPool(blocked, runner, 1, 20*time.Millisecond, time.Hour, func() time.Time { return now })
	done := make(chan struct{})
	go func() {
		pool.execute(t.Context(), "stale-worker", claimed)
		close(done)
	}()
	<-blocked.entered
	now = now.Add(21 * time.Millisecond)
	if requeued, _, err := base.RecoverLeasedRuns(now, runner.openPilotGate); err != nil || requeued != 1 {
		t.Fatalf("recovery = %d, %v", requeued, err)
	}
	fresh, err := base.ClaimNextRun("fresh-worker", time.Minute)
	if err != nil || fresh.ID != run.ID || fresh.AttemptCount != 2 {
		t.Fatalf("fresh claim = %+v, %v", fresh, err)
	}
	close(blocked.release)
	<-done
	if executor.calls.Load() != 0 {
		t.Fatalf("stale worker executed %d side effects", executor.calls.Load())
	}
	NewWorkerPool(base, runner, 1, time.Minute, time.Hour, func() time.Time { return now }).execute(t.Context(), "fresh-worker", fresh)
	if executor.calls.Load() != 1 {
		t.Fatalf("fresh executions = %d", executor.calls.Load())
	}
}

func TestRecoveredOpenPilotAgentRevalidatesDurableWorktreeBeforeSideEffect(t *testing.T) {
	for _, recovery := range []string{"automatic retry", "lease recovery"} {
		t.Run(recovery, func(t *testing.T) {
			now := time.Unix(100, 0).UTC()
			store := NewStore(func() time.Time { return now })
			if err := SeedDefaults(store); err != nil {
				t.Fatal(err)
			}
			target := OpenPilotTarget{RepositoryID: "legacy", RepositorySlug: "owner/repo", Issue: "1", ExecutorGeneration: "legacy"}
			state, run, err := store.ClaimAndCreateOpenPilotInitialRun("owner/repo", "1", trustedOpenPilotInput("owner/repo", "1", CreateRunInput{WorkflowID: string(WorkflowOpenPilot), MaxAttempts: 2}))
			if err != nil {
				t.Fatal(err)
			}
			plan := OpenPilotWorktreePlan{Repo: "owner/repo", RepoPath: "/repo", BaseBranch: "main", Branch: "branch", WorktreePath: "/worktree", Executed: true}
			state.Branch, state.Base, state.WorktreePath = plan.Branch, plan.BaseBranch, plan.WorktreePath
			state.ResumeInputs = map[string]string{"resume.open-pilot.branch-plan.repo": plan.Repo, "resume.open-pilot.branch-plan.repo_path": plan.RepoPath, "resume.open-pilot.branch-plan.base_branch": plan.BaseBranch, "resume.open-pilot.branch-plan.branch": plan.Branch, "resume.open-pilot.branch-plan.worktree_path": plan.WorktreePath, "resume.open-pilot.branch-plan.executed": "true"}
			if _, err := store.UpsertOpenPilotIssueState(state); err != nil {
				t.Fatal(err)
			}
			claimed, err := store.ClaimNextRun("worker", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			switch recovery {
			case "automatic retry":
				if _, err := store.FinishClaimedRun(run.ID, "worker", claimed.AttemptCount, RunFailed, "retry"); err != nil {
					t.Fatal(err)
				}
				if _, err := store.RequeueFailedRun(run.ID, now, "retry"); err != nil {
					t.Fatal(err)
				}
			case "lease recovery":
				now = claimed.LeaseExpiresAt.Add(time.Second)
				if _, _, err := store.RecoverLeasedRuns(now, openPilotAdmissionGateForTarget(t, target)); err != nil {
					t.Fatal(err)
				}
			}
			claimed, err = store.ClaimNextRun("worker", time.Minute)
			if err != nil || claimed.AttemptCount != 2 {
				t.Fatalf("reclaimed = %+v, %v", claimed, err)
			}
			store.mu.Lock()
			durable := store.openPilotIssueStates[openPilotIssueStateKey("owner/repo", "1")]
			durable.WorktreePath = "/other-worktree"
			store.openPilotIssueStates[openPilotIssueStateKey("owner/repo", "1")] = durable
			store.mu.Unlock()
			openCode := &captureOpenCodeExecutor{response: OpenCodeResponse{Content: "must not run"}}
			env := newOpenPilotAIReviewTestEnv(store)
			env.openCodeExecutor = openCode
			env.openPilotGate = openPilotAdmissionGateForTarget(t, target)
			result, err := (openPilotExecutor{env: env}).ExecutePipelineStep(t.Context(), claimed, PipelineStep{ID: "agent-work", Name: "open-pilot.agent-work"}, map[string]any{"open-pilot.branch-plan": plan, "open-pilot.prompt": "prompt"})
			if err != nil || !result.Halt || len(openCode.requests) != 0 {
				t.Fatalf("result=%+v requests=%d err=%v", result, len(openCode.requests), err)
			}
		})
	}
}

func TestMemoryRecoveryQuarantinesEveryDuplicateRunStateAndContinues(t *testing.T) {
	now := time.Unix(1_000, 0).UTC()
	store := NewStore(func() time.Time { return now })
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	openRun, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), MaxAttempts: 2, Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	otherRun, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowDevlog), MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	openClaim, err := store.ClaimNextRun("worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	otherClaim, err := store.ClaimNextRun("worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	for _, state := range []OpenPilotIssueState{{ID: "state-a", Repo: "OWNER/REPO", Issue: "1", RunID: openRun.ID}, {ID: "state-b", Repo: "OWNER/REPO", Issue: "2", RunID: openRun.ID}} {
		state.Phase = OpenPilotPhaseRequested
		store.openPilotIssueStates[openPilotIssueStateKey(state.Repo, state.Issue)] = state
	}
	store.mu.Unlock()
	now = openClaim.LeaseExpiresAt.Add(time.Second)
	requeued, failed, err := store.RecoverLeasedRuns(now, testOpenPilotAdmissionGate(t))
	if err != nil || requeued != 1 || failed != 0 {
		t.Fatalf("recovery = %d/%d, %v; other=%s", requeued, failed, err, otherClaim.ID)
	}
	openDetail, _ := store.RunDetail(openRun.ID)
	otherDetail, _ := store.RunDetail(otherRun.ID)
	if openDetail.Run.Status != RunPaused || otherDetail.Run.Status != RunQueued {
		t.Fatalf("runs: open=%+v other=%+v", openDetail.Run, otherDetail.Run)
	}
	for _, issue := range []string{"1", "2"} {
		state, _ := store.OpenPilotIssueState("OWNER/REPO", issue)
		if state.Phase != OpenPilotPhasePaused || state.FailingGate != "repository_affinity" || state.RecommendedNextAction != "manual_review" {
			t.Fatalf("state %s = %+v", issue, state)
		}
	}
	if again, _, err := store.RecoverLeasedRuns(now, testOpenPilotAdmissionGate(t)); err != nil || again != 0 {
		t.Fatalf("second recovery = %d, %v", again, err)
	}
}

type panicWorkflowExecutor struct{}

func (panicWorkflowExecutor) Execute(context.Context, Run) (WorkflowExecution, error) {
	panic("executor crashed")
}

type panicPipelineExecutor struct{ panicWorkflowExecutor }

func (panicPipelineExecutor) ExecutePipelineStep(context.Context, Run, PipelineStep, map[string]any) (PipelineStepExecution, error) {
	panic("pipeline executor crashed")
}

type failingPipelineExecutor struct{ panicWorkflowExecutor }

func (failingPipelineExecutor) ExecutePipelineStep(context.Context, Run, PipelineStep, map[string]any) (PipelineStepExecution, error) {
	return PipelineStepExecution{}, errors.New("repair execution failed")
}

func TestWorkerPanicRequeuesBelowMaxAttemptsWithBackoff(t *testing.T) {
	t.Setenv("SWITCHYARD_RUN_RETRY_BACKOFF", "30s")
	now := time.Unix(1_000, 0).UTC()
	store, runner := workerRecoveryStore(t, &now, panicWorkflowExecutor{})
	run, err := runner.Enqueue(CreateRunInput{WorkflowID: "worker-recovery", MaxAttempts: 2})
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	claimed, err := store.ClaimNextRun("worker-1", time.Minute)
	if err != nil {
		t.Fatalf("ClaimNextRun() error = %v", err)
	}
	pool := NewWorkerPool(store, runner, 1, time.Minute, time.Hour, func() time.Time { return now })
	pool.execute(t.Context(), "worker-1", claimed)

	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if detail.Run.Status != RunQueued || detail.Run.AttemptCount != 1 || !detail.Run.AvailableAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("requeued run = %+v", detail.Run)
	}
	if !workerRecoveryHasEvent(detail.Events, "run.failed") || !workerRecoveryHasEvent(detail.Events, "run.retry.scheduled") {
		t.Fatalf("events do not record failed then retry: %+v", detail.Events)
	}
	if _, err := store.ClaimNextRun("worker-2", time.Minute); err != ErrNotFound {
		t.Fatalf("ClaimNextRun() before backoff = %v, want ErrNotFound", err)
	}
	now = now.Add(30 * time.Second)
	claimed, err = store.ClaimNextRun("worker-2", time.Minute)
	if err != nil || claimed.ID != run.ID || claimed.AttemptCount != 2 {
		t.Fatalf("ClaimNextRun() after backoff = %+v, %v", claimed, err)
	}
}

func TestWorkerPanicAtMaxAttemptsRemainsFailed(t *testing.T) {
	now := time.Unix(1_000, 0).UTC()
	store, runner := workerRecoveryStore(t, &now, panicPipelineExecutor{})
	runner.pipelines = StaticPipelineRegistry{"worker-recovery": {ID: "panic-pipeline", WorkflowID: "worker-recovery", Steps: []PipelineStep{{ID: "panic", Position: 1, Name: "panic", Executor: "model"}}}}
	run, err := runner.Enqueue(CreateRunInput{WorkflowID: "worker-recovery", MaxAttempts: 1})
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	claimed, err := store.ClaimNextRun("worker-1", time.Minute)
	if err != nil {
		t.Fatalf("ClaimNextRun() error = %v", err)
	}
	NewWorkerPool(store, runner, 1, time.Minute, time.Hour, func() time.Time { return now }).execute(t.Context(), "worker-1", claimed)

	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if detail.Run.Status != RunFailed || detail.Run.AttemptCount != 1 || detail.Run.Summary != "worker panic: pipeline executor crashed" {
		t.Fatalf("terminal run = %+v", detail.Run)
	}
	if len(detail.Steps) != 1 || detail.Steps[0].Status != StepFailed || detail.Steps[0].Error != "worker panic: pipeline executor crashed" {
		t.Fatalf("terminal panic steps = %+v", detail.Steps)
	}
	if workerRecoveryHasEvent(detail.Events, "run.retry.scheduled") {
		t.Fatalf("terminal panic unexpectedly scheduled retry: %+v", detail.Events)
	}
}

func TestWorkerExhaustedMergeRetryReconcilesOnlyCurrentMergeReadyState(t *testing.T) {
	now := time.Unix(1_000, 0).UTC()
	newTerminalRun := func(t *testing.T, phase OpenPilotPhase, stateRunID string) (*MemoryStore, Run) {
		t.Helper()
		store := NewStore(func() time.Time { return now })
		if err := SeedDefaults(store); err != nil {
			t.Fatalf("SeedDefaults() error = %v", err)
		}
		pipeline := Pipeline{ID: "merge-terminal", WorkflowID: string(WorkflowOpenPilot), Steps: []PipelineStep{{ID: "merge-pr", Position: 11, Name: "open-pilot.merge-pr", Executor: "gitea"}}}
		run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), MaxAttempts: 1, PipelineSnapshot: &pipeline, Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}})
		if err != nil {
			t.Fatalf("CreateRun() error = %v", err)
		}
		seedOpenPilotStateAtPhase(t, store, OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "123", RunID: run.ID, GateStatus: OpenPilotGateStatus{"merge": "merge_ready"}, LastError: "temporary merge failure"}, OpenPilotPhaseMergeReady)
		if phase != OpenPilotPhaseMergeReady {
			if phase == OpenPilotPhaseIssueClosed {
				if _, err := store.TransitionOpenPilotIssueState("OWNER/REPO", "123", OpenPilotPhaseMerged); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.TransitionOpenPilotIssueState("OWNER/REPO", "123", phase); err != nil {
				t.Fatal(err)
			}
		}
		if stateRunID != "" {
			store.mu.Lock()
			state := store.openPilotIssueStates[openPilotIssueStateKey("OWNER/REPO", "123")]
			state.RunID = stateRunID
			store.openPilotIssueStates[openPilotIssueStateKey("OWNER/REPO", "123")] = state
			store.mu.Unlock()
		}
		claimed, err := store.ClaimNextRun("worker-1", time.Minute)
		if err != nil {
			t.Fatalf("ClaimNextRun() error = %v", err)
		}
		runner := NewRunner(store, MapExecutorRegistry{string(WorkflowOpenPilot): failingPipelineExecutor{}})
		NewWorkerPool(store, runner, 1, time.Minute, time.Hour, func() time.Time { return now }).execute(t.Context(), "worker-1", claimed)
		return store, run
	}

	store, run := newTerminalRun(t, OpenPilotPhaseMergeReady, "")
	state, err := store.OpenPilotIssueState("owner/repo", "123")
	if err != nil || state.RunID != run.ID || state.Phase != OpenPilotPhaseFailed || state.RecommendedNextAction != "manual_review" || state.FailingGate != "merge" || state.GateStatus["merge"] != "failed" {
		t.Fatalf("reconciled state = %+v, %v", state, err)
	}

	t.Run("lease exhaustion", func(t *testing.T) {
		store := NewStore(func() time.Time { return now })
		if err := SeedDefaults(store); err != nil {
			t.Fatal(err)
		}
		run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), MaxAttempts: 1, Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "124"}})
		if err != nil {
			t.Fatal(err)
		}
		seedOpenPilotStateAtPhase(t, store, OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "124", RunID: run.ID, GateStatus: OpenPilotGateStatus{"merge": "merge_ready"}, LastError: "preflight timed out"}, OpenPilotPhaseMergeReady)
		claimed, err := store.ClaimNextRun("worker-lease", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		now = claimed.LeaseExpiresAt.Add(time.Second)
		NewWorkerPool(store, NewRunner(store, nil), 1, time.Minute, time.Hour, func() time.Time { return now }).recoverLeases()
		state, err := store.OpenPilotIssueState("owner/repo", "124")
		if err != nil || state.Phase != OpenPilotPhaseFailed || state.RecommendedNextAction != "manual_review" {
			t.Fatalf("lease-reconciled state = %+v, %v", state, err)
		}
	})

	for _, test := range []struct {
		name  string
		phase OpenPilotPhase
		runID string
	}{
		{name: "paused", phase: OpenPilotPhasePaused},
		{name: "merged", phase: OpenPilotPhaseMerged},
		{name: "closed", phase: OpenPilotPhaseIssueClosed},
		{name: "newer run", phase: OpenPilotPhaseMergeReady, runID: "newer-run"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, _ := newTerminalRun(t, test.phase, test.runID)
			repo := "owner/repo"
			if test.runID != "" {
				repo = "OWNER/REPO"
			}
			state, err := store.OpenPilotIssueState(repo, "123")
			wantPhase := test.phase
			if test.runID != "" {
				wantPhase = OpenPilotPhasePaused
			}
			if err != nil || state.Phase != wantPhase || (test.runID != "" && (state.RunID != test.runID || state.RecommendedNextAction != "manual_review")) {
				t.Fatalf("preserved state = %+v, %v", state, err)
			}
		})
	}
}

func TestWorkerTerminalPathsReconcileOpenPilotCIRepairState(t *testing.T) {
	for _, test := range []struct {
		name     string
		executor WorkflowExecutor
		recover  bool
	}{
		{name: "panic", executor: panicPipelineExecutor{}},
		{name: "max attempts", executor: failingPipelineExecutor{}},
		{name: "lease expiry", executor: failingPipelineExecutor{}, recover: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, parent, _ := seedOpenPilotCIRepairParent(t)
			handoff, err := store.HandleOpenPilotCIFailure(testOpenPilotCIRepairRequest(parent, "worker-1", "verify failed"))
			if err != nil || handoff.ChildRun == nil {
				t.Fatalf("handoff = %+v, %v", handoff, err)
			}
			claimed, err := store.ClaimNextRun("repair-worker", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			runner := NewRunner(store, MapExecutorRegistry{string(WorkflowOpenPilot): test.executor})
			pool := NewWorkerPool(store, runner, 1, time.Minute, time.Hour, store.now)
			if test.recover {
				expired := claimed.LeaseExpiresAt.Add(time.Second)
				store.now = func() time.Time { return expired }
				pool.now = store.now
				pool.recoverLeases()
			} else {
				pool.execute(t.Context(), "repair-worker", claimed)
			}
			state, err := store.OpenPilotIssueState("owner/repo", "1")
			if err != nil {
				t.Fatal(err)
			}
			if state.Phase != OpenPilotPhaseFailed || state.RecommendedNextAction != "manual_review" || state.LastError == "" || state.RunID != claimed.ID {
				t.Fatalf("state = %+v", state)
			}
		})
	}
}

func TestOpenPilotCIRepairTerminalReconciliationPreservesOperatorOrNewerState(t *testing.T) {
	for _, test := range []struct {
		name  string
		phase OpenPilotPhase
		runID string
	}{
		{name: "operator pause", phase: OpenPilotPhasePaused},
		{name: "merged", phase: OpenPilotPhaseMerged},
		{name: "issue closed", phase: OpenPilotPhaseIssueClosed},
		{name: "newer run", phase: OpenPilotPhaseBranchPrepared, runID: "newer-run"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, parent, _ := seedOpenPilotCIRepairParent(t)
			handoff, err := store.HandleOpenPilotCIFailure(testOpenPilotCIRepairRequest(parent, "worker-1", "verify failed"))
			if err != nil || handoff.ChildRun == nil {
				t.Fatalf("handoff = %+v, %v", handoff, err)
			}
			child := *handoff.ChildRun
			if _, err := store.FinishRun(child.ID, RunFailed, "repair failed"); err != nil {
				t.Fatal(err)
			}
			state, err := store.OpenPilotIssueState("OWNER/REPO", "1")
			if err != nil {
				t.Fatal(err)
			}
			state.Phase = test.phase
			if test.runID != "" {
				state.RunID = test.runID
			}
			store.mu.Lock()
			store.openPilotIssueStates[openPilotIssueStateKey("OWNER/REPO", "1")] = state
			store.mu.Unlock()
			changed, err := store.ReconcileOpenPilotCIRepairTerminal(child.ID, configuredOpenPilotAdmissionGate(t))
			if err != nil || changed {
				t.Fatalf("changed/error = %t/%v", changed, err)
			}
			after, err := store.OpenPilotIssueState("OWNER/REPO", "1")
			if err != nil || after.Phase != test.phase || after.RunID != state.RunID {
				t.Fatalf("state = %+v, %v", after, err)
			}
		})
	}
}

func TestOpenPilotCIRepairTerminalReconciliationHandlesEveryPreMergePhase(t *testing.T) {
	phases := []OpenPilotPhase{OpenPilotPhaseRequested, OpenPilotPhaseValidated, OpenPilotPhaseBranchPrepared, OpenPilotPhaseAgentCompleted, OpenPilotPhaseVerified, OpenPilotPhaseCommitted, OpenPilotPhasePushed, OpenPilotPhasePRCreated, OpenPilotPhaseAIReviewed, OpenPilotPhaseCIPassed, OpenPilotPhaseMergeReady}
	for _, phase := range phases {
		t.Run(string(phase), func(t *testing.T) {
			store, parent, _ := seedOpenPilotCIRepairParent(t)
			handoff, err := store.HandleOpenPilotCIFailure(testOpenPilotCIRepairRequest(parent, "worker-1", "verify failed"))
			if err != nil || handoff.ChildRun == nil {
				t.Fatalf("handoff = %+v, %v", handoff, err)
			}
			child := *handoff.ChildRun
			if _, err := store.FinishRun(child.ID, RunFailed, "repair failed"); err != nil {
				t.Fatal(err)
			}
			store.mu.Lock()
			state := store.openPilotIssueStates[openPilotIssueStateKey("OWNER/REPO", "1")]
			state.Phase = phase
			store.openPilotIssueStates[openPilotIssueStateKey("OWNER/REPO", "1")] = state
			store.mu.Unlock()

			changed, err := store.ReconcileOpenPilotCIRepairTerminal(child.ID, configuredOpenPilotAdmissionGate(t))
			if err != nil || !changed {
				t.Fatalf("changed/error = %t/%v", changed, err)
			}
			after, err := store.OpenPilotIssueState("OWNER/REPO", "1")
			if err != nil || after.Phase != OpenPilotPhaseFailed || after.RecommendedNextAction != "manual_review" {
				t.Fatalf("state = %+v, %v", after, err)
			}
		})
	}
}

func TestOpenPilotCIRepairTerminalReconciliationIgnoresSuccessfulChild(t *testing.T) {
	store, parent, _ := seedOpenPilotCIRepairParent(t)
	handoff, err := store.HandleOpenPilotCIFailure(testOpenPilotCIRepairRequest(parent, "worker-1", "verify failed"))
	if err != nil || handoff.ChildRun == nil {
		t.Fatalf("handoff = %+v, %v", handoff, err)
	}
	child := *handoff.ChildRun
	if _, err := store.FinishRun(child.ID, RunSucceeded, "repair complete"); err != nil {
		t.Fatal(err)
	}
	changed, err := store.ReconcileOpenPilotCIRepairTerminal(child.ID, configuredOpenPilotAdmissionGate(t))
	if err != nil || changed {
		t.Fatalf("changed/error = %t/%v", changed, err)
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "1")
	if err != nil || state.Phase != OpenPilotPhaseBranchPrepared || state.RecommendedNextAction == "manual_review" {
		t.Fatalf("state = %+v, %v", state, err)
	}
}

func TestOpenPilotCIRepairBatchReconciliationBoundsTerminalMatches(t *testing.T) {
	store, parent, _ := seedOpenPilotCIRepairParent(t)
	handoff, err := store.HandleOpenPilotCIFailure(testOpenPilotCIRepairRequest(parent, "worker-1", "verify failed"))
	if err != nil || handoff.ChildRun == nil {
		t.Fatalf("handoff = %+v, %v", handoff, err)
	}
	child := *handoff.ChildRun
	if _, err := store.FinishRun(child.ID, RunFailed, "repair failed"); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	for i := range 150 {
		runID := fmt.Sprintf("nonterminal-%d", i)
		store.runs[runID] = Run{ID: runID, Status: RunQueued, Inputs: map[string]string{openPilotCIRepairParentRunIDKey: "parent"}}
		store.openPilotIssueStates[fmt.Sprintf("repo\x00%d", i)] = OpenPilotIssueState{Repo: "repo", Issue: fmt.Sprint(i), RunID: runID, Phase: OpenPilotPhaseBranchPrepared}
	}
	store.mu.Unlock()
	reconciled, err := store.ReconcileOpenPilotCIRepairTerminals(1, configuredOpenPilotAdmissionGate(t))
	if err != nil || reconciled != 1 {
		t.Fatalf("reconciled = %d, %v", reconciled, err)
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "1")
	if err != nil || state.Phase != OpenPilotPhaseFailed {
		t.Fatalf("state = %+v, %v", state, err)
	}
}

func TestWorkerRecoveryDoesNotReconcileUntrustedOpenPilotState(t *testing.T) {
	now := time.Unix(1_000, 0).UTC()
	store := NewStore(func() time.Time { return now })
	store.mu.Lock()
	for i := range 101 {
		runID := fmt.Sprintf("terminal-%d", i)
		store.runs[runID] = Run{ID: runID, Status: RunFailed, Summary: "repair failed", Inputs: map[string]string{openPilotCIRepairParentRunIDKey: "parent"}}
		store.openPilotIssueStates[openPilotIssueStateKey("repo", fmt.Sprint(i))] = OpenPilotIssueState{Repo: "repo", Issue: fmt.Sprint(i), RunID: runID, Phase: OpenPilotPhaseBranchPrepared}
	}
	store.mu.Unlock()
	pool := NewWorkerPool(store, NewRunner(store), 1, time.Minute, time.Hour, func() time.Time { return now })
	pool.recoverLeases()
	pool.recoverLeases()
	for _, state := range store.ListOpenPilotIssueStates() {
		if state.Phase != OpenPilotPhaseBranchPrepared {
			t.Fatalf("untrusted state mutated = %+v", state)
		}
	}
}

func TestWorkerRecoveryRetriesRunningStepAndRetainsCorrelations(t *testing.T) {
	now := time.Unix(1_000, 0).UTC()
	executor := &recordingPipelineExecutor{}
	store, runner := workerRecoveryStore(t, &now, executor)
	pipeline := Pipeline{ID: "worker-recovery-pipeline", WorkflowID: "worker-recovery", Steps: []PipelineStep{{ID: "request", Position: 1, Name: "worker.request", Executor: "model"}}}
	runner.pipelines = StaticPipelineRegistry{"worker-recovery": pipeline}
	run, err := runner.Enqueue(CreateRunInput{WorkflowID: "worker-recovery", MaxAttempts: 2})
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if _, err := store.ClaimNextRun("crashed-worker", time.Minute); err != nil {
		t.Fatalf("ClaimNextRun() error = %v", err)
	}
	step, err := store.CreateRunStep(CreateRunStepInput{RunID: run.ID, Position: 1, Name: "worker.request", Executor: "model", CorrelationIDs: map[string]string{
		CorrelationBackend:           "switchyard-model-broker",
		CorrelationDeprecatedBackend: "llama-line",
		"ocq_request_id":             "ocq-123",
		"opencode_session_id":        "session-456",
	}})
	if err != nil {
		t.Fatalf("CreateRunStep() error = %v", err)
	}
	if _, err := store.StartRunStep(step.ID); err != nil {
		t.Fatalf("StartRunStep() error = %v", err)
	}

	now = now.Add(2 * time.Minute)
	pool := NewWorkerPool(store, runner, 1, time.Minute, time.Hour, func() time.Time { return now })
	pool.recoverLeases()
	claimed, err := store.ClaimNextRun("recovery-worker", time.Minute)
	if err != nil {
		t.Fatalf("ClaimNextRun() after recovery error = %v", err)
	}
	pool.execute(t.Context(), "recovery-worker", claimed)

	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if detail.Run.Status != RunSucceeded || detail.Run.AttemptCount != 2 || len(detail.Steps) != 1 {
		t.Fatalf("recovered detail = %+v", detail)
	}
	recovered := detail.Steps[0]
	if recovered.Status != StepSucceeded || recovered.AttemptCount != 2 || recovered.CorrelationIDs[CorrelationBackend] != "switchyard-model-broker" || recovered.CorrelationIDs[CorrelationDeprecatedBackend] != "llama-line" || recovered.CorrelationIDs["ocq_request_id"] != "ocq-123" || recovered.CorrelationIDs["opencode_session_id"] != "session-456" {
		t.Fatalf("recovered step = %+v", recovered)
	}
	if len(executor.steps) != 1 || executor.steps[0] != "request" {
		t.Fatalf("executed steps = %+v", executor.steps)
	}
}

func workerRecoveryStore(t *testing.T, now *time.Time, executor WorkflowExecutor) (*MemoryStore, *Runner) {
	t.Helper()
	store := NewStore(func() time.Time { return *now })
	if err := store.UpsertWorkflow(Workflow{ID: "worker-recovery", Kind: "worker-recovery", Name: "Worker recovery"}); err != nil {
		t.Fatalf("UpsertWorkflow() error = %v", err)
	}
	return store, NewRunner(store, MapExecutorRegistry{"worker-recovery": executor})
}

func workerRecoveryHasEvent(events []RunEvent, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}
