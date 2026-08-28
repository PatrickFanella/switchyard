package controlplane

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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

func TestOpenPilotCancellationUsesRepositoryAffinity(t *testing.T) {
	var alphaCalls, betaCalls atomic.Int32
	alpha := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { alphaCalls.Add(1) }))
	defer alpha.Close()
	beta := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		betaCalls.Add(1)
		_, password, ok := request.BasicAuth()
		if !ok || password != strings.Repeat("b", 40) {
			response.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer beta.Close()

	manifest, manifestPath := testMultiRepositoryRuntimeManifest(t)
	manifest.Repositories[0].OpenCodeEndpoint = alpha.URL
	manifest.Repositories[1].OpenCodeEndpoint = beta.URL
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWITCHYARD_OPEN_PILOT_RUNTIME_MANIFEST", manifestPath)
	t.Setenv("SWITCHYARD_OPEN_PILOT_ACTIVATION_MODE", "isolated-test")
	t.Setenv("OPEN_PILOT_REPO_PATH", "")
	t.Setenv("OPEN_PILOT_WORKDIR", "")
	dispatcher := NewEnvCancelDispatcher()
	repository := manifest.Repositories[1]
	run := Run{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{
		"repo": repository.Slug, "issue": "9",
		openPilotRepositoryIDInputKey:       repository.ID,
		openPilotExecutorGenerationInputKey: repository.ExecutorGeneration,
	}}
	detail := RunDetail{Run: run, Steps: []RunStep{{Status: StepRunning, CorrelationIDs: map[string]string{"opencode_session_id": "session-beta"}}}}
	result := dispatcher.CancelRunDetail(t.Context(), detail)
	if alphaCalls.Load() != 0 || betaCalls.Load() != 1 {
		t.Fatalf("cancellation calls alpha=%d beta=%d events=%+v", alphaCalls.Load(), betaCalls.Load(), result.Events)
	}

	run.Inputs[openPilotRepositoryIDInputKey] = manifest.Repositories[0].ID
	result = dispatcher.CancelRunDetail(t.Context(), RunDetail{Run: run, Steps: detail.Steps})
	if betaCalls.Load() != 1 {
		t.Fatalf("hostile affinity reached executor: beta calls=%d", betaCalls.Load())
	}
	foundFailure := false
	for _, event := range result.Events {
		foundFailure = foundFailure || event.Type == "opencode.abort.failed"
	}
	if !foundFailure {
		t.Fatalf("hostile affinity events = %+v", result.Events)
	}
}

func TestOpenPilotRepositoryStatusProjectsEachExecutor(t *testing.T) {
	servers := make([]*httptest.Server, 0, 2)
	for index := range 2 {
		password := strings.Repeat(string(rune('a'+index)), 40)
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			if request.URL.Path == "/session" {
				_, supplied, ok := request.BasicAuth()
				if !ok || supplied != password {
					response.WriteHeader(http.StatusUnauthorized)
					return
				}
				response.Header().Set("Content-Type", "application/json")
				_, _ = response.Write([]byte("[]"))
				return
			}
			if request.URL.Path == "/healthz" {
				response.WriteHeader(http.StatusOK)
				return
			}
			response.WriteHeader(http.StatusNotFound)
		}))
		servers = append(servers, server)
		defer server.Close()
	}
	manifest, manifestPath := testMultiRepositoryRuntimeManifest(t)
	for index := range manifest.Repositories {
		manifest.Repositories[index].OpenCodeEndpoint = servers[index].URL
		manifest.Repositories[index].VerifierEndpoint = servers[index].URL
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWITCHYARD_OPEN_PILOT_RUNTIME_MANIFEST", manifestPath)
	t.Setenv("SWITCHYARD_OPEN_PILOT_ACTIVATION_MODE", "disabled")
	t.Setenv("OPEN_PILOT_REPO_PATH", "")
	t.Setenv("OPEN_PILOT_WORKDIR", "")
	status := NewEnvOpenPilotRepositoryStatusProvider().Status(t.Context())
	if !status.Configured || !status.Healthy || status.ActivationMode != "disabled" || len(status.Repositories) != 2 {
		t.Fatalf("repository status = %+v", status)
	}
	for index, repository := range status.Repositories {
		if !repository.Healthy || !repository.OpenCode.Healthy || !repository.VerifierHealthy || repository.MaxConcurrentVerification != 1 || repository.RepositoryID != manifest.Repositories[index].ID {
			t.Fatalf("repository status %d = %+v", index, repository)
		}
	}
}
