package controlplane

import (
	"context"
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
