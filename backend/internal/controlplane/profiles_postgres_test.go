package controlplane

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

// TestPostgresStoreAgentProfileVersionContract exercises the durable
// draft/activate/rollback/audit contract against a real database. It skips
// without SWITCHYARD_TEST_DATABASE_URL, so plain `make verify` does not cover
// it; run `make postgres-test` for the isolated-container gate.
func TestPostgresStoreAgentProfileVersionContract(t *testing.T) {
	databaseURL := os.Getenv("SWITCHYARD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set SWITCHYARD_TEST_DATABASE_URL to run PostgreSQL agent profile integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := OpenPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatalf("OpenPostgres() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	const profileID = "pg-contract-writer"
	const definitionID = "pg-contract-profile-definition"
	cleanup := func() {
		_, _ = db.ExecContext(ctx, `delete from workflow_definition_versions where id=$1`, definitionID)
		_, _ = db.ExecContext(ctx, `delete from workflow_definitions where id=$1`, definitionID)
		_, _ = db.ExecContext(ctx, `delete from agent_profile_audit where profile_id=$1`, profileID)
		_, _ = db.ExecContext(ctx, `delete from agent_profiles where id=$1`, profileID)
	}
	cleanup()
	t.Cleanup(cleanup)
	now := time.Now().UTC().Truncate(time.Microsecond)
	store := NewPostgresStore(db, func() time.Time { now = now.Add(time.Second); return now })

	if _, err := store.AgentProfileIdentity(profileID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing identity error = %v", err)
	}
	if _, err := store.ListAgentProfileVersions(profileID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing versions error = %v", err)
	}
	if _, err := store.SaveAgentProfileDraft(profileID, AgentProfileDraftInput{Name: "x", Role: "r", AllowedExecutors: []string{"shell"}, PromptContract: "c"}, "a"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid executor error = %v", err)
	}
	v1, err := store.SaveAgentProfileDraft(profileID, writerDraft(0, "pg v1"), "token:admin")
	if err != nil {
		t.Fatalf("SaveAgentProfileDraft() error = %v", err)
	}
	if v1.Version != 1 || v1.Status != AgentProfileStatusDraft || v1.Source != AgentProfileSourcePersisted || v1.Provenance == nil || v1.Provenance.Actor != "token:admin" || len(v1.AllowedModels) != 1 {
		t.Fatalf("v1 = %+v", v1)
	}
	if _, err := store.SaveAgentProfileDraft(profileID, writerDraft(0, "stale"), "b"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale save error = %v", err)
	}

	// Racing saves against the same base version: one winner, the rest conflict.
	var wg sync.WaitGroup
	results := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.SaveAgentProfileDraft(profileID, writerDraft(1, "race"), "racer")
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatalf("race error = %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("race wins = %d", wins)
	}
	identity, err := store.AgentProfileIdentity(profileID)
	if err != nil || identity.LatestVersion != 2 || identity.DraftVersion != 2 || identity.ActiveVersion != 0 || identity.StaticDefault {
		t.Fatalf("identity = %+v err=%v", identity, err)
	}
	superseded, _ := store.AgentProfileVersion(profileID, 1)
	if superseded.Status != AgentProfileStatusSuperseded || superseded.PromptContract != "pg v1" {
		t.Fatalf("superseded draft = %+v", superseded)
	}
	if _, err := store.ActivateAgentProfileVersion(profileID, AgentProfileActivationInput{Version: 1}, "a"); !errors.Is(err, ErrConflict) {
		t.Fatalf("activate superseded error = %v", err)
	}
	if _, err := store.ActiveAgentProfile(profileID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no active yet, got %v", err)
	}
	active, err := store.ActivateAgentProfileVersion(profileID, AgentProfileActivationInput{Version: 2, Reason: "go"}, "token:admin")
	if err != nil {
		t.Fatalf("ActivateAgentProfileVersion() error = %v", err)
	}
	if active.Status != AgentProfileStatusActive || active.ActivatedAt == nil {
		t.Fatalf("active = %+v", active)
	}
	registry := NewStoreBackedAgentProfileRegistry(store, DefaultAgentProfileRegistry())
	if effective, ok := registry.Profile(profileID); !ok || effective.Version != 2 || effective.PromptContract != "race" {
		t.Fatalf("effective = %+v ok=%v", effective, ok)
	}
	listed, err := store.ListActiveAgentProfiles()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range listed {
		found = found || (item.ID == profileID && item.Version == 2)
	}
	if !found {
		t.Fatalf("active list = %+v", listed)
	}

	// Definitions may reference the persisted profile once active; snapshot captures it.
	definition := WorkflowDefinition{ID: definitionID, Name: "PG profile", Status: "active", Pipeline: PipelineDefinition{ID: definitionID + "-pipeline", WorkflowID: definitionID, Name: "P", Steps: []PipelineStepDefinition{{ID: "step", Position: 1, Name: "step", Executor: "model", Profile: profileID, Inputs: map[string]string{"prompt": "x"}}}}}
	if _, err := store.UpsertWorkflowDefinition(definition); err != nil {
		t.Fatalf("UpsertWorkflowDefinition() error = %v", err)
	}
	narrowed := writerDraft(2, "narrow")
	narrowed.AllowedExecutors = []string{"opencode"}
	v3, err := store.SaveAgentProfileDraft(profileID, narrowed, "a")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateAgentProfileConsumers(store, v3.AgentProfile); !errors.Is(err, ErrInvalid) {
		t.Fatalf("consumer validation error = %v", err)
	}

	// Rollback rules and the new-version semantics.
	if _, err := store.RollbackAgentProfile(profileID, AgentProfileActivationInput{Version: 2}, "a"); !errors.Is(err, ErrConflict) {
		t.Fatalf("rollback to active error = %v", err)
	}
	if _, err := store.RollbackAgentProfile(profileID, AgentProfileActivationInput{Version: 3}, "a"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("rollback to never-active error = %v", err)
	}
	v4, err := store.SaveAgentProfileDraft(profileID, writerDraft(3, "pg v4"), "a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateAgentProfileVersion(profileID, AgentProfileActivationInput{Version: v4.Version}, "a"); err != nil {
		t.Fatal(err)
	}
	restored, err := store.RollbackAgentProfile(profileID, AgentProfileActivationInput{Version: 2, Reason: "regression"}, "token:admin")
	if err != nil {
		t.Fatalf("RollbackAgentProfile() error = %v", err)
	}
	if restored.Version != 5 || restored.PromptContract != "race" || restored.Provenance == nil || restored.Provenance.RolledBackFromVersion != 2 || restored.Provenance.Origin != AgentProfileOriginRollback {
		t.Fatalf("restored = %+v", restored)
	}
	identity, _ = store.AgentProfileIdentity(profileID)
	if identity.ActiveVersion != 5 || identity.LatestVersion != 5 || identity.DraftVersion != 0 {
		t.Fatalf("identity after rollback = %+v", identity)
	}
	versions, err := store.ListAgentProfileVersions(profileID)
	if err != nil || len(versions) != 5 || versions[0].Version != 5 {
		t.Fatalf("versions = %+v err=%v", versions, err)
	}
	original, _ := store.AgentProfileVersion(profileID, 2)
	if original.PromptContract != "race" || original.Status != AgentProfileStatusSuperseded || original.CreatedAt.IsZero() {
		t.Fatalf("historical body changed: %+v", original)
	}
	audit, err := store.ListAgentProfileAudit(profileID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(audit) != 7 || audit[0].Action != AgentProfileAuditRolledBack || audit[0].PreviousActiveVersion != 4 || audit[0].Actor != "token:admin" || audit[len(audit)-1].Action != AgentProfileAuditDraftSaved {
		t.Fatalf("audit = %+v", audit)
	}
	limited, _ := store.ListAgentProfileAudit(profileID, 2)
	if len(limited) != 2 {
		t.Fatalf("limited audit = %+v", limited)
	}
	var activeRows int
	if err := db.QueryRowContext(ctx, `select count(*) from agent_profile_versions where profile_id=$1 and status='active'`, profileID).Scan(&activeRows); err != nil || activeRows != 1 {
		t.Fatalf("active rows = %d err=%v", activeRows, err)
	}
}
