package controlplane

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
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

type recordingGiteaWriter struct {
	input GiteaPullRequestInput
	calls int
}

func (w *recordingGiteaWriter) CreatePullRequest(_ context.Context, input GiteaPullRequestInput) (GiteaPullRequest, error) {
	w.calls++
	w.input = input
	return GiteaPullRequest{ID: 9, Number: 9, State: "open", HTMLURL: "https://git/owner/repo/pulls/9", Head: GiteaPullRequestRef{Ref: input.Head, SHA: openPilotTestCommitSHA, Repo: pullRequestRepoIdentity(input.Repo)}, Base: GiteaPullRequestRef{Ref: input.Base, SHA: "base123", Repo: pullRequestRepoIdentity(input.Repo)}}, nil
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
	env.giteaReader = &scriptedCIGiteaReader{}
	runner := &Runner{store: store}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	seedOpenPilotStateAtPhase(t, store, OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "123", RunID: run.ID, Branch: "open-pilot/issue-123", Base: "main", ResumeInputs: map[string]string{
		"resume.open-pilot.agent-work.response": "agent response", "resume.open-pilot.branch-plan.repo": "owner/repo",
		"resume.open-pilot.branch-plan.base_branch": "main", "resume.open-pilot.branch-plan.branch": "open-pilot/issue-123",
	}}, OpenPilotPhasePushed)
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

func TestOpenPilotPRUsesDurableTargetAndBase(t *testing.T) {
	store := NewStore(nil)
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "owner/repo", "issue": "1", "base_branch": "retargeted"}})
	if err != nil {
		t.Fatal(err)
	}
	resume := map[string]string{
		"resume.open-pilot.branch-plan.repo": "owner/repo", "resume.open-pilot.branch-plan.base_branch": "main", "resume.open-pilot.branch-plan.branch": "open-pilot/issue-1",
		"resume.open-pilot.push-branch.branch": "open-pilot/issue-1", "resume.open-pilot.push-branch.remote_head_exists": "true",
	}
	seedOpenPilotStateAtPhase(t, store, OpenPilotIssueState{Repo: "owner/repo", Issue: "1", RunID: run.ID, Branch: "open-pilot/issue-1", Base: "main", ResumeInputs: resume}, OpenPilotPhasePushed)
	writer := &recordingGiteaWriter{}
	env := newOpenPilotAIReviewTestEnv(store)
	env.openPilotWriteMode, env.giteaWriter = "allow", writer
	state := map[string]any{"open-pilot.branch-plan": OpenPilotWorktreePlan{Repo: "attacker/repo", Branch: "attacker", BaseBranch: "retargeted"}}
	_, err = (openPilotExecutor{env: env}).ExecutePipelineStep(t.Context(), run, PipelineStep{ID: "pr-plan", Name: "open-pilot.pr-plan"}, state)
	if err != nil {
		t.Fatal(err)
	}
	if writer.calls != 1 || writer.input.Repo != "owner/repo" || writer.input.Head != "open-pilot/issue-1" || writer.input.Base != "main" {
		t.Fatalf("calls/input = %d/%+v", writer.calls, writer.input)
	}
}

func TestOpenPilotPRReconcilesCrashAfterCreate(t *testing.T) {
	t.Setenv("OPEN_PILOT_ALLOW_LOCAL_GIT_REMOTE", "true")
	repoPath, _, plan := preparedCleanupWorktree(t)
	remotePath := filepath.Join(t.TempDir(), "remote.git")
	runWorktreeTestGit(t, t.TempDir(), "init", "--bare", remotePath)
	runWorktreeTestGit(t, repoPath, "remote", "set-url", "origin", remotePath)
	runWorktreeTestGit(t, plan.WorktreePath, "push", "origin", plan.Branch)
	commitSHA := strings.TrimSpace(runGitOutput(t, plan.WorktreePath, "rev-parse", "HEAD"))
	store := NewStore(nil)
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	target := OpenPilotTarget{RepositoryID: "legacy", RepositorySlug: plan.Repo, Issue: "1", ExecutorGeneration: "legacy"}
	run, err := store.CreateRun(trustedOpenPilotInput(plan.Repo, "1", CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": plan.Repo, "issue": "1"}}))
	if err != nil {
		t.Fatal(err)
	}
	run.AttemptCount = 2
	resume := map[string]string{
		"resume.open-pilot.branch-plan.repo": plan.Repo, "resume.open-pilot.branch-plan.repo_path": plan.RepoPath,
		"resume.open-pilot.branch-plan.base_branch": plan.BaseBranch, "resume.open-pilot.branch-plan.branch": plan.Branch,
		"resume.open-pilot.branch-plan.worktree_path": plan.WorktreePath, "resume.open-pilot.branch-plan.executed": "true",
		"resume.open-pilot.push-branch.branch": plan.Branch, "resume.open-pilot.push-branch.remote": "origin",
		"resume.open-pilot.push-branch.worktree_path": plan.WorktreePath, "resume.open-pilot.push-branch.remote_head_exists": "true",
		"resume.open-pilot.commit-changes.sha": commitSHA,
	}
	seedOpenPilotStateAtPhase(t, store, OpenPilotIssueState{Repo: plan.Repo, Issue: "1", RunID: run.ID, Branch: plan.Branch, Base: plan.BaseBranch, WorktreePath: plan.WorktreePath, ResumeInputs: resume}, OpenPilotPhasePushed)
	if _, err := store.BindOpenPilotIssueStateAffinity(plan.Repo, "1", target); err != nil {
		t.Fatal(err)
	}
	pr := GiteaPullRequest{ID: 7, Number: 7, State: "open", HTMLURL: "https://git/owner/repo/pulls/7", Head: GiteaPullRequestRef{Ref: plan.Branch, SHA: commitSHA, Repo: pullRequestRepoIdentity(plan.Repo)}, Base: GiteaPullRequestRef{Ref: plan.BaseBranch, SHA: "base123", Repo: pullRequestRepoIdentity(plan.Repo)}}
	writer := &recordingGiteaWriter{}
	env := newOpenPilotAIReviewTestEnv(store)
	env.openPilotGate = openPilotAdmissionGateForTarget(t, target)
	env.worktreeManager = NewEnvOpenPilotWorktreeManager()
	env.openPilotWriteMode, env.giteaWriter, env.giteaReader = "allow", writer, &scriptedCIGiteaReader{findPR: pr, findFound: true}
	state := map[string]any{"open-pilot.branch-plan": plan}
	_, err = (openPilotExecutor{env: env}).ExecutePipelineStep(t.Context(), run, PipelineStep{ID: "pr-plan", Name: "open-pilot.pr-plan"}, state)
	if err != nil || writer.calls != 0 {
		t.Fatalf("error/writer calls = %v/%d", err, writer.calls)
	}
	saved, err := store.OpenPilotIssueState(plan.Repo, "1")
	if err != nil || saved.PullRequestNum != pr.Number || saved.Phase != OpenPilotPhasePRCreated {
		t.Fatalf("saved = %+v, error = %v", saved, err)
	}
}
