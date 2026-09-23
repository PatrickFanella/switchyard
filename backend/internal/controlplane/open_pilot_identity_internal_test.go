package controlplane

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestAutoCommitRejectsReplacedWorktreeGitFile(t *testing.T) {
	_, _, plan := preparedCleanupWorktree(t)
	gitFile := filepath.Join(plan.WorktreePath, ".git")
	if err := os.Remove(gitFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(gitFile, 0o755); err != nil {
		t.Fatal(err)
	}
	executors := &EnvWorkflowExecutors{openPilotAutoCommitMode: "allow"}
	_, err := executors.openPilotCommitChanges(t.Context(), Run{}, plan, nil)
	if err == nil || !strings.Contains(err.Error(), "regular non-symlink file") {
		t.Fatalf("error = %v", err)
	}
}

func TestAuthenticatedPushRejectsWrongOriginBeforeNetwork(t *testing.T) {
	for _, token := range []string{"", "test-token"} {
		t.Run("token="+token, func(t *testing.T) {
			repo, _, branchPlan := preparedCleanupWorktree(t)
			runWorktreeTestGit(t, repo, "remote", "set-url", "origin", "https://git.example.test/other/repo.git")
			t.Setenv("GITEA_TOKEN", token)
			t.Setenv("GITEA_BASE_URL", "https://git.example.test")
			plan := OpenPilotPushPlan{Repo: branchPlan.Repo, RepoPath: branchPlan.RepoPath, Mode: "allow", Remote: "origin", Branch: branchPlan.Branch, WorktreePath: branchPlan.WorktreePath}
			if err := (&EnvWorkflowExecutors{}).executeOpenPilotPush(t.Context(), plan); err == nil || !strings.Contains(err.Error(), "origin does not match") {
				t.Fatalf("error = %v", err)
			}
			if _, err := (&EnvWorkflowExecutors{}).openPilotRemoteBranchExists(t.Context(), plan); err == nil || !strings.Contains(err.Error(), "origin does not match") {
				t.Fatalf("remote check error = %v", err)
			}
		})
	}
}

func TestAuthenticatedRemoteRequiresBaseURLWithoutLocalOverride(t *testing.T) {
	_, _, branchPlan := preparedCleanupWorktree(t)
	plan := OpenPilotPushPlan{Repo: branchPlan.Repo, RepoPath: branchPlan.RepoPath, Mode: "allow", Remote: "origin", Branch: branchPlan.Branch, WorktreePath: branchPlan.WorktreePath}
	if _, err := validateOpenPilotAuthenticatedWorktree(t.Context(), plan); err == nil || !strings.Contains(err.Error(), "GITEA_BASE_URL is required") {
		t.Fatalf("error = %v", err)
	}
	env, cleanup, err := openPilotGitCredentialEnv()
	defer cleanup()
	if err != nil || !slices.Contains(env, "GIT_TERMINAL_PROMPT=0") {
		t.Fatalf("credential env = %v, err = %v", env, err)
	}
}

func TestPushPlanIdentitySerializationAndResumeFallback(t *testing.T) {
	plan := OpenPilotPushPlan{Repo: "owner/repo", RepoPath: "/workspace", Mode: "allow", Remote: "origin", Branch: "branch", WorktreePath: "/workspace/worktree"}
	inputs := plan.StepInputs()
	if inputs["repo"] != plan.Repo || inputs["repo_path"] != plan.RepoPath {
		t.Fatalf("inputs = %#v", inputs)
	}
	resumed := openPilotPushPlanFromResume(map[string]string{
		"resume.open-pilot.push-branch.mode":          "allow",
		"resume.open-pilot.push-branch.remote":        "origin",
		"resume.open-pilot.push-branch.branch":        "branch",
		"resume.open-pilot.push-branch.worktree_path": "/workspace/worktree",
		"resume.open-pilot.branch-plan.repo":          "owner/repo",
		"resume.open-pilot.branch-plan.repo_path":     "/workspace",
	})
	if resumed.Repo != plan.Repo || resumed.RepoPath != plan.RepoPath {
		t.Fatalf("resumed = %#v", resumed)
	}
}

func TestRecoveredPushRejectsCleanMovedWorktreeHEAD(t *testing.T) {
	repo, _, plan := preparedCleanupWorktree(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	runWorktreeTestGit(t, t.TempDir(), "init", "--bare", remote)
	runWorktreeTestGit(t, repo, "remote", "set-url", "origin", remote)
	runWorktreeTestGit(t, plan.WorktreePath, "commit", "--allow-empty", "-m", "persisted commit")
	persistedSHA := strings.TrimSpace(worktreeTestGitOutput(t, plan.WorktreePath, "rev-parse", "HEAD"))
	runWorktreeTestGit(t, plan.WorktreePath, "commit", "--allow-empty", "-m", "moved head")

	t.Setenv("OPEN_PILOT_ALLOW_LOCAL_GIT_REMOTE", "true")
	store := NewStore(nil)
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	target := OpenPilotTarget{RepositoryID: "legacy", RepositorySlug: "owner/repo", Issue: "1", ExecutorGeneration: "legacy"}
	run, err := store.CreateRun(trustedOpenPilotInput("owner/repo", "1", CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "owner/repo", "issue": "1"}}))
	if err != nil {
		t.Fatal(err)
	}
	seedOpenPilotStateAtPhase(t, store, OpenPilotIssueState{Repo: "owner/repo", Issue: "1", RunID: run.ID, Branch: plan.Branch, Base: plan.BaseBranch, WorktreePath: plan.WorktreePath, ResumeInputs: map[string]string{"resume.open-pilot.commit-changes.sha": persistedSHA}}, OpenPilotPhaseCommitted)
	if _, err := store.BindOpenPilotIssueStateAffinity("owner/repo", "1", target); err != nil {
		t.Fatal(err)
	}
	run.AttemptCount = 2
	state := map[string]any{"open-pilot.branch-plan": plan, "open-pilot.commit-result": OpenPilotCommitResult{SHA: persistedSHA, Executed: true}}
	env := &EnvWorkflowExecutors{store: store, openPilotGate: openPilotAdmissionGateForTarget(t, target), openPilotGitPushMode: "allow"}
	_, err = (openPilotExecutor{env: env}).ExecutePipelineStep(t.Context(), run, PipelineStep{ID: "push-branch", Name: "open-pilot.push-branch"}, state)
	if err == nil || !strings.Contains(err.Error(), "HEAD changed after the persisted Open Pilot commit") {
		t.Fatalf("error = %v", err)
	}
}

