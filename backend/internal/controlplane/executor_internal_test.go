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
