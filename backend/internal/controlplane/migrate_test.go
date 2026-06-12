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
