package controlplane

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

const devlogApprovalColumns = "id, run_id, artifact_id, artifact_sha256, adapter, destination, decision, actor, reason, created_at, expires_at"
const devlogReceiptColumns = "id, idempotency_key, run_id, artifact_id, artifact_sha256, adapter, destination, approval_id, status, attempts, reconciled, claim_token, external_id, external_url, last_error, requested_by, created_at, updated_at, delivered_at"

func scanDevlogApproval(row scanner) (DevlogDeliveryApproval, error) {
	var approval DevlogDeliveryApproval
	if err := row.Scan(&approval.ID, &approval.RunID, &approval.ArtifactID, &approval.ArtifactSHA256, &approval.Adapter, &approval.Destination, &approval.Decision, &approval.Actor, &approval.Reason, &approval.CreatedAt, &approval.ExpiresAt); err != nil {
		return DevlogDeliveryApproval{}, err
	}
	approval.CreatedAt = approval.CreatedAt.UTC()
	approval.ExpiresAt = approval.ExpiresAt.UTC()
	return approval, nil
}

func scanDevlogReceipt(row scanner) (DevlogDeliveryReceipt, error) {
	var receipt DevlogDeliveryReceipt
	var deliveredAt sql.NullTime
	if err := row.Scan(&receipt.ID, &receipt.IdempotencyKey, &receipt.RunID, &receipt.ArtifactID, &receipt.ArtifactSHA256, &receipt.Adapter, &receipt.Destination, &receipt.ApprovalID, &receipt.Status, &receipt.Attempts, &receipt.Reconciled, &receipt.claimToken, &receipt.ExternalID, &receipt.ExternalURL, &receipt.LastError, &receipt.RequestedBy, &receipt.CreatedAt, &receipt.UpdatedAt, &deliveredAt); err != nil {
		return DevlogDeliveryReceipt{}, err
	}
	receipt.CreatedAt = receipt.CreatedAt.UTC()
	receipt.UpdatedAt = receipt.UpdatedAt.UTC()
	if deliveredAt.Valid {
		at := deliveredAt.Time.UTC()
		receipt.DeliveredAt = &at
	}
	return receipt, nil
}

func (s *PostgresStore) RecordDevlogDeliveryApproval(approval DevlogDeliveryApproval) (DevlogDeliveryApproval, error) {
	approval.CreatedAt = approval.CreatedAt.UTC()
	approval.ExpiresAt = approval.ExpiresAt.UTC()
	if err := validateDevlogDeliveryApproval(approval); err != nil {
		return DevlogDeliveryApproval{}, err
	}
	row := s.db.QueryRowContext(s.operationContext(), `insert into devlog_delivery_approvals(`+devlogApprovalColumns+`) values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) on conflict (id) do nothing returning `+devlogApprovalColumns,
		approval.ID, approval.RunID, approval.ArtifactID, approval.ArtifactSHA256, approval.Adapter, approval.Destination, approval.Decision, approval.Actor, approval.Reason, approval.CreatedAt, approval.ExpiresAt)
	stored, err := scanDevlogApproval(row)
	if errors.Is(err, sql.ErrNoRows) {
		return DevlogDeliveryApproval{}, ErrConflict
	}
	if err != nil {
		return DevlogDeliveryApproval{}, mapPgErr(err)
	}
	return stored, nil
}

func (s *PostgresStore) DevlogDeliveryApproval(id string) (DevlogDeliveryApproval, error) {
	approval, err := scanDevlogApproval(s.db.QueryRowContext(s.operationContext(), `select `+devlogApprovalColumns+` from devlog_delivery_approvals where id=$1`, id))
	if err != nil {
		return DevlogDeliveryApproval{}, mapScanErr(err)
	}
	return approval, nil
}

func (s *PostgresStore) ListDevlogDeliveryApprovals(runID string) ([]DevlogDeliveryApproval, error) {
	rows, err := s.db.QueryContext(s.operationContext(), `select `+devlogApprovalColumns+` from devlog_delivery_approvals where run_id=$1 order by created_at desc, id desc limit $2`, runID, devlogDeliveryListLimit)
	if err != nil {
		return nil, mapPgErr(err)
	}
	defer rows.Close()
	out := []DevlogDeliveryApproval{}
	for rows.Next() {
		approval, err := scanDevlogApproval(rows)
		if err != nil {
			return nil, mapPgErr(err)
		}
		out = append(out, approval)
	}
	return out, mapPgErr(rows.Err())
}

