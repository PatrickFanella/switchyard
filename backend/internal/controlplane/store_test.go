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
	if len(detail.Events) != 4 {
		t.Fatalf("len(events) = %d, want 4: %+v", len(detail.Events), detail.Events)
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
	if detail.Events[2].Type != "changemaker.accepted" {
		t.Fatalf("events = %+v", detail.Events)
	}
}
