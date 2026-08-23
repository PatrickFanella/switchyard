package controlplane

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"
)

func TestPostgresStoreRetentionSkipsConcurrentTerminalTransitionIntegration(t *testing.T) {
	store, db, workflowID := newPostgresArtifactIntegrationStore(t, time.Date(2038, 1, 2, 12, 0, 0, 0, time.UTC))
	run, err := store.CreateRun(CreateRunInput{WorkflowID: workflowID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddArtifact(CreateArtifactInput{RunID: run.ID, Name: "transition-race", Kind: "text", URI: "data:text/plain,race"}); err != nil {
		t.Fatal(err)
	}

	transition, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transition.ExecContext(t.Context(), `update runs set status=$2, finished_at=$3 where id=$1`, run.ID, RunSucceeded, store.now()); err != nil {
		_ = transition.Rollback()
		t.Fatal(err)
	}
	pruned := make(chan retentionCall, 1)
	go func() {
		result, err := store.PruneRetention(store.now().Add(time.Hour))
		pruned <- retentionCall{result: result, err: err}
	}()
	first := awaitUnlockedRetention(t, pruned, transition)
	if first.err != nil {
		t.Fatal(first.err)
	}
	if err := transition.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RunDetail(run.ID); err != nil {
		t.Fatalf("concurrently transitioned run was pruned: %v", err)
	}

	second, err := store.PruneRetention(store.now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if second.Artifacts != 1 || second.ArtifactBytes != 4 {
		t.Fatalf("second prune = %+v", second)
	}
	if _, err := store.RunDetail(run.ID); err != ErrNotFound {
		t.Fatalf("run after second prune error = %v", err)
	}
}

func TestPostgresStoreRetentionSkipsConcurrentArtifactCreationIntegration(t *testing.T) {
	now := time.Date(2038, 2, 2, 12, 0, 0, 0, time.UTC)
	store, db, workflowID := newPostgresArtifactIntegrationStore(t, now)
	run, err := store.CreateRun(CreateRunInput{WorkflowID: workflowID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(run.ID, RunSucceeded, "done"); err != nil {
		t.Fatal(err)
	}

	creation, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	artifactID := newID("artifact")
	if _, err := creation.ExecContext(t.Context(), `insert into artifacts(id,run_id,name,kind,uri,created_at,storage_class,content_type,byte_size,sha256) values($1,$2,'creation-race','text','data:text/plain,race',$3,'inline-data-uri','text/plain',4,'test')`, artifactID, run.ID, now); err != nil {
		_ = creation.Rollback()
		t.Fatal(err)
	}
	if _, err := creation.ExecContext(t.Context(), `update artifact_lifecycle_totals set created_count=created_count+1, created_bytes=created_bytes+4 where singleton=true`); err != nil {
		_ = creation.Rollback()
		t.Fatal(err)
	}
	if _, err := creation.ExecContext(t.Context(), `insert into artifact_lifecycle_daily(measurement_date,created_count,created_bytes) values($1::date,1,4) on conflict (measurement_date) do update set created_count=artifact_lifecycle_daily.created_count+1, created_bytes=artifact_lifecycle_daily.created_bytes+4`, now.Format(time.DateOnly)); err != nil {
		_ = creation.Rollback()
		t.Fatal(err)
	}
	pruned := make(chan retentionCall, 1)
	go func() {
		result, err := store.PruneRetention(now.Add(time.Hour))
		pruned <- retentionCall{result: result, err: err}
	}()
	first := awaitUnlockedRetention(t, pruned, creation)
	if first.err != nil {
		t.Fatal(first.err)
	}
	if err := creation.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RunDetail(run.ID); err != nil {
		t.Fatalf("run with concurrent artifact creation was pruned: %v", err)
	}
	var retained int
	if err := db.QueryRowContext(t.Context(), `select count(*) from artifacts where id=$1`, artifactID).Scan(&retained); err != nil || retained != 1 {
		t.Fatalf("retained artifact count = %d, err = %v", retained, err)
	}

	second, err := store.PruneRetention(now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if second.Artifacts != 1 || second.ArtifactBytes != 4 {
		t.Fatalf("second prune = %+v", second)
	}
}

func TestPostgresStoreArtifactReportUsesUTCDatesIntegration(t *testing.T) {
	now := time.Date(2040, 8, 20, 0, 30, 0, 0, time.UTC)
	store, db, workflowID := newPostgresArtifactIntegrationStore(t, now)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.ExecContext(t.Context(), `set time zone 'America/Los_Angeles'`); err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: workflowID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddArtifact(CreateArtifactInput{RunID: run.ID, Name: "utc-date", Kind: "text", URI: "data:text/plain,utc"}); err != nil {
		t.Fatal(err)
	}
	report, err := store.ArtifactReport(t.Context(), ArtifactReportInput{Start: utcDay(now), End: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Buckets) != 1 || report.Buckets[0].Date != "2040-08-20" || report.Buckets[0].Created != 1 || report.Buckets[0].CreatedBytes != 3 {
		t.Fatalf("non-UTC session buckets = %+v", report.Buckets)
	}
}

type retentionCall struct {
	result RetentionResult
	err    error
}

func awaitUnlockedRetention(t *testing.T, result <-chan retentionCall, blocker *sql.Tx) retentionCall {
	t.Helper()
	select {
	case call := <-result:
		return call
	case <-time.After(2 * time.Second):
		_ = blocker.Rollback()
		select {
		case call := <-result:
			t.Fatalf("retention blocked on a concurrent writer: result=%+v err=%v", call.result, call.err)
		case <-time.After(2 * time.Second):
			t.Fatal("retention remained blocked after the concurrent writer rolled back")
		}
		return retentionCall{}
	}
}

func newPostgresArtifactIntegrationStore(t *testing.T, now time.Time) (*PostgresStore, *sql.DB, string) {
	t.Helper()
	databaseURL := os.Getenv("SWITCHYARD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set SWITCHYARD_TEST_DATABASE_URL to run PostgreSQL artifact integration tests")
	}
	if os.Getenv("SWITCHYARD_TEST_POSTGRES_ISOLATED") != "1" {
		t.Skip("run PostgreSQL artifact integration tests through make postgres-test")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	db, err := OpenPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	workflowID := newID("artifact-race")
	store := NewPostgresStore(db, func() time.Time { return now })
	if err := store.UpsertWorkflow(Workflow{ID: workflowID, Kind: WorkflowDevlog, Name: "Artifact integration", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = db.ExecContext(cleanupCtx, `delete from workflows where id=$1`, workflowID)
	})
	return store, db, workflowID
}
