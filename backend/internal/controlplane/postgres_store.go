package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	_ "github.com/jackc/pgx/v5/stdlib"
	"time"
)

type PostgresStore struct {
	db  *sql.DB
	now func() time.Time
	ctx context.Context
}

func (s *PostgresStore) WithContext(ctx context.Context) Store {
	copy := *s
	copy.ctx = ctx
	return &copy
}
func (s *PostgresStore) operationContext() context.Context {
	if s.ctx != nil {
		return s.ctx
	}
	return context.Background()
}
func (s *PostgresStore) Ready(ctx context.Context) error { return s.db.PingContext(ctx) }

const openPilotIssueStateColumns = "id, repo, issue, phase, run_id, last_step_name, branch, base, worktree_path, pull_request_url, pull_request_id, pull_request_number, merge_commit_sha, issue_closed, gate_status, resume_inputs, repair_attempts, max_repair_attempts, last_error, failing_gate, operator_summary, recommended_next_action, created_at, updated_at"
const workflowDefinitionColumns = "id, name, description, status, version, owns, uses, input_shape, pipeline, created_at, updated_at"
const modelBrokerRequestColumns = "id, status, model, path, client, client_priority, request_body, response_body, error, correlation_ids, created_at, started_at, finished_at, updated_at, cancel_requested_at"

func NewPostgresStore(db *sql.DB, now func() time.Time) *PostgresStore {
	if now == nil {
		now = time.Now
	}
	return &PostgresStore{db: db, now: now}
}
func OpenPostgres(ctx context.Context, databaseURL string) (*sql.DB, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

type scanner interface{ Scan(dest ...any) error }

func mapScanErr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
func mapPgErr(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("postgres store: %w", err)
}
