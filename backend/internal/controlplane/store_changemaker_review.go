package controlplane

import (
	"sort"
	"strings"
)

func (s *MemoryStore) ensureChangemakerReviewMapsLocked() {
	if s.changemakerVersions == nil {
		s.changemakerVersions = map[string][]ChangemakerDraftVersion{}
	}
	if s.changemakerDecisions == nil {
		s.changemakerDecisions = map[string]ChangemakerReviewDecision{}
	}
	if s.changemakerReceipts == nil {
		s.changemakerReceipts = map[string]DeliveryReceipt{}
	}
}

func normalizeChangemakerDecision(decision ChangemakerReviewDecision) ChangemakerReviewDecision {
	decision.CreatedAt = decision.CreatedAt.UTC()
	decision.ExpiresAt = decision.ExpiresAt.UTC()
	decision.Notes = strings.TrimSpace(decision.Notes)
	return decision
}

func (s *MemoryStore) ChangemakerDraftVersionByRun(runID string) (ChangemakerDraftVersion, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, versions := range s.changemakerVersions {
		for _, version := range versions {
			if version.RunID == runID {
				return version, nil
			}
		}
	}
	return ChangemakerDraftVersion{}, ErrNotFound
}

func (s *MemoryStore) ListChangemakerDraftVersions(threadID string) ([]ChangemakerDraftVersion, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]ChangemakerDraftVersion{}, s.changemakerVersions[threadID]...), nil
}

// RecordChangemakerRevision appends the new version and its revision-request
// decision in one step. It persists version 1 first when the thread has no
// history yet, and refuses the request unless ExpectedLatestVersion is still
// the highest version.
func (s *MemoryStore) RecordChangemakerRevision(input ChangemakerRevisionInput) (ChangemakerDraftVersion, ChangemakerReviewDecision, error) {
	input.Root.CreatedAt = input.Root.CreatedAt.UTC()
	input.Version.CreatedAt = input.Version.CreatedAt.UTC()
	input.Decision = normalizeChangemakerDecision(input.Decision)
	if err := validateChangemakerRevisionInput(input); err != nil {
		return ChangemakerDraftVersion{}, ChangemakerReviewDecision{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureChangemakerReviewMapsLocked()
	versions := append([]ChangemakerDraftVersion{}, s.changemakerVersions[input.Root.ThreadID]...)
	if len(versions) == 0 {
		versions = append(versions, input.Root)
	}
	if versions[len(versions)-1].Version != input.ExpectedLatestVersion {
		return ChangemakerDraftVersion{}, ChangemakerReviewDecision{}, ErrConflict
	}
	for _, existing := range s.changemakerVersions {
		for _, version := range existing {
			if version.RunID == input.Version.RunID || version.ID == input.Version.ID {
				return ChangemakerDraftVersion{}, ChangemakerReviewDecision{}, ErrConflict
			}
		}
	}
	if _, exists := s.changemakerDecisions[input.Decision.ID]; exists {
		return ChangemakerDraftVersion{}, ChangemakerReviewDecision{}, ErrConflict
	}
	versions = append(versions, input.Version)
	s.changemakerVersions[input.Root.ThreadID] = versions
	s.changemakerDecisions[input.Decision.ID] = input.Decision
	return input.Version, input.Decision, nil
}

func (s *MemoryStore) RecordChangemakerReviewDecision(decision ChangemakerReviewDecision) (ChangemakerReviewDecision, error) {
	decision = normalizeChangemakerDecision(decision)
	if err := validateChangemakerReviewDecision(decision); err != nil {
		return ChangemakerReviewDecision{}, err
	}
	if decision.Decision == ChangemakerDecisionRevisionRequested {
		return ChangemakerReviewDecision{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureChangemakerReviewMapsLocked()
	if _, exists := s.changemakerDecisions[decision.ID]; exists {
		return ChangemakerReviewDecision{}, ErrConflict
	}
	s.changemakerDecisions[decision.ID] = decision
	return decision, nil
}

func (s *MemoryStore) ChangemakerReviewDecision(id string) (ChangemakerReviewDecision, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	decision, ok := s.changemakerDecisions[id]
	if !ok {
		return ChangemakerReviewDecision{}, ErrNotFound
	}
	return decision, nil
}

func (s *MemoryStore) ListChangemakerReviewDecisions(threadID string) ([]ChangemakerReviewDecision, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []ChangemakerReviewDecision{}
	for _, decision := range s.changemakerDecisions {
		if decision.ThreadID == threadID {
			out = append(out, decision)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	if len(out) > changemakerReviewListLimit {
		out = out[:changemakerReviewListLimit]
	}
	return out, nil
}

func (s *MemoryStore) BeginChangemakerPublication(input BeginDeliveryInput) (DeliveryReceipt, bool, error) {
	if err := validateBeginDelivery(input); err != nil {
		return DeliveryReceipt{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureChangemakerReviewMapsLocked()
	return beginDeliveryReceiptLocked(s.changemakerReceipts, changemakerPublicationReceiptKind, s.now().UTC(), input)
}

func (s *MemoryStore) FinishChangemakerPublication(input FinishDeliveryInput) (DeliveryReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return finishDeliveryReceiptLocked(s.changemakerReceipts, s.now().UTC(), input)
}

func (s *MemoryStore) ChangemakerPublicationReceipt(idempotencyKey string) (DeliveryReceipt, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return deliveryReceiptFromMap(s.changemakerReceipts, idempotencyKey)
}

func (s *MemoryStore) ListChangemakerPublicationReceipts(runID string) ([]DeliveryReceipt, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return listDeliveryReceiptsFromMap(s.changemakerReceipts, runID, changemakerReceiptListLimit), nil
}
