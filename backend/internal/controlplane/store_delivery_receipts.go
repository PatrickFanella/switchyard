package controlplane

import (
	"sort"
	"strings"
	"time"
)

// deliveryReceiptKind names the per-workflow receipt table (PostgreSQL) and
// the identifier prefixes of its rows. Devlog delivery and Changemaker
// publication keep separate tables with the same claim contract.
type deliveryReceiptKind struct {
	table       string
	idPrefix    string
	claimPrefix string
}

var (
	devlogDeliveryReceiptKind         = deliveryReceiptKind{table: "devlog_delivery_receipts", idPrefix: "devlog-delivery", claimPrefix: "devlog-claim"}
	changemakerPublicationReceiptKind = deliveryReceiptKind{table: "changemaker_publication_receipts", idPrefix: "changemaker-publication", claimPrefix: "changemaker-claim"}
)

// beginDeliveryReceiptLocked claims a delivery identity in a memory map. A
// delivered receipt is returned unclaimed; a fresh in-flight claim is a
// conflict; a failed or stale claim is re-claimed with a new token.
func beginDeliveryReceiptLocked(receipts map[string]DeliveryReceipt, kind deliveryReceiptKind, now time.Time, input BeginDeliveryInput) (DeliveryReceipt, bool, error) {
	receipt, exists := receipts[input.IdempotencyKey]
	if exists {
		switch receipt.Status {
		case DeliveryDelivered:
			receipt.claimToken = ""
			return receipt, false, nil
		case DeliverySending:
			if now.Sub(receipt.UpdatedAt) < input.StaleAfter {
				receipt.claimToken = ""
				return receipt, false, ErrConflict
			}
		}
		receipt.Status = DeliverySending
		receipt.Attempts++
		receipt.ApprovalID = input.ApprovalID
		receipt.RequestedBy = input.Actor
		receipt.UpdatedAt = now
		receipt.claimToken = newID(kind.claimPrefix)
		receipts[input.IdempotencyKey] = receipt
		return receipt, true, nil
	}
	receipt = DeliveryReceipt{
		ID: newID(kind.idPrefix), IdempotencyKey: input.IdempotencyKey, RunID: input.RunID, ArtifactID: input.ArtifactID,
		ArtifactSHA256: input.ArtifactSHA256, Adapter: input.Adapter, Destination: input.Destination, ApprovalID: input.ApprovalID,
		Status: DeliverySending, Attempts: 1, RequestedBy: input.Actor, CreatedAt: now, UpdatedAt: now, claimToken: newID(kind.claimPrefix),
	}
	receipts[input.IdempotencyKey] = receipt
	return receipt, true, nil
}

func finishDeliveryReceiptLocked(receipts map[string]DeliveryReceipt, now time.Time, input FinishDeliveryInput) (DeliveryReceipt, error) {
	if input.IdempotencyKey == "" || input.ClaimToken == "" {
		return DeliveryReceipt{}, ErrInvalid
	}
	receipt, exists := receipts[input.IdempotencyKey]
	if !exists {
		return DeliveryReceipt{}, ErrNotFound
	}
	if receipt.Status != DeliverySending || receipt.claimToken != input.ClaimToken {
		return DeliveryReceipt{}, ErrConflict
	}
	receipt.Status = finishedDeliveryStatus(input)
	receipt.Reconciled = input.Delivered && input.Reconciled
	receipt.ExternalID = strings.TrimSpace(input.ExternalID)
	receipt.ExternalURL = strings.TrimSpace(input.ExternalURL)
	receipt.LastError = boundedDeliveryError(input.Error)
	receipt.UpdatedAt = now
	receipt.claimToken = ""
	if input.Delivered {
		receipt.DeliveredAt = &now
	} else {
		receipt.DeliveredAt = nil
	}
	receipts[input.IdempotencyKey] = receipt
	return receipt, nil
}

func deliveryReceiptFromMap(receipts map[string]DeliveryReceipt, key string) (DeliveryReceipt, error) {
	receipt, ok := receipts[key]
	if !ok {
		return DeliveryReceipt{}, ErrNotFound
	}
	receipt.claimToken = ""
	return receipt, nil
}

func listDeliveryReceiptsFromMap(receipts map[string]DeliveryReceipt, runID string, limit int) []DeliveryReceipt {
	out := []DeliveryReceipt{}
	for _, receipt := range receipts {
		if receipt.RunID == runID {
			receipt.claimToken = ""
			out = append(out, receipt)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
