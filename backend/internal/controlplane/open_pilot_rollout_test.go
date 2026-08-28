package controlplane

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

func testOpenPilotProofTargets(manifest OpenPilotRuntimeManifest) []OpenPilotProofTarget {
	targets := make([]OpenPilotProofTarget, 0, 2)
	for index, repository := range manifest.Repositories[:2] {
		targets = append(targets, OpenPilotProofTarget{
			RepositoryID: repository.ID, ExecutorGeneration: repository.ExecutorGeneration,
			RepositorySlug: repository.Slug, Issue: string(rune('1' + index)),
		})
	}
	return targets
}

func TestDurableOpenPilotProofGateAndClaimFence(t *testing.T) {
	now := time.Unix(1_000, 0).UTC()
	store := NewStore(func() time.Time { return now })
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	manifest, _ := testMultiRepositoryRuntimeManifest(t)
	targets := testOpenPilotProofTargets(manifest)
	expires := now.Add(time.Hour)
	status, err := store.ConfigureOpenPilotRollout(ConfigureOpenPilotRolloutInput{
		Mode: "proof", RegistryFingerprint: manifest.RegistryFingerprint,
		ProofTargets: targets, ExpiresAt: &expires,
	})
	if err != nil || status.Mode != "proof" || len(status.ProofTargets) != 2 {
		t.Fatalf("proof status=%+v error=%v", status, err)
	}
	registry, err := NewOpenPilotRepositoryRegistry(manifest)
	if err != nil {
		t.Fatal(err)
	}
	gate := &OpenPilotAdmissionGate{registry: registry, mode: "durable", rollout: store}
	if _, err := gate.Resolve(targets[0].RepositorySlug, targets[0].Issue); err != nil {
		t.Fatalf("proof target rejected: %v", err)
	}
	if _, err := gate.Resolve(targets[0].RepositorySlug, "99"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("non-proof target error=%v", err)
	}

	disallowed, err := store.CreateRun(trustedOpenPilotInput(targets[0].RepositorySlug, "99", CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": targets[0].RepositorySlug, "issue": "99"}, OpenPilotTarget: &OpenPilotTarget{RepositoryID: targets[0].RepositoryID, RepositorySlug: targets[0].RepositorySlug, Issue: "99", ExecutorGeneration: targets[0].ExecutorGeneration}}))
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := store.CreateRun(trustedOpenPilotInput(targets[0].RepositorySlug, targets[0].Issue, CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": targets[0].RepositorySlug, "issue": targets[0].Issue}, OpenPilotTarget: &OpenPilotTarget{RepositoryID: targets[0].RepositoryID, RepositorySlug: targets[0].RepositorySlug, Issue: targets[0].Issue, ExecutorGeneration: targets[0].ExecutorGeneration}}))
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimNextRun("worker", time.Minute)
	if err != nil || claimed.ID != allowed.ID || claimed.ID == disallowed.ID {
		t.Fatalf("claimed=%+v error=%v", claimed, err)
	}
}

func TestDurableOpenPilotProofFailureAndExpiryCloseGate(t *testing.T) {
	now := time.Unix(2_000, 0).UTC()
	store := NewStore(func() time.Time { return now })
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	manifest, _ := testMultiRepositoryRuntimeManifest(t)
	targets := testOpenPilotProofTargets(manifest)
	expires := now.Add(time.Hour)
	if _, err := store.ConfigureOpenPilotRollout(ConfigureOpenPilotRolloutInput{Mode: "proof", RegistryFingerprint: manifest.RegistryFingerprint, ProofTargets: targets, ExpiresAt: &expires}); err != nil {
		t.Fatal(err)
	}
	target := OpenPilotTarget{RepositoryID: targets[0].RepositoryID, RepositorySlug: targets[0].RepositorySlug, Issue: targets[0].Issue, ExecutorGeneration: targets[0].ExecutorGeneration}
	if _, _, err := store.ClaimAndCreateOpenPilotInitialRun(target.RepositorySlug, target.Issue, trustedOpenPilotInput(target.RepositorySlug, target.Issue, CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": target.RepositorySlug, "issue": target.Issue}, OpenPilotTarget: &target})); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionOpenPilotIssueState(targets[0].RepositorySlug, targets[0].Issue, OpenPilotPhaseFailed); err != nil {
		t.Fatal(err)
	}
	status, err := store.OpenPilotRolloutStatus()
	if err != nil || status.Mode != "disabled" || status.ClosedReason == "" {
		t.Fatalf("failed proof status=%+v error=%v", status, err)
	}

	if _, err := store.ConfigureOpenPilotRollout(ConfigureOpenPilotRolloutInput{Mode: "proof", RegistryFingerprint: manifest.RegistryFingerprint, ProofTargets: targets, ExpiresAt: &expires}); err != nil {
		t.Fatal(err)
	}
	now = expires.Add(time.Second)
	status, err = store.OpenPilotRolloutStatus()
	if err != nil || status.Mode != "disabled" || status.ClosedReason != "proof expired" {
		t.Fatalf("expired proof status=%+v error=%v", status, err)
	}
}

func TestDurableOpenPilotAllowRequiresBothSuccessfulProofTargets(t *testing.T) {
	now := time.Unix(3_000, 0).UTC()
	store := NewStore(func() time.Time { return now })
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	manifest, _ := testMultiRepositoryRuntimeManifest(t)
	targets := testOpenPilotProofTargets(manifest)
	expires := now.Add(time.Hour)
	input := ConfigureOpenPilotRolloutInput{Mode: "proof", RegistryFingerprint: manifest.RegistryFingerprint, ProofTargets: targets, ExpiresAt: &expires}
	if _, err := store.ConfigureOpenPilotRollout(input); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfigureOpenPilotRollout(ConfigureOpenPilotRolloutInput{Mode: "allow", RegistryFingerprint: manifest.RegistryFingerprint}); !errors.Is(err, ErrConflict) {
		t.Fatalf("premature allow error=%v", err)
	}
	store.mu.Lock()
	for _, target := range targets {
		store.openPilotIssueStates[openPilotIssueStateKey(target.RepositorySlug, target.Issue)] = OpenPilotIssueState{
			ID: "state-" + target.RepositoryID, Repo: target.RepositorySlug, Issue: target.Issue,
			RepositoryID: target.RepositoryID, ExecutorGeneration: target.ExecutorGeneration,
			CanonicalRepo: target.RepositorySlug, CanonicalIssue: target.Issue,
			Phase: OpenPilotPhaseIssueClosed, IssueClosed: true, CreatedAt: now, UpdatedAt: now,
		}
	}
	store.mu.Unlock()
	status, err := store.ConfigureOpenPilotRollout(ConfigureOpenPilotRolloutInput{Mode: "allow", RegistryFingerprint: manifest.RegistryFingerprint})
	if err != nil || status.Mode != "allow" || len(status.ProofTargets) != 2 {
		t.Fatalf("allow status=%+v error=%v", status, err)
	}
}

func TestPostgresDurableOpenPilotProofClaimFenceAndFailureClosure(t *testing.T) {
	databaseURL := os.Getenv("SWITCHYARD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set SWITCHYARD_TEST_DATABASE_URL to run PostgreSQL rollout gate integration")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	db, err := OpenPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(context.Background(), `update open_pilot_rollout_control set mode='legacy',registry_fingerprint='',proof_targets='[]'::jsonb,expires_at=null,closed_reason='',generation=generation+1,updated_at=clock_timestamp() where singleton`)
	store := NewPostgresStore(db, time.Now)
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	manifest, _ := testMultiRepositoryRuntimeManifest(t)
	baseIssue := time.Now().UnixNano()
	targets := testOpenPilotProofTargets(manifest)
	targets[0].Issue = fmt.Sprint(baseIssue)
	targets[1].Issue = fmt.Sprint(baseIssue + 1)
	expires := time.Now().UTC().Add(time.Hour)
	if _, err := store.ConfigureOpenPilotRollout(ConfigureOpenPilotRolloutInput{Mode: "proof", RegistryFingerprint: manifest.RegistryFingerprint, ProofTargets: targets, ExpiresAt: &expires}); err != nil {
		t.Fatal(err)
	}
	disallowedIssue := fmt.Sprint(baseIssue + 99)
	disallowedTarget := OpenPilotTarget{RepositoryID: targets[0].RepositoryID, RepositorySlug: targets[0].RepositorySlug, Issue: disallowedIssue, ExecutorGeneration: targets[0].ExecutorGeneration}
	disallowed, err := store.CreateRun(trustedOpenPilotInput(disallowedTarget.RepositorySlug, disallowedTarget.Issue, CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": disallowedTarget.RepositorySlug, "issue": disallowedTarget.Issue}, OpenPilotTarget: &disallowedTarget}))
	if err != nil {
		t.Fatal(err)
	}
	allowedTarget := OpenPilotTarget{RepositoryID: targets[0].RepositoryID, RepositorySlug: targets[0].RepositorySlug, Issue: targets[0].Issue, ExecutorGeneration: targets[0].ExecutorGeneration}
	allowed, err := store.CreateRun(trustedOpenPilotInput(allowedTarget.RepositorySlug, allowedTarget.Issue, CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": allowedTarget.RepositorySlug, "issue": allowedTarget.Issue}, OpenPilotTarget: &allowedTarget}))
	if err != nil {
		t.Fatal(err)
	}
	var claimed Run
	for range 100 {
		claimed, err = store.ClaimNextRun("proof-worker", time.Minute)
		if err != nil || claimed.ID == allowed.ID || claimed.ID == disallowed.ID {
			break
		}
	}
	if err != nil || claimed.ID != allowed.ID || claimed.ID == disallowed.ID {
		t.Fatalf("claimed=%+v allowed=%s disallowed=%s error=%v", claimed, allowed.ID, disallowed.ID, err)
	}
	if _, _, err := store.ClaimAndCreateOpenPilotInitialRun(allowedTarget.RepositorySlug, allowedTarget.Issue, trustedOpenPilotInput(allowedTarget.RepositorySlug, allowedTarget.Issue, CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": allowedTarget.RepositorySlug, "issue": allowedTarget.Issue}, OpenPilotTarget: &allowedTarget})); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionOpenPilotIssueState(allowedTarget.RepositorySlug, allowedTarget.Issue, OpenPilotPhaseFailed); err != nil {
		t.Fatal(err)
	}
	status, err := store.OpenPilotRolloutStatus()
	if err != nil || status.Mode != "disabled" || status.ClosedReason == "" {
		t.Fatalf("failed proof status=%+v error=%v", status, err)
	}
}
