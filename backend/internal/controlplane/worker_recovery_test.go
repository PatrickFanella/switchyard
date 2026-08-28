package controlplane

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

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
	state, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil || state.RunID != run.ID || state.Phase != OpenPilotPhaseFailed || state.RecommendedNextAction != "manual_review" || state.FailingGate != "merge" || state.GateStatus["merge"] != "failed" {
		t.Fatalf("reconciled state = %+v, %v", state, err)
	}

	t.Run("lease exhaustion", func(t *testing.T) {
		store := NewStore(func() time.Time { return now })
		if err := SeedDefaults(store); err != nil {
			t.Fatal(err)
		}
		run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), MaxAttempts: 1, Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "lease"}})
		if err != nil {
			t.Fatal(err)
		}
		seedOpenPilotStateAtPhase(t, store, OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "lease", RunID: run.ID, GateStatus: OpenPilotGateStatus{"merge": "merge_ready"}, LastError: "preflight timed out"}, OpenPilotPhaseMergeReady)
		claimed, err := store.ClaimNextRun("worker-lease", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		now = claimed.LeaseExpiresAt.Add(time.Second)
		NewWorkerPool(store, NewRunner(store, nil), 1, time.Minute, time.Hour, func() time.Time { return now }).recoverLeases()
		state, err := store.OpenPilotIssueState("OWNER/REPO", "lease")
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
			state, err := store.OpenPilotIssueState("OWNER/REPO", "123")
			if err != nil || state.Phase != test.phase || (test.runID != "" && state.RunID != test.runID) {
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
			state, err := store.OpenPilotIssueState("OWNER/REPO", "1")
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
			changed, err := store.ReconcileOpenPilotCIRepairTerminal(child.ID)
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

			changed, err := store.ReconcileOpenPilotCIRepairTerminal(child.ID)
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
	changed, err := store.ReconcileOpenPilotCIRepairTerminal(child.ID)
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
	reconciled, err := store.ReconcileOpenPilotCIRepairTerminals(1)
	if err != nil || reconciled != 1 {
		t.Fatalf("reconciled = %d, %v", reconciled, err)
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "1")
	if err != nil || state.Phase != OpenPilotPhaseFailed {
		t.Fatalf("state = %+v, %v", state, err)
	}
}

func TestWorkerRecoveryDrainsBoundedOpenPilotCIRepairReconciliation(t *testing.T) {
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
		if state.Phase != OpenPilotPhaseFailed {
			t.Fatalf("state not drained = %+v", state)
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
