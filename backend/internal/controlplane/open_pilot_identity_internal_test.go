package controlplane

import (
	"os"
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
