package controlplane

import (
	"context"
	"sort"
	"time"
)

func (s *MemoryStore) CreateRun(input CreateRunInput) (Run, error) {
	if input.WorkflowID == "" {
		return Run{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.workflows[input.WorkflowID]; !ok {
		return Run{}, ErrNotFound
	}
	if input.TriggerID != "" {
		if _, ok := s.triggers[input.TriggerID]; !ok {
			return Run{}, ErrNotFound
		}
	}
	now := s.now().UTC()
	maxAttempts := input.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	r := Run{ID: newID("run"), WorkflowID: input.WorkflowID, TriggerID: input.TriggerID, Status: RunQueued, Inputs: cloneMap(input.Inputs), CreatedAt: now, QueuedAt: now, AvailableAt: now, MaxAttempts: maxAttempts}
	if input.PipelineSnapshot != nil {
		snapshot := clonePipeline(*input.PipelineSnapshot)
		r.PipelineID = snapshot.ID
		r.PipelineVersion = pipelineVersion(snapshot)
		r.PipelineSnapshot = &snapshot
	}
	s.runs[r.ID] = r
	s.events[r.ID] = append(s.events[r.ID], RunEvent{ID: newID("evt"), RunID: r.ID, Type: "run.created", Message: "Run queued", CreatedAt: now})
	return r, nil
}

func (s *MemoryStore) CreateRunContext(_ context.Context, input CreateRunInput) (Run, error) {
	return s.CreateRun(input)
}

func (s *MemoryStore) StartRun(id string) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		return Run{}, ErrNotFound
	}
	now := s.now().UTC()
	r.Status = RunRunning
	r.AttemptCount++
	r.StartedAt = &now
	s.runs[id] = r
	s.events[id] = append(s.events[id], RunEvent{ID: newID("evt"), RunID: id, Type: "run.started", Message: "Run started", CreatedAt: now})
	return r, nil
}

