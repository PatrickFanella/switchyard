package controlplane

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

func TestOpenPilotCanceledBeforeExecutionResume(t *testing.T) {
	testOpenPilotCanceledBeforeExecutionResume(t, NewStore(nil))
}

func TestPostgresOpenPilotCanceledBeforeExecutionResume(t *testing.T) {
	url := os.Getenv("SWITCHYARD_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("requires PostgreSQL integration database")
	}
	db, err := OpenPostgres(t.Context(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(context.Background(), `delete from open_pilot_issue_states where repo like 'preexecution/%'`)
	testOpenPilotCanceledBeforeExecutionResume(t, NewPostgresStore(db, time.Now))
}

func testOpenPilotCanceledBeforeExecutionResume(t *testing.T, store Store) {
	t.Helper()
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	for _, withStep := range []bool{false, true} {
		repo := fmt.Sprintf("preexecution/r%d", time.Now().UnixNano())
		input := trustedOpenPilotInput(repo, "1", CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": repo, "issue": "1", "test_command": "git diff --check"}})
		_, prior, err := store.ClaimAndCreateOpenPilotInitialRun(repo, "1", input)
		if err != nil {
			t.Fatal(err)
		}
		if withStep {
			_, err = store.CreateRunStep(CreateRunStepInput{RunID: prior.ID, Position: 1, Name: "open-pilot.validate-issue", Executor: "internal"})
			if err != nil {
				t.Fatal(err)
			}
		}
		if _, err := store.FinishRun(prior.ID, RunCanceled, "operator canceled"); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		results := make(chan Run, 2)
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, run, e := store.ClaimAndCreateOpenPilotResumeRun(repo, "1", input)
				if e == nil {
					results <- run
				}
			}()
		}
		wg.Wait()
		close(results)
		count := 0
		for run := range results {
			count++
			if run.ID == prior.ID || run.Inputs["retry_from_step"] != "open-pilot.validate-issue" {
				t.Fatalf("unsafe restart: %#v", run)
			}
		}
		want := 1
		if withStep {
			want = 0
		}
		if count != want {
			t.Fatalf("withStep=%v claims=%d want=%d", withStep, count, want)
		}
		old, err := store.RunDetail(prior.ID)
		if err != nil || old.Run.Status != RunCanceled {
			t.Fatalf("prior run changed: %v", err)
		}
	}
}
