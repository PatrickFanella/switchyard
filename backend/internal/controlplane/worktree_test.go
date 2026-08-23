package controlplane

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEnvWorktreeManagerPrefersRepoPath(t *testing.T) {
	t.Setenv("OPEN_PILOT_REPO_PATH", "/preferred")
	t.Setenv("OPEN_PILOT_WORKDIR", "/legacy")

	manager := NewEnvOpenPilotWorktreeManager()
	if manager.repoPath != "/preferred" {
		t.Fatalf("repoPath = %q", manager.repoPath)
	}
}

func TestAllowWorktreeRequiresConfiguredPaths(t *testing.T) {
	for _, test := range []struct {
		name     string
		repoPath string
		root     string
		want     string
	}{
		{name: "missing repo", root: "/worktrees", want: "OPEN_PILOT_REPO_PATH"},
		{name: "missing root", repoPath: "/repo", want: "OPEN_PILOT_WORKTREE_ROOT"},
		{name: "relative repo", repoPath: "repo", root: "/worktrees", want: "must be absolute"},
		{name: "relative root", repoPath: "/repo", root: "worktrees", want: "must be absolute"},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := &GitOpenPilotWorktreeManager{mode: OpenPilotWorktreeAllow, repoPath: test.repoPath, root: test.root}
			_, err := manager.Prepare(t.Context(), Run{ID: "run-1"}, "owner/repo", "main", "branch")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestAllowWorktreeRejectsUnsafeConfiguredPaths(t *testing.T) {
	repo := initWorktreeTestRepo(t)
	nested := filepath.Join(repo, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name     string
		repoPath string
		root     string
		want     string
	}{
		{name: "filesystem root", repoPath: repo, root: "/", want: "must not be /"},
		{name: "same source and root", repoPath: repo, root: repo, want: "must differ"},
		{name: "source is not top-level", repoPath: nested, root: filepath.Join(t.TempDir(), "worktrees"), want: "is not Git top-level"},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := NewGitOpenPilotWorktreeManager(OpenPilotWorktreeAllow, test.repoPath, test.root)
			_, err := manager.Prepare(t.Context(), Run{ID: "run-1"}, "owner/repo", "main", "branch")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestAllowWorktreeCanonicalizesPaths(t *testing.T) {
	repo := initWorktreeTestRepo(t)
	container := t.TempDir()
	repoLink := filepath.Join(container, "repo-link")
	rootParent := filepath.Join(container, "root-parent")
	if err := os.Mkdir(rootParent, 0o755); err != nil {
		t.Fatal(err)
	}
	rootParentLink := filepath.Join(container, "root-parent-link")
	if err := os.Symlink(repo, repoLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(rootParent, rootParentLink); err != nil {
		t.Fatal(err)
	}

	manager := NewGitOpenPilotWorktreeManager(OpenPilotWorktreeAllow, repoLink, filepath.Join(rootParentLink, "worktrees"))
	plan, err := manager.Prepare(t.Context(), Run{ID: "run-1"}, "owner/repo", "main", "branch")
	if err != nil {
		t.Fatal(err)
	}
	if plan.RepoPath != repo {
		t.Fatalf("RepoPath = %q, want %q", plan.RepoPath, repo)
	}
	canonicalRoot := filepath.Join(rootParent, "worktrees")
	if err := requirePathWithin(canonicalRoot, plan.WorktreePath); err != nil {
		t.Fatalf("WorktreePath = %q: %v", plan.WorktreePath, err)
	}
	if !plan.Executed {
		t.Fatal("worktree was not created")
	}
}

func TestAllowWorktreeRejectsSymlinkedDestinationEscape(t *testing.T) {
	repo := initWorktreeTestRepo(t)
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "owner-repo")); err != nil {
		t.Fatal(err)
	}

	manager := NewGitOpenPilotWorktreeManager(OpenPilotWorktreeAllow, repo, root)
	_, err := manager.Prepare(t.Context(), Run{ID: "run-1"}, "owner/repo", "main", "branch")
	if err == nil || !strings.Contains(err.Error(), "planned worktree path") {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "run-1-branch")); !os.IsNotExist(err) {
		t.Fatalf("outside path was mutated: %v", err)
	}
}

func TestAllowWorktreeCreationCannotEscapeSwappedParent(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("fd-bound Git destination requires Linux /proc")
	}
	repo := initWorktreeTestRepo(t)
	root := t.TempDir()
	outside := t.TempDir()
	manager := NewGitOpenPilotWorktreeManager(OpenPilotWorktreeAllow, repo, root)
	parent := filepath.Join(root, "owner-repo")
	moved := filepath.Join(root, "held-parent")
	manager.beforeAdd = func() {
		if err := os.Rename(parent, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, parent); err != nil {
			t.Fatal(err)
		}
	}

	_, err := manager.Prepare(t.Context(), Run{ID: "run-1"}, "owner/repo", "main", "branch")
	if err == nil || !strings.Contains(err.Error(), "canonical intended path") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "run-1-branch")); !os.IsNotExist(statErr) {
		t.Fatalf("creation escaped through swapped parent: %v", statErr)
	}
}

