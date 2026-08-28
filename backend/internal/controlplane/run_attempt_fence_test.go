package controlplane

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemoryRunAttemptFenceRejectsReclaimedAttemptFromSameWorker(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	store := NewStore(func() time.Time { return now })
	if err := store.UpsertWorkflow(Workflow{ID: "generic", Kind: "generic", Name: "Generic"}); err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: "generic", MaxAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.ClaimNextRun("worker-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ctx := withRunAttemptFence(context.Background(), RunAttemptFence{RunID: run.ID, WorkerID: "worker-1", Attempt: first.AttemptCount})
	attemptStore := store.WithContext(ctx)
	step, err := attemptStore.CreateRunStep(CreateRunStepInput{RunID: run.ID, Position: 1, Name: "step", Executor: "model"})
	if err != nil {
		t.Fatal(err)
	}
	openPilotState, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: "owner/repo", Issue: "1", RunID: run.ID, Phase: OpenPilotPhaseRequested})
	if err != nil {
		t.Fatal(err)
	}

	now = now.Add(2 * time.Minute)
	if requeued, failed, err := store.RecoverLeasedRuns(now); err != nil || requeued != 1 || failed != 0 {
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

	mutations := []struct {
		name string
		call func() error
	}{
		{"finish run", func() error { _, err := attemptStore.FinishRun(run.ID, RunSucceeded, "stale"); return err }},
		{"create step", func() error {
			_, err := attemptStore.CreateRunStep(CreateRunStepInput{RunID: run.ID, Position: 2, Name: "stale", Executor: "model"})
			return err
		}},
		{"start step", func() error { _, err := attemptStore.StartRunStep(step.ID); return err }},
		{"update step", func() error {
			_, err := attemptStore.UpdateRunStep(step.ID, UpdateRunStepInput{Inputs: map[string]string{"stale": "true"}})
			return err
		}},
		{"finish step", func() error {
			_, err := attemptStore.FinishRunStep(step.ID, StepFailed, "stale", "stale", nil)
			return err
		}},
		{"merge correlations", func() error {
			_, err := attemptStore.MergeRunStepCorrelationIDs(step.ID, map[string]string{"stale": "true"})
			return err
		}},
		{"backend decision", func() error {
			return attemptStore.RecordRunStepBackendDecision(run.ID, step.ID, BackendDecision{Backend: "llama-line", DeprecatedBackend: "llama-line"})
		}},
		{"retry step", func() error { _, err := attemptStore.RetryRunStep(step.ID); return err }},
		{"recover step", func() error { _, err := attemptStore.RecoverRunStep(step.ID); return err }},
		{"cancel step", func() error { _, err := attemptStore.CancelRunStep(step.ID); return err }},
		{"event", func() error { _, err := attemptStore.AddEvent(run.ID, "stale", "stale"); return err }},
		{"artifact", func() error {
			_, err := attemptStore.AddArtifact(CreateArtifactInput{RunID: run.ID, Name: "stale", Kind: "text", URI: "data:text/plain,stale"})
			return err
		}},
		{"open pilot upsert", func() error {
			openPilotState.LastError = "stale"
			_, err := attemptStore.UpsertOpenPilotIssueState(openPilotState)
			return err
		}},
		{"open pilot transition", func() error {
			_, err := attemptStore.TransitionOpenPilotIssueState(openPilotState.Repo, openPilotState.Issue, OpenPilotPhaseValidated)
			return err
		}},
		{"open pilot affinity adoption", func() error {
			_, _, err := attemptStore.AdoptOpenPilotRunAffinity(run.ID, "worker-1", first.AttemptCount, nil)
			return err
		}},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			if err := mutation.call(); !errors.Is(err, ErrStaleRunAttempt) || !errors.Is(err, ErrConflict) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	after, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("stale mutations changed detail\nbefore=%+v\nafter=%+v", before, after)
	}
	metricsAfter, err := store.RuntimeMetrics()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(metricsBefore, metricsAfter) {
		t.Fatalf("stale mutations changed accounting\nbefore=%+v\nafter=%+v", metricsBefore, metricsAfter)
	}
	stateAfter, err := store.OpenPilotIssueState(openPilotState.Repo, openPilotState.Issue)
	if err != nil || stateAfter.LastError != "" || stateAfter.Phase != OpenPilotPhaseRequested {
		t.Fatalf("OpenPilotIssueState() = %+v, %v", stateAfter, err)
	}
}

