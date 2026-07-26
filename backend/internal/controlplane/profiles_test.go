package controlplane

import (
	"context"
	"strings"
	"testing"
	"time"
)

type profileRecordingExecutor struct {
	model    string
	contract string
}

func (e *profileRecordingExecutor) Execute(context.Context, Run) (WorkflowExecution, error) {
	return WorkflowExecution{}, nil
}

func (e *profileRecordingExecutor) ExecutePipelineStep(_ context.Context, _ Run, step PipelineStep, _ map[string]any) (PipelineStepExecution, error) {
	e.model = step.Model
	e.contract = step.profileContract
	return PipelineStepExecution{Summary: "recorded"}, nil
}

func TestRunnerEnforcesProfileDefaultsAndAuditMetadata(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := store.UpsertWorkflow(Workflow{ID: "profile-workflow", Kind: "profile-workflow", Name: "Profile Workflow"}); err != nil {
		t.Fatal(err)
	}
	executor := &profileRecordingExecutor{}
	runner := NewRunner(store, MapExecutorRegistry{"profile-workflow": executor})
	runner.SetAgentProfileRegistry(StaticAgentProfileRegistry{"writer": {
		ID: "writer", DefaultModel: "profile-default", PromptContract: "Write only grounded content.", AllowedExecutors: []string{"model"}, AllowedModels: []string{"profile-default"},
	}})
	runner.pipelines = StaticPipelineRegistry{"profile-workflow": {ID: "profile-pipeline", WorkflowID: "profile-workflow", Steps: []PipelineStep{{ID: "write", Position: 1, Name: "profile.write", Executor: "model", Profile: "writer"}}}}
	run, err := runner.RunOnce(context.Background(), CreateRunInput{WorkflowID: "profile-workflow"})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if executor.model != "profile-default" || executor.contract != "Write only grounded content." {
		t.Fatalf("executor model=%q contract=%q", executor.model, executor.contract)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	correlation := detail.Steps[0].CorrelationIDs
	if correlation["agent_profile_id"] != "writer" || correlation["agent_profile_model"] != "profile-default" || correlation["agent_profile_contract_sha256"] == "" {
		t.Fatalf("profile correlation = %+v", correlation)
	}
	if !strings.Contains(applyPipelineProfilePrompt(PipelineStep{profileContract: executor.contract}, "request"), "Profile contract") {
		t.Fatal("profile contract was not applied to prompt")
	}
}

func TestWorkflowDefinitionRejectsUnknownOrDisallowedProfiles(t *testing.T) {
	store := NewStore(nil)
	base := WorkflowDefinition{ID: "profile-definition", Name: "Profile Definition", Status: "draft", Pipeline: PipelineDefinition{ID: "profile-definition-pipeline", WorkflowID: "profile-definition", Name: "Profile Definition", Steps: []PipelineStepDefinition{{ID: "step", Position: 1, Name: "step", Executor: "model", Profile: "missing"}}}}
	if _, err := store.UpsertWorkflowDefinition(base); err != ErrInvalid {
		t.Fatalf("unknown profile error = %v, want ErrInvalid", err)
	}
	base.Pipeline.Steps[0].Profile = "content-writer"
	base.Pipeline.Steps[0].Executor = "command"
	if _, err := store.UpsertWorkflowDefinition(base); err != ErrInvalid {
		t.Fatalf("disallowed executor error = %v, want ErrInvalid", err)
	}
}
