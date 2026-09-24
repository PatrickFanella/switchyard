package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

const changemakerVersionColumns = "id, thread_id, version, run_id, coalesce(parent_version, 0), sources_run_id, source_mode, revision_notes, requested_by, created_at"
const changemakerDecisionColumns = "id, thread_id, version, run_id, artifact_id, draft_sha256, evidence_sha256, decision, notes, actor, adapter, destination, revision_run_id, created_at, expires_at"

func scanChangemakerVersion(row scanner) (ChangemakerDraftVersion, error) {
	var version ChangemakerDraftVersion
	if err := row.Scan(&version.ID, &version.ThreadID, &version.Version, &version.RunID, &version.ParentVersion, &version.SourcesRunID, &version.SourceMode, &version.RevisionNotes, &version.RequestedBy, &version.CreatedAt); err != nil {
		return ChangemakerDraftVersion{}, err
	}
	version.CreatedAt = version.CreatedAt.UTC()
	return version, nil
}

func scanChangemakerDecision(row scanner) (ChangemakerReviewDecision, error) {
	var decision ChangemakerReviewDecision
	if err := row.Scan(&decision.ID, &decision.ThreadID, &decision.Version, &decision.RunID, &decision.ArtifactID, &decision.DraftSHA256, &decision.EvidenceSHA256, &decision.Decision, &decision.Notes, &decision.Actor, &decision.Adapter, &decision.Destination, &decision.RevisionRunID, &decision.CreatedAt, &decision.ExpiresAt); err != nil {
		return ChangemakerReviewDecision{}, err
	}
	decision.CreatedAt = decision.CreatedAt.UTC()
	decision.ExpiresAt = decision.ExpiresAt.UTC()
	return decision, nil
}

type sqlQueryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func insertChangemakerVersion(ctx context.Context, db sqlQueryRower, version ChangemakerDraftVersion) (ChangemakerDraftVersion, error) {
	var parent any
	if version.ParentVersion > 0 {
		parent = version.ParentVersion
	}
	return scanChangemakerVersion(db.QueryRowContext(ctx, `insert into changemaker_draft_versions(id, thread_id, version, run_id, parent_version, sources_run_id, source_mode, revision_notes, requested_by, created_at)
		values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) returning `+changemakerVersionColumns,
		version.ID, version.ThreadID, version.Version, version.RunID, parent, version.SourcesRunID, version.SourceMode, version.RevisionNotes, version.RequestedBy, version.CreatedAt))
}

func insertChangemakerDecision(ctx context.Context, db sqlQueryRower, decision ChangemakerReviewDecision) (ChangemakerReviewDecision, error) {
	return scanChangemakerDecision(db.QueryRowContext(ctx, `insert into changemaker_review_decisions(`+changemakerDecisionColumns+`)
		values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) returning `+changemakerDecisionColumns,
		decision.ID, decision.ThreadID, decision.Version, decision.RunID, decision.ArtifactID, decision.DraftSHA256, decision.EvidenceSHA256, decision.Decision, decision.Notes, decision.Actor, decision.Adapter, decision.Destination, decision.RevisionRunID, decision.CreatedAt, decision.ExpiresAt))
}

func (s *PostgresStore) ChangemakerDraftVersionByRun(runID string) (ChangemakerDraftVersion, error) {
	version, err := scanChangemakerVersion(s.db.QueryRowContext(s.operationContext(), `select `+changemakerVersionColumns+` from changemaker_draft_versions where run_id=$1`, runID))
	if err != nil {
		return ChangemakerDraftVersion{}, mapScanErr(err)
	}
	return version, nil
}

func (s *PostgresStore) ListChangemakerDraftVersions(threadID string) ([]ChangemakerDraftVersion, error) {
	rows, err := s.db.QueryContext(s.operationContext(), `select `+changemakerVersionColumns+` from changemaker_draft_versions where thread_id=$1 order by version asc`, threadID)
	if err != nil {
		return nil, mapPgErr(err)
	}
	defer rows.Close()
	out := []ChangemakerDraftVersion{}
	for rows.Next() {
		version, err := scanChangemakerVersion(rows)
		if err != nil {
			return nil, mapPgErr(err)
		}
		out = append(out, version)
	}
	return out, mapPgErr(rows.Err())
}

