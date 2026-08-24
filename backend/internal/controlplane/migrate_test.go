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
