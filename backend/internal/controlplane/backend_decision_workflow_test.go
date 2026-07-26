package controlplane

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

const workflowDecisionResponse = `{"id":"upstream","choices":[{"message":{"content":"APPROVED: The completed work satisfies the issue."}}]}`

func TestWorkflowDeprecatedBackendAttempts(t *testing.T) {
	for _, tc := range []struct {
		name, workflow, backend, executor, step string
		run                                     func(*testing.T, *MemoryStore, *EnvWorkflowExecutors, *Runner)
	}{
		{
			name: "generic model uses the generic model executor", workflow: "generic", backend: "llama-line", step: "generate",
			run: func(t *testing.T, store *MemoryStore, _ *EnvWorkflowExecutors, runner *Runner) {
				finished, err := runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: "generic", PipelineSnapshot: &Pipeline{WorkflowID: "generic", Steps: []PipelineStep{{ID: "generate", Position: 1, Name: "generate", Executor: "model", Model: "test", Inputs: map[string]string{"prompt": "hello"}}}}})
				if err != nil || finished.Status != RunSucceeded {
					t.Fatalf("run = %+v, err = %v", finished, err)
				}
			},
		},
		{
			name: "generic model preserves evidence when llama-line returns an error", workflow: "generic", backend: "llama-line", step: "generate",
			run: func(t *testing.T, store *MemoryStore, _ *EnvWorkflowExecutors, runner *Runner) {
				finished, err := runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: "generic", PipelineSnapshot: &Pipeline{WorkflowID: "generic", Steps: []PipelineStep{{ID: "generate", Position: 1, Name: "generate", Executor: "model", Model: "error", Inputs: map[string]string{"prompt": "hello"}}}}})
				if err != nil || finished.Status != RunFailed {
					t.Fatalf("run = %+v, err = %v", finished, err)
				}
			},
		},
		{
			name: "Devlog generation uses generateDevlogWithModel", workflow: string(WorkflowDevlog), backend: "llama-line", step: "devlog.generate-markdown",
			run: func(t *testing.T, store *MemoryStore, _ *EnvWorkflowExecutors, runner *Runner) {
				finished, err := runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: string(WorkflowDevlog), PipelineSnapshot: &Pipeline{WorkflowID: string(WorkflowDevlog), Steps: []PipelineStep{{ID: "generate-markdown", Position: 1, Name: "devlog.generate-markdown", Executor: "model", Model: "test"}}}})
				if err != nil || finished.Status != RunSucceeded {
					t.Fatalf("run = %+v, err = %v", finished, err)
				}
			},
		},
		{
			name: "Open Pilot agent uses the OpenCode executor", workflow: string(WorkflowOpenPilot), executor: "ocq", step: "open-pilot.agent-work",
			run: func(t *testing.T, store *MemoryStore, env *EnvWorkflowExecutors, runner *Runner) {
				env.worktreeManager = workflowDecisionWorktree{plan: OpenPilotWorktreePlan{Executed: true, WorktreePath: "/worktree", Branch: "open-pilot/issue-1"}}
				finished, err := runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "owner/repo", "issue": "1"}, PipelineSnapshot: &Pipeline{WorkflowID: string(WorkflowOpenPilot), Steps: []PipelineStep{{ID: "branch-plan", Position: 1, Name: "open-pilot.branch-plan", Executor: "internal"}, {ID: "prepare-prompt", Position: 2, Name: "open-pilot.prepare-prompt", Executor: "internal"}, {ID: "agent-work", Position: 3, Name: "open-pilot.agent-work", Executor: "opencode", Model: "test"}}}})
				if err != nil || finished.Status != RunSucceeded {
					t.Fatalf("run = %+v, err = %v", finished, err)
				}
			},
		},
		{
			name: "Open Pilot agent preserves evidence when nested OCQ returns an error", workflow: string(WorkflowOpenPilot), executor: "ocq", step: "open-pilot.agent-work",
			run: func(t *testing.T, store *MemoryStore, env *EnvWorkflowExecutors, runner *Runner) {
				env.worktreeManager = workflowDecisionWorktree{plan: OpenPilotWorktreePlan{Executed: true, WorktreePath: "/worktree", Branch: "open-pilot/issue-1"}}
				finished, err := runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "owner/repo", "issue": "1"}, PipelineSnapshot: &Pipeline{WorkflowID: string(WorkflowOpenPilot), Steps: []PipelineStep{{ID: "branch-plan", Position: 1, Name: "open-pilot.branch-plan", Executor: "internal"}, {ID: "prepare-prompt", Position: 2, Name: "open-pilot.prepare-prompt", Executor: "internal"}, {ID: "agent-work", Position: 3, Name: "open-pilot.agent-work", Executor: "opencode", Model: "error"}}}})
				if err != nil || finished.Status != RunPaused {
					t.Fatalf("run = %+v, err = %v", finished, err)
				}
			},
		},
		{
			name: "Open Pilot AI review uses the model executor", workflow: string(WorkflowOpenPilot), backend: "llama-line", step: "open-pilot.ai-review",
			run: func(t *testing.T, store *MemoryStore, env *EnvWorkflowExecutors, _ *Runner) {
				run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "owner/repo", "issue": "1"}})
				if err != nil {
					t.Fatal(err)
				}
				step, err := createAndStartStep(store, CreateRunStepInput{RunID: run.ID, Position: 1, Name: "open-pilot.ai-review", Executor: "model", Model: "test"})
				if err != nil {
					t.Fatal(err)
				}
				out, err := (openPilotExecutor{env: env}).ExecutePipelineStep(withRunStepPreAttempt(t.Context(), store, run.ID, step.ID), run, PipelineStep{ID: "ai-review", Name: "open-pilot.ai-review", Model: "test"}, map[string]any{})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.FinishRunStep(step.ID, StepSucceeded, out.Summary, "", out.CorrelationIDs); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "Open Pilot AI review preserves evidence when llama-line returns an error", workflow: string(WorkflowOpenPilot), backend: "llama-line", step: "open-pilot.ai-review",
			run: func(t *testing.T, store *MemoryStore, env *EnvWorkflowExecutors, _ *Runner) {
				run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "owner/repo", "issue": "1"}})
				if err != nil {
					t.Fatal(err)
				}
				step, err := createAndStartStep(store, CreateRunStepInput{RunID: run.ID, Position: 1, Name: "open-pilot.ai-review", Executor: "model", Model: "error"})
				if err != nil {
					t.Fatal(err)
				}
				out, err := (openPilotExecutor{env: env}).ExecutePipelineStep(withRunStepPreAttempt(t.Context(), store, run.ID, step.ID), run, PipelineStep{ID: "ai-review", Name: "open-pilot.ai-review", Model: "error"}, map[string]any{})
				if err == nil {
					t.Fatal("expected upstream error")
				}
				if _, err := store.FinishRunStep(step.ID, StepFailed, out.Summary, err.Error(), out.CorrelationIDs); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.name == "generic model preserves evidence when llama-line returns an error" || tc.name == "Open Pilot agent preserves evidence when nested OCQ returns an error" || tc.name == "Open Pilot AI review preserves evidence when llama-line returns an error" {
					http.Error(w, "upstream failed", http.StatusBadGateway)
					return
				}
				_, _ = w.Write([]byte(workflowDecisionResponse))
			}))
			defer upstream.Close()
			t.Setenv("MODEL_BROKER_BACKEND", tc.backend)
			t.Setenv("OPENCODE_EXECUTOR_BACKEND", tc.executor)
			t.Setenv("LLAMA_LINE_BASE_URL", upstream.URL)
			t.Setenv("OCQ_BASE_URL", upstream.URL)
			store := NewStore(nil)
			if err := store.UpsertWorkflow(Workflow{ID: tc.workflow, Kind: WorkflowKind(tc.workflow), Name: tc.workflow}); err != nil {
				t.Fatal(err)
			}
			env := NewEnvWorkflowExecutors(store)
			runner := NewRunner(store, env)
			tc.run(t, store, env, runner)
			steps := store.ListRunSteps(store.ListRuns()[0].ID)
			for _, step := range steps {
				if step.Name == tc.step && step.CorrelationIDs[CorrelationDeprecatedBackend] != firstNonEmpty(tc.backend, tc.executor) {
					t.Fatalf("step correlations = %v", step.CorrelationIDs)
				}
			}
			assertWorkflowDeprecatedEvent(t, store, store.ListRuns()[0].ID, firstNonEmpty(tc.backend, tc.executor), true)
		})
	}
}

