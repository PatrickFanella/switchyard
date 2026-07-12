package controlplane

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordingPipelineExecutor struct {
	steps  []string
	models []string
}

type throttledPipelineExecutor struct {
	mu        sync.Mutex
	active    int
	maxActive int
}

type blockingCorrelatedPipelineExecutor struct {
	started chan struct{}
	release chan struct{}
}

func (e *throttledPipelineExecutor) Execute(context.Context, Run) (WorkflowExecution, error) {
	return WorkflowExecution{Summary: "fallback should not run"}, nil
}

func (e *throttledPipelineExecutor) ExecutePipelineStep(ctx context.Context, _ Run, step PipelineStep, _ map[string]any) (PipelineStepExecution, error) {
	if step.Executor != "model" {
		return PipelineStepExecution{Summary: "ran " + step.ID}, nil
	}
	e.mu.Lock()
	e.active++
	if e.active > e.maxActive {
		e.maxActive = e.active
	}
	e.mu.Unlock()
	select {
	case <-ctx.Done():
		e.mu.Lock()
		e.active--
		e.mu.Unlock()
		return PipelineStepExecution{}, ctx.Err()
	case <-time.After(20 * time.Millisecond):
	}
	e.mu.Lock()
	e.active--
	e.mu.Unlock()
	return PipelineStepExecution{Summary: "ran " + step.ID}, nil
}

func (e *blockingCorrelatedPipelineExecutor) Execute(context.Context, Run) (WorkflowExecution, error) {
	return WorkflowExecution{Summary: "fallback should not run"}, nil
}

func (e *blockingCorrelatedPipelineExecutor) PipelineStepCorrelationIDs(run Run, step PipelineStep, _ map[string]any) map[string]string {
	return map[string]string{"ocq_request_id": run.ID + "-" + step.ID, "opencode_executor": "switchyard"}
}

func (e *blockingCorrelatedPipelineExecutor) ExecutePipelineStep(ctx context.Context, _ Run, step PipelineStep, _ map[string]any) (PipelineStepExecution, error) {
	close(e.started)
	select {
	case <-ctx.Done():
		return PipelineStepExecution{}, ctx.Err()
	case <-e.release:
	}
	return PipelineStepExecution{Summary: "ran " + step.ID, CorrelationIDs: map[string]string{"ocq_request_id": "finished"}}, nil
}

func TestRunnerPersistsPipelineStepCorrelationBeforeExecutionCompletes(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := store.UpsertWorkflow(Workflow{ID: "test-workflow", Kind: WorkflowKind("test-workflow"), Name: "Test Workflow"}); err != nil {
		t.Fatalf("UpsertWorkflow() error = %v", err)
	}
	executor := &blockingCorrelatedPipelineExecutor{started: make(chan struct{}), release: make(chan struct{})}
	runner := NewRunner(store, MapExecutorRegistry{"test-workflow": executor})
	runner.pipelines = StaticPipelineRegistry{
		"test-workflow": {
			ID:         "test-pipeline",
			WorkflowID: "test-workflow",
			Name:       "Test Pipeline",
			Steps:      []PipelineStep{{ID: "agent-work", Position: 1, Name: "test.agent-work", Executor: "opencode"}},
		},
	}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: "test-workflow"})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := runner.ExecuteRun(context.Background(), run)
		done <- err
	}()
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("pipeline step did not start")
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if len(detail.Steps) != 1 || detail.Steps[0].Status != StepRunning || detail.Steps[0].CorrelationIDs["ocq_request_id"] != run.ID+"-agent-work" {
		t.Fatalf("steps = %+v", detail.Steps)
	}
	close(executor.release)
	if err := <-done; err != nil {
		t.Fatalf("ExecuteRun() error = %v", err)
	}
}

