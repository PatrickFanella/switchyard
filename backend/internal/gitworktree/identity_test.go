package gitworktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidatedIdentitySurvivesWorktreePathAndGitFileSwap(t *testing.T) {
	repo, worktree := testLinkedWorktree(t)
	identity, err := ValidatePath(t.Context(), Expected{WorktreePath: worktree, CommonDir: filepath.Join(repo, ".git"), Branch: "topic"})
	if err != nil {
		t.Fatal(err)
	}
	defer identity.Close()

	moved := worktree + "-moved"
	if err := os.Rename(worktree, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: /tmp/attacker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "attacker"), []byte("replacement\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	output, err := identity.Command(t.Context(), nil, "status", "--porcelain=v1").CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v: %s", err, output)
	}
	if strings.Contains(string(output), "attacker") {
		t.Fatalf("Git reopened replacement path: %s", output)
	}
}

func TestAuthenticatedIdentityRejectsRedirectAndCredentialConfigWithoutValues(t *testing.T) {
	for _, test := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "push URL", key: "remote.origin.pushurl", value: "credential-marker"},
		{name: "instead of", key: "url.https://redirect.example/.insteadOf", value: "credential-marker"},
		{name: "push instead of", key: "url.https://redirect.example/.pushInsteadOf", value: "credential-marker"},
		{name: "credential helper", key: "credential.helper", value: "credential-marker"},
		{name: "HTTP header", key: "http.extraheader", value: "Authorization: credential-marker"},
		{name: "SSH command", key: "core.sshCommand", value: "credential-marker"},
		{name: "hooks path", key: "core.hooksPath", value: "credential-marker"},
		{name: "filesystem monitor", key: "core.fsmonitor", value: "credential-marker"},
		{name: "clean filter", key: "filter.danger.clean", value: "credential-marker"},
		{name: "external diff", key: "diff.external", value: "credential-marker"},
		{name: "merge driver", key: "merge.danger.driver", value: "credential-marker"},
		{name: "signing program", key: "gpg.program", value: "credential-marker"},
		{name: "SSH signing program", key: "gpg.ssh.program", value: "credential-marker"},
		{name: "TLS verification", key: "http.sslVerify", value: "false"},
		{name: "TLS certificate", key: "http.sslCert", value: "credential-marker"},
		{name: "TLS key", key: "http.sslKey", value: "credential-marker"},
		{name: "protocol override", key: "protocol.ext.allow", value: "always"},
		{name: "Git proxy", key: "core.gitProxy", value: "credential-marker"},
		{name: "remote upload pack", key: "remote.origin.uploadpack", value: "credential-marker"},
		{name: "text conversion", key: "diff.danger.textconv", value: "credential-marker"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, worktree := testLinkedWorktree(t)
			testGit(t, repo, "remote", "add", "origin", "https://git.example.test/owner/repo.git")
			testGit(t, repo, "config", "--local", test.key, test.value)
			identity, err := ValidatePath(t.Context(), Expected{
				WorktreePath: worktree, CommonDir: filepath.Join(repo, ".git"), Branch: "topic",
				Repo: "owner/repo", RequireOrigin: true, OriginBaseURL: "https://git.example.test",
			})
			if identity.Worktree != nil {
				_ = identity.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "redirect or credential") {
				t.Fatalf("error = %v", err)
			}
			if strings.Contains(err.Error(), test.value) {
				t.Fatalf("error exposed config value: %v", err)
			}
		})
	}
}

func TestAuthenticatedIdentityRejectsUnsafeWorktreeConfig(t *testing.T) {
	for _, test := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "TLS certificate", key: "http.sslCert", value: "credential-marker"},
		{name: "TLS key", key: "http.sslKey", value: "credential-marker"},
		{name: "SSH command", key: "core.sshCommand", value: "credential-marker"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, worktree := testLinkedWorktree(t)
			testGit(t, repo, "remote", "add", "origin", "https://git.example.test/owner/repo.git")
			testGit(t, repo, "config", "--local", "extensions.worktreeConfig", "true")
			testGit(t, worktree, "config", "--worktree", test.key, test.value)

			identity, err := ValidatePath(t.Context(), Expected{
				WorktreePath: worktree, CommonDir: filepath.Join(repo, ".git"), Branch: "topic",
				Repo: "owner/repo", RequireOrigin: true, OriginBaseURL: "https://git.example.test",
			})
			if identity.Worktree != nil {
				_ = identity.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "redirect or credential") {
				t.Fatalf("error = %v", err)
			}
			if strings.Contains(err.Error(), test.value) {
				t.Fatalf("error exposed config value: %v", err)
			}
		})
	}
}