// BeginDevlogDelivery atomically claims the delivery identity. A delivered
// receipt is returned unclaimed; a fresh in-flight claim is a conflict; a
// failed or stale claim is re-claimed with a new token and attempt count.
func (s *PostgresStore) BeginDevlogDelivery(input BeginDevlogDeliveryInput) (DevlogDeliveryReceipt, bool, error) {
	if err := validateBeginDevlogDelivery(input); err != nil {
		return DevlogDeliveryReceipt{}, false, err
	}
	ctx := s.operationContext()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DevlogDeliveryReceipt{}, false, mapPgErr(err)
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now().UTC()
	token := newID("devlog-claim")
	receipt, err := scanDevlogReceipt(tx.QueryRowContext(ctx, `insert into devlog_delivery_receipts(`+devlogReceiptColumns+`)
		values($1,$2,$3,$4,$5,$6,$7,$8,'sending',1,false,$9,'','','',$10,$11,$11,null)
		on conflict (idempotency_key) do nothing returning `+devlogReceiptColumns,
		newID("devlog-delivery"), input.IdempotencyKey, input.RunID, input.ArtifactID, input.ArtifactSHA256, input.Adapter, input.Destination, input.ApprovalID, token, input.Actor, now))
	if err == nil {
		if err := tx.Commit(); err != nil {
			return DevlogDeliveryReceipt{}, false, mapPgErr(err)
		}
		return receipt, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return DevlogDeliveryReceipt{}, false, mapPgErr(err)
	}
	existing, err := scanDevlogReceipt(tx.QueryRowContext(ctx, `select `+devlogReceiptColumns+` from devlog_delivery_receipts where idempotency_key=$1 for update`, input.IdempotencyKey))
	if err != nil {
		return DevlogDeliveryReceipt{}, false, mapScanErr(err)
	}
	switch existing.Status {
	case DevlogDeliveryDelivered:
		existing.claimToken = ""
		return existing, false, nil
	case DevlogDeliverySending:
		if now.Sub(existing.UpdatedAt) < input.StaleAfter {
			existing.claimToken = ""
			return existing, false, ErrConflict
		}
	}
	updated, err := scanDevlogReceipt(tx.QueryRowContext(ctx, `update devlog_delivery_receipts
		set status='sending', attempts=attempts+1, approval_id=$2, requested_by=$3, claim_token=$4, updated_at=$5
		where idempotency_key=$1 returning `+devlogReceiptColumns,
		input.IdempotencyKey, input.ApprovalID, input.Actor, token, now))
	if err != nil {
		return DevlogDeliveryReceipt{}, false, mapScanErr(err)
	}
	if err := tx.Commit(); err != nil {
		return DevlogDeliveryReceipt{}, false, mapPgErr(err)
	}
	return updated, true, nil
}

func (s *PostgresStore) FinishDevlogDelivery(input FinishDevlogDeliveryInput) (DevlogDeliveryReceipt, error) {
	if input.IdempotencyKey == "" || input.ClaimToken == "" {
		return DevlogDeliveryReceipt{}, ErrInvalid
	}
	now := s.now().UTC()
	status := finishedDevlogDeliveryStatus(input)
	var deliveredAt *time.Time
	if input.Delivered {
		deliveredAt = &now
	}
	receipt, err := scanDevlogReceipt(s.db.QueryRowContext(s.operationContext(), `update devlog_delivery_receipts
		set status=$3, reconciled=$4, external_id=$5, external_url=$6, last_error=$7, updated_at=$8, delivered_at=$9, claim_token=''
		where idempotency_key=$1 and claim_token=$2 and status='sending' returning `+devlogReceiptColumns,
		input.IdempotencyKey, input.ClaimToken, status, input.Delivered && input.Reconciled, strings.TrimSpace(input.ExternalID), strings.TrimSpace(input.ExternalURL), boundedDevlogDeliveryError(input.Error), now, deliveredAt))
	if errors.Is(err, sql.ErrNoRows) {
		if _, lookupErr := s.DevlogDeliveryReceipt(input.IdempotencyKey); lookupErr != nil {
			return DevlogDeliveryReceipt{}, lookupErr
		}
		return DevlogDeliveryReceipt{}, ErrConflict
	}
	if err != nil {
		return DevlogDeliveryReceipt{}, mapPgErr(err)
	}
	return receipt, nil
}

func (s *PostgresStore) DevlogDeliveryReceipt(idempotencyKey string) (DevlogDeliveryReceipt, error) {
	receipt, err := scanDevlogReceipt(s.db.QueryRowContext(s.operationContext(), `select `+devlogReceiptColumns+` from devlog_delivery_receipts where idempotency_key=$1`, idempotencyKey))
	if err != nil {
		return DevlogDeliveryReceipt{}, mapScanErr(err)
	}
	receipt.claimToken = ""
	return receipt, nil
}

func (s *PostgresStore) ListDevlogDeliveryReceipts(runID string) ([]DevlogDeliveryReceipt, error) {
	rows, err := s.db.QueryContext(s.operationContext(), `select `+devlogReceiptColumns+` from devlog_delivery_receipts where run_id=$1 order by created_at desc, id desc limit $2`, runID, devlogDeliveryListLimit)
	if err != nil {
		return nil, mapPgErr(err)
	}
	defer rows.Close()
	out := []DevlogDeliveryReceipt{}
	for rows.Next() {
		receipt, err := scanDevlogReceipt(rows)
		if err != nil {
			return nil, mapPgErr(err)
		}
		receipt.claimToken = ""
		out = append(out, receipt)
	}
	return out, mapPgErr(rows.Err())
}
