package controlplane

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func policyTestRunner(t *testing.T, step PipelineStep, profile AgentProfile) (*MemoryStore, *Runner, *profileRecordingExecutor) {
	t.Helper()
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := store.UpsertWorkflow(Workflow{ID: "policy-workflow", Kind: "policy-workflow", Name: "Policy Workflow"}); err != nil {
		t.Fatal(err)
	}
	executor := &profileRecordingExecutor{}
	runner := NewRunner(store, MapExecutorRegistry{"policy-workflow": executor})
	runner.SetAgentProfileRegistry(StaticAgentProfileRegistry{profile.ID: withStaticAgentProfileMetadata(profile)})
	runner.pipelines = StaticPipelineRegistry{"policy-workflow": {ID: "policy-pipeline", WorkflowID: "policy-workflow", Steps: []PipelineStep{step}}}
	return store, runner, executor
}

var restrictedWriter = AgentProfile{ID: "writer", Name: "Writer", Role: "writer", DefaultModel: "model-a", AllowedModels: []string{"model-a", "model-b"}, AllowedExecutors: []string{"model"}, PromptContract: "Write."}

func TestRunModelOverrideIsValidatedAtDispatch(t *testing.T) {
	store, runner, executor := policyTestRunner(t, PipelineStep{ID: "write", Position: 1, Name: "profile.write", Executor: "model", Profile: "writer"}, restrictedWriter)

	_, err := runner.Enqueue(CreateRunInput{WorkflowID: "policy-workflow", Inputs: map[string]string{"model": "model-c"}})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("disallowed override error = %v, want ErrInvalid", err)
	}
	for _, want := range []string{`model override "model-c" rejected`, `step "profile.write"`, `agent profile "writer" does not allow model "model-c"`, "allowed: model-a, model-b"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("rejection %q does not contain %q", err.Error(), want)
		}
	}
	if runs := store.ListRuns(); len(runs) != 0 {
		t.Fatalf("a rejected override created runs: %+v", runs)
	}
	if _, err := runner.Enqueue(CreateRunInput{WorkflowID: "policy-workflow", Inputs: map[string]string{"model": "two words"}}); err == nil || !strings.Contains(err.Error(), "single token") {
		t.Fatalf("malformed override error = %v", err)
	}

	overridden, err := runner.RunOnce(context.Background(), CreateRunInput{WorkflowID: "policy-workflow", Inputs: map[string]string{"model": "model-b"}})
	if err != nil {
		t.Fatalf("allowed override error = %v", err)
	}
	if executor.model != "model-b" {
		t.Fatalf("executor model = %q, want the override", executor.model)
	}
	detail, _ := store.RunDetail(overridden.ID)
	if got := detail.Steps[0].CorrelationIDs["agent_profile_model_source"]; got != RunModelSourceOverride {
		t.Fatalf("override model source = %q", got)
	}

	inherited, err := runner.RunOnce(context.Background(), CreateRunInput{WorkflowID: "policy-workflow"})
	if err != nil {
		t.Fatal(err)
	}
	detail, _ = store.RunDetail(inherited.ID)
	if executor.model != "model-a" || detail.Steps[0].CorrelationIDs["agent_profile_model_source"] != RunModelSourceProfileDefault {
		t.Fatalf("inherited model = %q correlation = %+v", executor.model, detail.Steps[0].CorrelationIDs)
	}
}

func TestRunModelOverrideRejectedWhenNoStepAcceptsIt(t *testing.T) {
	store, runner, _ := policyTestRunner(t, PipelineStep{ID: "write", Position: 1, Name: "profile.write", Executor: "model", Profile: "writer", Model: "model-a"}, restrictedWriter)
	_, err := runner.Enqueue(CreateRunInput{WorkflowID: "policy-workflow", Inputs: map[string]string{"model": "model-b"}})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "has no step that accepts a run-level model override") {
		t.Fatalf("unused override error = %v", err)
	}
	if len(store.ListRuns()) != 0 {
		t.Fatal("rejected override created a run")
	}
	policy, ok := runner.WorkflowModelPolicy("policy-workflow", "")
	if !ok || policy.AcceptsOverride || policy.OverrideStatus != RunModelOverrideStatusNone {
		t.Fatalf("policy = %+v", policy)
	}
	step := policy.Steps[0]
	if step.Model != "model-a" || step.ModelSource != RunModelSourceWorkflow || step.InheritedModelSource != RunModelSourceWorkflow || step.AcceptsOverride {
		t.Fatalf("fixed-model step = %+v", step)
	}
}

