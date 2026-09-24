package controlplane

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestPostgresWorkflowDefinitionVersionContract runs the shared version
// contract against PostgreSQL and checks that migration 036 backfills the
// active version of definitions stored before it. It skips without
// SWITCHYARD_TEST_DATABASE_URL; run `make postgres-test`.
func TestPostgresWorkflowDefinitionVersionContract(t *testing.T) {
	databaseURL := os.Getenv("SWITCHYARD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set SWITCHYARD_TEST_DATABASE_URL to run PostgreSQL workflow definition version tests")
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
	const workflowID = "pg-versions-contract"
	const legacyID = "pg-versions-legacy"
	cleanup := func() {
		for _, id := range []string{workflowID, legacyID} {
			_, _ = db.ExecContext(context.Background(), `delete from runs where workflow_id=$1`, id)
			_, _ = db.ExecContext(context.Background(), `delete from workflow_definition_versions where id=$1`, id)
			_, _ = db.ExecContext(context.Background(), `delete from workflow_definitions where id=$1`, id)
			_, _ = db.ExecContext(context.Background(), `delete from workflows where id=$1`, id)
		}
	}
	cleanup()
	t.Cleanup(cleanup)
	now := time.Now().UTC().Truncate(time.Microsecond)
	store := NewPostgresStore(db, func() time.Time { now = now.Add(time.Second); return now })
	exerciseWorkflowDefinitionVersionContract(t, store, workflowID)

	// Rows written before migration 036 have no active_version. Re-running the
	// migration's backfill makes an active row serve its own version and
	// leaves a draft row without an active version.
	if _, err := store.SaveWorkflowDefinition(versionContractDefinition(legacyID, "legacy", "internal"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `update workflow_definitions set status='active', active_version=null where id=$1`, legacyID); err != nil {
		t.Fatal(err)
	}
	migration, err := migrationFS.ReadFile("migrations/036_workflow_definition_active_version.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, string(migration)); err != nil {
		t.Fatalf("re-apply migration 036: %v", err)
	}
	active, err := store.ActiveWorkflowDefinition(legacyID)
	if err != nil || active.Version != 1 || active.Status != "active" {
		t.Fatalf("backfilled active definition = %+v, %v", active, err)
	}
	if _, err := db.ExecContext(ctx, `update workflow_definitions set active_version=2 where id=$1`, legacyID); err == nil {
		t.Fatal("active_version beyond the latest version was accepted")
	}
}
