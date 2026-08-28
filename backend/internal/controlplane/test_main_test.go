package controlplane

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"strings"
	"testing"
)

const openPilotTestCommitSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func resetIsolatedPostgresRuns(t *testing.T, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}) {
	t.Helper()
	if os.Getenv("SWITCHYARD_TEST_POSTGRES_ISOLATED") != "1" {
		return
	}
	if _, err := db.ExecContext(t.Context(), `delete from runs`); err != nil {
		t.Fatalf("reset isolated PostgreSQL runs: %v", err)
	}
}

func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "switchyard-open-pilot-test-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(root)
	for _, args := range [][]string{{"init", root}, {"-C", root, "remote", "add", "origin", "https://example.test/owner/repo.git"}} {
		if output, commandErr := exec.Command("git", args...).CombinedOutput(); commandErr != nil {
			panic(string(output))
		}
	}
	_ = os.Setenv("OPEN_PILOT_REPO_PATH", root)
	_ = os.Unsetenv("OPEN_PILOT_SINGLETON_REPOSITORY")
	os.Exit(m.Run())
}

func trustedOpenPilotInput(repo, issue string, input CreateRunInput) CreateRunInput {
	if input.OpenPilotTarget == nil {
		input.OpenPilotTarget = &OpenPilotTarget{RepositoryID: "legacy", RepositorySlug: strings.ToLower(repo), Issue: strings.TrimLeft(issue, "0"), ExecutorGeneration: "legacy"}
	}
	if input.Inputs == nil {
		input.Inputs = map[string]string{}
	}
	setOpenPilotAffinityInputs(input.Inputs, *input.OpenPilotTarget)
	return input
}

func configuredOpenPilotAdmissionGate(t *testing.T) *OpenPilotAdmissionGate {
	t.Helper()
	gate, err := NewEnvOpenPilotAdmissionGate()
	if err != nil {
		t.Fatal(err)
	}
	return gate
}

func openPilotAdmissionGateForTarget(t *testing.T, target OpenPilotTarget) *OpenPilotAdmissionGate {
	t.Helper()
	manifest := validOpenPilotRuntimeManifest()
	manifest.RegistryGeneration = target.ExecutorGeneration
	manifest.Repositories[0].ID = target.RepositoryID
	manifest.Repositories[0].Slug = target.RepositorySlug
	manifest.Repositories[0].Aliases = []string{}
	manifest.Repositories[0].ExecutorGeneration = target.ExecutorGeneration
	registry, err := NewOpenPilotRepositoryRegistry(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return &OpenPilotAdmissionGate{registry: registry, bound: manifest.Repositories[0]}
}
