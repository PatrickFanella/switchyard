package controlplane

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEnvOpenPilotAdmissionGateRequiresExactMountedOrigin(t *testing.T) {
	t.Setenv("OPEN_PILOT_REPO_PATH", "")
	t.Setenv("OPEN_PILOT_WORKDIR", "")
	if _, err := NewEnvOpenPilotAdmissionGate(); err == nil {
		t.Fatal("missing source configured a gate")
	}

	missingOrigin := testOpenPilotGitRepository(t, "")
	t.Setenv("OPEN_PILOT_REPO_PATH", missingOrigin)
	if _, err := NewEnvOpenPilotAdmissionGate(); err == nil {
		t.Fatal("missing origin configured a gate")
	}

	credentialOrigin := testOpenPilotGitRepository(t, "https://user:password@example.test/owner/repo.git")
	t.Setenv("OPEN_PILOT_REPO_PATH", credentialOrigin)
	if _, err := NewEnvOpenPilotAdmissionGate(); err == nil {
		t.Fatal("credential-bearing origin configured a gate")
	}

	repo := testOpenPilotGitRepository(t, "https://example.test/owner/repo.git")
	t.Setenv("OPEN_PILOT_REPO_PATH", repo)
	t.Setenv("OPEN_PILOT_SINGLETON_REPOSITORY", "Owner/Repo")
	if _, err := NewEnvOpenPilotAdmissionGate(); err == nil {
		t.Fatal("case-mismatched singleton configured a gate")
	}
	t.Setenv("OPEN_PILOT_SINGLETON_REPOSITORY", "owner/repo")
	gate, err := NewEnvOpenPilotAdmissionGate()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPEN_PILOT_REPO_PATH", testOpenPilotGitRepository(t, "https://example.test/other/repo.git"))
	if target, err := gate.Resolve("owner/repo", "1"); err != nil || target.RepositorySlug != "owner/repo" {
		t.Fatalf("existing gate changed identity: %+v, %v", target, err)
	}
	if _, err := gate.Resolve("other/repo", "1"); err == nil {
		t.Fatal("existing gate rebound after source change")
	}
}

func testOpenPilotGitRepository(t *testing.T, remote string) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", dir}, {"-C", dir, "remote", "add", "origin", remote}} {
		if remote == "" && len(args) > 2 {
			continue
		}
		if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	return dir
}

func testOpenPilotAdmissionGate(t *testing.T) *OpenPilotAdmissionGate {
	t.Helper()
	manifest := validOpenPilotRuntimeManifest()
	registry, err := NewOpenPilotRepositoryRegistry(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return &OpenPilotAdmissionGate{registry: registry, bound: manifest.Repositories[0]}
}

func TestOpenPilotAffinityMustBeBothEmptyOrValid(t *testing.T) {
	store := NewStore(nil)
	for _, state := range []OpenPilotIssueState{
		{Repo: "owner/repo", Issue: "1", RepositoryID: "repo", Phase: OpenPilotPhaseRequested},
		{Repo: "owner/repo", Issue: "2", RepositoryID: "Bad_ID", ExecutorGeneration: "generation", Phase: OpenPilotPhaseRequested},
	} {
		if _, err := store.UpsertOpenPilotIssueState(state); !errors.Is(err, ErrInvalid) {
			t.Fatalf("UpsertOpenPilotIssueState(%+v) error = %v", state, err)
		}
	}
	state, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: "owner/repo", Issue: "3", Phase: OpenPilotPhaseRequested})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BindOpenPilotIssueStateAffinity(state.Repo, state.Issue, OpenPilotTarget{RepositoryID: "repo", RepositorySlug: "owner/repo", Issue: "3", ExecutorGeneration: "generation"}); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryOpenPilotUpsertOwnsStateIDs(t *testing.T) {
	store := NewStore(nil)
	first, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: "owner/repo", Issue: "1", Phase: OpenPilotPhaseRequested})
	if err != nil || !openPilotIssueStateIDPattern.MatchString(first.ID) {
		t.Fatalf("generated state = %+v, %v", first, err)
	}
	for _, input := range []OpenPilotIssueState{
		{ID: "caller-controlled", Repo: "owner/other", Issue: "2", Phase: OpenPilotPhaseRequested},
		{ID: first.ID, Repo: "owner/other", Issue: "2", Phase: OpenPilotPhaseRequested},
	} {
		if _, err := store.UpsertOpenPilotIssueState(input); err == nil {
			t.Fatalf("duplicate or invalid ID accepted: %+v", input)
		}
	}
	updated := first
	updated.ID = "opi-caller-replacement"
	updated.Phase = OpenPilotPhaseValidated
	updated, err = store.UpsertOpenPilotIssueState(updated)
	if err != nil || updated.ID != first.ID {
		t.Fatalf("immutable ID update = %+v, %v", updated, err)
	}
}