func TestAllowWorktreeRejectsPreexistingDestinationLeaf(t *testing.T) {
	repo := initWorktreeTestRepo(t)
	root := t.TempDir()
	leaf := filepath.Join(root, "owner-repo", "run-1-branch")
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatal(err)
	}
	manager := NewGitOpenPilotWorktreeManager(OpenPilotWorktreeAllow, repo, root)
	_, err := manager.Prepare(t.Context(), Run{ID: "run-1"}, "owner/repo", "main", "branch")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %v", err)
	}
}

func TestAllowWorktreeRejectsInvalidOriginBeforeCreatingRoot(t *testing.T) {
	for _, test := range []struct {
		name        string
		origin      string
		breakConfig bool
		want        string
	}{
		{name: "missing", want: "missing or unreadable"},
		{name: "unreadable", breakConfig: true},
		{name: "unparseable", origin: "not-a-remote", want: "unparseable"},
		{name: "mismatched", origin: "https://git.example.test/other/repo.git", want: "does not match"},
		{name: "extra path", origin: "https://git.example.test/extra/owner/repo.git", want: "unparseable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := initWorktreeTestRepoWithoutOrigin(t)
			if test.origin != "" {
				runWorktreeTestGit(t, repo, "remote", "add", "origin", test.origin)
			}
			if test.breakConfig {
				if err := os.WriteFile(filepath.Join(repo, ".git", "config"), []byte("[broken\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			root := filepath.Join(t.TempDir(), "not-created")
			manager := NewGitOpenPilotWorktreeManager(OpenPilotWorktreeAllow, repo, root)
			_, err := manager.Prepare(t.Context(), Run{ID: "run-1"}, "owner/repo", "main", "branch")
			if err == nil || (test.want != "" && !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if _, statErr := os.Stat(root); !os.IsNotExist(statErr) {
				t.Fatalf("worktree root created before origin rejection: %v", statErr)
			}
		})
	}
}

func TestAllowWorktreeRequiresExactRequestedRepo(t *testing.T) {
	repo := initWorktreeTestRepo(t)
	for _, requested := range []string{"OWNER/repo", "owner/repo/extra", " owner/repo", "owner/repo.git"} {
		t.Run(requested, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "not-created")
			manager := NewGitOpenPilotWorktreeManager(OpenPilotWorktreeAllow, repo, root)
			_, err := manager.Prepare(t.Context(), Run{ID: "run-1"}, requested, "main", "branch")
			if err == nil {
				t.Fatalf("requested repo %q was accepted", requested)
			}
			if _, statErr := os.Stat(root); !os.IsNotExist(statErr) {
				t.Fatalf("worktree root created before repo rejection: %v", statErr)
			}
		})
	}
}

func TestCleanupRejectsRetargetedRoot(t *testing.T) {
	repo := initWorktreeTestRepo(t)
	container := t.TempDir()
	rootA := filepath.Join(container, "root-a")
	rootB := filepath.Join(container, "root-b")
	if err := os.Mkdir(rootA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(rootB, 0o755); err != nil {
		t.Fatal(err)
	}
	rootLink := filepath.Join(container, "root")
	if err := os.Symlink(rootA, rootLink); err != nil {
		t.Fatal(err)
	}
	manager := NewGitOpenPilotWorktreeManager(OpenPilotWorktreeAllow, repo, rootLink)
	manager.cleanupMode = OpenPilotWorktreeCleanupOnSuccess
	plan, err := manager.Prepare(t.Context(), Run{ID: "run-1"}, "owner/repo", "main", "branch")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(rootLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(rootB, rootLink); err != nil {
		t.Fatal(err)
	}

	_, err = manager.Cleanup(t.Context(), plan, true)
	if err == nil || !strings.Contains(err.Error(), "persisted worktree path") {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(plan.WorktreePath); err != nil {
		t.Fatalf("worktree was mutated: %v", err)
	}
}

func TestCleanupRejectsSymlinkSwappedWorktree(t *testing.T) {
	_, manager, plan := preparedCleanupWorktree(t)
	moved := filepath.Join(filepath.Dir(plan.WorktreePath), "moved")
	if err := os.Rename(plan.WorktreePath, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, plan.WorktreePath); err != nil {
		t.Fatal(err)
	}

	_, err := manager.Cleanup(t.Context(), plan, true)
	if err == nil || !strings.Contains(err.Error(), "lexical path") {
		t.Fatalf("error = %v", err)
	}
}

func TestCleanupRejectsWrongBranch(t *testing.T) {
	_, manager, plan := preparedCleanupWorktree(t)
	runWorktreeTestGit(t, plan.WorktreePath, "switch", "-c", "other-branch")

	_, err := manager.Cleanup(t.Context(), plan, true)
	if err == nil || !strings.Contains(err.Error(), "does not match planned branch") {
		t.Fatalf("error = %v", err)
	}
}

func TestCleanupRejectsUnregisteredPath(t *testing.T) {
	repo, manager, plan := preparedCleanupWorktree(t)
	runWorktreeTestGit(t, repo, "worktree", "remove", plan.WorktreePath)
	if err := os.MkdirAll(plan.WorktreePath, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := manager.Cleanup(t.Context(), plan, true)
	if err == nil || (!strings.Contains(err.Error(), "not registered") && !strings.Contains(err.Error(), "backlink does not match")) {
		t.Fatalf("error = %v", err)
	}
}

func TestCleanupRejectsChangedPersistedIdentity(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*OpenPilotWorktreePlan)
		want   string
	}{
		{
			name: "source",
			change: func(plan *OpenPilotWorktreePlan) {
				plan.RepoPath = filepath.Dir(plan.RepoPath)
			},
			want: "source repository path",
		},
		{
			name: "planned worktree",
			change: func(plan *OpenPilotWorktreePlan) {
				plan.PlannedWorktreePath += "-other"
			},
			want: "planned worktree path",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, manager, plan := preparedCleanupWorktree(t)
			test.change(&plan)
			_, err := manager.Cleanup(t.Context(), plan, true)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestCleanupRemovesRegisteredExpectedBranch(t *testing.T) {
	_, manager, plan := preparedCleanupWorktree(t)
	cleanup, err := manager.Cleanup(t.Context(), plan, true)
	if err != nil {
		t.Fatal(err)
	}
	if !cleanup.Executed {
		t.Fatalf("cleanup = %#v", cleanup)
	}
}

func TestManagedWorktreeIdentityRejectsTampering(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string, OpenPilotWorktreePlan)
		want   string
	}{
		{
			name: "replaced git file",
			mutate: func(t *testing.T, _ string, plan OpenPilotWorktreePlan) {
				gitFile := filepath.Join(plan.WorktreePath, ".git")
				if err := os.Remove(gitFile); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(gitFile, 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: "regular non-symlink file",
		},
		{
			name: "fake common directory",
			mutate: func(t *testing.T, _ string, plan OpenPilotWorktreePlan) {
				otherRepo := initWorktreeTestRepo(t)
				otherRoot := t.TempDir()
				otherManager := NewGitOpenPilotWorktreeManager(OpenPilotWorktreeAllow, otherRepo, otherRoot)
				other, err := otherManager.Prepare(t.Context(), Run{ID: "other"}, "owner/repo", "main", "other-branch")
				if err != nil {
					t.Fatal(err)
				}
				contents, err := os.ReadFile(filepath.Join(other.WorktreePath, ".git"))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(plan.WorktreePath, ".git"), contents, 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: "outside the configured common directory",
		},
		{
			name: "wrong branch",
			mutate: func(t *testing.T, _ string, plan OpenPilotWorktreePlan) {
				runWorktreeTestGit(t, plan.WorktreePath, "switch", "-c", "wrong-branch")
			},
			want: "does not match expected branch",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, _, plan := preparedCleanupWorktree(t)
			test.mutate(t, repo, plan)
			_, err := validateOpenPilotWorktreeIdentity(t.Context(), plan.Repo, plan.RepoPath, plan.WorktreePath, plan.Branch, false)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestManagedWorktreeIdentityRejectsUnregisteredPath(t *testing.T) {
	_, _, plan := preparedCleanupWorktree(t)
	copyPath := filepath.Join(filepath.Dir(plan.WorktreePath), "copy")
	if err := os.Mkdir(copyPath, 0o755); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(plan.WorktreePath, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(copyPath, ".git"), contents, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = validateOpenPilotWorktreeIdentity(t.Context(), plan.Repo, plan.RepoPath, copyPath, plan.Branch, false)
	if err == nil || (!strings.Contains(err.Error(), "not registered") && !strings.Contains(err.Error(), "backlink does not match")) {
		t.Fatalf("error = %v", err)
	}
}

func TestManagedWorktreeIdentityRequiresMatchingOriginForCredentials(t *testing.T) {
	repo, _, plan := preparedCleanupWorktree(t)
	t.Setenv("GITEA_BASE_URL", "https://git.example.test")
	runWorktreeTestGit(t, repo, "remote", "set-url", "origin", "https://git.example.test/other/repo.git")
	_, err := validateOpenPilotWorktreeIdentity(t.Context(), plan.Repo, plan.RepoPath, plan.WorktreePath, plan.Branch, true)
	if err == nil || !strings.Contains(err.Error(), "origin does not match") {
		t.Fatalf("error = %v", err)
	}
	runWorktreeTestGit(t, repo, "remote", "set-url", "origin", "https://git.example.test/owner/repo.git")
	if _, err := validateOpenPilotWorktreeIdentity(t.Context(), plan.Repo, plan.RepoPath, plan.WorktreePath, plan.Branch, true); err != nil {
		t.Fatal(err)
	}
	runWorktreeTestGit(t, repo, "remote", "set-url", "origin", "https://evil.example.test/owner/repo.git")
	if _, err := validateOpenPilotWorktreeIdentity(t.Context(), plan.Repo, plan.RepoPath, plan.WorktreePath, plan.Branch, true); err == nil {
		t.Fatal("same repository slug on an untrusted origin host was accepted")
	}
	runWorktreeTestGit(t, repo, "remote", "set-url", "origin", "https://git.example.test/extra/owner/repo.git")
	if _, err := validateOpenPilotWorktreeIdentity(t.Context(), plan.Repo, plan.RepoPath, plan.WorktreePath, plan.Branch, true); err == nil {
		t.Fatal("requested repository under an unexpected path prefix was accepted")
	}
}

func TestWorktreePrepareRejectsExecutableLocalConfigBeforeCheckout(t *testing.T) {
	repo := initWorktreeTestRepo(t)
	marker := filepath.Join(t.TempDir(), "executed")
	runWorktreeTestGit(t, repo, "config", "--local", "filter.danger.smudge", "touch "+marker)
	runWorktreeTestGit(t, repo, "config", "--local", "filter.danger.required", "true")
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("README.md filter=danger\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := NewGitOpenPilotWorktreeManager(OpenPilotWorktreeAllow, repo, t.TempDir())
	_, err := manager.Prepare(t.Context(), Run{ID: "unsafe-config"}, "owner/repo", "main", "unsafe-config")
	if err == nil || !strings.Contains(err.Error(), "redirect or credential") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("local filter executed before rejection: %v", statErr)
	}
}

func TestRequirePathWithinRejectsEscape(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "workspace", "worktrees")
	if err := requirePathWithin(root, filepath.Join(root, "repo", "run")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root, filepath.Join(root, "..", "outside")} {
		if err := requirePathWithin(root, path); err == nil {
			t.Fatalf("path %q accepted", path)
		}
	}
}

func initWorktreeTestRepo(t *testing.T) string {
	t.Helper()
	dir := initWorktreeTestRepoWithoutOrigin(t)
	runWorktreeTestGit(t, dir, "remote", "add", "origin", "https://git.example.test/owner/repo.git")
	return dir
}

func initWorktreeTestRepoWithoutOrigin(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required")
	}
	dir := t.TempDir()
	runWorktreeTestGit(t, dir, "init")
	runWorktreeTestGit(t, dir, "config", "user.email", "switchyard@example.test")
	runWorktreeTestGit(t, dir, "config", "user.name", "Switchyard Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runWorktreeTestGit(t, dir, "add", "README.md")
	runWorktreeTestGit(t, dir, "commit", "-m", "initial")
	runWorktreeTestGit(t, dir, "branch", "-M", "main")
	return dir
}

func preparedCleanupWorktree(t *testing.T) (string, *GitOpenPilotWorktreeManager, OpenPilotWorktreePlan) {
	t.Helper()
	repo := initWorktreeTestRepo(t)
	manager := NewGitOpenPilotWorktreeManager(OpenPilotWorktreeAllow, repo, t.TempDir())
	manager.cleanupMode = OpenPilotWorktreeCleanupOnSuccess
	plan, err := manager.Prepare(t.Context(), Run{ID: "run-1"}, "owner/repo", "main", "planned-branch")
	if err != nil {
		t.Fatal(err)
	}
	return repo, manager, plan
}

func runWorktreeTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}