func TestCommitReconcilesCrashAfterGitCommitWithoutDuplicate(t *testing.T) {
	t.Setenv("OPEN_PILOT_GIT_AUTHOR_NAME", "open-pilot")
	t.Setenv("OPEN_PILOT_GIT_AUTHOR_EMAIL", "bot@example.test")
	_, _, plan := preparedCleanupWorktree(t)
	if err := os.WriteFile(filepath.Join(plan.WorktreePath, "crash-reconcile.txt"), []byte("change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewStore(nil)
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": plan.Repo, "issue": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	seedOpenPilotStateAtPhase(t, store, OpenPilotIssueState{Repo: plan.Repo, Issue: "1", RunID: run.ID}, OpenPilotPhaseVerified)
	env := &EnvWorkflowExecutors{store: store, openPilotAutoCommitMode: "allow"}
	first, err := env.openPilotCommitChanges(t.Context(), run, plan, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := env.openPilotCommitChanges(t.Context(), run, plan, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(worktreeTestGitOutput(t, plan.WorktreePath, "log", "-1", "--format=%an <%ae>|%cn <%ce>")); got != "open-pilot <bot@example.test>|open-pilot <bot@example.test>" {
		t.Fatalf("commit identity = %q", got)
	}
	if !first.Executed || second.SHA != first.SHA {
		t.Fatalf("first = %+v, second = %+v", first, second)
	}
	if count := strings.TrimSpace(worktreeTestGitOutput(t, plan.WorktreePath, "rev-list", "--count", "HEAD")); count != "2" {
		t.Fatalf("commit count = %s", count)
	}
}

func worktreeTestGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