func (s *MemoryStore) ClaimNextRun(workerID string, lease time.Duration) (Run, error) {
	if workerID == "" || lease <= 0 {
		return Run{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	var selected *Run
	for _, run := range s.runs {
		if run.Status != RunQueued || run.AvailableAt.After(now) {
			continue
		}
		candidate := run
		if selected == nil || candidate.CreatedAt.Before(selected.CreatedAt) || (candidate.CreatedAt.Equal(selected.CreatedAt) && candidate.ID < selected.ID) {
			selected = &candidate
		}
	}
	if selected == nil {
		return Run{}, ErrNotFound
	}
	r := *selected
	expires := now.Add(lease).UTC()
	r.Status = RunRunning
	r.AttemptCount++
	if r.StartedAt == nil {
		r.StartedAt = &now
	}
	r.ClaimedAt = &now
	r.ClaimedBy = workerID
	r.LeaseExpiresAt = &expires
	s.runs[r.ID] = r
	s.events[r.ID] = append(s.events[r.ID], RunEvent{ID: newID("evt"), RunID: r.ID, Type: "run.started", Message: "Run claimed by " + workerID, CreatedAt: now})
	return r, nil
}

func (s *MemoryStore) ExtendRunLease(id string, workerID string, lease time.Duration) (Run, error) {
	if id == "" || workerID == "" || lease <= 0 {
		return Run{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		return Run{}, ErrNotFound
	}
	if r.Status != RunRunning || r.ClaimedBy != workerID {
		return Run{}, ErrInvalid
	}
	now := s.now().UTC()
	expires := now.Add(lease).UTC()
	r.ClaimedAt = &now
	r.LeaseExpiresAt = &expires
	s.runs[id] = r
	s.events[id] = append(s.events[id], RunEvent{ID: newID("evt"), RunID: id, Type: "run.lease.extended", Message: "Lease extended by " + workerID, CreatedAt: now})
	return r, nil
}

func (s *MemoryStore) RecoverLeasedRuns(at time.Time) (int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := at.UTC()
	requeued := 0
	failed := 0
	for id, run := range s.runs {
		if run.Status != RunRunning || run.LeaseExpiresAt == nil || run.LeaseExpiresAt.After(now) {
			continue
		}
		run.ClaimedAt = nil
		run.ClaimedBy = ""
		run.LeaseExpiresAt = nil
		if run.AttemptCount < run.MaxAttempts {
			run.Status = RunQueued
			run.AvailableAt = now
			s.events[id] = append(s.events[id], RunEvent{ID: newID("evt"), RunID: id, Type: "run.recovered", Message: "Worker lease expired; run requeued", CreatedAt: now})
			requeued++
		} else {
			run.Status = RunFailed
			run.FinishedAt = &now
			run.Summary = "Worker lease expired"
			for stepID, step := range s.steps {
				if step.RunID == id && step.Status == StepRunning {
					step.Status = StepFailed
					step.Error = run.Summary
					step.FinishedAt = &now
					s.steps[stepID] = step
					s.events[id] = append(s.events[id], RunEvent{ID: newID("evt"), RunID: id, Type: "step.failed", Message: run.Summary, CreatedAt: now})
				}
			}
			s.events[id] = append(s.events[id], RunEvent{ID: newID("evt"), RunID: id, Type: "run.failed", Message: run.Summary, CreatedAt: now})
			failed++
		}
		s.runs[id] = run
	}
	return requeued, failed, nil
}

func (s *MemoryStore) RequeueFailedRun(id string, availableAt time.Time, summary string) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[id]
	if !ok {
		return Run{}, ErrNotFound
	}
	if (run.Status != RunFailed && run.Status != RunTimedOut) || run.AttemptCount >= run.MaxAttempts {
		return Run{}, ErrInvalid
	}
	run.Status = RunQueued
	run.AvailableAt = availableAt.UTC()
	run.FinishedAt = nil
	run.Summary = summary
	run.ClaimedAt = nil
	run.ClaimedBy = ""
	run.LeaseExpiresAt = nil
	s.runs[id] = run
	s.events[id] = append(s.events[id], RunEvent{ID: newID("evt"), RunID: id, Type: "run.retry.scheduled", Message: summary, CreatedAt: s.now().UTC()})
	return run, nil
}

func (s *MemoryStore) FinishRun(id string, status RunStatus, summary string) (Run, error) {
	return s.finishRun(id, "", status, summary)
}

func (s *MemoryStore) FinishClaimedRun(id string, workerID string, status RunStatus, summary string) (Run, error) {
	if workerID == "" {
		return Run{}, ErrInvalid
	}
	return s.finishRun(id, workerID, status, summary)
}

func (s *MemoryStore) finishRun(id string, workerID string, status RunStatus, summary string) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		return Run{}, ErrNotFound
	}
	if workerID != "" && (r.Status != RunRunning || r.ClaimedBy != workerID) {
		return Run{}, ErrInvalid
	}
	now := s.now().UTC()
	r.Status = status
	r.FinishedAt = &now
	r.ClaimedAt = nil
	r.ClaimedBy = ""
	r.LeaseExpiresAt = nil
	r.Summary = summary
	s.runs[id] = r
	if stepStatus, terminal := terminalStepStatusForRun(status); terminal {
		for stepID, step := range s.steps {
			if step.RunID == id && step.Status == StepRunning {
				step.Status = stepStatus
				step.Error = summary
				step.FinishedAt = &now
				s.steps[stepID] = step
				s.events[id] = append(s.events[id], RunEvent{ID: newID("evt"), RunID: id, Type: "step." + string(stepStatus), Message: summary, CreatedAt: now})
			}
		}
	}
	s.events[id] = append(s.events[id], RunEvent{ID: newID("evt"), RunID: id, Type: "run." + string(status), Message: summary, CreatedAt: now})
	return r, nil
}

