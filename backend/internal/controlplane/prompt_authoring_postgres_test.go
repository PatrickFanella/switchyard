package controlplane

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestPostgresPromptAuthoringContract runs the prompt lifecycle against
// PostgreSQL, which also checks that the workflow-definition version bound in a
// run's pipeline snapshot survives the jsonb round trip. It skips without
// SWITCHYARD_TEST_DATABASE_URL; run `make postgres-test`.
func TestPostgresPromptAuthoringContract(t *testing.T) {
	databaseURL := os.Getenv("SWITCHYARD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set SWITCHYARD_TEST_DATABASE_URL to run PostgreSQL prompt authoring tests")
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
	const workflowID = "pg-prompt-authoring"
	cleanup := func() {
		_, _ = db.ExecContext(context.Background(), `delete from runs where workflow_id=$1`, workflowID)
		_, _ = db.ExecContext(context.Background(), `delete from workflow_definition_versions where id=$1`, workflowID)
		_, _ = db.ExecContext(context.Background(), `delete from workflow_definitions where id=$1`, workflowID)
		_, _ = db.ExecContext(context.Background(), `delete from workflows where id=$1`, workflowID)
	}
	cleanup()
	t.Cleanup(cleanup)
	now := time.Now().UTC().Truncate(time.Microsecond)
	store := NewPostgresStore(db, func() time.Time { now = now.Add(time.Second); return now })
	exercisePromptAuthoringContract(t, store, workflowID)
}
