package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgconn"
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

func (s *PostgresStore) lockRunAttemptFenceTx(tx *sql.Tx, runID string) error {
	fence, present, fenceErr := runAttemptFenceFromContext(s.operationContext())
	if !present {
		return nil
	}
	if fenceErr != nil || fence.RunID != runID {
		return ErrStaleRunAttempt
	}
	var live bool
	err := tx.QueryRowContext(s.operationContext(), `select status=$2 and claimed_by=$3 and attempt_count=$4 and lease_expires_at is not null and lease_expires_at > clock_timestamp() from runs where id=$1 for update`, runID, RunRunning, fence.WorkerID, fence.Attempt).Scan(&live)
	if err != nil || !live {
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return ErrStaleRunAttempt
	}
	return nil
}

func (s *PostgresStore) lockRunAttemptFenceForStepTx(tx *sql.Tx, stepID string) (string, error) {
	var runID string
	if err := tx.QueryRowContext(s.operationContext(), `select run_id from run_steps where id=$1`, stepID).Scan(&runID); err != nil {
		return "", mapScanErr(err)
	}
	return runID, s.lockRunAttemptFenceTx(tx, runID)
}
func (s *PostgresStore) Ready(ctx context.Context) error { return s.db.PingContext(ctx) }

const openPilotIssueStateColumns = "id, repo, issue, repository_id, executor_generation, canonical_repo, canonical_issue, legacy_repo, legacy_issue, phase, run_id, last_step_name, branch, base, worktree_path, pull_request_url, pull_request_id, pull_request_number, merge_commit_sha, issue_closed, gate_status, resume_inputs, repair_attempts, max_repair_attempts, last_error, failing_gate, operator_summary, recommended_next_action, created_at, updated_at"
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
	return mapPgErr(err)
}
func mapPgErr(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "open_pilot_canonical_reservation" {
		return ErrConflict
	}
	return fmt.Errorf("postgres store: %w", err)
}