func TestMemoryOpenPilotIDReuseCannotBypassAliasReservation(t *testing.T) {
	store := NewStore(nil)
	target, err := testOpenPilotAdmissionGate(t).Resolve("Subcult/Switchyard", "1")
	if err != nil {
		t.Fatal(err)
	}
	state, _, err := store.ClaimAndCreateOpenPilotInitialRun(target.RepositorySlug, target.Issue, CreateRunInput{WorkflowID: string(WorkflowOpenPilot), OpenPilotTarget: &target})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.UpsertOpenPilotIssueState(OpenPilotIssueState{ID: state.ID, Repo: "legacy/switchyard", Issue: "01", Phase: OpenPilotPhaseRequested})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("ID reuse error = %v", err)
	}
}

func TestUnconfiguredAdmissionGateRejectsEveryRepository(t *testing.T) {
	gate := &OpenPilotAdmissionGate{}
	for _, repo := range []string{"owner/first", "owner/second"} {
		if _, err := gate.Resolve(repo, "1"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Resolve(%q) error = %v", repo, err)
		}
	}
}

func TestOpenPilotConcurrentCanonicalAdmissionAllowsOne(t *testing.T) {
	store := NewStore(nil)
	runner := NewRunner(store, MapExecutorRegistry{})
	runner.SetOpenPilotAdmissionGate(testOpenPilotAdmissionGate(t))
	start := make(chan struct{})
	errorsFound := make(chan error, 2)
	var wait sync.WaitGroup
	for _, repo := range []string{"Subcult/Switchyard", "legacy/switchyard"} {
		wait.Add(1)
		go func(repo string) {
			defer wait.Done()
			<-start
			_, err := runner.Enqueue(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": repo, "issue": "01"}})
			errorsFound <- err
		}(repo)
	}
	close(start)
	wait.Wait()
	close(errorsFound)
	succeeded, rejected := 0, 0
	for err := range errorsFound {
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrConflict) || errors.Is(err, ErrInvalid) || errors.Is(err, ErrAlreadyClaimed) {
			rejected++
		} else {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("succeeded=%d rejected=%d", succeeded, rejected)
	}
}

func TestOpenPilotAdmissionCanonicalizesAndPersistsTrustedAffinity(t *testing.T) {
	store := NewStore(nil)
	runner := NewRunner(store, MapExecutorRegistry{})
	runner.SetOpenPilotAdmissionGate(testOpenPilotAdmissionGate(t))
	run, err := runner.Enqueue(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "LEGACY/SWITCHYARD", "issue": "01"}})
	if err != nil {
		t.Fatal(err)
	}
	if run.Inputs["repo"] != "Subcult/Switchyard" || run.Inputs["issue"] != "1" {
		t.Fatalf("run inputs = %v", run.Inputs)
	}
	state, err := store.OpenPilotIssueState("Subcult/Switchyard", "1")
	if err != nil {
		t.Fatal(err)
	}
	if state.RepositoryID != "switchyard" || state.ExecutorGeneration != "generation-one" || state.CanonicalRepo != "Subcult/Switchyard" || state.CanonicalIssue != "1" || state.LegacyRepo != "Subcult/Switchyard" {
		t.Fatalf("state = %+v", state)
	}
}

