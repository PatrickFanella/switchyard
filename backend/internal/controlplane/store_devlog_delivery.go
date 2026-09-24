package controlplane

import (
	"sort"
	"strings"
)

func (s *MemoryStore) ensureDevlogDeliveryMapsLocked() {
	if s.devlogApprovals == nil {
		s.devlogApprovals = map[string]DevlogDeliveryApproval{}
	}
	if s.devlogReceipts == nil {
		s.devlogReceipts = map[string]DevlogDeliveryReceipt{}
	}
}

func (s *MemoryStore) RecordDevlogDeliveryApproval(approval DevlogDeliveryApproval) (DevlogDeliveryApproval, error) {
	approval.CreatedAt = approval.CreatedAt.UTC()
	approval.ExpiresAt = approval.ExpiresAt.UTC()
	if err := validateDevlogDeliveryApproval(approval); err != nil {
		return DevlogDeliveryApproval{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureDevlogDeliveryMapsLocked()
	if _, exists := s.devlogApprovals[approval.ID]; exists {
		return DevlogDeliveryApproval{}, ErrConflict
	}
	s.devlogApprovals[approval.ID] = approval
	return approval, nil
}

func (s *MemoryStore) DevlogDeliveryApproval(id string) (DevlogDeliveryApproval, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	approval, ok := s.devlogApprovals[id]
	if !ok {
		return DevlogDeliveryApproval{}, ErrNotFound
	}
	return approval, nil
}

func (s *MemoryStore) ListDevlogDeliveryApprovals(runID string) ([]DevlogDeliveryApproval, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []DevlogDeliveryApproval{}
	for _, approval := range s.devlogApprovals {
		if approval.RunID == runID {
			out = append(out, approval)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	if len(out) > devlogDeliveryListLimit {
		out = out[:devlogDeliveryListLimit]
	}
	return out, nil
}

func (s *MemoryStore) BeginDevlogDelivery(input BeginDevlogDeliveryInput) (DevlogDeliveryReceipt, bool, error) {
	if err := validateBeginDevlogDelivery(input); err != nil {
		return DevlogDeliveryReceipt{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureDevlogDeliveryMapsLocked()
	now := s.now().UTC()
	receipt, exists := s.devlogReceipts[input.IdempotencyKey]
	if exists {
		switch receipt.Status {
		case DevlogDeliveryDelivered:
			receipt.claimToken = ""
			return receipt, false, nil
		case DevlogDeliverySending:
			if now.Sub(receipt.UpdatedAt) < input.StaleAfter {
				receipt.claimToken = ""
				return receipt, false, ErrConflict
			}
		}
		receipt.Status = DevlogDeliverySending
		receipt.Attempts++
		receipt.ApprovalID = input.ApprovalID
		receipt.RequestedBy = input.Actor
		receipt.UpdatedAt = now
		receipt.claimToken = newID("devlog-claim")
		s.devlogReceipts[input.IdempotencyKey] = receipt
		return receipt, true, nil
	}
	receipt = DevlogDeliveryReceipt{
		ID: newID("devlog-delivery"), IdempotencyKey: input.IdempotencyKey, RunID: input.RunID, ArtifactID: input.ArtifactID,
		ArtifactSHA256: input.ArtifactSHA256, Adapter: input.Adapter, Destination: input.Destination, ApprovalID: input.ApprovalID,
		Status: DevlogDeliverySending, Attempts: 1, RequestedBy: input.Actor, CreatedAt: now, UpdatedAt: now, claimToken: newID("devlog-claim"),
	}
	s.devlogReceipts[input.IdempotencyKey] = receipt
	return receipt, true, nil
}

func (s *MemoryStore) FinishDevlogDelivery(input FinishDevlogDeliveryInput) (DevlogDeliveryReceipt, error) {
	if input.IdempotencyKey == "" || input.ClaimToken == "" {
		return DevlogDeliveryReceipt{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	receipt, exists := s.devlogReceipts[input.IdempotencyKey]
	if !exists {
		return DevlogDeliveryReceipt{}, ErrNotFound
	}
	if receipt.Status != DevlogDeliverySending || receipt.claimToken != input.ClaimToken {
		return DevlogDeliveryReceipt{}, ErrConflict
	}
	now := s.now().UTC()
	receipt.Status = finishedDevlogDeliveryStatus(input)
	receipt.Reconciled = input.Delivered && input.Reconciled
	receipt.ExternalID = strings.TrimSpace(input.ExternalID)
	receipt.ExternalURL = strings.TrimSpace(input.ExternalURL)
	receipt.LastError = boundedDevlogDeliveryError(input.Error)
	receipt.UpdatedAt = now
	receipt.claimToken = ""
	if input.Delivered {
		receipt.DeliveredAt = &now
	} else {
		receipt.DeliveredAt = nil
	}
	s.devlogReceipts[input.IdempotencyKey] = receipt
	return receipt, nil
}

func (s *MemoryStore) DevlogDeliveryReceipt(idempotencyKey string) (DevlogDeliveryReceipt, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	receipt, ok := s.devlogReceipts[idempotencyKey]
	if !ok {
		return DevlogDeliveryReceipt{}, ErrNotFound
	}
	receipt.claimToken = ""
	return receipt, nil
}

func (s *MemoryStore) ListDevlogDeliveryReceipts(runID string) ([]DevlogDeliveryReceipt, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []DevlogDeliveryReceipt{}
	for _, receipt := range s.devlogReceipts {
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
	if len(out) > devlogDeliveryListLimit {
		out = out[:devlogDeliveryListLimit]
	}
	return out, nil
}
