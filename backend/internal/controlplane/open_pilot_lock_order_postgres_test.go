package controlplane

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestPostgresStoreOpenPilotStateBeforeRunLockOrderIntegration(t *testing.T) {
	databaseURL := os.Getenv("SWITCHYARD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set SWITCHYARD_TEST_DATABASE_URL to run Postgres lock-order integration test")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	db, err := OpenPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := NewPostgresStore(db, time.Now)
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	resetIsolatedPostgresRuns(t, db)
	gate := testOpenPilotAdmissionGate(t)

	for _, test := range []struct {
		name string
		run  func(context.Context, string) error
	}{
		{name: "adoption", run: func(callCtx context.Context, runID string) error {
			_, _, err := store.WithContext(callCtx).AdoptOpenPilotRunAffinity(runID, "worker", 1, gate)
			return err
		}},
		{name: "lease recovery", run: func(callCtx context.Context, _ string) error {
			_, _, err := store.WithContext(callCtx).RecoverLeasedRuns(time.Now().UTC(), gate)
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			issue := fmt.Sprint(time.Now().UnixNano())
			run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "Subcult/Switchyard", "issue": issue}, MaxAttempts: 2})
			if err != nil {
				t.Fatal(err)
			}
			leaseInterval := "-1 minute"
			if test.name == "adoption" {
				leaseInterval = "1 minute"
			}
			if _, err := db.ExecContext(ctx, `update runs set status=$2,attempt_count=1,claimed_by='worker',claimed_at=clock_timestamp(),lease_expires_at=clock_timestamp()+$3::interval where id=$1`, run.ID, RunRunning, leaseInterval); err != nil {
				t.Fatal(err)
			}
			state, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: "Subcult/Switchyard", Issue: issue, RunID: run.ID, Phase: OpenPilotPhaseRequested})
			if err != nil {
				t.Fatal(err)
			}

			blocker, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback()
			if _, err := blocker.ExecContext(ctx, `select id from open_pilot_issue_states where id=$1 for update`, state.ID); err != nil {
				t.Fatal(err)
			}
			callCtx, callCancel := context.WithTimeout(ctx, 5*time.Second)
			defer callCancel()
			result := make(chan error, 1)
			go func() { result <- test.run(callCtx, run.ID) }()

			waitCtx, waitCancel := context.WithTimeout(ctx, 2*time.Second)
			defer waitCancel()
			for {
				var waiting bool
				err := db.QueryRowContext(waitCtx, `select exists(select 1 from pg_stat_activity where pid <> pg_backend_pid() and wait_event_type='Lock' and query like 'select id from open_pilot_issue_states where run_id=$1%')`).Scan(&waiting)
				if err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case <-waitCtx.Done():
					t.Fatal("store operation did not block on the state row")
				case <-time.After(10 * time.Millisecond):
				}
			}

			lockCtx, lockCancel := context.WithTimeout(ctx, time.Second)
			defer lockCancel()
			if _, err := blocker.ExecContext(lockCtx, `select id from runs where id=$1 for update`, run.ID); err != nil {
				t.Fatalf("run row was locked before state row: %v", err)
			}
			if err := blocker.Commit(); err != nil {
				t.Fatal(err)
			}
			if err := <-result; err != nil {
				t.Fatalf("store operation failed after state release: %v", err)
			}
		})
	}
}

func TestPostgresRecoveryQuarantinesDuplicateRunStatesAndContinuesIntegration(t *testing.T) {
	databaseURL := os.Getenv("SWITCHYARD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set SWITCHYARD_TEST_DATABASE_URL to run Postgres recovery integration test")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	db, err := OpenPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := NewPostgresStore(db, time.Now)
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	resetIsolatedPostgresRuns(t, db)
	issue := fmt.Sprint(time.Now().UnixNano())
	target := *trustedOpenPilotInput("Subcult/Switchyard", issue, CreateRunInput{}).OpenPilotTarget
	_, openRun, err := store.ClaimAndCreateOpenPilotInitialRun("Subcult/Switchyard", issue, CreateRunInput{WorkflowID: string(WorkflowOpenPilot), OpenPilotTarget: &target, MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	otherRun, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowDevlog), MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `update runs set status=$2,claimed_by='worker',attempt_count=attempt_count+1,claimed_at=clock_timestamp(),lease_expires_at=clock_timestamp()+interval '1 minute' where id=any($1::text[])`, []string{openRun.ID, otherRun.ID}, RunRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `insert into open_pilot_issue_states(id,repo,issue,phase,run_id,created_at,updated_at) values($1,'Subcult/Switchyard',$2,'requested',$3,now(),now())`, newID("duplicate-state"), issue+"1", openRun.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := db.ExecContext(ctx, `update runs set lease_expires_at=$2 where id=any($1::text[])`, []string{openRun.ID, otherRun.ID}, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	requeued, failed, err := store.RecoverLeasedRuns(now, testOpenPilotAdmissionGate(t))
	if err != nil || requeued != 1 || failed != 0 {
		t.Fatalf("recovery=%d/%d error=%v", requeued, failed, err)
	}
	openDetail, _ := store.RunDetail(openRun.ID)
	otherDetail, _ := store.RunDetail(otherRun.ID)
	if openDetail.Run.Status != RunPaused || otherDetail.Run.Status != RunQueued {
		t.Fatalf("runs: open=%+v other=%+v", openDetail.Run, otherDetail.Run)
	}
	var quarantined int
	if err := db.QueryRowContext(ctx, `select count(*) from open_pilot_issue_states where run_id=$1 and phase='paused' and failing_gate='repository_affinity' and recommended_next_action='manual_review'`, openRun.ID).Scan(&quarantined); err != nil || quarantined != 2 {
		t.Fatalf("quarantined=%d error=%v", quarantined, err)
	}
}