func TestOpenPilotAdmissionRejectsSecondRepoAndReservedInputs(t *testing.T) {
	store := NewStore(nil)
	runner := NewRunner(store, MapExecutorRegistry{})
	runner.SetOpenPilotAdmissionGate(testOpenPilotAdmissionGate(t))
	for _, input := range []CreateRunInput{
		{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "other/repo", "issue": "1"}},
		{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "Subcult/Switchyard", "issue": "1", "_switchyard.open_pilot.future_affinity": "forged"}},
		{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "Subcult/Switchyard", "issue": "1", openPilotRepositoryIDInputKey: "forged"}},
		{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "Subcult/Switchyard", "issue": "1", openPilotExecutorGenerationInputKey: "forged"}},
		{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "Subcult/Switchyard", "issue": "1", "executor_generation": "forged"}},
		{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "Subcult/Switchyard", "issue": "1", "resume.open-pilot.repository_id": "forged"}},
		{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "Subcult/Switchyard", "issue": "1", "open-pilot.executor-generation": "forged"}},
		{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "Subcult/Switchyard", "issue": "1", "_SWITCHYARD.open-pilot.repository-id": "forged"}},
		{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "Subcult/Switchyard", "issue": "1", openPilotLifecycleResumeInputKey: "true"}},
	} {
		if _, err := runner.Enqueue(input); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Enqueue(%v) error = %v", input.Inputs, err)
		}
	}
	if len(store.ListRuns()) != 0 || len(store.ListOpenPilotIssueStates()) != 0 {
		t.Fatal("rejected admission caused durable side effects")
	}
}

func TestReservedOpenPilotAffinityInputsAreWorkflowScoped(t *testing.T) {
	for _, key := range []string{"repository_id", "executor_generation", "canonical_repo"} {
		if err := validateCreateRunInput(CreateRunInput{WorkflowID: string(WorkflowDevlog), Inputs: map[string]string{key: "caller-owned"}}); err != nil {
			t.Fatalf("non-Open-Pilot input %q rejected: %v", key, err)
		}
	}
	if err := validateCreateRunInput(CreateRunInput{WorkflowID: string(WorkflowDevlog), Inputs: map[string]string{"api_token": "secret"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("sensitive non-Open-Pilot input error = %v", err)
	}
}

func TestOpenPilotCanonicalLockKeyIsNULFree(t *testing.T) {
	key := openPilotCanonicalLockKey("Subcult/Switchyard", "123")
	if key != "subcult/switchyard#123" || strings.IndexByte(key, 0) >= 0 {
		t.Fatalf("canonical lock key = %q", key)
	}
}

func TestOpenPilotEstablishedAffinityIsImmutable(t *testing.T) {
	store := NewStore(nil)
	state, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: "owner/repo", Issue: "1", Phase: OpenPilotPhaseRequested})
	if err != nil {
		t.Fatal(err)
	}
	target := OpenPilotTarget{RepositoryID: "repo", RepositorySlug: "owner/repo", Issue: "1", ExecutorGeneration: "generation"}
	state, err = store.BindOpenPilotIssueStateAffinity(state.Repo, state.Issue, target)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*OpenPilotIssueState){
		func(state *OpenPilotIssueState) { state.RepositoryID = "other" },
		func(state *OpenPilotIssueState) { state.ExecutorGeneration = "other" },
		func(state *OpenPilotIssueState) { state.CanonicalRepo = "other/repo" },
		func(state *OpenPilotIssueState) { state.LegacyRepo = "OTHER/REPO" },
	} {
		forged := state
		mutate(&forged)
		if _, err := store.UpsertOpenPilotIssueState(forged); !errors.Is(err, ErrConflict) {
			t.Fatalf("identity replacement error = %v", err)
		}
	}
}

func TestMemoryResumeRejectsEitherCanonicalCollisionAlias(t *testing.T) {
	store := NewStore(nil)
	target, err := testOpenPilotAdmissionGate(t).Resolve("Subcult/Switchyard", "1")
	if err != nil {
		t.Fatal(err)
	}
	_, run, err := store.ClaimAndCreateOpenPilotInitialRun("Subcult/Switchyard", "1", CreateRunInput{WorkflowID: string(WorkflowOpenPilot), OpenPilotTarget: &target})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(run.ID, RunFailed, "failed"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: "legacy/switchyard", Issue: "01", Phase: OpenPilotPhaseRequested}); !errors.Is(err, ErrConflict) {
		t.Fatalf("old alias writer collision error = %v", err)
	}
}

func TestOpenPilotCanonicalCollisionBlocksTerminalState(t *testing.T) {
	store := NewStore(nil)
	if _, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: "subcult/switchyard", Issue: "01", Phase: OpenPilotPhaseRequested}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionOpenPilotIssueState("subcult/switchyard", "01", OpenPilotPhaseFailed); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(store, MapExecutorRegistry{})
	runner.SetOpenPilotAdmissionGate(testOpenPilotAdmissionGate(t))
	if _, err := runner.Enqueue(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "Subcult/Switchyard", "issue": "1"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("collision error = %v", err)
	}
}

