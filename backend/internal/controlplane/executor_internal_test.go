package controlplane

import (
	"strings"
	"testing"
	"time"
)

type failingOpenPilotReadStore struct {
	*MemoryStore
}

func (s *failingOpenPilotReadStore) OpenPilotIssueState(repo, issue string) (OpenPilotIssueState, error) {
	if repo == "OWNER/REPO" && issue == "123" {
		return OpenPilotIssueState{}, ErrInvalid
	}
	return s.MemoryStore.OpenPilotIssueState(repo, issue)
}

type failingOpenPilotWriteStore struct {
	*MemoryStore
}

func TestRunnerFailsWhenWorkflowExecutorIsNotRegistered(t *testing.T) {
	store := NewStore(nil)
	if err := store.UpsertWorkflow(Workflow{ID: "unsupported", Kind: "workflow", Name: "Unsupported"}); err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: "unsupported"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := NewRunner(store, MapExecutorRegistry{}).ExecuteRun(t.Context(), run)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != RunFailed {
		t.Fatalf("status = %q, want %q", got.Status, RunFailed)
	}
	if !strings.Contains(got.Summary, "not registered") {
		t.Fatalf("summary = %q", got.Summary)
	}
}

func (s *failingOpenPilotWriteStore) UpsertOpenPilotIssueState(OpenPilotIssueState) (OpenPilotIssueState, error) {
	return OpenPilotIssueState{}, ErrInvalid
}

func TestPersistOpenPilotIssueStateSurfacesReadFailure(t *testing.T) {
	store := &failingOpenPilotReadStore{MemoryStore: NewStore(func() time.Time { return time.Unix(100, 0) })}
	executor := openPilotExecutor{env: &EnvWorkflowExecutors{store: store}}
	err := executor.persistOpenPilotIssueState(Run{ID: "run-1", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}}, OpenPilotPhaseValidated, "open-pilot.validate-issue", "", map[string]any{"open-pilot.prompt": "prompt"})
	if err == nil {
		t.Fatal("expected persistence failure")
	}
	if err != ErrInvalid {
		t.Fatalf("err = %v", err)
	}
}

func TestHandlePipelineStepFailureLogsPersistenceFailure(t *testing.T) {
	store := &failingOpenPilotWriteStore{MemoryStore: NewStore(func() time.Time { return time.Unix(100, 0) })}
	if err := store.UpsertWorkflow(Workflow{ID: string(WorkflowOpenPilot), Kind: "workflow", Name: "Open Pilot"}); err != nil {
		t.Fatalf("UpsertWorkflow() error = %v", err)
	}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	executor := openPilotExecutor{env: &EnvWorkflowExecutors{store: store}}
	executor.HandlePipelineStepFailure(t.Context(), run, PipelineStep{ID: "validate-issue", Name: "open-pilot.validate-issue"}, map[string]any{"open-pilot.prompt": "prompt"}, ErrInvalid)
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	found := false
	for _, event := range detail.Events {
		if event.Type == "open-pilot.lifecycle.failure.persistence.failed" {
			found = true
			if !strings.Contains(event.Message, "open-pilot.validate-issue") {
				t.Fatalf("event.Message = %q", event.Message)
			}
		}
	}
	if !found {
		t.Fatalf("events = %+v", detail.Events)
	}
}

func TestOpenPilotAgentWorkCorrelationProvidesAbortHandle(t *testing.T) {
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "")
	run := Run{ID: "run-123", Inputs: map[string]string{"opencode_session_id": "ses-native"}}
	step := PipelineStep{ID: "agent-work", Name: "open-pilot.agent-work", Executor: "opencode"}
	correlation := (openPilotExecutor{}).PipelineStepCorrelationIDs(run, step, map[string]any{})
	if correlation["opencode_request_id"] == "" || correlation["opencode_session_id"] != "ses-native" || correlation["ocq_request_id"] != "" {
		t.Fatalf("correlation = %+v", correlation)
	}
	if correlation[CorrelationDeprecatedBackend] != "" || correlation["opencode_executor_backend"] != "" {
		t.Fatalf("planning must not claim an attempt: %+v", correlation)
	}
}

func TestOpenCodeSessionIDPrefersNativeKey(t *testing.T) {
	inputs := map[string]string{"opencode_session_id": "native-session", "ocq_session_id": "legacy-session"}
	if got := openCodeSessionID(inputs); got != "native-session" {
		t.Fatalf("openCodeSessionID() = %q", got)
	}
}

func TestOpenPilotAgentSessionIDReusesHydratedCorrelation(t *testing.T) {
	state := map[string]any{"open-pilot.agent-correlation": map[string]string{"opencode_session_id": "ses-reuse"}}
	if got := openPilotAgentSessionID(nil, state); got != "ses-reuse" {
		t.Fatalf("session = %q", got)
	}
	if got := openPilotAgentSessionID(map[string]string{"opencode_session_id": "ses-explicit"}, state); got != "ses-explicit" {
		t.Fatalf("explicit session = %q", got)
	}
}
