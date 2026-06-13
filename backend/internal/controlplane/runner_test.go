package controlplane

import (
	"context"
	"reflect"
	"testing"
	"time"
)

type recordingPipelineExecutor struct {
	steps  []string
	models []string
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
