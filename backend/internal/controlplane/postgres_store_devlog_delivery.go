package controlplane

import (
	"database/sql"
	"errors"
)

const devlogApprovalColumns = "id, run_id, artifact_id, artifact_sha256, adapter, destination, decision, actor, reason, created_at, expires_at"

func scanDevlogApproval(row scanner) (DevlogDeliveryApproval, error) {
	var approval DevlogDeliveryApproval
	if err := row.Scan(&approval.ID, &approval.RunID, &approval.ArtifactID, &approval.ArtifactSHA256, &approval.Adapter, &approval.Destination, &approval.Decision, &approval.Actor, &approval.Reason, &approval.CreatedAt, &approval.ExpiresAt); err != nil {
		return DevlogDeliveryApproval{}, err
	}
	approval.CreatedAt = approval.CreatedAt.UTC()
	approval.ExpiresAt = approval.ExpiresAt.UTC()
	return approval, nil
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
	return s.beginDeliveryReceipt(devlogDeliveryReceiptKind, input)
}

func (s *PostgresStore) FinishDevlogDelivery(input FinishDevlogDeliveryInput) (DevlogDeliveryReceipt, error) {
	return s.finishDeliveryReceipt(devlogDeliveryReceiptKind, input)
}

func (s *PostgresStore) DevlogDeliveryReceipt(idempotencyKey string) (DevlogDeliveryReceipt, error) {
	return s.deliveryReceipt(devlogDeliveryReceiptKind, idempotencyKey)
}

func (s *PostgresStore) ListDevlogDeliveryReceipts(runID string) ([]DevlogDeliveryReceipt, error) {
	return s.listDeliveryReceipts(devlogDeliveryReceiptKind, runID, devlogDeliveryListLimit)
}
