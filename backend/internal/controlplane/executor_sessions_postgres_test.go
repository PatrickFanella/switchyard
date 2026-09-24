package controlplane

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestPostgresExecutorSessionStepsIntegration proves the PostgreSQL scan
// matches the memory store: retention floor, session-bearing filter, session
// ID filter, repository affinity projection, ordering, and truncation.
func TestPostgresExecutorSessionStepsIntegration(t *testing.T) {
	databaseURL := os.Getenv("SWITCHYARD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set SWITCHYARD_TEST_DATABASE_URL to run PostgreSQL executor session integration test")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	db, err := OpenPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	store := NewPostgresStore(db, func() time.Time { return now })
	workflowID := newID("executor-sessions")
	marker := newID("ses")
	if err := store.UpsertWorkflow(Workflow{ID: workflowID, Kind: WorkflowDevlog, Name: "Executor sessions", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = db.ExecContext(cleanupCtx, `delete from runs where workflow_id=$1`, workflowID)
		_, _ = db.ExecContext(cleanupCtx, `delete from workflows where id=$1`, workflowID)
	})
	floor := now.Add(-24 * time.Hour)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	step := func(run Run, position int, correlation map[string]string) RunStep {
		t.Helper()
		created, err := store.CreateRunStep(CreateRunStepInput{RunID: run.ID, Position: position, Name: "agent", Executor: "opencode", Inputs: map[string]string{"prompt": "secret prompt"}, CorrelationIDs: correlation})
		must(err)
		return created
	}

	now = floor.Add(-time.Hour)
	old, err := store.CreateRun(CreateRunInput{WorkflowID: workflowID})
	must(err)
	step(old, 1, map[string]string{"opencode_session_id": marker + "-old"})

	now = floor.Add(time.Hour)
	alpha, err := store.CreateRun(CreateRunInput{WorkflowID: workflowID, Inputs: map[string]string{openPilotRepositoryIDInputKey: "alpha", openPilotExecutorGenerationInputKey: "gen-1", "repo": "acme/alpha", "api_key": "sk-proj-postgressecret1234567890"}})
	must(err)
	_, err = store.StartRun(alpha.ID)
	must(err)
	first := step(alpha, 1, map[string]string{"opencode_session_id": marker + "-alpha", "opencode_request_id": "req-1"})
	_, err = store.StartRunStep(first.ID)
	must(err)
	step(alpha, 2, map[string]string{"trace_id": "no-session"})
	now = now.Add(time.Minute)
	legacy := step(alpha, 3, map[string]string{"ocq_session_id": marker + "-legacy"})

	records := func(input ExecutorSessionStepsInput) ExecutorSessionStepPage {
		t.Helper()
		input.RetainedSince = floor
		if input.Limit == 0 {
			input.Limit = executorSessionMaxScan
		}
		page, err := store.ListExecutorSessionSteps(ctx, input)
		must(err)
		mine := ExecutorSessionStepPage{Truncated: page.Truncated}
		for _, record := range page.Records {
			if record.WorkflowID == workflowID {
				mine.Records = append(mine.Records, record)
			}
		}
		return mine
	}

	page := records(ExecutorSessionStepsInput{})
	if len(page.Records) != 2 || page.Records[0].StepID != legacy.ID || page.Records[1].StepID != first.ID {
		t.Fatalf("records = %+v", page.Records)
	}
	got := page.Records[1]
	if got.RepositoryID != "alpha" || got.ExecutorGeneration != "gen-1" || got.Repo != "acme/alpha" || got.RunStatus != RunRunning || got.StepStatus != StepRunning || got.StepStartedAt == nil {
		t.Fatalf("projection = %+v", got)
	}
	if got.CorrelationIDs["opencode_request_id"] != "req-1" {
		t.Fatalf("correlation = %+v", got.CorrelationIDs)
	}

	filtered := records(ExecutorSessionStepsInput{SessionID: marker + "-legacy"})
	if len(filtered.Records) != 1 || filtered.Records[0].StepID != legacy.ID {
		t.Fatalf("session filter = %+v", filtered.Records)
	}
	if old := records(ExecutorSessionStepsInput{SessionID: marker + "-old"}); len(old.Records) != 0 {
		t.Fatalf("record older than the floor returned: %+v", old.Records)
	}
	limited, err := store.ListExecutorSessionSteps(ctx, ExecutorSessionStepsInput{RetainedSince: floor, SessionID: marker + "-alpha", Limit: 1})
	must(err)
	if len(limited.Records) != 1 || limited.Truncated {
		t.Fatalf("single-match limit = %+v", limited)
	}

	memory := NewStore(func() time.Time { return now })
	if _, err := memory.ListExecutorSessionSteps(ctx, ExecutorSessionStepsInput{RetainedSince: floor, Limit: 0}); err == nil {
		t.Fatal("memory store accepted an invalid scan limit")
	}
	if _, err := store.ListExecutorSessionSteps(ctx, ExecutorSessionStepsInput{RetainedSince: floor, Limit: executorSessionMaxScan + 1}); err == nil {
		t.Fatal("postgres store accepted an oversized scan limit")
	}
}