func TestWorkflowDeprecatedBackendAttemptIsNotRecordedBeforeOutboundAttempt(t *testing.T) {
	t.Run("missing configuration and malformed request do not invoke callback", func(t *testing.T) {
		for _, broker := range []*EnvModelBroker{{llamaLineBaseURL: "", client: http.DefaultClient}, {llamaLineBaseURL: "://bad", client: http.DefaultClient}} {
			called := false
			t.Setenv("MODEL_BROKER_BACKEND", "llama-line")
			_, _ = broker.Generate(t.Context(), ModelRequest{Model: "test", Messages: []ModelMessage{{Role: "user", Content: "x"}}, PreAttempt: func(BackendDecision) error { called = true; return nil }})
			if called {
				t.Fatal("pre-attempt callback was invoked")
			}
		}
	})
	t.Run("no worktree pause has no attempt", func(t *testing.T) {
		store := NewStore(nil)
		if err := store.UpsertWorkflow(Workflow{ID: string(WorkflowOpenPilot), Kind: WorkflowOpenPilot, Name: "Open Pilot"}); err != nil {
			t.Fatal(err)
		}
		t.Setenv("OPENCODE_EXECUTOR_BACKEND", "ocq")
		runner := NewRunner(store, NewEnvWorkflowExecutors(store))
		_, _ = runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "owner/repo", "issue": "1"}, PipelineSnapshot: &Pipeline{Steps: []PipelineStep{{ID: "agent-work", Position: 1, Name: "open-pilot.agent-work", Executor: "opencode", Model: "test"}}}})
		assertWorkflowDeprecatedEvent(t, store, store.ListRuns()[0].ID, "ocq", false)
	})
	t.Run("limiter rejection and cancellation have no attempt", func(t *testing.T) {
		for _, err := range []error{errors.New("limiter rejected execution"), context.Canceled} {
			store := NewStore(nil)
			if upsertErr := store.UpsertWorkflow(Workflow{ID: "generic", Kind: "generic", Name: "Generic"}); upsertErr != nil {
				t.Fatal(upsertErr)
			}
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
			defer upstream.Close()
			t.Setenv("MODEL_BROKER_BACKEND", "llama-line")
			t.Setenv("LLAMA_LINE_BASE_URL", upstream.URL)
			runner := NewRunner(store, NewEnvWorkflowExecutors(store))
			runner.SetExecutorLimiter(workflowDecisionLimiter{err: err})
			_, _ = runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: "generic", PipelineSnapshot: &Pipeline{Steps: []PipelineStep{{ID: "generate", Position: 1, Name: "generate", Executor: "model", Model: "test", Inputs: map[string]string{"prompt": "x"}}}}})
			if calls.Load() != 0 {
				t.Fatal("unexpected outbound call")
			}
			assertWorkflowDeprecatedEvent(t, store, store.ListRuns()[0].ID, "llama-line", false)
		}
	})
	t.Run("atomic decision persistence failure prevents upstream IO", func(t *testing.T) {
		var calls atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
		defer upstream.Close()
		t.Setenv("MODEL_BROKER_BACKEND", "llama-line")
		t.Setenv("LLAMA_LINE_BASE_URL", upstream.URL)
		memory := NewStore(nil)
		if err := memory.UpsertWorkflow(Workflow{ID: "generic", Kind: "generic", Name: "Generic"}); err != nil {
			t.Fatal(err)
		}
		store := workflowDecisionFailStore{Store: memory}
		runner := NewRunner(store, NewEnvWorkflowExecutors(store))
		finished, err := runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: "generic", PipelineSnapshot: &Pipeline{Steps: []PipelineStep{{ID: "generate", Position: 1, Name: "generate", Executor: "model", Model: "test", Inputs: map[string]string{"prompt": "x"}}}}})
		if err != nil || finished.Status != RunFailed || calls.Load() != 0 {
			t.Fatalf("run=%+v err=%v upstream calls=%d", finished, err, calls.Load())
		}
	})
}

