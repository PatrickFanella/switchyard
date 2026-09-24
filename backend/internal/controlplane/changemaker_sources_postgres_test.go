package controlplane

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestPostgresChangemakerSeedUpgradesUntouchedVersionOne checks the upgrade
// path used by deployed databases that store the shipped prompt-only version 1:
// seeding appends the grounded pipeline as version 2, keeps version 1 for
// rollback, round-trips the output contracts, and is idempotent. It skips
// without SWITCHYARD_TEST_DATABASE_URL; run `make postgres-test`.
func TestPostgresChangemakerSeedUpgradesUntouchedVersionOne(t *testing.T) {
	databaseURL := os.Getenv("SWITCHYARD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set SWITCHYARD_TEST_DATABASE_URL to run PostgreSQL Changemaker seed tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, err := OpenPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatalf("OpenPostgres() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	cleanup := func() {
		_, _ = db.ExecContext(context.Background(), `delete from workflow_definition_versions where id=$1`, changemakerDailyDigestID)
		_, _ = db.ExecContext(context.Background(), `delete from workflow_definitions where id=$1`, changemakerDailyDigestID)
	}
	cleanup()
	t.Cleanup(cleanup)
	store := NewPostgresStore(db, func() time.Time { return changemakerFixtureNow })
	legacy := WorkflowDefinition{ID: changemakerDailyDigestID, Name: "Changemaker Daily Digest", Description: "Generate a timed narrative digest from a configurable prompt.", Status: "active", Owns: []string{"prompt", "draft artifact", "timer"}, Uses: []string{"model-broker"}, InputShape: JSONShape{Type: "object", Properties: map[string]JSONShapeProperty{"topic": {Type: "string", Description: "Digest topic or scope"}, "prompt": {Type: "string", Description: "Writer prompt"}}, Required: []string{"topic", "prompt"}}, Pipeline: changemakerDailyDigestLegacyPipeline()}
	if _, err := store.UpsertWorkflowDefinition(legacy); err != nil {
		t.Fatalf("UpsertWorkflowDefinition(legacy) error = %v", err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := SeedDefaults(store); err != nil {
			t.Fatalf("SeedDefaults() attempt %d error = %v", attempt, err)
		}
	}
	current, err := store.WorkflowDefinition(changemakerDailyDigestID)
	if err != nil {
		t.Fatalf("WorkflowDefinition() error = %v", err)
	}
	if current.Version != 2 || current.Status != "active" || len(current.Pipeline.Steps) != 4 || current.Pipeline.Steps[1].Executor != "source" {
		t.Fatalf("current = %+v", current)
	}
	if err := validateWorkflowDefinitionForActivation(current); err != nil {
		t.Fatalf("stored grounded definition fails activation validation: %v", err)
	}
	pipeline := pipelineFromDefinition(current)
	for _, step := range pipeline.Steps[1:] {
		if step.OutputContract == nil || step.OutputContract.Version != 1 {
			t.Fatalf("step %s lost its output contract: %+v", step.ID, step.OutputContract)
		}
	}
	versions, err := store.ListWorkflowDefinitionVersions(changemakerDailyDigestID)
	if err != nil || len(versions) != 2 || versions[1].Version != 1 || len(versions[1].Pipeline.Steps) != 3 {
		t.Fatalf("versions = %+v err = %v", versions, err)
	}
}