func TestOpenPilotAliasEquivalentCollisionsBothDirectionsAndTerminal(t *testing.T) {
	for _, test := range []struct {
		name, legacyRepo, admittedRepo string
	}{
		{name: "legacy alias then canonical", legacyRepo: "legacy/switchyard", admittedRepo: "Subcult/Switchyard"},
		{name: "legacy canonical then alias", legacyRepo: "subcult/switchyard", admittedRepo: "LEGACY/SWITCHYARD"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewStore(nil)
			if _, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: test.legacyRepo, Issue: "01", Phase: OpenPilotPhaseRequested}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.TransitionOpenPilotIssueState(test.legacyRepo, "01", OpenPilotPhaseFailed); err != nil {
				t.Fatal(err)
			}
			runner := NewRunner(store, MapExecutorRegistry{})
			runner.SetOpenPilotAdmissionGate(testOpenPilotAdmissionGate(t))
			if _, err := runner.Enqueue(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": test.admittedRepo, "issue": "1"}}); !errors.Is(err, ErrConflict) {
				t.Fatalf("alias-equivalent terminal collision error = %v", err)
			}
		})
	}
}

func TestPrepareOpenPilotStateRunInputReplacesOnlyDurableAffinity(t *testing.T) {
	store := NewStore(nil)
	runner := NewRunner(store, MapExecutorRegistry{})
	runner.SetOpenPilotAdmissionGate(testOpenPilotAdmissionGate(t))
	_, err := runner.Enqueue(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "legacy/switchyard", "issue": "01", "user.note": "preserve"}})
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.OpenPilotIssueState("Subcult/Switchyard", "1")
	if err != nil {
		t.Fatal(err)
	}
	inputs := cloneMap(state.ResumeInputs)
	inputs[openPilotRepositoryIDInputKey] = "hostile"
	inputs[openPilotExecutorGenerationInputKey] = "hostile"
	inputs[openPilotLifecycleResumeInputKey] = "true"
	prepared, err := runner.prepareOpenPilotStateRunInput(state, CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Inputs[openPilotRepositoryIDInputKey] != state.RepositoryID || prepared.Inputs[openPilotExecutorGenerationInputKey] != state.ExecutorGeneration || prepared.Inputs[openPilotLifecycleResumeInputKey] != "" || prepared.Inputs["user.note"] != "preserve" {
		t.Fatalf("prepared inputs = %+v", prepared.Inputs)
	}
	inputs["open-pilot.executor-generation"] = "hostile"
	if _, err := runner.prepareOpenPilotStateRunInput(state, CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: inputs}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("arbitrary reserved input error = %v", err)
	}
}

func TestMemoryAdoptsMigratedOpenPilotRunAffinityAtEarlyPhases(t *testing.T) {
	for _, phase := range []OpenPilotPhase{OpenPilotPhaseRequested, OpenPilotPhaseValidated} {
		t.Run(string(phase), func(t *testing.T) {
			store := NewStore(nil)
			if err := SeedDefaults(store); err != nil {
				t.Fatal(err)
			}
			run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "legacy/switchyard", "issue": "01"}})
			if err != nil {
				t.Fatal(err)
			}
			state, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: "legacy/switchyard", Issue: "01", RunID: run.ID, Phase: phase})
			if err != nil {
				t.Fatal(err)
			}
			run, err = store.ClaimNextRun("adopter", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			run, state, err = store.AdoptOpenPilotRunAffinity(run.ID, "adopter", run.AttemptCount, testOpenPilotAdmissionGate(t))
			if err != nil {
				t.Fatal(err)
			}
			if state.Phase != phase || state.Repo != "Subcult/Switchyard" || state.Issue != "1" || state.LegacyRepo != "legacy/switchyard" || state.LegacyIssue != "01" || state.RepositoryID != "switchyard" || state.CanonicalRepo != "Subcult/Switchyard" || run.Inputs["repo"] != state.Repo || run.Inputs["issue"] != state.Issue || run.Inputs[openPilotRepositoryIDInputKey] != state.RepositoryID || run.Inputs[openPilotExecutorGenerationInputKey] != state.ExecutorGeneration {
				t.Fatalf("adopted run=%+v state=%+v", run, state)
			}
		})
	}
}

