package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
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

func TestWorkflowCreatorMigrationDeclaresDefinitionTables(t *testing.T) {
	data, err := migrationFS.ReadFile("migrations/008_workflow_creator.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(data)
	for _, field := range []string{"create table if not exists workflow_definitions", "input_shape jsonb not null default '{}'::jsonb", "pipeline jsonb not null", "workflow_definitions_status_idx"} {
		if !strings.Contains(sql, field) {
			t.Fatalf("workflow creator migration missing %q", field)
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

func TestRunPipelineSnapshotMigrationDeclaresVersionedSnapshotFields(t *testing.T) {
	data, err := migrationFS.ReadFile("migrations/014_run_pipeline_snapshot.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(data)
	for _, field := range []string{"pipeline_id", "pipeline_version", "pipeline_snapshot", "runs_pipeline_version_idx"} {
		if !strings.Contains(sql, field) {
			t.Fatalf("run pipeline snapshot migration missing %q", field)
		}
	}
}

func TestWebhookMigrationDeclaresAtomicAdmissionLedger(t *testing.T) {
	data, err := migrationFS.ReadFile("migrations/024_webhook_dispatch.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"webhook_auth_config", "owner text not null", "generation bigint not null", "receipt_retention_seconds", "webhook_receipt_metrics", "receipts_pruned", "webhook_trigger_auth", "webhook_trigger_auth_keys", "webhook_deliveries", "primary key(trigger_id, delivery_id)", "unique(run_id)", "body_sha256"} {
		if !strings.Contains(string(data), field) {
			t.Errorf("webhook migration missing %q", field)
		}
	}
}

func TestRetentionMigrationDeclaresPartialIndexes(t *testing.T) {
	data, err := migrationFS.ReadFile("migrations/015_retention_indexes.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(data)
	for _, field := range []string{"runs_retention_idx", "model_broker_requests_retention_idx", "where status in"} {
		if !strings.Contains(sql, field) {
			t.Fatalf("retention migration missing %q", field)
		}
	}
}

func TestTransactionalAccountingMigrationDeclaresTriggersFloorAndRollbackSafety(t *testing.T) {
	data, err := migrationFS.ReadFile("migrations/023_transactional_artifact_and_compatibility_accounting.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(data)
	for _, field := range []string{"lock table artifacts in share row exclusive mode", "observation_retained_floor_count", "retained_count", "account_artifact_insert", "account_artifact_delete", "current_setting('switchyard.artifact_lifecycle_accounting', true) = 'trigger'", "compatibility_observation_daily", "alter column first_attempt_at set not null", "drop column if exists source_revision"} {
		if !strings.Contains(string(data), field) {
			t.Errorf("transactional accounting migration missing %q", field)
		}
	}
	if strings.Index(sql, "lock table artifacts") > strings.Index(sql, "from artifacts") {
		t.Fatal("artifact table must be locked before the retained snapshot")
	}
}

func TestNewArtifactWritersSelectTriggerAccountingBeforeMutations(t *testing.T) {
	runs, err := os.ReadFile("postgres_store_runs.go")
	if err != nil {
		t.Fatal(err)
	}
	runsText := string(runs)
	setAt := strings.Index(runsText, "enableArtifactTriggerLifecycleAccounting")
	insertAt := strings.Index(runsText, "insert into artifacts")
	if setAt < 0 || insertAt < 0 || setAt > insertAt {
		t.Fatal("AddArtifact must select trigger lifecycle accounting before artifact insert")
	}
	if strings.Contains(runsText, "created_count=created_count+") {
		t.Fatal("new artifact writer must not directly update created totals")
	}
	retention, err := os.ReadFile("postgres_store_model_broker.go")
	if err != nil {
		t.Fatal(err)
	}
	retentionText := string(retention)
	pruneStart := strings.Index(retentionText, "func (s *PostgresStore) PruneRetention")
	pruneEnd := strings.Index(retentionText[pruneStart:], "const retentionBatchSize")
	pruneBody := retentionText[pruneStart : pruneStart+pruneEnd]
	setAt = strings.Index(pruneBody, "enableArtifactTriggerLifecycleAccounting")
	deleteAt := strings.Index(pruneBody, "delete from artifacts")
	if setAt < 0 || deleteAt < 0 || setAt > deleteAt {
		t.Fatal("PruneRetention must select trigger lifecycle accounting before artifact delete")
	}
	if strings.Contains(pruneBody, "pruned_count=pruned_count+") {
		t.Fatal("new retention writer must not directly update pruned totals")
	}
}

func TestArtifactAndCompatibilityMigrationsPreserveFloorsAndOldReadersIntegration(t *testing.T) {
	databaseURL := os.Getenv("SWITCHYARD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set SWITCHYARD_TEST_DATABASE_URL to run migration contract integration test")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	db, err := OpenPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `drop schema if exists migration_contract_test cascade; create schema migration_contract_test; set local search_path to migration_contract_test`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `create table artifacts(id text primary key, created_at timestamptz not null, storage_class text not null, byte_size integer not null)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `insert into artifacts values('measured',now(),'inline-data-uri',7),('unmeasured',now(),'legacy-inline',19)`); err != nil {
		t.Fatal(err)
	}
	artifactMigration, err := migrationFS.ReadFile("migrations/021_artifact_measurement.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, string(artifactMigration)); err != nil {
		t.Fatalf("apply artifact migration: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `create table compatibility_usage_daily(usage_date date not null,backend text not null,attempts bigint not null default 0,first_attempt_at timestamptz not null,last_attempt_at timestamptz not null,primary key(usage_date,backend))`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `insert into compatibility_usage_daily values(current_date,'ocq',1,now(),now())`); err != nil {
		t.Fatal(err)
	}
	observationMigration, err := migrationFS.ReadFile("migrations/022_compatibility_observations.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, string(observationMigration)); err != nil {
		t.Fatalf("apply compatibility migration: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `insert into compatibility_usage_daily(usage_date,backend,attempts,first_attempt_at,last_attempt_at,source_revision,deployment_id,observation_started_at,observation_completed_at,observation_segment,closed_at) values(current_date-1,'llama-line',0,null,null,'0123456789abcdef0123456789abcdef01234567','deployment',current_date-1,current_date,'segment',now())`); err != nil {
		t.Fatal(err)
	}
	correctionMigration, err := migrationFS.ReadFile("migrations/023_transactional_artifact_and_compatibility_accounting.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, string(correctionMigration)); err != nil {
		t.Fatalf("apply transactional correction migration: %v", err)
	}
	var floorCount, floorBytes, floorUnmeasured, retained, retainedBytes, retainedUnmeasured, created, pruned int64
	if err := tx.QueryRowContext(ctx, `select observation_retained_floor_count,observation_retained_floor_bytes,observation_retained_floor_bytes_unmeasured,retained_count,retained_bytes,retained_bytes_unmeasured,created_count,pruned_count from artifact_lifecycle_totals where singleton=true`).Scan(&floorCount, &floorBytes, &floorUnmeasured, &retained, &retainedBytes, &retainedUnmeasured, &created, &pruned); err != nil {
		t.Fatal(err)
	}
	if floorCount != 2 || floorBytes != 7 || floorUnmeasured != 1 || retained != 2 || retainedBytes != 7 || retainedUnmeasured != 1 || created != 0 || pruned != 0 {
		t.Fatalf("artifact observation floor/counts = floor(%d,%d,%d) retained(%d,%d,%d) lifecycle(%d,%d)", floorCount, floorBytes, floorUnmeasured, retained, retainedBytes, retainedUnmeasured, created, pruned)
	}
	var attempts int64
	var first, last time.Time
	if err := tx.QueryRowContext(ctx, `select attempts,first_attempt_at,last_attempt_at from compatibility_usage_daily where backend='ocq'`).Scan(&attempts, &first, &last); err != nil {
		t.Fatalf("old usage reader failed: %v", err)
	}
	if attempts != 1 || first.IsZero() || last.IsZero() {
		t.Fatalf("old usage row changed: attempts=%d first=%v last=%v", attempts, first, last)
	}
	var nonNullColumns int
	if err := tx.QueryRowContext(ctx, `select count(*) from pg_attribute where attrelid='compatibility_usage_daily'::regclass and attname in ('attempts','first_attempt_at','last_attempt_at') and attnotnull`).Scan(&nonNullColumns); err != nil {
		t.Fatal(err)
	}
	var observations int
	if err := tx.QueryRowContext(ctx, `select count(*) from compatibility_observation_daily`).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if nonNullColumns != 3 || observations != 1 {
		t.Fatalf("migration contract: non-null old columns=%d observations=%d", nonNullColumns, observations)
	}
}

func TestModelBrokerFeedbackMigrationDeclaresDurableFeedback(t *testing.T) {
	data, err := migrationFS.ReadFile("migrations/016_model_broker_feedback.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(data)
	for _, field := range []string{"create table if not exists model_broker_feedback", "references model_broker_requests", "rating between -1 and 1", "model_broker_feedback_request_idx"} {
		if !strings.Contains(sql, field) {
			t.Fatalf("feedback migration missing %q", field)
		}
	}
}

func TestWorkflowDefinitionVersionMigrationDeclaresHistory(t *testing.T) {
	data, err := migrationFS.ReadFile("migrations/017_workflow_definition_versions.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(data)
	for _, field := range []string{"add column if not exists version", "create table if not exists workflow_definition_versions", "primary key(id, version)", "on conflict(id, version) do nothing"} {
		if !strings.Contains(sql, field) {
			t.Fatalf("workflow version migration missing %q", field)
		}
	}
}

func TestNativeWorkflowActivationMigrationCorrectsStaleStatuses(t *testing.T) {
	data, err := migrationFS.ReadFile("migrations/018_activate_native_workflows.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(data)
	for _, field := range []string{"update workflows", "'open-pilot'", "'devlog'", "status = 'planned'", "status = 'active'"} {
		if !strings.Contains(sql, field) {
			t.Fatalf("native workflow activation migration missing %q", field)
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
	committedPhase, err := migrationFS.ReadFile("migrations/020_allow_committed_open_pilot_phase.sql")
	if err != nil {
		t.Fatalf("read committed phase migration: %v", err)
	}
	if !strings.Contains(string(committedPhase), "'committed'") {
		t.Fatal("committed phase migration does not allow committed")
	}
}

func TestOpenPilotRepositoryAffinityMigrationContract(t *testing.T) {
	data, err := migrationFS.ReadFile("migrations/025_open_pilot_repository_affinity.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, required := range []string{"repository_id", "executor_generation", "canonical_repo", "canonical_issue", "canonical_candidates", "legacy_repo", "legacy_issue", "open_pilot_issue_states_affinity_check", "open_pilot_issue_states_identity_shadow", "open_pilot_canonical_reservation"} {
		if !strings.Contains(text, required) {
			t.Errorf("affinity migration missing %q", required)
		}
	}
	if strings.Contains(strings.ToLower(text), "unique(canonical_repo") || strings.Contains(strings.ToLower(text), "unique index") {
		t.Fatal("Phase 1 must not add canonical uniqueness")
	}
	if strings.Contains(strings.ToLower(text), "chr(0)") || strings.IndexByte(text, 0) >= 0 {
		t.Fatal("affinity migration contains a NUL lock-key encoding")
	}
}

func TestOpenPilotCandidateMonotonicityMigrationContract(t *testing.T) {
	data, err := migrationFS.ReadFile("migrations/026_open_pilot_candidate_monotonicity.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, required := range []string{"old.canonical_candidates", "open_pilot_canonical_candidates_monotonic_check", "open_pilot_canonical_reservation", "pg_advisory_xact_lock"} {
		if !strings.Contains(text, required) {
			t.Errorf("candidate monotonicity migration missing %q", required)
		}
	}
	if strings.Contains(strings.ToLower(text), "chr(0)") || strings.IndexByte(text, 0) >= 0 {
		t.Fatal("candidate monotonicity migration contains a NUL lock-key encoding")
	}
}

func TestOpenPilotRepositoryAffinityMigrationAllowsFirstAdmission(t *testing.T) {
	databaseURL := os.Getenv("SWITCHYARD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set SWITCHYARD_TEST_DATABASE_URL to run Postgres migration admission test")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	admin, err := OpenPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("open_pilot_first_admission_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, `create schema `+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	db, err := OpenPostgres(ctx, databaseURL+" search_path="+schema)
	if err != nil {
		_, _ = admin.ExecContext(context.Background(), `drop schema `+schema+` cascade`)
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = admin.ExecContext(cleanupCtx, `drop schema `+schema+` cascade`)
		_ = admin.Close()
	})
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := NewPostgresStore(db, time.Now)
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(store, MapExecutorRegistry{})
	runner.SetOpenPilotAdmissionGate(testOpenPilotAdmissionGate(t))
	run, err := runner.Enqueue(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "LEGACY/SWITCHYARD", "issue": "01"}})
	if err != nil {
		t.Fatalf("first admission after migration: %v", err)
	}
	if run.Inputs["repo"] != "Subcult/Switchyard" || run.Inputs["issue"] != "1" {
		t.Fatalf("first admitted run inputs = %v", run.Inputs)
	}
}

func TestOpenPilotRepositoryAffinityMigrationBackfillsLegacyCollisions(t *testing.T) {
	databaseURL := os.Getenv("SWITCHYARD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set SWITCHYARD_TEST_DATABASE_URL to run Postgres migration integration test")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	db, err := OpenPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	schema := fmt.Sprintf("open_pilot_affinity_%d", time.Now().UnixNano())
	if _, err := tx.ExecContext(ctx, `create schema `+schema+`; set local search_path to `+schema+`; create table open_pilot_issue_states(id text primary key, repo text not null, issue text not null)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `insert into open_pilot_issue_states values ('one','OWNER/REPO','01'),('two','owner/repo','1')`); err != nil {
		t.Fatal(err)
	}
	migration, err := migrationFS.ReadFile("migrations/025_open_pilot_repository_affinity.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, string(migration)); err != nil {
		t.Fatalf("legacy collision backfill: %v", err)
	}
	var count int
	if err := tx.QueryRowContext(ctx, `select count(*) from open_pilot_issue_states where canonical_repo='owner/repo' and canonical_issue='1' and repository_id is null and executor_generation is null`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("backfilled collision count = %d, %v", count, err)
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
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	mixedRepo, mixedIssue := "OWNER/POSTGRES-MIXED-WRITER", "01"
	if _, err := db.ExecContext(ctx, `delete from open_pilot_issue_states where repo=$1 and issue=$2`, mixedRepo, mixedIssue); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `insert into open_pilot_issue_states(id,repo,issue,phase,created_at,updated_at) values($1,$2,$3,'requested',now(),now())`, newID("opi-old"), mixedRepo, mixedIssue); err != nil {
		t.Fatalf("simulated old insert: %v", err)
	}
	mixed, err := store.OpenPilotIssueState(mixedRepo, mixedIssue)
	if err != nil || mixed.LegacyRepo != mixedRepo || mixed.LegacyIssue != mixedIssue || mixed.CanonicalRepo != strings.ToLower(mixedRepo) || mixed.CanonicalIssue != "1" || mixed.RepositoryID != "" {
		t.Fatalf("new read of old row = %+v, %v", mixed, err)
	}
	forged := mixed
	forged.RepositoryID, forged.ExecutorGeneration = "postgres-mixed", "generation-one"
	if _, err := store.UpsertOpenPilotIssueState(forged); !errors.Is(err, ErrConflict) {
		t.Fatalf("generic affinity write error = %v", err)
	}
	if mixed, err = store.BindOpenPilotIssueStateAffinity(mixedRepo, mixedIssue, OpenPilotTarget{RepositoryID: "postgres-mixed", RepositorySlug: strings.ToLower(mixedRepo), Issue: "1", ExecutorGeneration: "generation-one"}); err != nil || mixed.RepositoryID != "postgres-mixed" {
		t.Fatalf("trusted affinity bind = %+v, %v", mixed, err)
	}
	if _, err := db.ExecContext(ctx, `update open_pilot_issue_states set repository_id=null,executor_generation=null where repo=$1 and issue=$2`, mixedRepo, mixedIssue); err == nil {
		t.Fatal("simulated old update removed a trusted reservation")
	}
	if mixed, err = store.OpenPilotIssueState(mixedRepo, mixedIssue); err != nil || mixed.RepositoryID != "postgres-mixed" || mixed.ExecutorGeneration != "generation-one" || mixed.CanonicalIssue != "1" {
		t.Fatalf("new read after old update = %+v, %v", mixed, err)
	}
	if _, err := db.ExecContext(ctx, `update open_pilot_issue_states set repository_id='forged',executor_generation=null where repo=$1 and issue=$2`, mixedRepo, mixedIssue); err == nil {
		t.Fatal("database accepted half-populated affinity")
	}
	if _, err := db.ExecContext(ctx, `update open_pilot_issue_states set repository_id=null,executor_generation='forged' where repo=$1 and issue=$2`, mixedRepo, mixedIssue); err == nil {
		t.Fatal("database accepted reverse half-populated affinity")
	}
	aliasIssue := "810001"
	aliasTarget := OpenPilotTarget{RepositoryID: "postgres-alias", RepositorySlug: "Subcult/Switchyard", RepositoryCandidates: []string{"legacy/switchyard", "subcult/switchyard"}, Issue: aliasIssue, ExecutorGeneration: "generation-alias"}
	_, trustedRun, err := store.ClaimAndCreateOpenPilotInitialRun(aliasTarget.RepositorySlug, aliasIssue, CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": aliasTarget.RepositorySlug, "issue": aliasIssue}, OpenPilotTarget: &aliasTarget})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(trustedRun.ID, RunFailed, "terminal reservation"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `update open_pilot_issue_states set phase=phase where run_id=$1`, trustedRun.ID); err != nil {
		t.Fatalf("old writer update failed: %v", err)
	}
	if _, err := db.ExecContext(ctx, `update open_pilot_issue_states set canonical_candidates=array['subcult/switchyard'] where run_id=$1`, trustedRun.ID); err == nil {
		t.Fatal("database accepted canonical candidate shrink")
	}
	var retained bool
	if err := db.QueryRowContext(ctx, `select 'legacy/switchyard'=any(canonical_candidates) from open_pilot_issue_states where run_id=$1`, trustedRun.ID).Scan(&retained); err != nil || !retained {
		t.Fatalf("retired alias reservation retained=%v error=%v", retained, err)
	}
	if _, err := db.ExecContext(ctx, `insert into open_pilot_issue_states(id,repo,issue,phase,created_at,updated_at) values($1,'legacy/switchyard',$2,'requested',now(),now())`, newID("old-alias"), aliasIssue); err == nil {
		t.Fatal("old alias writer bypassed trusted terminal reservation")
	}
	oldFirstIssue := "810002"
	if _, err := db.ExecContext(ctx, `insert into open_pilot_issue_states(id,repo,issue,phase,created_at,updated_at) values($1,'legacy/switchyard',$2,'failed',now(),now())`, newID("old-first"), oldFirstIssue); err != nil {
		t.Fatal(err)
	}
	oldFirstTarget := aliasTarget
	oldFirstTarget.Issue = oldFirstIssue
	if _, _, err := store.ClaimAndCreateOpenPilotInitialRun(oldFirstTarget.RepositorySlug, oldFirstIssue, CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": oldFirstTarget.RepositorySlug, "issue": oldFirstIssue}, OpenPilotTarget: &oldFirstTarget}); !errors.Is(err, ErrConflict) {
		t.Fatalf("trusted writer versus old alias error = %v", err)
	}
	oldCollisionIssue := "810003"
	if _, err := db.ExecContext(ctx, `insert into open_pilot_issue_states(id,repo,issue,phase,created_at,updated_at) values($1,'OWNER/OLD-COLLISION',$3,'requested',now(),now()),($2,'owner/old-collision',$3,'failed',now(),now())`, newID("old-one"), newID("old-two"), oldCollisionIssue); err != nil {
		t.Fatalf("existing legacy collision was not preserved: %v", err)
	}
	updateIssue := "810004"
	if _, err := db.ExecContext(ctx, `insert into open_pilot_issue_states(id,repo,issue,phase,created_at,updated_at) values($1,'owner/update-source',$2,'requested',now(),now())`, newID("old-update"), updateIssue); err != nil {
		t.Fatal(err)
	}
	updateTarget := aliasTarget
	updateTarget.Issue = updateIssue
	if _, _, err := store.ClaimAndCreateOpenPilotInitialRun(updateTarget.RepositorySlug, updateIssue, CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": updateTarget.RepositorySlug, "issue": updateIssue}, OpenPilotTarget: &updateTarget}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `update open_pilot_issue_states set repo='legacy/switchyard',issue=$2 where repo='owner/update-source' and issue=$1`, updateIssue, updateIssue); err == nil {
		t.Fatal("old update bypassed trusted alias reservation")
	}
	raceIssue := "810005"
	raceStart := make(chan struct{})
	raceResults := make(chan error, 2)
	go func() {
		<-raceStart
		_, raceErr := db.ExecContext(ctx, `insert into open_pilot_issue_states(id,repo,issue,repository_id,executor_generation,canonical_repo,canonical_issue,canonical_candidates,phase,created_at,updated_at) values($1,'Subcult/Switchyard',$2,'postgres-race','generation-race','Subcult/Switchyard',$2,array['legacy/switchyard','subcult/switchyard'],'requested',now(),now())`, newID("trusted-race"), raceIssue)
		raceResults <- raceErr
	}()
	go func() {
		<-raceStart
		_, raceErr := db.ExecContext(ctx, `insert into open_pilot_issue_states(id,repo,issue,phase,created_at,updated_at) values($1,'legacy/switchyard',$2,'requested',now(),now())`, newID("old-race"), raceIssue)
		raceResults <- raceErr
	}()
	close(raceStart)
	raceSucceeded := 0
	for range 2 {
		if err := <-raceResults; err == nil {
			raceSucceeded++
		}
	}
	if raceSucceeded != 1 {
		t.Fatalf("canonical/alias reservation race successes = %d", raceSucceeded)
	}

	adoptIssue := "810006"
	adoptRun, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "legacy/switchyard", "issue": adoptIssue}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: "legacy/switchyard", Issue: adoptIssue, RunID: adoptRun.ID, Phase: OpenPilotPhaseValidated}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `update runs set status=$2,claimed_by='adopter',attempt_count=attempt_count+1,claimed_at=clock_timestamp(),lease_expires_at=clock_timestamp()+interval '1 minute' where id=$1`, adoptRun.ID, RunRunning); err != nil {
		t.Fatal(err)
	}
	adoptDetail, err := store.RunDetail(adoptRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	adoptRun = adoptDetail.Run
	adoptRun, adoptedState, err := store.AdoptOpenPilotRunAffinity(adoptRun.ID, "adopter", adoptRun.AttemptCount, testOpenPilotAdmissionGate(t))
	if err != nil || adoptedState.Phase != OpenPilotPhaseValidated || adoptedState.RepositoryID != "switchyard" || adoptRun.Inputs[openPilotRepositoryIDInputKey] != "switchyard" {
		t.Fatalf("postgres affinity adoption run=%+v state=%+v error=%v", adoptRun, adoptedState, err)
	}
	for index, unsafeCase := range []string{"stale", "half", "collision"} {
		unsafeIssue := fmt.Sprintf("81001%d", index)
		unsafeRun, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "legacy/switchyard", "issue": unsafeIssue}})
		if err != nil {
			t.Fatal(err)
		}
		unsafeState, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: "legacy/switchyard", Issue: unsafeIssue, RunID: unsafeRun.ID, Phase: OpenPilotPhaseRequested})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `update runs set status=$2,claimed_by='adopter',attempt_count=attempt_count+1,claimed_at=clock_timestamp(),lease_expires_at=clock_timestamp()+interval '1 minute' where id=$1`, unsafeRun.ID, RunRunning); err != nil {
			t.Fatal(err)
		}
		unsafeDetail, err := store.RunDetail(unsafeRun.ID)
		if err != nil {
			t.Fatal(err)
		}
		unsafeRun = unsafeDetail.Run
		switch unsafeCase {
		case "stale":
			_, err = db.ExecContext(ctx, `update open_pilot_issue_states set run_id='stale-run' where id=$1`, unsafeState.ID)
		case "half":
			_, err = db.ExecContext(ctx, `update runs set inputs=jsonb_set(inputs,$2::text[],'"switchyard"'::jsonb) where id=$1`, unsafeRun.ID, []string{openPilotRepositoryIDInputKey})
		case "collision":
			_, err = db.ExecContext(ctx, `insert into open_pilot_issue_states(id,repo,issue,phase,created_at,updated_at) values($1,'Subcult/Switchyard',$2,'failed',now(),now())`, newID("adopt-collision"), unsafeIssue)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.AdoptOpenPilotRunAffinity(unsafeRun.ID, "adopter", unsafeRun.AttemptCount, testOpenPilotAdmissionGate(t)); !errors.Is(err, ErrConflict) {
			t.Fatalf("postgres unsafe %s adoption error = %v", unsafeCase, err)
		}
		pausedRun, _ := store.RunDetail(unsafeRun.ID)
		pausedState, _ := store.OpenPilotIssueState("legacy/switchyard", unsafeIssue)
		if pausedRun.Run.Status != RunPaused || pausedState.Phase != OpenPilotPhasePaused || pausedState.RecommendedNextAction != "manual_review" {
			t.Fatalf("postgres unsafe %s run=%+v state=%+v", unsafeCase, pausedRun.Run, pausedState)
		}
	}
	for _, test := range []struct{ issue, want string }{
		{issue: strings.Repeat("0", 256) + "1", want: "1"},
		{issue: strings.Repeat("0", 256) + "9223372036854775808", want: ""},
	} {
		repo := "owner/sql-normalization-" + newID("repo")
		if _, err := db.ExecContext(ctx, `insert into open_pilot_issue_states(id,repo,issue,phase,created_at,updated_at) values($1,$2,$3,'requested',now(),now())`, newID("opi"), repo, test.issue); err != nil {
			t.Fatal(err)
		}
		var canonical sql.NullString
		if err := db.QueryRowContext(ctx, `select canonical_issue from open_pilot_issue_states where repo=$1 and issue=$2`, repo, test.issue).Scan(&canonical); err != nil {
			t.Fatal(err)
		}
		if canonical.String != test.want || canonical.Valid != (test.want != "") {
			t.Fatalf("SQL canonical issue = %+v, want %q", canonical, test.want)
		}
	}
	lockRepo, lockIssue := "OWNER/POSTGRES-CANONICAL-LOCK", "900001"
	if _, err := db.ExecContext(ctx, `delete from open_pilot_issue_states where repo=$1 and issue=$2`, lockRepo, lockIssue); err != nil {
		t.Fatal(err)
	}
	target := OpenPilotTarget{RepositoryID: "postgres-lock", RepositorySlug: lockRepo, Issue: lockIssue, ExecutorGeneration: "generation-lock"}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, _, err := store.ClaimAndCreateOpenPilotInitialRun(lockRepo, lockIssue, CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": lockRepo, "issue": lockIssue}, OpenPilotTarget: &target})
			results <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	succeeded, blocked := 0, 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrConflict) || errors.Is(err, ErrAlreadyClaimed) {
			blocked++
		} else {
			t.Fatalf("concurrent canonical admission: %v", err)
		}
	}
	if succeeded != 1 || blocked != 1 {
		t.Fatalf("concurrent canonical admission succeeded=%d blocked=%d", succeeded, blocked)
	}
	resumeRepo, resumeIssue := "OWNER/POSTGRES-RESUME-"+newID("test"), "7"
	resumeTarget := OpenPilotTarget{RepositoryID: "postgres-resume", RepositorySlug: strings.ToLower(resumeRepo), Issue: resumeIssue, ExecutorGeneration: "generation-resume"}
	_, resumeRun, err := store.ClaimAndCreateOpenPilotInitialRun(resumeRepo, resumeIssue, CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": resumeRepo, "issue": resumeIssue}, OpenPilotTarget: &resumeTarget})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(resumeRun.ID, RunFailed, "failed"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `alter table open_pilot_issue_states disable trigger open_pilot_issue_states_identity_shadow`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `insert into open_pilot_issue_states(id,repo,issue,canonical_repo,canonical_issue,canonical_candidates,phase,created_at,updated_at) values($1,$2,$3,$2,$3,array[$2],'requested',now(),now())`, newID("opi"), strings.ToLower(resumeRepo), resumeIssue); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `alter table open_pilot_issue_states enable trigger open_pilot_issue_states_identity_shadow`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ClaimAndCreateOpenPilotResumeRun(resumeRepo, resumeIssue, trustedOpenPilotInput(resumeRepo, resumeIssue, CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": resumeRepo, "issue": resumeIssue}, OpenPilotTarget: &resumeTarget})); !errors.Is(err, ErrConflict) {
		t.Fatalf("postgres resume collision error = %v", err)
	}
	lookupIssue := fmt.Sprint(time.Now().UnixNano())
	legacyLookup, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: "legacy/switchyard", Issue: "0" + lookupIssue, Phase: OpenPilotPhaseRequested})
	if err != nil {
		t.Fatal(err)
	}
	lookupTarget := OpenPilotTarget{RepositoryID: "switchyard", RepositorySlug: "Subcult/Switchyard", RepositoryCandidates: []string{"legacy/switchyard", "Subcult/Switchyard"}, Issue: lookupIssue, ExecutorGeneration: "generation-one"}
	if resolved, err := store.OpenPilotIssueStateForTarget(lookupTarget); err != nil || resolved.ID != legacyLookup.ID {
		t.Fatalf("canonical Postgres lookup = %+v, %v", resolved, err)
	}
	if _, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: "subcult/switchyard", Issue: "00" + lookupIssue, Phase: OpenPilotPhaseRequested}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.OpenPilotIssueStateForTarget(lookupTarget); !errors.Is(err, ErrConflict) {
		t.Fatalf("ambiguous Postgres lookup error = %v", err)
	}
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
	var exists bool
	if err := db.QueryRowContext(ctx, `select exists (select 1 from information_schema.tables where table_name = 'workflow_definitions')`).Scan(&exists); err != nil {
		t.Fatalf("check workflow_definitions table: %v", err)
	}
	if !exists {
		t.Fatal("workflow_definitions table was not created")
	}
	store := NewPostgresStore(db, func() time.Time { return time.Unix(100, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	definition, err := store.UpsertWorkflowDefinition(WorkflowDefinition{ID: "daily-digest", Name: "Daily Digest", Description: "Generate a daily digest from a prompt.", Status: "active", Owns: []string{"digest drafts"}, Uses: []string{"llama-line"}, InputShape: JSONShape{Type: "object", Properties: map[string]JSONShapeProperty{"topic": {Type: "string"}}, Required: []string{"topic"}}, Pipeline: PipelineDefinition{ID: "daily-digest-pipeline", WorkflowID: "daily-digest", Name: "Daily Digest Pipeline", Steps: []PipelineStepDefinition{{ID: "generate", Position: 1, Name: "digest.generate", Executor: "model", Profile: "content-writer", Model: "${model}", Inputs: map[string]string{"template": "Write about {{topic}}"}}}}})
	if err != nil {
		t.Fatalf("UpsertWorkflowDefinition() error = %v", err)
	}
	if definition.Pipeline.Steps[0].Executor != "model" || definition.InputShape.Properties["topic"].Type != "string" {
		t.Fatalf("definition = %+v", definition)
	}
	fetchedDefinition, err := store.WorkflowDefinition("daily-digest")
	if err != nil {
		t.Fatalf("WorkflowDefinition() error = %v", err)
	}
	if fetchedDefinition.ID != definition.ID || fetchedDefinition.Pipeline.ID != "daily-digest-pipeline" {
		t.Fatalf("fetchedDefinition = %+v", fetchedDefinition)
	}
	definitions, err := store.ListWorkflowDefinitions()
	if err != nil {
		t.Fatalf("ListWorkflowDefinitions() error = %v", err)
	}
	if len(definitions) < 1 {
		t.Fatalf("definitions = %+v", definitions)
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
	if detail.Run.Status != RunPaused || len(detail.Events) == 0 || len(detail.Steps) != 4 || len(detail.Artifacts) != 2 {
		t.Fatalf("detail = %+v", detail)
	}
	if detail.Artifacts[0].StorageClass != "inline-data-uri" || detail.Artifacts[0].ByteSize == 0 || detail.Artifacts[0].SHA256 == "" || detail.Artifacts[0].RetentionUntil == nil {
		t.Fatalf("artifact lifecycle metadata = %+v", detail.Artifacts[0])
	}
	report, err := store.ArtifactReport(ctx, ArtifactReportInput{Start: utcDay(time.Unix(100, 0)), End: time.Unix(100, 0).UTC()})
	if err != nil {
		t.Fatalf("ArtifactReport() error = %v", err)
	}
	if report.Retained < int64(len(detail.Artifacts)) || report.CreatedTotal < int64(len(detail.Artifacts)) || len(report.Buckets) != 1 || report.Buckets[0].Created < int64(len(detail.Artifacts)) {
		t.Fatalf("artifact report = %+v", report)
	}
	if err := store.RecordCompatibilityUsage(ctx, "ocq", time.Unix(100, 0)); err != nil {
		t.Fatalf("RecordCompatibilityUsage() error = %v", err)
	}
	retirement, err := store.CompatibilityRetirementStatus(ctx, time.Unix(100, 0))
	if err != nil || len(retirement.Backends) != 2 || retirement.Backends[1].Backend != "ocq" || retirement.Backends[1].Attempts90D != 1 {
		t.Fatalf("retirement = %+v err=%v", retirement, err)
	}
	snapshot, err := store.OperatorRunSnapshot(ctx, time.Unix(100, 0), 5)
	if err != nil || len(snapshot.Recent) == 0 {
		t.Fatalf("snapshot = %+v err=%v", snapshot, err)
	}
}
