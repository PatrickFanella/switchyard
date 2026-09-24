package controlplane

import (
	"context"
	"sort"
)

// SearchEvidence scans retained runs, steps, and model requests in memory,
// applies the shared candidate matcher, and returns one keyset page in
// (created_at desc, kind desc, id desc) order.
func (s *MemoryStore) SearchEvidence(_ context.Context, input EvidenceSearchInput) (EvidenceSearchPage, error) {
	input, err := normalizeEvidenceSearchInput(input)
	if err != nil {
		return EvidenceSearchPage{}, err
	}
	s.mu.RLock()
	candidates := make([]evidenceCandidate, 0)
	kinds := map[string]bool{}
	for _, kind := range evidenceScopeKinds(input.Scope) {
		kinds[kind] = true
	}
	if kinds[EvidenceKindRun] {
		for _, run := range s.runs {
			if run.CreatedAt.Before(input.RetainedSince) {
				continue
			}
			candidates = append(candidates, evidenceCandidateFromRun(run))
		}
	}
	if kinds[EvidenceKindStep] {
		for _, step := range s.steps {
			if step.CreatedAt.Before(input.RetainedSince) {
				continue
			}
			candidates = append(candidates, evidenceCandidateFromStep(step, s.runs[step.RunID].WorkflowID))
		}
	}
	if kinds[EvidenceKindRequest] {
		for _, req := range s.modelBrokerRequests {
			if req.CreatedAt.Before(input.RetainedSince) {
				continue
			}
			candidates = append(candidates, evidenceCandidateFromModelRequest(req))
		}
	}
	s.mu.RUnlock()
	hits := make([]EvidenceHit, 0)
	for _, candidate := range candidates {
		if !evidenceCandidateMatches(candidate, input) || !evidenceHitAfterCursor(candidate.hit, input) {
			continue
		}
		hits = append(hits, candidate.hit)
	}
	sort.Slice(hits, func(i, j int) bool { return evidenceHitNewer(hits[i], hits[j]) })
	page := EvidenceSearchPage{Hits: hits}
	if len(page.Hits) > input.Limit {
		page.HasMore = true
		page.Hits = page.Hits[:input.Limit]
	}
	return page, nil
}
