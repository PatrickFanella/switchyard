package controlplane

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testMultiRepositoryRuntimeManifest(t *testing.T) (OpenPilotRuntimeManifest, string) {
	t.Helper()
	root := t.TempDir()
	manifest := validOpenPilotRuntimeManifest()
	manifest.RegistryGeneration = "registry-v2"
	manifest.Repositories = nil
	for index, id := range []string{"alpha", "beta"} {
		source := filepath.Join(root, id, "source")
		worktrees := filepath.Join(root, id, "worktrees")
		if err := os.MkdirAll(source, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(worktrees, 0o700); err != nil {
			t.Fatal(err)
		}
		passwordPath := filepath.Join(root, id, "password")
		password := strings.Repeat(string(rune('a'+index)), 40)
		if err := os.WriteFile(passwordPath, []byte(password+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		manifest.Repositories = append(manifest.Repositories, OpenPilotRuntimeRepository{
			ID: id, Slug: "owner/" + id, Aliases: []string{"legacy/" + id},
			ExecutorGeneration: "generation-" + id, SourcePath: source, WorktreeRoot: worktrees,
			OpenCodeEndpoint: "http://opencode-" + id + ":4096", VerifierEndpoint: "http://verifier-" + id + ":8080",
			PasswordFile: passwordPath,
		})
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "manifest.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return manifest, path
}

func TestEnvOpenPilotAdmissionGateActivationModes(t *testing.T) {
	_, manifestPath := testMultiRepositoryRuntimeManifest(t)
	t.Setenv("SWITCHYARD_OPEN_PILOT_RUNTIME_MANIFEST", manifestPath)
	t.Setenv("OPEN_PILOT_REPO_PATH", "")
	t.Setenv("OPEN_PILOT_WORKDIR", "")

	t.Setenv("SWITCHYARD_OPEN_PILOT_ACTIVATION_MODE", "disabled")
	disabled, err := NewEnvOpenPilotAdmissionGate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := disabled.Resolve("owner/alpha", "1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("disabled Resolve error = %v", err)
	}

	t.Setenv("SWITCHYARD_OPEN_PILOT_ACTIVATION_MODE", "isolated-test")
	enabled, err := NewEnvOpenPilotAdmissionGate()
	if err != nil {
		t.Fatal(err)
	}
	for _, repository := range []string{"owner/alpha", "legacy/beta"} {
		if _, err := enabled.Resolve(repository, "7"); err != nil {
			t.Fatalf("isolated-test Resolve(%q) error = %v", repository, err)
		}
	}

	t.Setenv("SWITCHYARD_OPEN_PILOT_ACTIVATION_MODE", "allow-everything")
	if _, err := NewEnvOpenPilotAdmissionGate(); err == nil {
		t.Fatal("unknown activation mode configured a gate")
	}
}

func TestOpenPilotExecutionBundlesRouteByProtectedAffinity(t *testing.T) {
	manifest, manifestPath := testMultiRepositoryRuntimeManifest(t)
	t.Setenv("SWITCHYARD_OPEN_PILOT_RUNTIME_MANIFEST", manifestPath)
	t.Setenv("SWITCHYARD_OPEN_PILOT_ACTIVATION_MODE", "isolated-test")
	t.Setenv("OPEN_PILOT_REPO_PATH", "")
	t.Setenv("OPEN_PILOT_WORKDIR", "")

	env := NewEnvWorkflowExecutors()
	if env.openPilotGateError != nil {
		t.Fatal(env.openPilotGateError)
	}
	if len(env.openPilotBundles) != 2 {
		t.Fatalf("bundle count = %d", len(env.openPilotBundles))
	}
	for _, repository := range manifest.Repositories {
		run := Run{Inputs: map[string]string{
			"repo": repository.Slug, "issue": "9",
			openPilotRepositoryIDInputKey:       repository.ID,
			openPilotExecutorGenerationInputKey: repository.ExecutorGeneration,
		}}
		bundle, err := env.openPilotExecutionForRun(run)
		if err != nil {
			t.Fatal(err)
		}
		openCode := bundle.openCodeExecutor.(*EnvOpenCodeExecutor)
		runner := bundle.commandRunner.(*PolicyCommandRunner)
		worktrees := bundle.worktreeManager.(*GitOpenPilotWorktreeManager)
		if bundle.repository.ID != repository.ID || openCode.baseURL != repository.OpenCodeEndpoint || runner.verifierURL != repository.VerifierEndpoint || runner.policy.Workdir != repository.SourcePath || worktrees.repoPath != repository.SourcePath || worktrees.root != repository.WorktreeRoot {
			t.Fatalf("repository %s routed to mismatched bundle: %+v", repository.ID, bundle.repository)
		}
	}

	hostile := Run{Inputs: map[string]string{
		"repo": manifest.Repositories[0].Slug, "issue": "9",
		openPilotRepositoryIDInputKey:       manifest.Repositories[1].ID,
		openPilotExecutorGenerationInputKey: manifest.Repositories[1].ExecutorGeneration,
	}}
	if _, err := env.openPilotExecutionForRun(hostile); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mismatched affinity error = %v", err)
	}
}

func TestReadOpenPilotPasswordFileRejectsUnsafeInputs(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "password")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 40)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if password, err := readOpenPilotPasswordFile(path); err != nil || len(password) != 40 {
		t.Fatalf("valid password = %q, %v", password, err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readOpenPilotPasswordFile(path); err == nil {
		t.Fatal("public password file accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", path); err != nil {
		t.Fatal(err)
	}
	if _, err := readOpenPilotPasswordFile(path); err == nil {
		t.Fatal("password symlink accepted")
	}
}
