package controlplane

import "time"

func (s *MemoryStore) OpenPilotRolloutStatus() (OpenPilotRolloutStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reconcileOpenPilotRolloutLocked(s.now().UTC())
	return cloneOpenPilotRolloutStatus(s.openPilotRollout), nil
}

func (s *MemoryStore) ConfigureOpenPilotRollout(input ConfigureOpenPilotRolloutInput) (OpenPilotRolloutStatus, error) {
	now := s.now().UTC()
	if err := validateOpenPilotRolloutInput(input, now); err != nil {
		return OpenPilotRolloutStatus{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reconcileOpenPilotRolloutLocked(now)
	if input.Mode == "allow" {
		if s.openPilotRollout.Mode != "proof" || s.openPilotRollout.RegistryFingerprint != input.RegistryFingerprint || !s.openPilotProofSucceededLocked(s.openPilotRollout.ProofTargets) {
			return OpenPilotRolloutStatus{}, ErrConflict
		}
	}
	targets := cloneOpenPilotProofTargets(input.ProofTargets)
	expires := input.ExpiresAt
	if input.Mode == "allow" {
		targets = cloneOpenPilotProofTargets(s.openPilotRollout.ProofTargets)
		expires = s.openPilotRollout.ExpiresAt
	}
	s.openPilotRollout = OpenPilotRolloutStatus{
		Mode: input.Mode, RegistryFingerprint: input.RegistryFingerprint, ProofTargets: targets,
		ExpiresAt: expires, ClosedReason: input.Reason,
		Generation: s.openPilotRollout.Generation + 1, UpdatedAt: now,
	}
	return cloneOpenPilotRolloutStatus(s.openPilotRollout), nil
}

func (s *MemoryStore) reconcileOpenPilotRolloutLocked(now time.Time) {
	status := &s.openPilotRollout
	if status.Mode != "proof" {
		return
	}
	if status.ExpiresAt == nil || !status.ExpiresAt.After(now) {
		status.Mode, status.ClosedReason, status.UpdatedAt = "disabled", "proof expired", now
		status.Generation++
		return
	}
	for _, target := range status.ProofTargets {
		state, ok := s.openPilotProofStateLocked(target)
		if ok && (state.Phase == OpenPilotPhaseFailed || state.Phase == OpenPilotPhasePaused) {
			status.Mode, status.ClosedReason, status.UpdatedAt = "disabled", "proof target entered "+string(state.Phase), now
			status.Generation++
			return
		}
	}
}

func (s *MemoryStore) openPilotProofSucceededLocked(targets []OpenPilotProofTarget) bool {
	for _, target := range targets {
		state, ok := s.openPilotProofStateLocked(target)
		if !ok || state.Phase != OpenPilotPhaseIssueClosed || !state.IssueClosed {
			return false
		}
	}
	return len(targets) == 2
}

func (s *MemoryStore) openPilotProofStateLocked(target OpenPilotProofTarget) (OpenPilotIssueState, bool) {
	for _, state := range s.openPilotIssueStates {
		if state.RepositoryID == target.RepositoryID && state.ExecutorGeneration == target.ExecutorGeneration && state.CanonicalIssue == target.Issue {
			return state, true
		}
	}
	return OpenPilotIssueState{}, false
}

func cloneOpenPilotRolloutStatus(status OpenPilotRolloutStatus) OpenPilotRolloutStatus {
	status.ProofTargets = cloneOpenPilotProofTargets(status.ProofTargets)
	if status.ExpiresAt != nil {
		copy := status.ExpiresAt.UTC()
		status.ExpiresAt = &copy
	}
	return status
}