func TestWorkflowModelPolicyIntersectsProfileAndExecutorAllowlists(t *testing.T) {
	t.Setenv("DEVLOG_MODEL", "m1")
	t.Setenv("DEVLOG_ALLOWED_MODELS", "m1,m2,m3")
	now := time.Unix(100, 0)
	store := NewStore(func() time.Time { now = now.Add(time.Second); return now })
	runner := NewRunner(store)

	policy, ok := runner.WorkflowModelPolicy(string(WorkflowDevlog), "")
	if !ok {
		t.Fatal("devlog has no pipeline policy")
	}
	var generate RunModelPolicyStep
	for _, step := range policy.Steps {
		if step.StepID == "generate-markdown" {
			generate = step
		}
	}
	if !generate.AcceptsOverride || !generate.OverrideRestricted || !reflect.DeepEqual(generate.AllowedOverrides, []string{"m1", "m2", "m3"}) {
		t.Fatalf("generate step = %+v", generate)
	}
	if generate.InheritedModel != "m1" || generate.InheritedModelSource != RunModelSourceExecutorDefault || generate.ProfileID != "devlog-summarizer" || generate.ProfileSource != AgentProfileSourceStatic || generate.ProfileCaptured != RunProfileCapturedSnapshot {
		t.Fatalf("generate inherited policy = %+v", generate)
	}

	draft := AgentProfileDraftInput{Name: "Devlog Summarizer", Role: "summarizer", DefaultModel: "m2", AllowedModels: []string{"m2", "m9"}, AllowedExecutors: []string{"model"}, PromptContract: "Summarize."}
	saved, err := store.SaveAgentProfileDraft("devlog-summarizer", draft, "token:admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateAgentProfileVersion("devlog-summarizer", AgentProfileActivationInput{Version: saved.Version}, "token:admin"); err != nil {
		t.Fatal(err)
	}
	policy, _ = runner.WorkflowModelPolicy(string(WorkflowDevlog), "m3")
	if !reflect.DeepEqual(policy.AllowedOverrides, []string{"m2"}) || policy.OverrideStatus != RunModelOverrideStatusRejected {
		t.Fatalf("intersected policy = %+v", policy)
	}
	if len(policy.Rejections) != 1 || !strings.Contains(policy.Rejections[0], `does not allow model "m3"`) || !strings.Contains(policy.Rejections[0], "allowed: m2") {
		t.Fatalf("rejections = %+v", policy.Rejections)
	}
	policy, _ = runner.WorkflowModelPolicy(string(WorkflowDevlog), "m9")
	if policy.OverrideStatus != RunModelOverrideStatusRejected || !strings.Contains(strings.Join(policy.Rejections, ";"), `model "m9" is not allowed for workflow devlog`) {
		t.Fatalf("executor allowlist rejection = %+v", policy)
	}
	policy, _ = runner.WorkflowModelPolicy(string(WorkflowDevlog), "m2")
	if policy.OverrideStatus != RunModelOverrideStatusAccepted || len(policy.Rejections) != 0 {
		t.Fatalf("accepted override = %+v", policy)
	}
	if _, ok := runner.WorkflowModelPolicy("no-such-workflow", ""); ok {
		t.Fatal("unknown workflow returned a policy")
	}
}

func TestRunModelPolicyKeepsOriginalProfileAfterLaterActivation(t *testing.T) {
	now := time.Unix(100, 0)
	store := NewStore(func() time.Time { now = now.Add(time.Second); return now })
	assertRunKeepsOriginalProfilePolicy(t, store)
}

