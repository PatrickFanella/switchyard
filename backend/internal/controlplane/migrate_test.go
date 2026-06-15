package controlplane

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestControlPlaneMigrationDeclaresDurableTables(t *testing.T) {
	data, err := migrationFS.ReadFile("migrations/001_controlplane.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(data)
	for _, table := range []string{"workflows", "triggers", "runs", "run_events", "artifacts"} {
		if !strings.Contains(sql, "create table if not exists "+table) {
			t.Fatalf("migration missing table %q", table)
		}
	}
}

func TestTriggerSchedulerMigrationDeclaresMetadata(t *testing.T) {
	data, err := migrationFS.ReadFile("migrations/002_trigger_scheduler.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(data)
	for _, field := range []string{"last_run_at", "updated_at", "last_error", "triggers_due_idx"} {
		if !strings.Contains(sql, field) {
			t.Fatalf("scheduler migration missing %q", field)
		}
	}
}

func TestRunStepsMigrationDeclaresStepTables(t *testing.T) {
	data, err := migrationFS.ReadFile("migrations/003_run_steps.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(data)
	for _, field := range []string{"run_steps", "attempt_count", "correlation_ids", "run_step_id", "run_steps_run_id_position_idx"} {
		if !strings.Contains(sql, field) {
			t.Fatalf("run steps migration missing %q", field)
		}
	}
}

func TestRunQueueMigrationDeclaresLeaseFields(t *testing.T) {
	data, err := migrationFS.ReadFile("migrations/004_run_queue_leases.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(data)
	for _, field := range []string{"queued_at", "available_at", "claimed_by", "lease_expires_at", "attempt_count", "runs_queue_idx"} {
		if !strings.Contains(sql, field) {
			t.Fatalf("run queue migration missing %q", field)
		}
	}
}

func TestOpenPilotStateMigrationDeclaresLifecycleTable(t *testing.T) {
	data, err := migrationFS.ReadFile("migrations/006_open_pilot_state.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(data)
	for _, field := range []string{"create table if not exists open_pilot_issue_states", "unique(repo, issue)", "open_pilot_issue_states_phase_idx", "open_pilot_issue_states_run_id_idx", "run_id text not null default ''", "last_error text not null default ''", "repair_attempts integer not null default 0", "max_repair_attempts integer not null default 2", "constraint open_pilot_issue_states_gate_status_check", "constraint open_pilot_issue_states_resume_inputs_check", "constraint open_pilot_issue_states_phase_check", "constraint open_pilot_issue_states_repair_attempts_check"} {
		if !strings.Contains(sql, field) {
			t.Fatalf("open pilot migration missing %q", field)
		}
	}
	diagnostics, err := migrationFS.ReadFile("migrations/007_open_pilot_failure_diagnostics.sql")
	if err != nil {
		t.Fatalf("read diagnostics migration: %v", err)
	}
	diagnosticsSQL := string(diagnostics)
	for _, field := range []string{"add column if not exists failing_gate text", "add column if not exists operator_summary text", "add column if not exists recommended_next_action text"} {
		if !strings.Contains(diagnosticsSQL, field) {
			t.Fatalf("open pilot diagnostics migration missing %q", field)
		}
	}
}

func TestPostgresStoreOpenPilotIssueStateIntegration(t *testing.T) {
	databaseURL := os.Getenv("SWITCHYARD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set SWITCHYARD_TEST_DATABASE_URL to run Postgres open pilot state integration test")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	db, err := OpenPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatalf("OpenPostgres() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	store := NewPostgresStore(db, func() time.Time { return time.Unix(100, 0) })
	repo := "OWNER/POSTGRES-OPEN-PILOT"
	issue := "1"
	saved, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: repo, Issue: issue, Phase: OpenPilotPhaseRequested, GateStatus: OpenPilotGateStatus{"ai_review": "pending"}, ResumeInputs: map[string]string{"branch": "open-pilot/issue-1"}, MaxRepairAttempts: 0, FailingGate: "local_verify", OperatorSummary: "Local verification failed", RecommendedNextAction: "rerun_verify"})
	if err != nil {
		t.Fatalf("UpsertOpenPilotIssueState() error = %v", err)
	}
	if saved.MaxRepairAttempts != openPilotDefaultMaxRepairAttempts {
		t.Fatalf("saved.MaxRepairAttempts = %d, want %d", saved.MaxRepairAttempts, openPilotDefaultMaxRepairAttempts)
	}
	fetched, err := store.OpenPilotIssueState(repo, issue)
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if fetched.ID != saved.ID || fetched.GateStatus["ai_review"] != "pending" || fetched.ResumeInputs["branch"] != "open-pilot/issue-1" || fetched.FailingGate != "local_verify" || fetched.OperatorSummary != "Local verification failed" || fetched.RecommendedNextAction != "rerun_verify" {
		t.Fatalf("fetched = %+v", fetched)
	}
	listed := store.ListOpenPilotIssueStates()
	found := false
	for _, state := range listed {
		if state.ID == saved.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("listed = %+v", listed)
	}
	transitioned, err := store.TransitionOpenPilotIssueState(repo, issue, OpenPilotPhaseValidated)
	if err != nil {
		t.Fatalf("TransitionOpenPilotIssueState() error = %v", err)
	}
	if transitioned.Phase != OpenPilotPhaseValidated || transitioned.MaxRepairAttempts != openPilotDefaultMaxRepairAttempts {
		t.Fatalf("transitioned = %+v", transitioned)
	}
	updated, err := store.OpenPilotIssueState(repo, issue)
	if err != nil {
		t.Fatalf("OpenPilotIssueState() after transition error = %v", err)
	}
	if updated.Phase != OpenPilotPhaseValidated {
		t.Fatalf("updated = %+v", updated)
	}
	if _, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: repo, Issue: issue, Phase: OpenPilotPhaseIssueClosed}); err == nil {
		t.Fatal("expected requested/validated -> issue_closed upsert to be rejected")
	}
}

func TestPostgresStoreIntegration(t *testing.T) {
	databaseURL := os.Getenv("SWITCHYARD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set SWITCHYARD_TEST_DATABASE_URL to run Postgres store integration test")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	db, err := OpenPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatalf("OpenPostgres() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	store := NewPostgresStore(db, func() time.Time { return time.Unix(100, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	if err := store.UpsertTrigger(Trigger{ID: "manual-open-pilot", WorkflowID: string(WorkflowOpenPilot), Type: TriggerManual, Name: "Run Open Pilot now", Description: "Manual trigger for issue-to-PR automation.", Enabled: true, Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}}); err != nil {
		t.Fatalf("UpsertTrigger() error = %v", err)
	}
	runner := NewRunner(store)
	run, err := runner.RunTrigger(ctx, "manual-open-pilot")
	if err != nil {
		t.Fatalf("RunTrigger() error = %v", err)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if detail.Run.Status != RunSucceeded || len(detail.Events) != 4 {
		t.Fatalf("detail = %+v", detail)
	}
}