func (s *MemoryStore) CreateRunStep(input CreateRunStepInput) (RunStep, error) {
	if input.RunID == "" || input.Position < 1 || input.Name == "" || input.Executor == "" {
		return RunStep{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.runs[input.RunID]; !ok {
		return RunStep{}, ErrNotFound
	}
	for _, step := range s.steps {
		if step.RunID == input.RunID && step.Position == input.Position {
			return RunStep{}, ErrInvalid
		}
	}
	now := s.now().UTC()
	step := RunStep{ID: newID("step"), RunID: input.RunID, Position: input.Position, Name: input.Name, Executor: input.Executor, Model: input.Model, Status: StepQueued, Inputs: cloneMap(input.Inputs), CorrelationIDs: cloneMap(input.CorrelationIDs), CreatedAt: now}
	s.steps[step.ID] = step
	s.events[input.RunID] = append(s.events[input.RunID], RunEvent{ID: newID("evt"), RunID: input.RunID, Type: "step.created", Message: step.Name, CreatedAt: now})
	return step, nil
}

func (s *MemoryStore) StartRunStep(id string) (RunStep, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	step, ok := s.steps[id]
	if !ok {
		return RunStep{}, ErrNotFound
	}
	if step.Status != StepQueued {
		return RunStep{}, ErrInvalid
	}
	now := s.now().UTC()
	step.Status = StepRunning
	step.AttemptCount++
	step.StartedAt = &now
	step.FinishedAt = nil
	step.Error = ""
	step.OutputSummary = ""
	s.steps[id] = step
	s.events[step.RunID] = append(s.events[step.RunID], RunEvent{ID: newID("evt"), RunID: step.RunID, Type: "step.started", Message: step.Name, CreatedAt: now})
	return step, nil
}

func (s *MemoryStore) UpdateRunStep(id string, input UpdateRunStepInput) (RunStep, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	step, ok := s.steps[id]
	if !ok {
		return RunStep{}, ErrNotFound
	}
	if input.Inputs != nil {
		step.Inputs = cloneMap(input.Inputs)
	}
	s.steps[id] = step
	return step, nil
}

func (s *MemoryStore) FinishRunStep(id string, status RunStepStatus, outputSummary string, errText string, correlationIDs map[string]string) (RunStep, error) {
	if !isTerminalStepStatus(status) {
		return RunStep{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	step, ok := s.steps[id]
	if !ok {
		return RunStep{}, ErrNotFound
	}
	if step.Status != StepRunning && step.Status != StepQueued {
		return RunStep{}, ErrInvalid
	}
	now := s.now().UTC()
	step.Status = status
	step.OutputSummary = outputSummary
	step.Error = errText
	step.CorrelationIDs = mergeStringMaps(step.CorrelationIDs, correlationIDs)
	step.FinishedAt = &now
	s.steps[id] = step
	message := outputSummary
	if message == "" {
		message = errText
	}
	if message == "" {
		message = step.Name
	}
	s.events[step.RunID] = append(s.events[step.RunID], RunEvent{ID: newID("evt"), RunID: step.RunID, Type: "step." + string(status), Message: message, CreatedAt: now})
	return step, nil
}

func (s *MemoryStore) MergeRunStepCorrelationIDs(id string, correlationIDs map[string]string) (RunStep, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	step, ok := s.steps[id]
	if !ok {
		return RunStep{}, ErrNotFound
	}
	step.CorrelationIDs = mergeStringMaps(step.CorrelationIDs, correlationIDs)
	s.steps[id] = step
	return step, nil
}

func (s *MemoryStore) RecordRunStepBackendDecision(runID, stepID string, decision BackendDecision) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.runs[runID]; !ok {
		return ErrNotFound
	}
	step, ok := s.steps[stepID]
	if !ok || step.RunID != runID {
		return ErrNotFound
	}
	step.CorrelationIDs = mergeStringMaps(step.CorrelationIDs, decision.CorrelationIDs())
	s.steps[stepID] = step
	if decision.DeprecatedBackend != "" {
		s.events[runID] = append(s.events[runID], RunEvent{ID: newID("evt"), RunID: runID, Type: DeprecatedBackendAttemptedEvent, Message: decision.DeprecatedBackend, CreatedAt: s.now().UTC()})
	}
	return nil
}

func (s *MemoryStore) RetryRunStep(id string) (RunStep, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	step, ok := s.steps[id]
	if !ok {
		return RunStep{}, ErrNotFound
	}
	if step.Status != StepFailed && step.Status != StepCanceled && step.Status != StepTimedOut && step.Status != StepPaused {
		return RunStep{}, ErrInvalid
	}
	now := s.now().UTC()
	step.Status = StepQueued
	step.StartedAt = nil
	step.FinishedAt = nil
	step.Error = ""
	step.OutputSummary = ""
	s.steps[id] = step
	s.events[step.RunID] = append(s.events[step.RunID], RunEvent{ID: newID("evt"), RunID: step.RunID, Type: "step.retry", Message: step.Name, CreatedAt: now})
	return step, nil
}

func (s *MemoryStore) RecoverRunStep(id string) (RunStep, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	step, ok := s.steps[id]
	if !ok {
		return RunStep{}, ErrNotFound
	}
	if step.Status != StepRunning {
		return RunStep{}, ErrInvalid
	}
	now := s.now().UTC()
	step.Status = StepQueued
	step.StartedAt = nil
	step.FinishedAt = nil
	step.Error = ""
	step.OutputSummary = ""
	s.steps[id] = step
	s.events[step.RunID] = append(s.events[step.RunID], RunEvent{ID: newID("evt"), RunID: step.RunID, Type: "step.recovered", Message: step.Name, CreatedAt: now})
	return step, nil
}

func (s *MemoryStore) CancelRunStep(id string) (RunStep, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	step, ok := s.steps[id]
	if !ok {
		return RunStep{}, ErrNotFound
	}
	if step.Status != StepQueued && step.Status != StepRunning {
		return RunStep{}, ErrInvalid
	}
	now := s.now().UTC()
	step.Status = StepCanceled
	step.FinishedAt = &now
	step.Error = "Canceled by operator"
	s.steps[id] = step
	s.events[step.RunID] = append(s.events[step.RunID], RunEvent{ID: newID("evt"), RunID: step.RunID, Type: "step.canceled", Message: step.Name, CreatedAt: now})
	return step, nil
}

func (s *MemoryStore) ListRunSteps(runID string) []RunStep {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []RunStep{}
	for _, step := range s.steps {
		if step.RunID == runID {
			out = append(out, step)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Position < out[j].Position })
	return out
}

func (s *MemoryStore) RunStep(id string) (RunStep, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	step, ok := s.steps[id]
	if !ok {
		return RunStep{}, ErrNotFound
	}
	return step, nil
}

func (s *MemoryStore) AddEvent(runID, typ, message string) (RunEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.runs[runID]; !ok {
		return RunEvent{}, ErrNotFound
	}
	e := RunEvent{ID: newID("evt"), RunID: runID, Type: typ, Message: message, CreatedAt: s.now().UTC()}
	s.events[runID] = append(s.events[runID], e)
	return e, nil
}

func (s *MemoryStore) AddArtifact(input CreateArtifactInput) (Artifact, error) {
	if err := validateArtifactInput(input); err != nil {
		return Artifact{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.runs[input.RunID]; !ok {
		return Artifact{}, ErrNotFound
	}
	now := s.now().UTC()
	artifact := Artifact{ID: newID("art"), RunID: input.RunID, Name: input.Name, Kind: input.Kind, URI: input.URI, CreatedAt: now}
	s.artifacts[input.RunID] = append(s.artifacts[input.RunID], artifact)
	s.events[input.RunID] = append(s.events[input.RunID], RunEvent{ID: newID("evt"), RunID: input.RunID, Type: "artifact.created", Message: input.Name + " (" + input.Kind + ")", CreatedAt: now})
	return artifact, nil
}

func (s *MemoryStore) ListRuns() []Run {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Run, 0, len(s.runs))
	for _, r := range s.runs {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func (s *MemoryStore) ListRunsPage(input ListRunsPageInput) (RunPage, error) {
	limit := input.Limit
	if limit < 1 || limit > 100 {
		return RunPage{}, ErrInvalid
	}
	runs := s.ListRuns()
	filtered := make([]Run, 0, min(len(runs), limit+1))
	for _, run := range runs {
		if input.BeforeCreated != nil {
			if run.CreatedAt.After(*input.BeforeCreated) || (run.CreatedAt.Equal(*input.BeforeCreated) && run.ID >= input.BeforeID) {
				continue
			}
		}
		filtered = append(filtered, run)
		if len(filtered) == limit+1 {
			break
		}
	}
	page := RunPage{Runs: filtered}
	if len(page.Runs) > limit {
		page.HasMore = true
		page.Runs = page.Runs[:limit]
	}
	return page, nil
}

func (s *MemoryStore) ListRunsPageContext(_ context.Context, input ListRunsPageInput) (RunPage, error) {
	return s.ListRunsPage(input)
}

func (s *MemoryStore) RunDetail(id string) (RunDetail, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.runs[id]
	if !ok {
		return RunDetail{}, ErrNotFound
	}
	return RunDetail{Run: r, Steps: s.listRunStepsLocked(id), Events: append([]RunEvent(nil), s.events[id]...), Artifacts: append([]Artifact(nil), s.artifacts[id]...)}, nil
}

func (s *MemoryStore) RunDetailContext(_ context.Context, id string) (RunDetail, error) {
	return s.RunDetail(id)
}

func (s *MemoryStore) listRunStepsLocked(runID string) []RunStep {
	out := []RunStep{}
	for _, step := range s.steps {
		if step.RunID == runID {
			out = append(out, step)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Position < out[j].Position })
	return out
}
