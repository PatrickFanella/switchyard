package controlplane

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestPostgresRunAttemptFenceRejectsReclaimedAttemptFromSameWorker(t *testing.T) {
	databaseURL := os.Getenv("SWITCHYARD_TEST_DATABASE_URL")
	if databaseURL == "" || os.Getenv("SWITCHYARD_TEST_POSTGRES_ISOLATED") != "1" {
		t.Skip("run make postgres-test with an isolated PostgreSQL database")
	}
	db, err := OpenPostgres(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	store := NewPostgresStore(db, time.Now)
	resetIsolatedPostgresRuns(t, db)
	workflowID := newID("fence-workflow")
	if err := store.UpsertWorkflow(Workflow{ID: workflowID, Kind: WorkflowDevlog, Name: "Fence test", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `delete from workflows where id=$1`, workflowID)
	})
	run, err := store.CreateRun(CreateRunInput{WorkflowID: workflowID, MaxAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.ClaimNextRun("worker-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	attemptStore := store.WithContext(withRunAttemptFence(t.Context(), RunAttemptFence{RunID: run.ID, WorkerID: "worker-1", Attempt: first.AttemptCount}))
	step, err := attemptStore.CreateRunStep(CreateRunStepInput{RunID: run.ID, Position: 1, Name: "step", Executor: "model"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `update runs set lease_expires_at=clock_timestamp()-interval '1 second' where id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	if requeued, failed, err := store.RecoverLeasedRuns(time.Now().UTC()); err != nil || requeued != 1 || failed != 0 {
		t.Fatalf("RecoverLeasedRuns() = %d, %d, %v", requeued, failed, err)
	}
	second, err := store.ClaimNextRun("worker-1", time.Minute)
	if err != nil || second.AttemptCount != first.AttemptCount+1 {
		t.Fatalf("ClaimNextRun() = %+v, %v", second, err)
	}
	before, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	metricsBefore, err := store.RuntimeMetrics()
	if err != nil {
		t.Fatal(err)
	}
	mutations := []func() error{
		func() error { _, err := attemptStore.FinishRun(run.ID, RunSucceeded, "stale"); return err },
		func() error {
			_, err := attemptStore.CreateRunStep(CreateRunStepInput{RunID: run.ID, Position: 2, Name: "stale", Executor: "model"})
			return err
		},
		func() error { _, err := attemptStore.StartRunStep(step.ID); return err },
		func() error {
			_, err := attemptStore.UpdateRunStep(step.ID, UpdateRunStepInput{Inputs: map[string]string{"stale": "true"}})
			return err
		},
		func() error {
			_, err := attemptStore.FinishRunStep(step.ID, StepFailed, "stale", "stale", nil)
			return err
		},
		func() error {
			_, err := attemptStore.MergeRunStepCorrelationIDs(step.ID, map[string]string{"stale": "true"})
			return err
		},
		func() error {
			return attemptStore.RecordRunStepBackendDecision(run.ID, step.ID, BackendDecision{Backend: "llama-line", DeprecatedBackend: "llama-line"})
		},
		func() error { _, err := attemptStore.RetryRunStep(step.ID); return err },
		func() error { _, err := attemptStore.RecoverRunStep(step.ID); return err },
		func() error { _, err := attemptStore.CancelRunStep(step.ID); return err },
		func() error { _, err := attemptStore.AddEvent(run.ID, "stale", "stale"); return err },
		func() error {
			_, err := attemptStore.AddArtifact(CreateArtifactInput{RunID: run.ID, Name: "stale", Kind: "text", URI: "data:text/plain,stale"})
			return err
		},
	}
	for i, mutation := range mutations {
		if err := mutation(); !errors.Is(err, ErrStaleRunAttempt) || !errors.Is(err, ErrConflict) {
			t.Fatalf("mutation %d error = %v", i, err)
		}
	}
	after, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	metricsAfter, err := store.RuntimeMetrics()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(metricsBefore, metricsAfter) {
		t.Fatalf("stale mutations changed durable state")
	}
}