// RecordChangemakerRevision locks the thread with a transaction-scoped
// advisory lock, persists version 1 if needed, checks that
// ExpectedLatestVersion is still the highest version, and writes the new
// version and its decision.
func (s *PostgresStore) RecordChangemakerRevision(input ChangemakerRevisionInput) (ChangemakerDraftVersion, ChangemakerReviewDecision, error) {
	input.Root.CreatedAt = input.Root.CreatedAt.UTC()
	input.Version.CreatedAt = input.Version.CreatedAt.UTC()
	input.Decision = normalizeChangemakerDecision(input.Decision)
	if err := validateChangemakerRevisionInput(input); err != nil {
		return ChangemakerDraftVersion{}, ChangemakerReviewDecision{}, err
	}
	ctx := s.operationContext()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ChangemakerDraftVersion{}, ChangemakerReviewDecision{}, mapPgErr(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `select pg_advisory_xact_lock(hashtextextended('switchyard.changemaker-thread:' || $1::text, 0))`, input.Root.ThreadID); err != nil {
		return ChangemakerDraftVersion{}, ChangemakerReviewDecision{}, mapPgErr(err)
	}
	var latest sql.NullInt64
	if err := tx.QueryRowContext(ctx, `select max(version) from changemaker_draft_versions where thread_id=$1`, input.Root.ThreadID).Scan(&latest); err != nil {
		return ChangemakerDraftVersion{}, ChangemakerReviewDecision{}, mapPgErr(err)
	}
	if !latest.Valid {
		if _, err := insertChangemakerVersion(ctx, tx, input.Root); err != nil {
			return ChangemakerDraftVersion{}, ChangemakerReviewDecision{}, mapChangemakerPgErr(err)
		}
		latest = sql.NullInt64{Int64: 1, Valid: true}
	}
	if int(latest.Int64) != input.ExpectedLatestVersion {
		return ChangemakerDraftVersion{}, ChangemakerReviewDecision{}, ErrConflict
	}
	version, err := insertChangemakerVersion(ctx, tx, input.Version)
	if err != nil {
		return ChangemakerDraftVersion{}, ChangemakerReviewDecision{}, mapChangemakerPgErr(err)
	}
	decision, err := insertChangemakerDecision(ctx, tx, input.Decision)
	if err != nil {
		return ChangemakerDraftVersion{}, ChangemakerReviewDecision{}, mapChangemakerPgErr(err)
	}
	if err := tx.Commit(); err != nil {
		return ChangemakerDraftVersion{}, ChangemakerReviewDecision{}, mapPgErr(err)
	}
	return version, decision, nil
}

func (s *PostgresStore) RecordChangemakerReviewDecision(decision ChangemakerReviewDecision) (ChangemakerReviewDecision, error) {
	decision = normalizeChangemakerDecision(decision)
	if err := validateChangemakerReviewDecision(decision); err != nil {
		return ChangemakerReviewDecision{}, err
	}
	if decision.Decision == ChangemakerDecisionRevisionRequested {
		return ChangemakerReviewDecision{}, ErrInvalid
	}
	stored, err := insertChangemakerDecision(s.operationContext(), s.db, decision)
	if err != nil {
		return ChangemakerReviewDecision{}, mapChangemakerPgErr(err)
	}
	return stored, nil
}

func (s *PostgresStore) ChangemakerReviewDecision(id string) (ChangemakerReviewDecision, error) {
	decision, err := scanChangemakerDecision(s.db.QueryRowContext(s.operationContext(), `select `+changemakerDecisionColumns+` from changemaker_review_decisions where id=$1`, id))
	if err != nil {
		return ChangemakerReviewDecision{}, mapScanErr(err)
	}
	return decision, nil
}

func (s *PostgresStore) ListChangemakerReviewDecisions(threadID string) ([]ChangemakerReviewDecision, error) {
	rows, err := s.db.QueryContext(s.operationContext(), `select `+changemakerDecisionColumns+` from changemaker_review_decisions where thread_id=$1 order by created_at desc, id desc limit $2`, threadID, changemakerReviewListLimit)
	if err != nil {
		return nil, mapPgErr(err)
	}
	defer rows.Close()
	out := []ChangemakerReviewDecision{}
	for rows.Next() {
		decision, err := scanChangemakerDecision(rows)
		if err != nil {
			return nil, mapPgErr(err)
		}
		out = append(out, decision)
	}
	return out, mapPgErr(rows.Err())
}

func (s *PostgresStore) BeginChangemakerPublication(input BeginDeliveryInput) (DeliveryReceipt, bool, error) {
	return s.beginDeliveryReceipt(changemakerPublicationReceiptKind, input)
}

func (s *PostgresStore) FinishChangemakerPublication(input FinishDeliveryInput) (DeliveryReceipt, error) {
	return s.finishDeliveryReceipt(changemakerPublicationReceiptKind, input)
}

func (s *PostgresStore) ChangemakerPublicationReceipt(idempotencyKey string) (DeliveryReceipt, error) {
	return s.deliveryReceipt(changemakerPublicationReceiptKind, idempotencyKey)
}

func (s *PostgresStore) ListChangemakerPublicationReceipts(runID string) ([]DeliveryReceipt, error) {
	return s.listDeliveryReceipts(changemakerPublicationReceiptKind, runID, changemakerReceiptListLimit)
}

// mapChangemakerPgErr reports duplicate identifiers or a concurrent version
// append as ErrConflict and constraint violations as ErrInvalid.
func mapChangemakerPgErr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && strings.HasPrefix(pgErr.ConstraintName, "changemaker_") {
		switch pgErr.Code {
		case "23505":
			return ErrConflict
		case "23514":
			return ErrInvalid
		}
	}
	return mapPgErr(err)
}
