package controlplane_test

import (
	"context"
	"testing"
	"time"

	"git.subcult.tv/PatrickFanella/switchyard/internal/controlplane"
)

func TestSchedulerInitializesNextRunForScheduledTriggers(t *testing.T) {
	now := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
	store := controlplane.NewStore(func() time.Time { return now })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	if _, err := store.SetTriggerEnabled("daily-changemaker", true); err != nil {
		t.Fatalf("SetTriggerEnabled() error = %v", err)
	}
	runner := controlplane.NewRunner(store, controlplane.MapExecutorRegistry{})
	scheduler := controlplane.NewScheduler(store, runner, time.Minute, func() time.Time { return now })

	if err := scheduler.Tick(context.Background()); err != nil {
		t.Fatalf("Tick() error = %v", err)
	}
	trigger, ok := store.Trigger("daily-changemaker")
	if !ok {
		t.Fatal("daily-changemaker not found")
	}
	if trigger.NextRunAt == nil || !trigger.NextRunAt.Equal(time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("next_run_at = %v", trigger.NextRunAt)
	}
}

func TestSchedulerDispatchesDueTrigger(t *testing.T) {
	now := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	store := controlplane.NewStore(func() time.Time { return now })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	if _, err := store.SetTriggerEnabled("daily-changemaker", true); err != nil {
		t.Fatalf("SetTriggerEnabled() error = %v", err)
	}
	due := now.Add(-time.Minute)
	if _, err := store.UpdateTriggerScheduleState("daily-changemaker", nil, &due, ""); err != nil {
		t.Fatalf("UpdateTriggerScheduleState() error = %v", err)
	}
	runner := controlplane.NewRunner(store, controlplane.MapExecutorRegistry{"changemaker": controlplane.WorkflowExecutorFunc(func(context.Context, controlplane.Run) (controlplane.WorkflowExecution, error) {
		return controlplane.WorkflowExecution{Summary: "scheduler dispatch", Events: []controlplane.WorkflowExecutionEvent{{Type: "changemaker.accepted", Message: "accepted"}}}, nil
	})})
	scheduler := controlplane.NewScheduler(store, runner, time.Minute, func() time.Time { return now })

	if err := scheduler.Tick(context.Background()); err != nil {
		t.Fatalf("Tick() error = %v", err)
	}
	trigger, _ := store.Trigger("daily-changemaker")
	if trigger.LastRunAt == nil || !trigger.LastRunAt.Equal(now) {
		t.Fatalf("last_run_at = %v", trigger.LastRunAt)
	}
	runs := store.ListRuns()
	if len(runs) != 1 || runs[0].TriggerID != "daily-changemaker" || runs[0].Summary != "scheduler dispatch" {
		t.Fatalf("runs = %+v", runs)
	}
}
