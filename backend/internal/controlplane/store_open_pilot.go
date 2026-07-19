package controlplane

import "sort"

func (s *MemoryStore) OpenPilotIssueState(repo, issue string) (OpenPilotIssueState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, ok := s.openPilotIssueStates[openPilotIssueStateKey(repo, issue)]
	if !ok {
		return OpenPilotIssueState{}, ErrNotFound
	}
	return cloneOpenPilotIssueState(state), nil
}

func (s *MemoryStore) UpsertOpenPilotIssueState(input OpenPilotIssueState) (OpenPilotIssueState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	key := openPilotIssueStateKey(input.Repo, input.Issue)
	var existing *OpenPilotIssueState
	if saved, ok := s.openPilotIssueStates[key]; ok {
		existing = &saved
	}
	state, err := validateOpenPilotIssueStateForUpsert(input, existing)
	if err != nil {
		return OpenPilotIssueState{}, err
	}
	if existing == nil {
		if state.ID == "" {
			state.ID = newID("opi")
		}
		if state.CreatedAt.IsZero() {
			state.CreatedAt = now
		}
	}
	state.Repo = input.Repo
	state.Issue = input.Issue
	state.UpdatedAt = now
	state.GateStatus = cloneOpenPilotGateStatus(state.GateStatus)
	state.ResumeInputs = cloneMap(state.ResumeInputs)
	s.openPilotIssueStates[key] = state
	return cloneOpenPilotIssueState(state), nil
}

func (s *MemoryStore) TransitionOpenPilotIssueState(repo, issue string, next OpenPilotPhase) (OpenPilotIssueState, error) {
	if repo == "" || issue == "" || next == "" {
		return OpenPilotIssueState{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	key := openPilotIssueStateKey(repo, issue)
	if existing, ok := s.openPilotIssueStates[key]; ok {
		if err := normalizeOpenPilotRepairCounts(&existing); err != nil {
			return OpenPilotIssueState{}, err
		}
		if err := ValidateOpenPilotTransition(existing.Phase, next); err != nil {
			return OpenPilotIssueState{}, err
		}
		existing.Phase = next
		existing.UpdatedAt = now
		s.openPilotIssueStates[key] = existing
		return cloneOpenPilotIssueState(existing), nil
	}
	if err := ValidateOpenPilotTransition("", next); err != nil {
		return OpenPilotIssueState{}, err
	}
	state := OpenPilotIssueState{
		ID:                newID("opi"),
		Repo:              repo,
		Issue:             issue,
		Phase:             next,
		IssueClosed:       false,
		RepairAttempts:    0,
		MaxRepairAttempts: 2,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	s.openPilotIssueStates[key] = state
	return cloneOpenPilotIssueState(state), nil
}

func (s *MemoryStore) ClaimOpenPilotIssueStateForResume(repo, issue string, resumeInputs map[string]string) (OpenPilotIssueState, error) {
	if repo == "" || issue == "" {
		return OpenPilotIssueState{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := openPilotIssueStateKey(repo, issue)
	existing, ok := s.openPilotIssueStates[key]
	if !ok {
		return OpenPilotIssueState{}, ErrNotFound
	}
	if !isOpenPilotResumablePhase(existing.Phase) {
		return OpenPilotIssueState{}, ErrInvalid
	}
	if err := normalizeOpenPilotRepairCounts(&existing); err != nil {
		return OpenPilotIssueState{}, err
	}
	existing.Phase = openPilotResumeClaimPhase(existing.Phase, resumeInputs)
	existing.ResumeInputs = cloneMap(resumeInputs)
	existing.LastError = ""
	existing.FailingGate = ""
	existing.OperatorSummary = ""
	existing.RecommendedNextAction = ""
	existing.UpdatedAt = s.now().UTC()
	s.openPilotIssueStates[key] = existing
	return cloneOpenPilotIssueState(existing), nil
}

func (s *MemoryStore) ListOpenPilotIssueStates() []OpenPilotIssueState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]OpenPilotIssueState, 0, len(s.openPilotIssueStates))
	for _, state := range s.openPilotIssueStates {
		out = append(out, cloneOpenPilotIssueState(state))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repo == out[j].Repo {
			return out[i].Issue < out[j].Issue
		}
		return out[i].Repo < out[j].Repo
	})
	return out
}

func openPilotIssueStateKey(repo, issue string) string {
	return repo + "\x00" + issue
}
