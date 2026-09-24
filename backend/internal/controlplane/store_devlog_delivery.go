package controlplane

import (
	"sort"
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
	return beginDeliveryReceiptLocked(s.devlogReceipts, devlogDeliveryReceiptKind, s.now().UTC(), input)
}

func (s *MemoryStore) FinishDevlogDelivery(input FinishDevlogDeliveryInput) (DevlogDeliveryReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return finishDeliveryReceiptLocked(s.devlogReceipts, s.now().UTC(), input)
}

func (s *MemoryStore) DevlogDeliveryReceipt(idempotencyKey string) (DevlogDeliveryReceipt, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return deliveryReceiptFromMap(s.devlogReceipts, idempotencyKey)
}

func (s *MemoryStore) ListDevlogDeliveryReceipts(runID string) ([]DevlogDeliveryReceipt, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return listDeliveryReceiptsFromMap(s.devlogReceipts, runID, devlogDeliveryListLimit), nil
}
