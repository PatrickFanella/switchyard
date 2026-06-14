package controlplane

import (
	"context"
	"errors"
	"testing"
	"time"
)

type failingCommandRunner struct {
	result CommandResult
	err    error
}

func (f failingCommandRunner) Run(ctx context.Context, command string, workdir ...string) (CommandResult, error) {
	_ = ctx
	_ = command
	_ = workdir
	return f.result, f.err
}

type failingGiteaWriter struct{ err error }

func (f failingGiteaWriter) CreatePullRequest(ctx context.Context, input GiteaPullRequestInput) (GiteaPullRequest, error) {
	_ = ctx
	_ = input
	return GiteaPullRequest{}, f.err
}

func TestOpenPilotVerifyFailureDiagnosticsPersisted(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	env := newOpenPilotAIReviewTestEnv(store)
	env.commandRunner = failingCommandRunner{result: CommandResult{Output: "verification failed"}, err: errors.New("verification failed")}
	runner := &Runner{store: store}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123", "test_command": "make verify"}})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	_, err = runner.executePipeline(t.Context(), run, Pipeline{ID: "test", WorkflowID: string(WorkflowOpenPilot), Steps: []PipelineStep{{ID: "verify-command", Position: 5, Name: "open-pilot.verify-command", Executor: "command", Inputs: map[string]string{"command": "make verify"}}}}, openPilotExecutor{env: env}, openPilotExecutor{env: env})
	if err == nil {
		t.Fatal("expected verify failure")
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if state.FailingGate != "local_verify" || state.RecommendedNextAction != "rerun_verify" || state.OperatorSummary == "" {
		t.Fatalf("state = %+v", state)
	}
}

func TestOpenPilotPushFailureDiagnosticsPersisted(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	env := newOpenPilotAIReviewTestEnv(store)
	env.openPilotGitPushMode = "allow"
	runner := &Runner{store: store}
	if _, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "123", Phase: OpenPilotPhaseRequested, ResumeInputs: map[string]string{"resume.open-pilot.agent-work.response": "agent response"}}); err != nil {
		t.Fatalf("seed requested state error = %v", err)
	}
	if _, err := store.TransitionOpenPilotIssueState("OWNER/REPO", "123", OpenPilotPhaseFailed); err != nil {
		t.Fatalf("transition state to failed error = %v", err)
	}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	pipeline := Pipeline{ID: "test", WorkflowID: string(WorkflowOpenPilot), Steps: []PipelineStep{{ID: "push-branch", Position: 6, Name: "open-pilot.push-branch", Executor: "git"}}}
	_, err = runner.executePipeline(t.Context(), run, pipeline, openPilotExecutor{env: env}, openPilotExecutor{env: env})
	if err == nil {
		t.Fatal("expected push failure")
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if state.FailingGate != "git_push" || state.RecommendedNextAction != "fix_agent_commit" || state.OperatorSummary == "" {
		t.Fatalf("state = %+v", state)
	}
}

func TestOpenPilotPRFailureDiagnosticsPersisted(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	env := newOpenPilotAIReviewTestEnv(store)
	env.openPilotWriteMode = "allow"
	env.giteaWriter = failingGiteaWriter{err: errors.New("gitea token unauthorized")}
	runner := &Runner{store: store}
	if _, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "123", Phase: OpenPilotPhaseRequested, ResumeInputs: map[string]string{"resume.open-pilot.agent-work.response": "agent response"}}); err != nil {
		t.Fatalf("seed requested state error = %v", err)
	}
	if _, err := store.TransitionOpenPilotIssueState("OWNER/REPO", "123", OpenPilotPhaseFailed); err != nil {
		t.Fatalf("transition state to failed error = %v", err)
	}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	pipeline := Pipeline{ID: "test", WorkflowID: string(WorkflowOpenPilot), Steps: []PipelineStep{{ID: "pr-plan", Position: 7, Name: "open-pilot.pr-plan", Executor: "gitea"}}}
	_, err = runner.executePipeline(t.Context(), run, pipeline, openPilotExecutor{env: env}, openPilotExecutor{env: env})
	if err == nil {
		t.Fatal("expected PR failure")
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if state.FailingGate != "pull_request" || state.RecommendedNextAction != "check_gitea_token" || state.OperatorSummary == "" {
		t.Fatalf("state = %+v", state)
	}
}
