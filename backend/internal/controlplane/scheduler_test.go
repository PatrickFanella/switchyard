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
	if _, err := store.SetTriggerEnabled("daily-changemaker-native", true); err != nil {
		t.Fatalf("SetTriggerEnabled() error = %v", err)
	}
	runner := controlplane.NewRunner(store, controlplane.MapExecutorRegistry{})
	scheduler := controlplane.NewScheduler(store, runner, time.Minute, func() time.Time { return now })

	if err := scheduler.Tick(context.Background()); err != nil {
		t.Fatalf("Tick() error = %v", err)
	}
	trigger, ok := store.Trigger("daily-changemaker-native")
	if !ok {
		t.Fatal("daily-changemaker-native not found")
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
	if _, err := store.SetTriggerEnabled("daily-changemaker-native", true); err != nil {
		t.Fatalf("SetTriggerEnabled() error = %v", err)
	}
	due := now.Add(-time.Minute)
	if _, err := store.UpdateTriggerScheduleState("daily-changemaker-native", nil, &due, ""); err != nil {
		t.Fatalf("UpdateTriggerScheduleState() error = %v", err)
	}
	runner := controlplane.NewRunner(store, controlplane.MapExecutorRegistry{"changemaker-daily-digest": controlplane.WorkflowExecutorFunc(func(context.Context, controlplane.Run) (controlplane.WorkflowExecution, error) {
		return controlplane.WorkflowExecution{Summary: "scheduler dispatch", Events: []controlplane.WorkflowExecutionEvent{{Type: "changemaker.accepted", Message: "accepted"}}}, nil
	})})
	scheduler := controlplane.NewScheduler(store, runner, time.Minute, func() time.Time { return now })

	if err := scheduler.Tick(context.Background()); err != nil {
		t.Fatalf("Tick() error = %v", err)
	}
	trigger, _ := store.Trigger("daily-changemaker-native")
	if trigger.LastRunAt == nil || !trigger.LastRunAt.Equal(now) {
		t.Fatalf("last_run_at = %v", trigger.LastRunAt)
	}
	runs := store.ListRuns()
	if len(runs) != 1 || runs[0].TriggerID != "daily-changemaker-native" || runs[0].Summary != "scheduler dispatch" {
		t.Fatalf("runs = %+v", runs)
	}
}

func TestSchedulerSupportsAllFiveCronFieldsAndTimezone(t *testing.T) {
	now := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	store := controlplane.NewStore(func() time.Time { return now })
	if err := store.UpsertWorkflow(controlplane.Workflow{ID: "scheduled", Kind: "scheduled", Name: "Scheduled"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertTrigger(controlplane.Trigger{ID: "monthly", WorkflowID: "scheduled", Type: controlplane.TriggerCron, Name: "Monthly", Enabled: true, Schedule: "30 9 15 2 *"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertTrigger(controlplane.Trigger{ID: "weekday", WorkflowID: "scheduled", Type: controlplane.TriggerCron, Name: "Weekday", Enabled: true, Schedule: "0,30 8-10 * * 1-5"}); err != nil {
		t.Fatal(err)
	}
	scheduler := controlplane.NewScheduler(store, controlplane.NewRunner(store), time.Minute, func() time.Time { return now })
	location, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	scheduler.SetLocation(location)
	if err := scheduler.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	monthly, _ := store.Trigger("monthly")
	wantMonthly := time.Date(2026, time.February, 15, 15, 30, 0, 0, time.UTC)
	if monthly.NextRunAt == nil || !monthly.NextRunAt.Equal(wantMonthly) {
		t.Fatalf("monthly next = %v, want %v", monthly.NextRunAt, wantMonthly)
	}
	weekday, _ := store.Trigger("weekday")
	wantWeekday := time.Date(2026, time.January, 1, 14, 0, 0, 0, time.UTC)
	if weekday.NextRunAt == nil || !weekday.NextRunAt.Equal(wantWeekday) {
		t.Fatalf("weekday next = %v, want %v", weekday.NextRunAt, wantWeekday)
	}
}

func TestSchedulerRecordsInvalidCronInsteadOfGuessing(t *testing.T) {
	store := controlplane.NewStore(nil)
	if err := store.UpsertWorkflow(controlplane.Workflow{ID: "scheduled", Kind: "scheduled", Name: "Scheduled"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertTrigger(controlplane.Trigger{ID: "invalid", WorkflowID: "scheduled", Type: controlplane.TriggerCron, Name: "Invalid", Enabled: true, Schedule: "99 9 * * *"}); err != nil {
		t.Fatal(err)
	}
	scheduler := controlplane.NewScheduler(store, controlplane.NewRunner(store), time.Minute, nil)
	if err := scheduler.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	trigger, _ := store.Trigger("invalid")
	if trigger.NextRunAt != nil || trigger.LastError == "" {
		t.Fatalf("trigger = %+v", trigger)
	}
}