func TestRunnerLimitsPipelineExecutorConcurrency(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := store.UpsertWorkflow(Workflow{ID: "test-workflow", Kind: WorkflowKind("test-workflow"), Name: "Test Workflow"}); err != nil {
		t.Fatalf("UpsertWorkflow() error = %v", err)
	}
	executor := &throttledPipelineExecutor{}
	runner := NewRunner(store, MapExecutorRegistry{"test-workflow": executor})
	runner.pipelines = StaticPipelineRegistry{
		"test-workflow": {
			ID:         "test-pipeline",
			WorkflowID: "test-workflow",
			Name:       "Test Pipeline",
			Steps: []PipelineStep{
				{ID: "collect", Position: 1, Name: "test.collect", Executor: "internal"},
				{ID: "generate", Position: 2, Name: "test.generate", Executor: "model"},
			},
		},
	}
	runner.SetExecutorLimiter(NewSemaphoreExecutorLimiter(map[string]int{"model": 1}))

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := runner.RunOnce(context.Background(), CreateRunInput{WorkflowID: "test-workflow"}); err != nil {
				t.Errorf("RunOnce() error = %v", err)
			}
		}()
	}
	wg.Wait()

	if executor.maxActive != 1 {
		t.Fatalf("max active model steps = %d", executor.maxActive)
	}
}

func (e *recordingPipelineExecutor) Execute(context.Context, Run) (WorkflowExecution, error) {
	return WorkflowExecution{Summary: "fallback should not run"}, nil
}

func (e *recordingPipelineExecutor) ExecutePipelineStep(_ context.Context, _ Run, step PipelineStep, _ map[string]any) (PipelineStepExecution, error) {
	e.steps = append(e.steps, step.ID)
	e.models = append(e.models, step.Model)
	return PipelineStepExecution{Summary: "ran " + step.ID}, nil
}

func (e *recordingPipelineExecutor) ResolvePipelineStepModel(_ string, step PipelineStep, _ map[string]string) (string, error) {
	if step.Model == "${model}" {
		return "resolved-model", nil
	}
	return step.Model, nil
}

func TestRunnerExecutesEnqueuedPipelineSnapshotAfterDefinitionChanges(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := store.UpsertWorkflow(Workflow{ID: "test-workflow", Kind: WorkflowKind("test-workflow"), Name: "Test Workflow"}); err != nil {
		t.Fatalf("UpsertWorkflow() error = %v", err)
	}
	executor := &recordingPipelineExecutor{}
	runner := NewRunner(store, MapExecutorRegistry{"test-workflow": executor})
	original := Pipeline{ID: "original", WorkflowID: "test-workflow", Steps: []PipelineStep{{ID: "original-step", Position: 1, Name: "test.original", Executor: "command"}}}
	runner.pipelines = StaticPipelineRegistry{"test-workflow": original}

	run, err := runner.Enqueue(CreateRunInput{WorkflowID: "test-workflow"})
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if run.PipelineID != original.ID || run.PipelineVersion == "" || run.PipelineSnapshot == nil {
		t.Fatalf("enqueued run missing pipeline identity: %+v", run)
	}
	runner.pipelines = StaticPipelineRegistry{"test-workflow": {ID: "replacement", WorkflowID: "test-workflow", Steps: []PipelineStep{{ID: "replacement-step", Position: 1, Name: "test.replacement", Executor: "command"}}}}
	run, err = store.StartRun(run.ID)
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	if _, err := runner.ExecuteRun(t.Context(), run); err != nil {
		t.Fatalf("ExecuteRun() error = %v", err)
	}
	if !reflect.DeepEqual(executor.steps, []string{"original-step"}) {
		t.Fatalf("executed steps = %v, want original snapshot", executor.steps)
	}
}

