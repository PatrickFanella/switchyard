package controlplane

import (
	"context"
	"database/sql"
	"os"
	"sync"
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

func TestPostgresStoreArtifactTriggersAccountEachInsertAndDeleteOnceIntegration(t *testing.T) {
	now := time.Now().UTC()
	store, db, workflowID := newPostgresArtifactIntegrationStore(t, now)
	run, err := store.CreateRun(CreateRunInput{WorkflowID: workflowID})
	if err != nil {
		t.Fatal(err)
	}
	before := readArtifactLifecycleAccounting(t, db, now)
	inline, err := store.AddArtifact(CreateArtifactInput{RunID: run.ID, Name: "measured", Kind: "text", URI: "data:text/plain,hello"})
	if err != nil {
		t.Fatal(err)
	}
	unmeasuredID := newID("artifact")
	unmeasuredTx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enableArtifactTriggerLifecycleAccounting(t.Context(), unmeasuredTx); err != nil {
		_ = unmeasuredTx.Rollback()
		t.Fatal(err)
	}
	if _, err := unmeasuredTx.ExecContext(t.Context(), `insert into artifacts(id,run_id,name,kind,uri,created_at,storage_class,content_type,byte_size,sha256) values($1,$2,'unmeasured','link','https://example.invalid/evidence',$3,'legacy-inline','',17,'test')`, unmeasuredID, run.ID, now); err != nil {
		_ = unmeasuredTx.Rollback()
		t.Fatal(err)
	}
	if err := unmeasuredTx.Commit(); err != nil {
		t.Fatal(err)
	}
	afterInsert := readArtifactLifecycleAccounting(t, db, now)
	wantInsert := before.add(artifactLifecycleAccounting{retained: 2, retainedBytes: 5, retainedUnmeasured: 1, created: 2, createdBytes: 5, dailyCreated: 2, dailyCreatedBytes: 5})
	if afterInsert != wantInsert {
		t.Fatalf("accounting after inserts = %+v, want %+v", afterInsert, wantInsert)
	}
	deleteTx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enableArtifactTriggerLifecycleAccounting(t.Context(), deleteTx); err != nil {
		_ = deleteTx.Rollback()
		t.Fatal(err)
	}
	if _, err := deleteTx.ExecContext(t.Context(), `delete from artifacts where id in ($1,$2)`, inline.ID, unmeasuredID); err != nil {
		_ = deleteTx.Rollback()
		t.Fatal(err)
	}
	if err := deleteTx.Commit(); err != nil {
		t.Fatal(err)
	}
	afterDelete := readArtifactLifecycleAccounting(t, db, now)
	wantDelete := before.add(artifactLifecycleAccounting{created: 2, createdBytes: 5, pruned: 2, prunedBytes: 5, prunedUnmeasured: 1, dailyCreated: 2, dailyCreatedBytes: 5, dailyPruned: 2, dailyPrunedBytes: 5, dailyPrunedUnmeasured: 1})
	if afterDelete != wantDelete {
		t.Fatalf("accounting after deletes = %+v, want %+v", afterDelete, wantDelete)
	}
}

func TestPostgresStoreArtifactAccountingSupportsConcurrentOldAndNewWritersIntegration(t *testing.T) {
	now := time.Now().UTC()
	store, db, workflowID := newPostgresArtifactIntegrationStore(t, now)
	run, err := store.CreateRun(CreateRunInput{WorkflowID: workflowID})
	if err != nil {
		t.Fatal(err)
	}
	before := readArtifactLifecycleAccounting(t, db, now)
	oldID := newID("old-artifact")
	start := make(chan struct{})
	errors := make(chan error, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		errors <- oldWriterAddArtifact(t.Context(), db, run.ID, oldID, now, "old")
	}()
	go func() {
		defer wait.Done()
		<-start
		_, err := store.AddArtifact(CreateArtifactInput{RunID: run.ID, Name: "new", Kind: "text", URI: "data:text/plain,new"})
		errors <- err
	}()
	close(start)
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	after := readArtifactLifecycleAccounting(t, db, now)
	want := before.add(artifactLifecycleAccounting{retained: 2, retainedBytes: 6, created: 2, createdBytes: 6, dailyCreated: 2, dailyCreatedBytes: 6})
	if after != want {
		t.Fatalf("mixed writer accounting = %+v, want %+v", after, want)
	}
	oldPrune, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := oldPrune.ExecContext(t.Context(), `delete from artifacts where id=$1`, oldID); err != nil {
		t.Fatal(err)
	}
	if _, err := oldPrune.ExecContext(t.Context(), `update artifact_lifecycle_totals set pruned_count=pruned_count+1,pruned_bytes=pruned_bytes+3 where singleton=true`); err != nil {
		t.Fatal(err)
	}
	if _, err := oldPrune.ExecContext(t.Context(), `insert into artifact_lifecycle_daily(measurement_date,pruned_count,pruned_bytes) values(($1::timestamptz at time zone 'UTC')::date,1,3) on conflict(measurement_date) do update set pruned_count=artifact_lifecycle_daily.pruned_count+1,pruned_bytes=artifact_lifecycle_daily.pruned_bytes+3`, now); err != nil {
		t.Fatal(err)
	}
	if err := oldPrune.Commit(); err != nil {
		t.Fatal(err)
	}
	want = want.add(artifactLifecycleAccounting{retained: -1, retainedBytes: -3, pruned: 1, prunedBytes: 3, dailyPruned: 1, dailyPrunedBytes: 3})
	if got := readArtifactLifecycleAccounting(t, db, now); got != want {
		t.Fatalf("old prune accounting = %+v, want %+v", got, want)
	}

	rollback, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rollback.ExecContext(t.Context(), `insert into artifacts(id,run_id,name,kind,uri,created_at,storage_class,content_type,byte_size,sha256) values($1,$2,'rollback','text','data:text/plain,no',$3,'inline-data-uri','text/plain',2,'test')`, newID("old-rollback"), run.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := rollback.ExecContext(t.Context(), `update artifact_lifecycle_totals set created_count=created_count+1,created_bytes=created_bytes+2 where singleton=true`); err != nil {
		t.Fatal(err)
	}
	if err := rollback.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := readArtifactLifecycleAccounting(t, db, now); got != want {
		t.Fatalf("old writer rollback accounting = %+v, want %+v", got, want)
	}
}

func oldWriterAddArtifact(ctx context.Context, db *sql.DB, runID, artifactID string, createdAt time.Time, contents string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `insert into artifacts(id,run_id,name,kind,uri,created_at,storage_class,content_type,byte_size,sha256) values($1,$2,'old','text',$3,$4,'inline-data-uri','text/plain',$5,'test')`, artifactID, runID, "data:text/plain,"+contents, createdAt, len(contents)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update artifact_lifecycle_totals set created_count=created_count+1,created_bytes=created_bytes+$1 where singleton=true`, len(contents)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `insert into artifact_lifecycle_daily(measurement_date,created_count,created_bytes) values(($1::timestamptz at time zone 'UTC')::date,1,$2) on conflict(measurement_date) do update set created_count=artifact_lifecycle_daily.created_count+1,created_bytes=artifact_lifecycle_daily.created_bytes+excluded.created_bytes`, createdAt, len(contents)); err != nil {
		return err
	}
	return tx.Commit()
}

type artifactLifecycleAccounting struct {
	retained, retainedBytes, retainedUnmeasured int64
	created, createdBytes                       int64
	pruned, prunedBytes, prunedUnmeasured       int64
	dailyCreated, dailyCreatedBytes             int64
	dailyPruned, dailyPrunedBytes               int64
	dailyPrunedUnmeasured                       int64
}

func (a artifactLifecycleAccounting) add(b artifactLifecycleAccounting) artifactLifecycleAccounting {
	return artifactLifecycleAccounting{
		retained: a.retained + b.retained, retainedBytes: a.retainedBytes + b.retainedBytes, retainedUnmeasured: a.retainedUnmeasured + b.retainedUnmeasured,
		created: a.created + b.created, createdBytes: a.createdBytes + b.createdBytes,
		pruned: a.pruned + b.pruned, prunedBytes: a.prunedBytes + b.prunedBytes, prunedUnmeasured: a.prunedUnmeasured + b.prunedUnmeasured,
		dailyCreated: a.dailyCreated + b.dailyCreated, dailyCreatedBytes: a.dailyCreatedBytes + b.dailyCreatedBytes,
		dailyPruned: a.dailyPruned + b.dailyPruned, dailyPrunedBytes: a.dailyPrunedBytes + b.dailyPrunedBytes, dailyPrunedUnmeasured: a.dailyPrunedUnmeasured + b.dailyPrunedUnmeasured,
	}
}

func readArtifactLifecycleAccounting(t *testing.T, db *sql.DB, day time.Time) artifactLifecycleAccounting {
	t.Helper()
	var got artifactLifecycleAccounting
	if err := db.QueryRowContext(t.Context(), `select retained_count,retained_bytes,retained_bytes_unmeasured,created_count,created_bytes,pruned_count,pruned_bytes,pruned_bytes_unmeasured from artifact_lifecycle_totals where singleton=true`).Scan(&got.retained, &got.retainedBytes, &got.retainedUnmeasured, &got.created, &got.createdBytes, &got.pruned, &got.prunedBytes, &got.prunedUnmeasured); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(t.Context(), `select coalesce(created_count,0),coalesce(created_bytes,0),coalesce(pruned_count,0),coalesce(pruned_bytes,0),coalesce(pruned_bytes_unmeasured,0) from artifact_lifecycle_daily where measurement_date=$1::date`, day.Format(time.DateOnly)).Scan(&got.dailyCreated, &got.dailyCreatedBytes, &got.dailyPruned, &got.dailyPrunedBytes, &got.dailyPrunedUnmeasured); err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	return got
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