func TestAuthenticatedIdentityRejectsSymlinkedWorktreeConfig(t *testing.T) {
	repo, worktree := testLinkedWorktree(t)
	testGit(t, repo, "config", "--local", "extensions.worktreeConfig", "true")
	gitFile, err := os.ReadFile(filepath.Join(worktree, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	gitdir, err := parseGitdir(string(gitFile))
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "config.worktree")
	if err := os.WriteFile(config, []byte("[http]\n\tsslCert = credential-marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(config, filepath.Join(gitdir, "config.worktree")); err != nil {
		t.Fatal(err)
	}

	identity, err := ValidatePath(t.Context(), Expected{
		WorktreePath: worktree, CommonDir: filepath.Join(repo, ".git"), Branch: "topic",
	})
	if identity.Worktree != nil {
		_ = identity.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "unable to inspect") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidatedIdentityRejectsSiblingMetadataBacklink(t *testing.T) {
	repo, worktree := testLinkedWorktree(t)
	gitFile, err := os.ReadFile(filepath.Join(worktree, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	gitdir, err := parseGitdir(string(gitFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitdir, "gitdir"), []byte(filepath.Join(filepath.Dir(worktree), "sibling", ".git")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	identity, err := ValidatePath(t.Context(), Expected{WorktreePath: worktree, CommonDir: filepath.Join(repo, ".git"), Branch: "topic"})
	if identity.Worktree != nil {
		_ = identity.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "backlink does not match") {
		t.Fatalf("error = %v", err)
	}
}

func TestSafeGitCommandDisablesExecutionAndRequiresTLSVerification(t *testing.T) {
	args := strings.Join(SafeCommandArgs(false), " ")
	for _, required := range []string{
		"core.hooksPath=/dev/null",
		"commit.gpgSign=false",
		"tag.gpgSign=false",
		"push.gpgSign=false",
		"http.sslVerify=true",
		"protocol.allow=never",
		"protocol.ext.allow=never",
		"core.gitProxy=",
	} {
		if !strings.Contains(args, required) {
			t.Fatalf("safe args missing %q: %s", required, args)
		}
	}
	for _, forbidden := range []string{"http.sslCert=", "http.sslKey="} {
		if strings.Contains(args, forbidden) {
			t.Fatalf("safe args contain broken TLS override %q: %s", forbidden, args)
		}
	}
	if !strings.Contains(strings.Join(SanitizedEnvironment(nil), "\n"), "GIT_TERMINAL_PROMPT=0") {
		t.Fatal("sanitized environment permits terminal prompts")
	}
}

func TestMatchingOriginRejectsURLMetadata(t *testing.T) {
	for _, origin := range []string{
		"https://user@git.example.test/owner/repo.git",
		"https://git.example.test/owner/repo.git?token=secret",
		"https://git.example.test/owner/repo.git#secret",
	} {
		if matchingOrigin(origin, "https://git.example.test", "owner/repo") {
			t.Fatalf("accepted origin %q", origin)
		}
	}
	for _, base := range []string{
		"https://user@git.example.test",
		"https://git.example.test?token=secret",
		"https://git.example.test#secret",
	} {
		if matchingOrigin("https://git.example.test/owner/repo.git", base, "owner/repo") {
			t.Fatalf("accepted base URL %q", base)
		}
	}
}

func testLinkedWorktree(t *testing.T) (string, string) {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	worktree := filepath.Join(t.TempDir(), "worktree")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	testGit(t, repo, "init", "--initial-branch=main")
	testGit(t, repo, "config", "user.name", "Switchyard Test")
	testGit(t, repo, "config", "user.email", "switchyard@example.test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, repo, "add", "README.md")
	testGit(t, repo, "commit", "-m", "initial")
	testGit(t, repo, "worktree", "add", "-b", "topic", worktree, "main")
	return repo, worktree
}

func testGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}
