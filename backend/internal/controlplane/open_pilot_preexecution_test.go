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
	testOpenPilotPausedBeforeBranchResume(t, NewStore(nil))
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
	testOpenPilotPausedBeforeBranchResume(t, NewPostgresStore(db, time.Now))
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
			if _, err := store.FinishRun(run.ID, RunCanceled, "test complete"); err != nil {
				t.Fatal(err)
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

func testOpenPilotPausedBeforeBranchResume(t *testing.T, store Store) {
	t.Helper()
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	for _, branchStatus := range []RunStepStatus{StepPaused, StepFailed, StepSucceeded} {
		repo := fmt.Sprintf("preexecution/p%d", time.Now().UnixNano())
		input := trustedOpenPilotInput(repo, "1", CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": repo, "issue": "1"}})
		saved, prior, err := store.ClaimAndCreateOpenPilotInitialRun(repo, "1", input)
		if err != nil {
			t.Fatal(err)
		}
		for i, st := range []RunStepStatus{StepSucceeded, branchStatus} {
			name := []string{"open-pilot.validate-issue", "open-pilot.branch-plan"}[i]
			step, err := store.CreateRunStep(CreateRunStepInput{RunID: prior.ID, Position: i + 1, Name: name, Executor: "internal"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.FinishRunStep(step.ID, st, "preflight", "", nil); err != nil {
				t.Fatal(err)
			}
		}
		saved.Phase, saved.LastStepName = OpenPilotPhasePaused, "open-pilot.branch-plan"
		if _, err := store.UpsertOpenPilotIssueState(saved); err != nil {
			t.Fatal(err)
		}
		if _, err := store.FinishRun(prior.ID, RunPaused, "preflight"); err != nil {
			t.Fatal(err)
		}
		_, run, err := store.ClaimAndCreateOpenPilotResumeRun(repo, "1", input)
		if branchStatus == StepPaused {
			if err != nil || run.Inputs["retry_from_step"] != "open-pilot.validate-issue" {
				actual, _ := store.OpenPilotIssueState(repo, "1")
				detail, _ := store.RunDetail(prior.ID)
				t.Fatalf("resume=%+v err=%v state=%+v prior=%+v steps=%+v", run, err, actual, detail.Run, detail.Steps)
			}
			if _, err := store.FinishRun(run.ID, RunCanceled, "test complete"); err != nil {
				t.Fatal(err)
			}
		} else if err == nil {
			t.Fatalf("resumed non-preflight branch status %s", branchStatus)
		}
	}
}

func TestCanceledBeforeExecutionResumeCreatesFirstWorktree(t *testing.T) {
	store := NewStore(nil)
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	input := trustedOpenPilotInput("owner/repo", "1", CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "owner/repo", "issue": "1"}})
	_, prior, err := store.ClaimAndCreateOpenPilotInitialRun("owner/repo", "1", input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(prior.ID, RunCanceled, "before execution"); err != nil {
		t.Fatal(err)
	}
	_, run, err := store.ClaimAndCreateOpenPilotResumeRun("owner/repo", "1", input)
	if err != nil {
		t.Fatal(err)
	}
	env := newOpenPilotAIReviewTestEnv(store)
	env.openPilotGate = openPilotAdmissionGateForTarget(t, *input.OpenPilotTarget)
	env.issueFetcher = &stubIssueFetcher{issue: GiteaIssue{ID: 1, Number: 1, Repository: "owner/repo", State: "open", Title: "proof"}}
	env.worktreeManager = NewGitOpenPilotWorktreeManager(OpenPilotWorktreeAllow, initWorktreeTestRepo(t), t.TempDir())
	executor := openPilotExecutor{env: env}
	state := map[string]any{}
	for _, id := range []string{"validate-issue", "branch-plan"} {
		result, err := executor.ExecutePipelineStep(t.Context(), run, PipelineStep{ID: id, Name: "open-pilot." + id}, state)
		if err != nil || result.Halt {
			t.Fatalf("%s result=%+v err=%v", id, result, err)
		}
	}
	saved, err := store.OpenPilotIssueState("owner/repo", "1")
	if err != nil || saved.Phase != OpenPilotPhaseBranchPrepared || saved.WorktreePath == "" {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
}