func TestMemoryExactAffinityAdoptionRefreshesAliasReservations(t *testing.T) {
	store := NewStore(nil)
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	target, err := testOpenPilotAdmissionGate(t).Resolve("Subcult/Switchyard", "71")
	if err != nil {
		t.Fatal(err)
	}
	target.RepositoryCandidates = nil
	inputs := map[string]string{"repo": target.RepositorySlug, "issue": target.Issue}
	setOpenPilotAffinityInputs(inputs, target)
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: target.RepositorySlug, Issue: target.Issue, RunID: run.ID, Phase: OpenPilotPhaseValidated})
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	state.RepositoryID, state.ExecutorGeneration = target.RepositoryID, target.ExecutorGeneration
	state.CanonicalRepo, state.CanonicalIssue = target.RepositorySlug, target.Issue
	store.openPilotIssueStates[openPilotIssueStateKey(state.Repo, state.Issue)] = state
	store.openPilotReservations[state.ID] = []string{"legacy/switchyard", "subcult/switchyard"}
	store.mu.Unlock()
	run, err = store.ClaimNextRun("adopter", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AdoptOpenPilotRunAffinity(run.ID, "adopter", run.AttemptCount, openPilotAdmissionGateForTarget(t, target)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: "legacy/switchyard", Issue: "071", Phase: OpenPilotPhaseRequested}); !errors.Is(err, ErrConflict) {
		t.Fatalf("refreshed alias reservation error = %v", err)
	}
}

func TestOpenPilotCandidateUnionIsSortedAndNeverShrinks(t *testing.T) {
	got := unionOpenPilotCandidates([]string{"legacy/switchyard", "subcult/switchyard"}, []string{"Subcult/Switchyard", "new/switchyard"})
	want := []string{"legacy/switchyard", "new/switchyard", "subcult/switchyard"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("union = %v, want %v", got, want)
	}
}

func TestMemoryAdoptionRequiresExactLiveClaim(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	store := NewStore(func() time.Time { return now })
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "legacy/switchyard", "issue": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: "legacy/switchyard", Issue: "1", RunID: run.ID, Phase: OpenPilotPhaseRequested}); err != nil {
		t.Fatal(err)
	}
	run, _ = store.ClaimNextRun("worker", time.Minute)
	for _, claim := range []struct {
		worker  string
		attempt int
	}{{"other", run.AttemptCount}, {"worker", run.AttemptCount + 1}} {
		if _, _, err := store.AdoptOpenPilotRunAffinity(run.ID, claim.worker, claim.attempt, testOpenPilotAdmissionGate(t)); !errors.Is(err, ErrConflict) {
			t.Fatalf("claim %+v error = %v", claim, err)
		}
	}
	now = run.LeaseExpiresAt.Add(time.Second)
	if _, _, err := store.AdoptOpenPilotRunAffinity(run.ID, "worker", run.AttemptCount, testOpenPilotAdmissionGate(t)); !errors.Is(err, ErrConflict) {
		t.Fatalf("expired claim error = %v", err)
	}
	state, _ := store.OpenPilotIssueState("legacy/switchyard", "1")
	if state.RepositoryID != "" {
		t.Fatalf("stale adoption mutated state: %+v", state)
	}
}

func TestMemoryRejectsAffinityAdoptionForTerminalRun(t *testing.T) {
	store := NewStore(nil)
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "legacy/switchyard", "issue": "01"}})
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: "legacy/switchyard", Issue: "01", RunID: run.ID, Phase: OpenPilotPhaseRequested})
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	run.Status = RunFailed
	store.runs[run.ID] = run
	store.mu.Unlock()
	if _, _, err := store.AdoptOpenPilotRunAffinity(run.ID, "adopter", 1, testOpenPilotAdmissionGate(t)); !errors.Is(err, ErrConflict) {
		t.Fatalf("adoption error = %v", err)
	}
	saved, err := store.OpenPilotIssueState(state.Repo, state.Issue)
	if err != nil {
		t.Fatal(err)
	}
	if saved.RepositoryID != "" || saved.Phase != OpenPilotPhaseRequested {
		t.Fatalf("terminal run mutated state: %+v", saved)
	}
}