// assertRunKeepsOriginalProfilePolicy is shared by the memory and PostgreSQL
// tests: a queued run keeps its snapshot profile version, model, and model
// source after a later activation, before and after it executes.
func assertRunKeepsOriginalProfilePolicy(t *testing.T, store Store) {
	t.Helper()
	const workflowID = "policy-retention"
	const profileID = "policy-retention-writer"
	if err := store.UpsertWorkflow(Workflow{ID: workflowID, Kind: workflowID, Name: "Policy Retention"}); err != nil {
		t.Fatal(err)
	}
	executor := &profileRecordingExecutor{}
	runner := NewRunner(store, MapExecutorRegistry{workflowID: executor})
	runner.pipelines = StaticPipelineRegistry{workflowID: {ID: workflowID, WorkflowID: workflowID, Steps: []PipelineStep{{ID: "write", Position: 1, Name: "retention.write", Executor: "model", Profile: profileID}}}}
	draft := func(expected int, contract string, models ...string) AgentProfileDraftInput {
		return AgentProfileDraftInput{ExpectedVersion: expected, Name: "Writer", Role: "writer", DefaultModel: models[0], AllowedModels: models, AllowedExecutors: []string{"model"}, PromptContract: contract}
	}
	v1, err := store.SaveAgentProfileDraft(profileID, draft(0, "contract v1", "model-a", "model-b"), "token:admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateAgentProfileVersion(profileID, AgentProfileActivationInput{Version: v1.Version}, "token:admin"); err != nil {
		t.Fatal(err)
	}
	queued, err := runner.Enqueue(CreateRunInput{WorkflowID: workflowID, Inputs: map[string]string{"model": "model-b"}})
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	v2, err := store.SaveAgentProfileDraft(profileID, draft(1, "contract v2", "model-z"), "token:admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateAgentProfileVersion(profileID, AgentProfileActivationInput{Version: v2.Version}, "token:admin"); err != nil {
		t.Fatal(err)
	}

	// The new active version would reject the queued run's override.
	if _, err := runner.Enqueue(CreateRunInput{WorkflowID: workflowID, Inputs: map[string]string{"model": "model-b"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("override under v2 error = %v", err)
	}

	stored, err := store.RunDetail(queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	before := runner.RunModelPolicy(stored.Run, stored.Steps)
	if before == nil || before.OverrideStatus != RunModelOverrideStatusAccepted || len(before.Steps) != 1 {
		t.Fatalf("queued policy = %+v", before)
	}
	if step := before.Steps[0]; step.ProfileVersion != 1 || step.ProfileCaptured != RunProfileCapturedSnapshot || step.Model != "model-b" || step.ModelSource != RunModelSourceOverride || step.Recorded || step.InheritedModel != "model-a" {
		t.Fatalf("queued step policy = %+v", step)
	}

	started, err := store.StartRun(queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.ExecuteRun(context.Background(), started); err != nil {
		t.Fatalf("ExecuteRun() error = %v", err)
	}
	if executor.model != "model-b" || executor.contract != "contract v1" {
		t.Fatalf("executed with model %q contract %q, want the v1 snapshot", executor.model, executor.contract)
	}
	finished, err := store.RunDetail(queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	after := runner.RunModelPolicy(finished.Run, finished.Steps)
	if step := after.Steps[0]; !step.Recorded || step.ProfileVersion != 1 || step.Model != "model-b" || step.ModelSource != RunModelSourceOverride || step.ProfileContractSHA256 != v1.PromptContractSHA256 {
		t.Fatalf("recorded step policy = %+v", step)
	}
	preview, _ := runner.WorkflowModelPolicy(workflowID, "")
	if preview.Steps[0].ProfileVersion != 2 || preview.Steps[0].InheritedModel != "model-z" {
		t.Fatalf("new-run preview = %+v", preview.Steps[0])
	}
}

func TestRunModelPolicyIsNilWithoutPipelineSnapshot(t *testing.T) {
	var runner *Runner
	if runner.RunModelPolicy(Run{}, nil) != nil {
		t.Fatal("nil runner returned a policy")
	}
	if NewRunner(NewStore(nil)).RunModelPolicy(Run{ID: "legacy"}, nil) != nil {
		t.Fatal("run without snapshot returned a policy")
	}
}