func TestRunnerExecutesRegisteredLinearPipeline(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := store.UpsertWorkflow(Workflow{ID: "test-workflow", Kind: WorkflowKind("test-workflow"), Name: "Test Workflow"}); err != nil {
		t.Fatalf("UpsertWorkflow() error = %v", err)
	}
	executor := &recordingPipelineExecutor{}
	runner := NewRunner(store, MapExecutorRegistry{"test-workflow": executor})
	runner.pipelines = StaticPipelineRegistry{
		"test-workflow": {
			ID:         "test-pipeline",
			WorkflowID: "test-workflow",
			Name:       "Test Pipeline",
			Steps: []PipelineStep{
				{ID: "second", Position: 2, Name: "test.second", Executor: "internal", Model: "${model}"},
				{ID: "first", Position: 1, Name: "test.first", Executor: "internal"},
			},
		},
	}

	run, err := runner.RunOnce(context.Background(), CreateRunInput{WorkflowID: "test-workflow"})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if run.Status != RunSucceeded || run.Summary != "Workflow dispatched" {
		t.Fatalf("run = %+v", run)
	}
	if !reflect.DeepEqual(executor.steps, []string{"first", "second"}) {
		t.Fatalf("executed steps = %+v", executor.steps)
	}
	if !reflect.DeepEqual(executor.models, []string{"", "resolved-model"}) {
		t.Fatalf("executed models = %+v", executor.models)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if len(detail.Steps) != 2 || detail.Steps[0].Name != "test.first" || detail.Steps[1].Name != "test.second" {
		t.Fatalf("steps = %+v", detail.Steps)
	}
	for _, step := range detail.Steps {
		if step.Status != StepSucceeded || step.AttemptCount != 1 {
			t.Fatalf("step = %+v", step)
		}
	}
}

func TestRunnerSkipsEarlierStepsForResumeRun(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := store.UpsertWorkflow(Workflow{ID: "test-workflow", Kind: WorkflowKind("test-workflow"), Name: "Test Workflow"}); err != nil {
		t.Fatalf("UpsertWorkflow() error = %v", err)
	}
	executor := &recordingPipelineExecutor{}
	runner := NewRunner(store, MapExecutorRegistry{"test-workflow": executor})
	runner.pipelines = StaticPipelineRegistry{
		"test-workflow": {
			ID:         "test-pipeline",
			WorkflowID: "test-workflow",
			Name:       "Test Pipeline",
			Steps: []PipelineStep{
				{ID: "first", Position: 1, Name: "test.first", Executor: "internal"},
				{ID: "second", Position: 2, Name: "test.second", Executor: "internal"},
				{ID: "third", Position: 3, Name: "test.third", Executor: "internal"},
			},
		},
	}

	run, err := runner.RunOnce(context.Background(), CreateRunInput{WorkflowID: "test-workflow", Inputs: map[string]string{"retry_from_step_position": "2", "retry_from_step": "test.second"}})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if run.Status != RunSucceeded {
		t.Fatalf("run = %+v", run)
	}
	if !reflect.DeepEqual(executor.steps, []string{"second", "third"}) {
		t.Fatalf("executed steps = %+v", executor.steps)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if len(detail.Steps) != 2 || detail.Steps[0].Name != "test.second" || detail.Steps[1].Name != "test.third" {
		t.Fatalf("steps = %+v", detail.Steps)
	}
}

func TestPipelineResumeRejectsMismatchedIdentityAndPosition(t *testing.T) {
	steps := []PipelineStep{{ID: "first", Position: 1, Name: "test.first"}, {ID: "second", Position: 2, Name: "test.second"}}
	_, err := pipelineResumePosition(Run{Inputs: map[string]string{"retry_from_step": "test.second", "retry_from_step_position": "1"}}, steps)
	if err == nil || !strings.Contains(err.Error(), "position 2, not 1") {
		t.Fatalf("pipelineResumePosition() error = %v", err)
	}
	if _, err := pipelineResumePosition(Run{Inputs: map[string]string{"retry_from_step": "test.missing"}}, steps); err == nil {
		t.Fatal("expected unknown resume step to fail")
	}
}

func TestRunnerRecordsDeadlineAsTimedOut(t *testing.T) {
	store := NewStore(nil)
	if err := store.UpsertWorkflow(Workflow{ID: "timeout", Kind: "timeout", Name: "Timeout"}); err != nil {
		t.Fatalf("UpsertWorkflow() error = %v", err)
	}
	runner := NewRunner(store, MapExecutorRegistry{"timeout": WorkflowExecutorFunc(func(context.Context, Run) (WorkflowExecution, error) {
		return WorkflowExecution{}, context.DeadlineExceeded
	})})
	run, err := runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: "timeout"})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if run.Status != RunTimedOut {
		t.Fatalf("run.Status = %s, want timed_out", run.Status)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if len(detail.Steps) != 1 || detail.Steps[0].Status != StepTimedOut {
		t.Fatalf("steps = %+v", detail.Steps)
	}
}
