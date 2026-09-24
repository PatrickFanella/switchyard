package controlplane

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Generic PostgreSQL delivery receipts. kind.table is a fixed identifier from
// deliveryReceiptKind, never caller input.

const deliveryReceiptColumns = "id, idempotency_key, run_id, artifact_id, artifact_sha256, adapter, destination, approval_id, status, attempts, reconciled, claim_token, external_id, external_url, last_error, requested_by, created_at, updated_at, delivered_at"

func scanDeliveryReceipt(row scanner) (DeliveryReceipt, error) {
	var receipt DeliveryReceipt
	var deliveredAt sql.NullTime
	if err := row.Scan(&receipt.ID, &receipt.IdempotencyKey, &receipt.RunID, &receipt.ArtifactID, &receipt.ArtifactSHA256, &receipt.Adapter, &receipt.Destination, &receipt.ApprovalID, &receipt.Status, &receipt.Attempts, &receipt.Reconciled, &receipt.claimToken, &receipt.ExternalID, &receipt.ExternalURL, &receipt.LastError, &receipt.RequestedBy, &receipt.CreatedAt, &receipt.UpdatedAt, &deliveredAt); err != nil {
		return DeliveryReceipt{}, err
	}
	receipt.CreatedAt = receipt.CreatedAt.UTC()
	receipt.UpdatedAt = receipt.UpdatedAt.UTC()
	if deliveredAt.Valid {
		at := deliveredAt.Time.UTC()
		receipt.DeliveredAt = &at
	}
	return receipt, nil
}

func (s *PostgresStore) beginDeliveryReceipt(kind deliveryReceiptKind, input BeginDeliveryInput) (DeliveryReceipt, bool, error) {
	if err := validateBeginDelivery(input); err != nil {
		return DeliveryReceipt{}, false, err
	}
	ctx := s.operationContext()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DeliveryReceipt{}, false, mapPgErr(err)
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now().UTC()
	token := newID(kind.claimPrefix)
	receipt, err := scanDeliveryReceipt(tx.QueryRowContext(ctx, `insert into `+kind.table+`(`+deliveryReceiptColumns+`)
		values($1,$2,$3,$4,$5,$6,$7,$8,'sending',1,false,$9,'','','',$10,$11,$11,null)
		on conflict (idempotency_key) do nothing returning `+deliveryReceiptColumns,
		newID(kind.idPrefix), input.IdempotencyKey, input.RunID, input.ArtifactID, input.ArtifactSHA256, input.Adapter, input.Destination, input.ApprovalID, token, input.Actor, now))
	if err == nil {
		if err := tx.Commit(); err != nil {
			return DeliveryReceipt{}, false, mapPgErr(err)
		}
		return receipt, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return DeliveryReceipt{}, false, mapPgErr(err)
	}
	existing, err := scanDeliveryReceipt(tx.QueryRowContext(ctx, `select `+deliveryReceiptColumns+` from `+kind.table+` where idempotency_key=$1 for update`, input.IdempotencyKey))
	if err != nil {
		return DeliveryReceipt{}, false, mapScanErr(err)
	}
	switch existing.Status {
	case DeliveryDelivered:
		existing.claimToken = ""
		return existing, false, nil
	case DeliverySending:
		if now.Sub(existing.UpdatedAt) < input.StaleAfter {
			existing.claimToken = ""
			return existing, false, ErrConflict
		}
	}
	updated, err := scanDeliveryReceipt(tx.QueryRowContext(ctx, `update `+kind.table+`
		set status='sending', attempts=attempts+1, approval_id=$2, requested_by=$3, claim_token=$4, updated_at=$5
		where idempotency_key=$1 returning `+deliveryReceiptColumns,
		input.IdempotencyKey, input.ApprovalID, input.Actor, token, now))
	if err != nil {
		return DeliveryReceipt{}, false, mapScanErr(err)
	}
	if err := tx.Commit(); err != nil {
		return DeliveryReceipt{}, false, mapPgErr(err)
	}
	return updated, true, nil
}

func (s *PostgresStore) finishDeliveryReceipt(kind deliveryReceiptKind, input FinishDeliveryInput) (DeliveryReceipt, error) {
	if input.IdempotencyKey == "" || input.ClaimToken == "" {
		return DeliveryReceipt{}, ErrInvalid
	}
	now := s.now().UTC()
	status := finishedDeliveryStatus(input)
	var deliveredAt *time.Time
	if input.Delivered {
		deliveredAt = &now
	}
	receipt, err := scanDeliveryReceipt(s.db.QueryRowContext(s.operationContext(), `update `+kind.table+`
		set status=$3, reconciled=$4, external_id=$5, external_url=$6, last_error=$7, updated_at=$8, delivered_at=$9, claim_token=''
		where idempotency_key=$1 and claim_token=$2 and status='sending' returning `+deliveryReceiptColumns,
		input.IdempotencyKey, input.ClaimToken, status, input.Delivered && input.Reconciled, strings.TrimSpace(input.ExternalID), strings.TrimSpace(input.ExternalURL), boundedDeliveryError(input.Error), now, deliveredAt))
	if errors.Is(err, sql.ErrNoRows) {
		if _, lookupErr := s.deliveryReceipt(kind, input.IdempotencyKey); lookupErr != nil {
			return DeliveryReceipt{}, lookupErr
		}
		return DeliveryReceipt{}, ErrConflict
	}
	if err != nil {
		return DeliveryReceipt{}, mapPgErr(err)
	}
	return receipt, nil
}

func (s *PostgresStore) deliveryReceipt(kind deliveryReceiptKind, idempotencyKey string) (DeliveryReceipt, error) {
	receipt, err := scanDeliveryReceipt(s.db.QueryRowContext(s.operationContext(), `select `+deliveryReceiptColumns+` from `+kind.table+` where idempotency_key=$1`, idempotencyKey))
	if err != nil {
		return DeliveryReceipt{}, mapScanErr(err)
	}
	receipt.claimToken = ""
	return receipt, nil
}

func (s *PostgresStore) listDeliveryReceipts(kind deliveryReceiptKind, runID string, limit int) ([]DeliveryReceipt, error) {
	rows, err := s.db.QueryContext(s.operationContext(), `select `+deliveryReceiptColumns+` from `+kind.table+` where run_id=$1 order by created_at desc, id desc limit $2`, runID, limit)
	if err != nil {
		return nil, mapPgErr(err)
	}
	defer rows.Close()
	out := []DeliveryReceipt{}
	for rows.Next() {
		receipt, err := scanDeliveryReceipt(rows)
		if err != nil {
			return nil, mapPgErr(err)
		}
		receipt.claimToken = ""
		out = append(out, receipt)
	}
	return out, mapPgErr(rows.Err())
}