func TestMemoryUnsafeOpenPilotAdoptionPausesRunAndState(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*MemoryStore, Run, OpenPilotIssueState)
	}{
		{name: "stale run", mutate: func(store *MemoryStore, _ Run, state OpenPilotIssueState) {
			state.RunID = "stale"
			store.openPilotIssueStates[openPilotIssueStateKey(state.Repo, state.Issue)] = state
		}},
		{name: "half affinity", mutate: func(store *MemoryStore, run Run, _ OpenPilotIssueState) {
			run.Inputs[openPilotRepositoryIDInputKey] = "switchyard"
			store.runs[run.ID] = run
		}},
		{name: "alias collision", mutate: func(store *MemoryStore, _ Run, _ OpenPilotIssueState) {
			collision := OpenPilotIssueState{ID: newID("opi"), Repo: "Subcult/Switchyard", Issue: "1", CanonicalRepo: "subcult/switchyard", CanonicalIssue: "1", Phase: OpenPilotPhaseFailed}
			store.openPilotIssueStates[openPilotIssueStateKey(collision.Repo, collision.Issue)] = collision
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewStore(nil)
			if err := SeedDefaults(store); err != nil {
				t.Fatal(err)
			}
			run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "legacy/switchyard", "issue": "01"}})
			if err != nil {
				t.Fatal(err)
			}
			state, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: "legacy/switchyard", Issue: "01", RunID: run.ID, Phase: OpenPilotPhaseRequested})
			if err != nil {
				t.Fatal(err)
			}
			run, err = store.ClaimNextRun("adopter", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			store.mu.Lock()
			test.mutate(store, run, state)
			store.mu.Unlock()
			if _, _, err := store.AdoptOpenPilotRunAffinity(run.ID, "adopter", run.AttemptCount, testOpenPilotAdmissionGate(t)); !errors.Is(err, ErrConflict) {
				t.Fatalf("adoption error = %v", err)
			}
			detail, _ := store.RunDetail(run.ID)
			paused, _ := store.OpenPilotIssueState(state.Repo, state.Issue)
			if detail.Run.Status != RunPaused || paused.Phase != OpenPilotPhasePaused || paused.RecommendedNextAction != "manual_review" {
				t.Fatalf("run=%+v state=%+v", detail.Run, paused)
			}
		})
	}
}

func TestOpenPilotWorkerGateRejectsAffinityMismatchBeforeExecution(t *testing.T) {
	store := NewStore(nil)
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "Subcult/Switchyard", "issue": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: "Subcult/Switchyard", Issue: "1", Phase: OpenPilotPhaseRequested, RunID: run.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BindOpenPilotIssueStateAffinity(state.Repo, state.Issue, OpenPilotTarget{RepositoryID: "switchyard", RepositorySlug: "Subcult/Switchyard", Issue: "1", ExecutorGeneration: "wrong-generation"}); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(store, MapExecutorRegistry{})
	runner.SetOpenPilotAdmissionGate(testOpenPilotAdmissionGate(t))
	if _, err := runner.ExecuteRun(context.Background(), run); !errors.Is(err, ErrConflict) {
		t.Fatalf("ExecuteRun() error = %v", err)
	}
	if len(store.ListRunSteps(run.ID)) != 0 {
		t.Fatal("affinity mismatch reached pipeline execution")
	}
}

func TestOpenPilotWorkerPausesRunWithMissingDurableState(t *testing.T) {
	store := NewStore(nil)
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	target, err := testOpenPilotAdmissionGate(t).Resolve("Subcult/Switchyard", "1")
	if err != nil {
		t.Fatal(err)
	}
	inputs := map[string]string{"repo": target.RepositorySlug, "issue": target.Issue}
	setOpenPilotAffinityInputs(inputs, target)
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	run, err = store.StartRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(store, MapExecutorRegistry{})
	runner.SetOpenPilotAdmissionGate(testOpenPilotAdmissionGate(t))
	if _, err := runner.ExecuteRun(t.Context(), run); !errors.Is(err, ErrConflict) {
		t.Fatalf("ExecuteRun() error = %v", err)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil || detail.Run.Status != RunPaused || len(detail.Steps) != 0 {
		t.Fatalf("detail=%+v err=%v", detail, err)
	}
}
