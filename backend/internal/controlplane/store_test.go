package controlplane_test

import (
	"context"
	"testing"
	"time"

	"git.subcult.tv/PatrickFanella/switchyard/internal/controlplane"
)

func TestSeedDefaultsCreatesWorkflowAndTriggerCatalog(t *testing.T) {
	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}

	workflows := store.ListWorkflows()
	if len(workflows) != 3 {
		t.Fatalf("len(workflows) = %d, want 3", len(workflows))
	}
	if workflows[0].ID != "changemaker" || workflows[1].ID != "devlog" || workflows[2].ID != "open-pilot" {
		t.Fatalf("workflows sorted by id = %+v", workflows)
	}

	triggers := store.ListTriggers()
	if len(triggers) != 4 {
		t.Fatalf("len(triggers) = %d, want 4", len(triggers))
	}
}

func TestRunnerCreatesRunEnvelope(t *testing.T) {
	now := time.Unix(100, 0)
	store := controlplane.NewStore(func() time.Time { return now })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	runner := controlplane.NewRunner(store, controlplane.MapExecutorRegistry{})

	run, err := runner.RunTrigger(t.Context(), "manual-open-pilot")
	if err != nil {
		t.Fatalf("RunTrigger() error = %v", err)
	}
	if run.WorkflowID != "open-pilot" || run.TriggerID != "manual-open-pilot" {
		t.Fatalf("run = %+v", run)
	}
	if run.Status != controlplane.RunSucceeded {
		t.Fatalf("status = %q, want succeeded", run.Status)
	}

	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if len(detail.Steps) != 1 || detail.Steps[0].Status != controlplane.StepSucceeded || detail.Steps[0].Name != "open-pilot.execute" {
		t.Fatalf("steps = %+v", detail.Steps)
	}
	if len(detail.Events) != 7 {
		t.Fatalf("len(events) = %d, want 7: %+v", len(detail.Events), detail.Events)
	}
}

func TestRunnerDispatchesChangemakerExecutor(t *testing.T) {
	now := time.Unix(100, 0)
	store := controlplane.NewStore(func() time.Time { return now })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	runner := controlplane.NewRunner(store, controlplane.MapExecutorRegistry{
		"changemaker": controlplane.WorkflowExecutorFunc(func(_ context.Context, run controlplane.Run) (controlplane.WorkflowExecution, error) {
			if run.WorkflowID != "changemaker" {
				t.Fatalf("workflow = %s", run.WorkflowID)
			}
			return controlplane.WorkflowExecution{Summary: "dispatched test changemaker", Events: []controlplane.WorkflowExecutionEvent{{Type: "changemaker.accepted", Message: "accepted"}}}, nil
		}),
	})

	run, err := runner.RunTrigger(t.Context(), "daily-changemaker")
	if err != nil {
		t.Fatalf("RunTrigger() error = %v", err)
	}
	if run.Status != controlplane.RunSucceeded || run.Summary != "dispatched test changemaker" {
		t.Fatalf("run = %+v", run)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if len(detail.Steps) != 1 || detail.Steps[0].Status != controlplane.StepSucceeded {
		t.Fatalf("steps = %+v", detail.Steps)
	}
	if detail.Events[4].Type != "changemaker.accepted" {
		t.Fatalf("events = %+v", detail.Events)
	}
}

func TestStoreAddsArtifacts(t *testing.T) {
	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	run, err := store.CreateRun(controlplane.CreateRunInput{WorkflowID: "devlog"})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	artifact, err := store.AddArtifact(controlplane.CreateArtifactInput{RunID: run.ID, Name: "devlog.md", Kind: "markdown", URI: "data:text/markdown,hello"})
	if err != nil {
		t.Fatalf("AddArtifact() error = %v", err)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if len(detail.Artifacts) != 1 || detail.Artifacts[0].ID != artifact.ID {
		t.Fatalf("artifacts = %+v", detail.Artifacts)
	}
	if detail.Events[len(detail.Events)-1].Type != "artifact.created" {
		t.Fatalf("events = %+v", detail.Events)
	}
}

func TestStoreRunStepLifecycle(t *testing.T) {
	now := time.Unix(100, 0)
	store := controlplane.NewStore(func() time.Time { return now })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	run, err := store.CreateRun(controlplane.CreateRunInput{WorkflowID: "devlog"})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	step, err := store.CreateRunStep(controlplane.CreateRunStepInput{RunID: run.ID, Position: 1, Name: "collect history", Executor: "internal", Inputs: map[string]string{"scope": "recent"}})
	if err != nil {
		t.Fatalf("CreateRunStep() error = %v", err)
	}
	if step.Status != controlplane.StepQueued || step.AttemptCount != 0 {
		t.Fatalf("created step = %+v", step)
	}
	step, err = store.StartRunStep(step.ID)
	if err != nil {
		t.Fatalf("StartRunStep() error = %v", err)
	}
	if step.Status != controlplane.StepRunning || step.AttemptCount != 1 || step.StartedAt == nil {
		t.Fatalf("started step = %+v", step)
	}
	step, err = store.FinishRunStep(step.ID, controlplane.StepFailed, "", "boom", map[string]string{"request": "req-1"})
	if err != nil {
		t.Fatalf("FinishRunStep() error = %v", err)
	}
	if step.Status != controlplane.StepFailed || step.Error != "boom" || step.CorrelationIDs["request"] != "req-1" {
		t.Fatalf("failed step = %+v", step)
	}
	step, err = store.RetryRunStep(step.ID)
	if err != nil {
		t.Fatalf("RetryRunStep() error = %v", err)
	}
	if step.Status != controlplane.StepQueued || step.Error != "" || step.StartedAt != nil || step.FinishedAt != nil {
		t.Fatalf("retried step = %+v", step)
	}
	steps := store.ListRunSteps(run.ID)
	if len(steps) != 1 || steps[0].ID != step.ID {
		t.Fatalf("steps = %+v", steps)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if len(detail.Steps) != 1 || detail.Steps[0].ID != step.ID {
		t.Fatalf("detail steps = %+v", detail.Steps)
	}
}

func TestStoreRunStepCancelValidation(t *testing.T) {
	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	run, err := store.CreateRun(controlplane.CreateRunInput{WorkflowID: "devlog"})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	step, err := store.CreateRunStep(controlplane.CreateRunStepInput{RunID: run.ID, Position: 1, Name: "generate", Executor: "model"})
	if err != nil {
		t.Fatalf("CreateRunStep() error = %v", err)
	}
	step, err = store.CancelRunStep(step.ID)
	if err != nil {
		t.Fatalf("CancelRunStep() error = %v", err)
	}
	if step.Status != controlplane.StepCanceled || step.FinishedAt == nil {
		t.Fatalf("canceled step = %+v", step)
	}
	if _, err := store.CancelRunStep(step.ID); err == nil {
		t.Fatal("CancelRunStep() succeeded for terminal step")
	}
}
