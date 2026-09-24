package controlplane

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestPostgresRunModelPolicyKeepsOriginalProfile checks that a run's durable
// pipeline snapshot keeps its profile version, override, and model source
// after a later activation. It skips without SWITCHYARD_TEST_DATABASE_URL; run
// `make postgres-test`.
func TestPostgresRunModelPolicyKeepsOriginalProfile(t *testing.T) {
	databaseURL := os.Getenv("SWITCHYARD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set SWITCHYARD_TEST_DATABASE_URL to run PostgreSQL run model policy tests")
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
		_, _ = db.ExecContext(context.Background(), `delete from runs where workflow_id=$1`, "policy-retention")
		_, _ = db.ExecContext(context.Background(), `delete from workflows where id=$1`, "policy-retention")
		_, _ = db.ExecContext(context.Background(), `delete from agent_profile_audit where profile_id=$1`, "policy-retention-writer")
		_, _ = db.ExecContext(context.Background(), `delete from agent_profiles where id=$1`, "policy-retention-writer")
	}
	cleanup()
	t.Cleanup(cleanup)
	now := time.Now().UTC().Truncate(time.Microsecond)
	assertRunKeepsOriginalProfilePolicy(t, NewPostgresStore(db, func() time.Time { now = now.Add(time.Second); return now }))
}