type blockingAttemptLimiter struct {
	entered chan struct{}
	release chan struct{}
}

func (l blockingAttemptLimiter) Acquire(context.Context, string) (func(), error) {
	close(l.entered)
	<-l.release
	return func() {}, nil
}

func TestRunnerStaleAttemptStopsBeforeOutboundCall(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	store := NewStore(func() time.Time { return now })
	if err := store.UpsertWorkflow(Workflow{ID: "generic", Kind: "generic", Name: "Generic"}); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer upstream.Close()
	t.Setenv("MODEL_BROKER_BACKEND", "llama-line")
	t.Setenv("LLAMA_LINE_BASE_URL", upstream.URL)
	run, err := store.CreateRun(CreateRunInput{WorkflowID: "generic", MaxAttempts: 3, PipelineSnapshot: &Pipeline{Steps: []PipelineStep{{ID: "generate", Position: 1, Name: "generate", Executor: "model", Model: "test", Inputs: map[string]string{"prompt": "x"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimNextRun("worker-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	limiter := blockingAttemptLimiter{entered: make(chan struct{}), release: make(chan struct{})}
	runner := NewRunner(store, NewEnvWorkflowExecutors(store))
	runner.SetExecutorLimiter(limiter)
	done := make(chan error, 1)
	go func() {
		_, err := runner.ExecuteClaimedRun(context.Background(), claimed, "worker-1")
		done <- err
	}()
	<-limiter.entered
	now = now.Add(2 * time.Minute)
	if requeued, _, err := store.RecoverLeasedRuns(now); err != nil || requeued != 1 {
		t.Fatalf("RecoverLeasedRuns() = %d, %v", requeued, err)
	}
	if reclaimed, err := store.ClaimNextRun("worker-1", time.Minute); err != nil || reclaimed.AttemptCount != claimed.AttemptCount+1 {
		t.Fatalf("ClaimNextRun() = %+v, %v", reclaimed, err)
	}
	close(limiter.release)
	if err := <-done; !errors.Is(err, ErrStaleRunAttempt) {
		t.Fatalf("ExecuteClaimedRun() error = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("outbound calls = %d", calls.Load())
	}
	if detail, err := store.RunDetail(run.ID); err != nil || detail.Run.AttemptCount != 2 || detail.Run.Status != RunRunning {
		t.Fatalf("RunDetail() = %+v, %v", detail, err)
	}
}

func TestMemoryRunAttemptFenceRejectsMalformedAndMismatchedContext(t *testing.T) {
	store := NewStore(nil)
	if err := store.UpsertWorkflow(Workflow{ID: "generic", Kind: "generic", Name: "Generic"}); err != nil {
		t.Fatal(err)
	}
	run, _ := store.CreateRun(CreateRunInput{WorkflowID: "generic"})
	claimed, _ := store.ClaimNextRun("worker-1", time.Minute)
	contexts := []context.Context{
		context.WithValue(context.Background(), runAttemptFenceContextKey{}, "malformed"),
		withRunAttemptFence(context.Background(), RunAttemptFence{RunID: run.ID, WorkerID: "other", Attempt: claimed.AttemptCount}),
		withRunAttemptFence(context.Background(), RunAttemptFence{RunID: "other", WorkerID: "worker-1", Attempt: claimed.AttemptCount}),
	}
	for _, ctx := range contexts {
		if _, err := store.WithContext(ctx).AddEvent(run.ID, "test", "test"); !errors.Is(err, ErrStaleRunAttempt) {
			t.Fatalf("AddEvent() error = %v", err)
		}
	}
	if _, err := store.AddEvent(run.ID, "operator", "allowed"); err != nil {
		t.Fatalf("unfenced mutation failed: %v", err)
	}
}
