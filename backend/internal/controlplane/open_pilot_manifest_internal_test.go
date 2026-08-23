package controlplane

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type manifestCommandRunner struct {
	results []CommandResult
	calls   int
}

func (r *manifestCommandRunner) Run(_ context.Context, _ string, _ ...string) (CommandResult, error) {
	if r.calls >= len(r.results) {
		return CommandResult{}, errors.New("unexpected command")
	}
	result := r.results[r.calls]
	r.calls++
	return result, nil
}

func TestOpenPilotVerifyCommandPersistsEqualManifest(t *testing.T) {
	runner := &manifestCommandRunner{results: []CommandResult{
		{Decision: CommandDecision{Allowed: true, Reason: "allowed"}, Executed: true, Manifest: "sha256:same"},
		{Decision: CommandDecision{Allowed: true, Reason: "allowed"}, Executed: true, Manifest: "sha256:same"},
	}}
	store := NewStore(time.Now)
	executor := openPilotExecutor{env: &EnvWorkflowExecutors{commandRunner: runner, store: store}}
	state := map[string]any{}
	out, err := executor.ExecutePipelineStep(t.Context(), Run{ID: "run-manifest", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "1", "test_command": "make verify"}}, PipelineStep{ID: "verify-command", Name: "open-pilot.verify-command"}, state)
	if err != nil {
		t.Fatalf("ExecutePipelineStep() error = %v", err)
	}
	if got := state["open-pilot.verify-command.manifest"]; got != "sha256:same" {
		t.Fatalf("state manifest = %v", got)
	}
	if got := out.CorrelationIDs["worktree_manifest"]; got != "sha256:same" {
		t.Fatalf("correlation manifest = %q", got)
	}
	saved, err := store.OpenPilotIssueState("OWNER/REPO", "1")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if got := saved.ResumeInputs["resume.open-pilot.verify-command.manifest"]; got != "sha256:same" {
		t.Fatalf("persisted manifest = %q", got)
	}
}

func TestOpenPilotVerifyCommandRejectsManifestMismatch(t *testing.T) {
	runner := &manifestCommandRunner{results: []CommandResult{
		{Decision: CommandDecision{Allowed: true, Reason: "allowed"}, Executed: true, Manifest: "sha256:before"},
		{Decision: CommandDecision{Allowed: true, Reason: "allowed"}, Executed: true, Manifest: "sha256:after"},
	}}
	executor := openPilotExecutor{env: &EnvWorkflowExecutors{commandRunner: runner}}
	state := map[string]any{}
	_, err := executor.ExecutePipelineStep(t.Context(), Run{Inputs: map[string]string{"test_command": "make verify"}}, PipelineStep{ID: "verify-command", Name: "open-pilot.verify-command"}, state)
	if err == nil || !strings.Contains(err.Error(), "manifest changed") {
		t.Fatalf("ExecutePipelineStep() error = %v", err)
	}
	if _, ok := state["open-pilot.verify-command.manifest"]; ok {
		t.Fatalf("mismatched manifest persisted: %+v", state)
	}
}

func TestOpenPilotVerifyCommandRejectsMissingManifest(t *testing.T) {
	runner := &manifestCommandRunner{results: []CommandResult{
		{Decision: CommandDecision{Allowed: true, Reason: "allowed"}, Executed: true},
		{Decision: CommandDecision{Allowed: true, Reason: "allowed"}, Executed: true, Manifest: "sha256:after"},
	}}
	executor := openPilotExecutor{env: &EnvWorkflowExecutors{commandRunner: runner}}
	_, err := executor.ExecutePipelineStep(t.Context(), Run{Inputs: map[string]string{"test_command": "make verify"}}, PipelineStep{ID: "verify-command", Name: "open-pilot.verify-command"}, map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "nonempty worktree manifests") {
		t.Fatalf("ExecutePipelineStep() error = %v", err)
	}
}

func TestOpenPilotAutoCommitRejectsMutationAfterVerify(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")
	worktreePath := filepath.Join(t.TempDir(), "worktree")
	if err := os.Mkdir(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}
	runManifestGit(t, repoPath, "init", "-b", "main")
	runManifestGit(t, repoPath, "config", "user.name", "Test")
	runManifestGit(t, repoPath, "config", "user.email", "test@example.test")
	if err := os.WriteFile(filepath.Join(repoPath, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runManifestGit(t, repoPath, "add", "tracked.txt")
	runManifestGit(t, repoPath, "commit", "-m", "base")
	runManifestGit(t, repoPath, "worktree", "add", "-b", "open-pilot/issue-1", worktreePath, "main")
	if err := os.WriteFile(filepath.Join(worktreePath, "generated.txt"), []byte("verified\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := OpenPilotWorktreePlan{Repo: "OWNER/REPO", RepoPath: repoPath, BaseBranch: "main", Branch: "open-pilot/issue-1", PlannedWorktreePath: worktreePath, WorktreePath: worktreePath, Executed: true}
	identity, err := validateOpenPilotWorktreeIdentity(t.Context(), plan.Repo, plan.RepoPath, plan.WorktreePath, plan.Branch, false)
	if err != nil {
		t.Fatalf("validate identity: %v", err)
	}
	manifest, err := identity.Manifest(t.Context())
	_ = identity.Close()
	if err != nil {
		t.Fatalf("Manifest() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, "generated.txt"), []byte("mutated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := &EnvWorkflowExecutors{openPilotAutoCommitMode: "allow"}
	_, err = env.openPilotCommitChanges(t.Context(), Run{Inputs: map[string]string{"issue": "1", "test_command": "make verify"}}, plan, map[string]any{
		"open-pilot.gate-status":             map[string]string{"local_verify": "passed"},
		"open-pilot.verify-command.manifest": manifest,
	})
	if err == nil || !strings.Contains(err.Error(), "changed after verification") {
		t.Fatalf("openPilotCommitChanges() error = %v", err)
	}
	cmd := exec.Command("git", "-C", worktreePath, "diff", "--cached", "--quiet")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mutation was staged before rejection: %v: %s", err, output)
	}
}

func runManifestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}
