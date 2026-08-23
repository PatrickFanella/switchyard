package controlplane_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/PatrickFanella/switchyard/internal/controlplane"
)

type lifecycleStoreFactory struct {
	name string
	new  func(*testing.T, *time.Time) controlplane.Store
}

func TestStoreLifecycleContract(t *testing.T) {
	for _, factory := range lifecycleStoreFactories() {
		t.Run(factory.name, func(t *testing.T) {
			t.Run("model_cancel_audit_is_first_write_wins", func(t *testing.T) {
				now := time.Unix(1_000, 0).UTC()
				store := factory.new(t, &now)
				req, _, err := store.BeginModelBrokerRequest(controlplane.CreateModelBrokerRequestInput{ID: "audit-first", Model: "test"})
				if err != nil {
					t.Fatal(err)
				}
				req, err = store.TransitionModelBrokerRequest(req.ID, controlplane.TransitionModelBrokerRequestInput{ExpectedStatus: controlplane.ModelBrokerRequestRunning, Status: controlplane.ModelBrokerRequestCancelRequested, CorrelationIDs: map[string]string{"audit_action": "cancel", "audit_actor": "first", "audit_reason": "first reason"}})
				if err != nil {
					t.Fatal(err)
				}
				firstRequestedAt := *req.CancelRequestedAt
				now = now.Add(time.Hour)
				rows, err := store.RequestCancelModelBrokerRequests(map[string]string{"audit_action": "cancel_all", "audit_actor": "second", "audit_reason": "second reason"})
				if err != nil || len(rows) != 0 {
					t.Fatalf("recancel rows=%d err=%v", len(rows), err)
				}
				stored, err := store.ModelBrokerRequest(req.ID)
				if err != nil || stored.CorrelationIDs["audit_action"] != "cancel" || stored.CorrelationIDs["audit_actor"] != "first" || stored.CorrelationIDs["audit_reason"] != "first reason" || stored.CancelRequestedAt == nil || !stored.CancelRequestedAt.Equal(firstRequestedAt) {
					t.Fatalf("stored=%+v err=%v", stored, err)
				}
				partial, _, err := store.BeginModelBrokerRequest(controlplane.CreateModelBrokerRequestInput{ID: "audit-partial", Model: "test"})
				if err != nil {
					t.Fatal(err)
				}
				partial, err = store.TransitionModelBrokerRequest(partial.ID, controlplane.TransitionModelBrokerRequestInput{ExpectedStatus: controlplane.ModelBrokerRequestRunning, Status: controlplane.ModelBrokerRequestCancelRequested, CorrelationIDs: map[string]string{"audit_action": "cancel"}})
				if err != nil {
					t.Fatal(err)
				}
				partial, err = store.MergeModelBrokerRequestCorrelationIDs(partial.ID, map[string]string{"audit_action": "drop", "audit_actor": "later", "audit_reason": "later reason"})
				if err != nil || partial.CorrelationIDs["audit_action"] != "cancel" || partial.CorrelationIDs["audit_actor"] != "" || partial.CorrelationIDs["audit_reason"] != "" {
					t.Fatalf("partial audit tuple mixed: %+v err=%v", partial.CorrelationIDs, err)
				}
			})
			t.Run("failed_attempt_requeues_after_delay_and_completes", func(t *testing.T) {
				now := time.Unix(1_000, 0).UTC()
				store := factory.new(t, &now)
				run := lifecycleContractRun(t, store, 2)

				claimed, err := store.ClaimNextRun("worker-one", time.Minute)
				if err != nil {
					t.Fatalf("ClaimNextRun() error = %v", err)
				}
				if claimed.ID != run.ID || claimed.Status != controlplane.RunRunning || claimed.AttemptCount != 1 {
					t.Fatalf("first claim = %+v", claimed)
				}
				if _, err := store.FinishClaimedRun(run.ID, "worker-one", controlplane.RunFailed, "transient failure"); err != nil {
					t.Fatalf("FinishClaimedRun(failed) error = %v", err)
				}

				availableAt := now.Add(time.Minute)
				requeued, err := store.RequeueFailedRun(run.ID, availableAt, "retry scheduled")
				if err != nil {
					t.Fatalf("RequeueFailedRun() error = %v", err)
				}
				if requeued.Status != controlplane.RunQueued || !requeued.AvailableAt.Equal(availableAt) {
					t.Fatalf("requeued = %+v", requeued)
				}
				if _, err := store.ClaimNextRun("worker-two", time.Minute); err != controlplane.ErrNotFound {
					t.Fatalf("ClaimNextRun() before available time error = %v, want ErrNotFound", err)
				}

				now = availableAt
				claimed, err = store.ClaimNextRun("worker-two", time.Minute)
				if err != nil {
					t.Fatalf("ClaimNextRun() after available time error = %v", err)
				}
				if claimed.AttemptCount != 2 || claimed.ClaimedBy != "worker-two" {
					t.Fatalf("second claim = %+v", claimed)
				}
				finished, err := store.FinishClaimedRun(run.ID, "worker-two", controlplane.RunSucceeded, "completed")
				if err != nil {
					t.Fatalf("FinishClaimedRun(succeeded) error = %v", err)
				}
				if finished.Status != controlplane.RunSucceeded || finished.AttemptCount != 2 || finished.ClaimedBy != "" || finished.LeaseExpiresAt != nil || finished.FinishedAt == nil {
					t.Fatalf("finished = %+v", finished)
				}
			})

			t.Run("queued_cancellation_prevents_claim", func(t *testing.T) {
				now := time.Unix(2_000, 0).UTC()
				store := factory.new(t, &now)
				run := lifecycleContractRun(t, store, 1)

				canceled, err := store.FinishRun(run.ID, controlplane.RunCanceled, "Canceled by operator")
				if err != nil {
					t.Fatalf("FinishRun(canceled) error = %v", err)
				}
				if canceled.Status != controlplane.RunCanceled || canceled.FinishedAt == nil {
					t.Fatalf("canceled = %+v", canceled)
				}
				if _, err := store.ClaimNextRun("worker", time.Minute); err != controlplane.ErrNotFound {
					t.Fatalf("ClaimNextRun() after cancellation error = %v, want ErrNotFound", err)
				}
			})

			t.Run("correlation_updates_merge_without_erasure", func(t *testing.T) {
				now := time.Unix(5_000, 0).UTC()
				store := factory.new(t, &now)
				req, err := store.CreateModelBrokerRequest(controlplane.CreateModelBrokerRequestInput{ID: "correlation-" + factory.name, Model: "test", RequestBody: `{}`})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.MergeModelBrokerRequestCorrelationIDs(req.ID, map[string]string{"request_id": "initial"}); err != nil {
					t.Fatal(err)
				}
				finished, err := store.FinishModelBrokerRequest(req.ID, controlplane.FinishModelBrokerRequestInput{CorrelationIDs: map[string]string{"ocq_request_id": "attempt"}})
				if err != nil || finished.CorrelationIDs["request_id"] != "initial" || finished.CorrelationIDs["ocq_request_id"] != "attempt" {
					t.Fatalf("request=%+v err=%v", finished, err)
				}
			})

			t.Run("backend_decision_is_atomic_and_recovery_retains_correlations", func(t *testing.T) {
				now := time.Unix(5_500, 0).UTC()
				store := factory.new(t, &now)
				run := lifecycleContractRun(t, store, 2)
				step, err := store.CreateRunStep(controlplane.CreateRunStepInput{RunID: run.ID, Position: 1, Name: "deprecated generation", Executor: "model", CorrelationIDs: map[string]string{"request_id": "preserved"}})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.StartRunStep(step.ID); err != nil {
					t.Fatal(err)
				}
				decision := controlplane.BackendDecision{Backend: "llama-line", DeprecatedBackend: "llama-line", CompatibilityWarning: "deprecated-external-backend"}
				if err := store.RecordRunStepBackendDecision(run.ID, step.ID, decision); err != nil {
					t.Fatal(err)
				}
				if err := store.RecordRunStepBackendDecision(run.ID, "missing", decision); err == nil {
					t.Fatal("missing step decision unexpectedly succeeded")
				}
				recovered, err := store.RecoverRunStep(step.ID)
				if err != nil || recovered.Status != controlplane.StepQueued {
					t.Fatalf("RecoverRunStep() = %+v, %v", recovered, err)
				}
				if recovered.CorrelationIDs["request_id"] != "preserved" || recovered.CorrelationIDs[controlplane.CorrelationDeprecatedBackend] != "llama-line" {
					t.Fatalf("recovered correlations=%v", recovered.CorrelationIDs)
				}
				if _, err := store.StartRunStep(step.ID); err != nil {
					t.Fatal(err)
				}
				finished, err := store.FinishRunStep(step.ID, controlplane.StepSucceeded, "", "", nil)
				if err != nil || finished.CorrelationIDs["request_id"] != "preserved" || finished.CorrelationIDs[controlplane.CorrelationBackend] != "llama-line" {
					t.Fatalf("terminal nil merge lost correlations: %+v, %v", finished, err)
				}
				detail, err := store.RunDetail(run.ID)
				if err != nil {
					t.Fatal(err)
				}
				count := 0
				for _, event := range detail.Events {
					if event.Type == controlplane.DeprecatedBackendAttemptedEvent {
						count++
					}
				}
				if count != 1 {
					t.Fatalf("deprecated decision events=%d, want 1", count)
				}
			})

			t.Run("competing_claims_assign_a_run_once", func(t *testing.T) {
				now := time.Unix(2_500, 0).UTC()
				store := factory.new(t, &now)
				run := lifecycleContractRun(t, store, 1)

				start := make(chan struct{})
				results := make(chan lifecycleClaimResult, 2)
				var claims sync.WaitGroup
				for _, workerID := range []string{"worker-one", "worker-two"} {
					claims.Add(1)
					go func(workerID string) {
						defer claims.Done()
						<-start
						claimed, err := store.ClaimNextRun(workerID, time.Minute)
						results <- lifecycleClaimResult{run: claimed, err: err}
					}(workerID)
				}
				close(start)
				claims.Wait()
				close(results)

				claimedCount := 0
				for result := range results {
					if result.err == nil {
						if result.run.ID != run.ID || result.run.Status != controlplane.RunRunning || result.run.AttemptCount != 1 {
							t.Fatalf("claimed run = %+v", result.run)
						}
						claimedCount++
						continue
					}
					if result.err != controlplane.ErrNotFound {
						t.Fatalf("competing ClaimNextRun() error = %v, want ErrNotFound", result.err)
					}
				}
				if claimedCount != 1 {
					t.Fatalf("successful competing claims = %d, want 1", claimedCount)
				}
			})

			t.Run("terminal_claim_failure_closes_running_steps", func(t *testing.T) {
				now := time.Unix(2_750, 0).UTC()
				store := factory.new(t, &now)
				run := lifecycleContractRun(t, store, 1)
				if _, err := store.ClaimNextRun("panic-worker", time.Minute); err != nil {
					t.Fatal(err)
				}
				step, err := store.CreateRunStep(controlplane.CreateRunStepInput{RunID: run.ID, Position: 1, Name: "panic-owned", Executor: "model", CorrelationIDs: map[string]string{controlplane.CorrelationDeprecatedBackend: "llama-line"}})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.StartRunStep(step.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := store.FinishClaimedRun(run.ID, "panic-worker", controlplane.RunFailed, "worker panic: boom"); err != nil {
					t.Fatal(err)
				}
				detail, err := store.RunDetail(run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if len(detail.Steps) != 1 || detail.Steps[0].Status != controlplane.StepFailed || detail.Steps[0].Error != "worker panic: boom" || detail.Steps[0].CorrelationIDs[controlplane.CorrelationDeprecatedBackend] != "llama-line" {
					t.Fatalf("terminal panic steps = %+v", detail.Steps)
				}
			})

			t.Run("expired_lease_requeues_then_fails_when_attempts_exhausted", func(t *testing.T) {
				now := time.Unix(3_000, 0).UTC()
				store := factory.new(t, &now)
				run := lifecycleContractRun(t, store, 2)

				if _, err := store.ClaimNextRun("worker-one", time.Minute); err != nil {
					t.Fatalf("first ClaimNextRun() error = %v", err)
				}
				now = now.Add(2 * time.Minute)
				requeued, failed, err := store.RecoverLeasedRuns(now)
				if err != nil {
					t.Fatalf("first RecoverLeasedRuns() error = %v", err)
				}
				if requeued != 1 || failed != 0 {
					t.Fatalf("first recovery requeued=%d failed=%d", requeued, failed)
				}

				claimed, err := store.ClaimNextRun("worker-two", time.Minute)
				if err != nil {
					t.Fatalf("second ClaimNextRun() error = %v", err)
				}
				if claimed.AttemptCount != 2 {
					t.Fatalf("second claim = %+v", claimed)
				}
				step, err := store.CreateRunStep(controlplane.CreateRunStepInput{RunID: run.ID, Position: 1, Name: "lease-owned", Executor: "model", CorrelationIDs: map[string]string{controlplane.CorrelationDeprecatedBackend: "llama-line"}})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.StartRunStep(step.ID); err != nil {
					t.Fatal(err)
				}
				now = now.Add(2 * time.Minute)
				requeued, failed, err = store.RecoverLeasedRuns(now)
				if err != nil {
					t.Fatalf("second RecoverLeasedRuns() error = %v", err)
				}
				if requeued != 0 || failed != 1 {
					t.Fatalf("second recovery requeued=%d failed=%d", requeued, failed)
				}
				detail, err := store.RunDetail(run.ID)
				if err != nil {
					t.Fatalf("RunDetail() error = %v", err)
				}
				if detail.Run.Status != controlplane.RunFailed || detail.Run.Summary != "Worker lease expired" || detail.Run.ClaimedBy != "" || detail.Run.LeaseExpiresAt != nil {
					t.Fatalf("recovered run = %+v", detail.Run)
				}
				if len(detail.Steps) != 1 || detail.Steps[0].Status != controlplane.StepFailed || detail.Steps[0].Error != "Worker lease expired" || detail.Steps[0].CorrelationIDs[controlplane.CorrelationDeprecatedBackend] != "llama-line" {
					t.Fatalf("terminal recovered steps = %+v", detail.Steps)
				}
			})

			t.Run("model_admission_and_cancel_timestamps_match", func(t *testing.T) {
				now := time.Unix(8_000, 0).UTC()
				store := factory.new(t, &now)
				accepted, admitted, err := store.AdmitModelBrokerRequest(controlplane.CreateModelBrokerRequestInput{ID: "accepted-" + factory.name, Model: "test"}, 1)
				if err != nil || !admitted || accepted.Status != controlplane.ModelBrokerRequestQueued {
					t.Fatalf("accepted=%+v admitted=%v err=%v", accepted, admitted, err)
				}
				dropped, admitted, err := store.AdmitModelBrokerRequest(controlplane.CreateModelBrokerRequestInput{ID: "dropped-" + factory.name, Model: "test"}, 1)
				if err != controlplane.ErrQueueFull || admitted || dropped.Status != controlplane.ModelBrokerRequestDropped {
					t.Fatalf("dropped=%+v admitted=%v err=%v", dropped, admitted, err)
				}
				replayed, admitted, err := store.AdmitModelBrokerRequest(controlplane.CreateModelBrokerRequestInput{ID: dropped.ID, Model: "changed"}, 1)
				if err != controlplane.ErrQueueFull || admitted || replayed.Model != "test" {
					t.Fatalf("replayed=%+v admitted=%v err=%v", replayed, admitted, err)
				}
				running, _, err := store.BeginModelBrokerRequest(controlplane.CreateModelBrokerRequestInput{ID: "running-" + factory.name, Model: "test"})
				if err != nil {
					t.Fatal(err)
				}
				first, err := store.CancelModelBrokerRequest(running.ID)
				if err != nil || first.CancelRequestedAt == nil {
					t.Fatalf("first cancel=%+v err=%v", first, err)
				}
				now = now.Add(time.Hour)
				second, err := store.CancelModelBrokerRequest(running.ID)
				if err != nil || second.CancelRequestedAt == nil || !second.CancelRequestedAt.Equal(*first.CancelRequestedAt) {
					t.Fatalf("second cancel=%+v err=%v first=%v", second, err, first.CancelRequestedAt)
				}
			})
		})
	}
}

func lifecycleStoreFactories() []lifecycleStoreFactory {
	return []lifecycleStoreFactory{
		{name: "memory", new: func(t *testing.T, now *time.Time) controlplane.Store {
			t.Helper()
			store := controlplane.NewStore(func() time.Time { return *now })
			workflowID := lifecycleWorkflowID(t)
			if err := store.UpsertWorkflow(controlplane.Workflow{ID: workflowID, Kind: controlplane.WorkflowDevlog, Name: "Lifecycle contract", Status: "active"}); err != nil {
				t.Fatalf("UpsertWorkflow() error = %v", err)
			}
			return lifecycleWorkflowStore{Store: store, workflowID: workflowID}
		}},
		{name: "postgres", new: newPostgresLifecycleStore},
	}
}

func newPostgresLifecycleStore(t *testing.T, now *time.Time) controlplane.Store {
	t.Helper()
	databaseURL := os.Getenv("SWITCHYARD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set SWITCHYARD_TEST_DATABASE_URL to run Postgres lifecycle store contract")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	db, err := controlplane.OpenPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatalf("OpenPostgres() error = %v", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		t.Fatalf("open lifecycle contract connection: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `select pg_advisory_lock($1)`, int64(7_031_260_001)); err != nil {
		_ = conn.Close()
		_ = db.Close()
		t.Fatalf("lock lifecycle contract database: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := conn.ExecContext(cleanupCtx, `select pg_advisory_unlock($1)`, int64(7_031_260_001)); err != nil {
			t.Errorf("unlock lifecycle contract database: %v", err)
		}
		if err := conn.Close(); err != nil {
			t.Errorf("close lifecycle contract connection: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Errorf("close lifecycle contract database: %v", err)
		}
	})
	if os.Getenv("SWITCHYARD_TEST_POSTGRES_ISOLATED") != "1" {
		nonterminal, err := lifecycleNonterminalRunCount(ctx, conn)
		if err != nil {
			t.Skipf("Postgres lifecycle store contract requires the isolated-container gate (run make postgres-test); refusing to mutate manually supplied database because its active runs could not be checked: %v", err)
		}
		if nonterminal > 0 {
			t.Skipf("Postgres lifecycle store contract requires the isolated-container gate (run make postgres-test); refusing to mutate manually supplied database with %d unrelated active/nonterminal runs", nonterminal)
		}
		t.Skip("Postgres lifecycle store contract requires SWITCHYARD_TEST_POSTGRES_ISOLATED=1 from scripts/test-postgres.sh; run make postgres-test")
	}
	if err := controlplane.Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	store := controlplane.NewPostgresStore(db, func() time.Time { return *now })
	workflowID := lifecycleWorkflowID(t)
	if err := store.UpsertWorkflow(controlplane.Workflow{ID: workflowID, Kind: controlplane.WorkflowDevlog, Name: "Lifecycle contract", Status: "active"}); err != nil {
		t.Fatalf("UpsertWorkflow() error = %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := db.ExecContext(cleanupCtx, `delete from runs where workflow_id=$1`, workflowID); err != nil {
			t.Errorf("cleanup runs: %v", err)
		}
		if _, err := db.ExecContext(cleanupCtx, `delete from workflows where id=$1`, workflowID); err != nil {
			t.Errorf("cleanup workflow: %v", err)
		}
	})
	return lifecycleWorkflowStore{Store: store, workflowID: workflowID}
}

type lifecycleClaimResult struct {
	run controlplane.Run
	err error
}

func lifecycleNonterminalRunCount(ctx context.Context, conn interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (int, error) {
	var runsTableExists bool
	if err := conn.QueryRowContext(ctx, `select to_regclass('runs') is not null`).Scan(&runsTableExists); err != nil {
		return 0, err
	}
	if !runsTableExists {
		return 0, nil
	}
	var count int
	err := conn.QueryRowContext(ctx, `select count(*) from runs where status not in ($1, $2, $3)`, controlplane.RunSucceeded, controlplane.RunFailed, controlplane.RunCanceled).Scan(&count)
	return count, err
}

type lifecycleWorkflowStore struct {
	controlplane.Store
	workflowID string
}

func lifecycleContractRun(t *testing.T, store controlplane.Store, maxAttempts int) controlplane.Run {
	t.Helper()
	workflowStore, ok := store.(lifecycleWorkflowStore)
	if !ok {
		t.Fatalf("contract store does not provide a workflow")
	}
	run, err := store.CreateRun(controlplane.CreateRunInput{WorkflowID: workflowStore.workflowID, MaxAttempts: maxAttempts})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	return run
}

func lifecycleWorkflowID(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("lifecycle-contract-%d", time.Now().UnixNano())
}