func assertWorkflowDeprecatedEvent(t *testing.T, store *MemoryStore, runID, backend string, want bool) {
	t.Helper()
	detail, err := store.RunDetail(runID)
	if err != nil {
		t.Fatal(err)
	}
	got := false
	for _, event := range detail.Events {
		if event.Type == DeprecatedBackendAttemptedEvent && event.Message == backend {
			got = true
		}
	}
	if got != want {
		t.Fatalf("deprecated event=%v, want %v", got, want)
	}
}

type workflowDecisionWorktree struct{ plan OpenPilotWorktreePlan }

func (w workflowDecisionWorktree) Prepare(context.Context, Run, string, string, string) (OpenPilotWorktreePlan, error) {
	return w.plan, nil
}
func (w workflowDecisionWorktree) Cleanup(context.Context, OpenPilotWorktreePlan, bool) (OpenPilotWorktreeCleanupPlan, error) {
	return OpenPilotWorktreeCleanupPlan{}, nil
}

type workflowDecisionLimiter struct{ err error }

func (l workflowDecisionLimiter) Acquire(context.Context, string) (func(), error) { return nil, l.err }

type workflowDecisionFailStore struct{ Store }

func (workflowDecisionFailStore) RecordRunStepBackendDecision(string, string, BackendDecision) error {
	return errors.New("atomic decision write failed")
}
