package controlplane

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"time"
)

var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid input")
)

type Store interface {
	UpsertWorkflow(Workflow) error
	UpsertTrigger(Trigger) error
	OpenPilotIssueState(repo, issue string) (OpenPilotIssueState, error)
	UpsertOpenPilotIssueState(OpenPilotIssueState) (OpenPilotIssueState, error)
	TransitionOpenPilotIssueState(repo, issue string, next OpenPilotPhase) (OpenPilotIssueState, error)
	ListOpenPilotIssueStates() []OpenPilotIssueState
	ListWorkflows() []Workflow
	ListTriggers() []Trigger
	Trigger(string) (Trigger, bool)
	SetTriggerEnabled(string, bool) (Trigger, error)
	UpdateTriggerScheduleState(string, *time.Time, *time.Time, string) (Trigger, error)
	CreateRun(CreateRunInput) (Run, error)
	StartRun(string) (Run, error)
	ClaimNextRun(string, time.Duration) (Run, error)
	ExtendRunLease(string, string, time.Duration) (Run, error)
	RecoverLeasedRuns(time.Time) (int, int, error)
	FinishRun(string, RunStatus, string) (Run, error)
	FinishClaimedRun(string, string, RunStatus, string) (Run, error)
	CreateRunStep(CreateRunStepInput) (RunStep, error)
	StartRunStep(string) (RunStep, error)
	UpdateRunStep(string, UpdateRunStepInput) (RunStep, error)
	FinishRunStep(string, RunStepStatus, string, string, map[string]string) (RunStep, error)
	RetryRunStep(string) (RunStep, error)
	CancelRunStep(string) (RunStep, error)
	ListRunSteps(string) []RunStep
	RunStep(string) (RunStep, error)
	AddEvent(string, string, string) (RunEvent, error)
	AddArtifact(CreateArtifactInput) (Artifact, error)
	ListRuns() []Run
	RunDetail(string) (RunDetail, error)
}

type MemoryStore struct {
	mu                   sync.RWMutex
	now                  func() time.Time
	workflows            map[string]Workflow
	triggers             map[string]Trigger
	openPilotIssueStates map[string]OpenPilotIssueState
	runs                 map[string]Run
	steps                map[string]RunStep
	events               map[string][]RunEvent
	artifacts            map[string][]Artifact
}

func NewStore(now func() time.Time) *MemoryStore {
	if now == nil {
		now = time.Now
	}
	return &MemoryStore{
		now:                  now,
		workflows:            map[string]Workflow{},
		triggers:             map[string]Trigger{},
		openPilotIssueStates: map[string]OpenPilotIssueState{},
		runs:                 map[string]Run{},
		steps:                map[string]RunStep{},
		events:               map[string][]RunEvent{},
		artifacts:            map[string][]Artifact{},
	}
}

func newID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return prefix + "-" + time.Now().UTC().Format("20060102150405.000000000")
	}
	return prefix + "-" + hex.EncodeToString(b[:])
}

func (s *MemoryStore) UpsertWorkflow(w Workflow) error {
	if w.ID == "" || w.Kind == "" || w.Name == "" {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workflows[w.ID] = w
	return nil
}

func (s *MemoryStore) UpsertTrigger(t Trigger) error {
	if t.ID == "" || t.WorkflowID == "" || t.Type == "" || t.Name == "" {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.workflows[t.WorkflowID]; !ok {
		return ErrNotFound
	}
	now := s.now().UTC()
	if t.UpdatedAt == nil {
		t.UpdatedAt = &now
	}
	s.triggers[t.ID] = t
	return nil
}

func (s *MemoryStore) ListWorkflows() []Workflow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Workflow, 0, len(s.workflows))
	for _, w := range s.workflows {
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *MemoryStore) ListTriggers() []Trigger {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Trigger, 0, len(s.triggers))
	for _, t := range s.triggers {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

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

func (s *MemoryStore) Trigger(id string) (Trigger, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.triggers[id]
	return t, ok
}

func (s *MemoryStore) SetTriggerEnabled(id string, enabled bool) (Trigger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.triggers[id]
	if !ok {
		return Trigger{}, ErrNotFound
	}
	now := s.now().UTC()
	t.Enabled = enabled
	t.UpdatedAt = &now
	s.triggers[id] = t
	return t, nil
}

func (s *MemoryStore) UpdateTriggerScheduleState(id string, lastRunAt *time.Time, nextRunAt *time.Time, lastError string) (Trigger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.triggers[id]
	if !ok {
		return Trigger{}, ErrNotFound
	}
	now := s.now().UTC()
	if lastRunAt != nil {
		last := lastRunAt.UTC()
		t.LastRunAt = &last
	}
	if nextRunAt != nil {
		next := nextRunAt.UTC()
		t.NextRunAt = &next
	}
	t.LastError = lastError
	t.UpdatedAt = &now
	s.triggers[id] = t
	return t, nil
}

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
	r := Run{ID: newID("run"), WorkflowID: input.WorkflowID, TriggerID: input.TriggerID, Status: RunQueued, Inputs: cloneMap(input.Inputs), CreatedAt: now, QueuedAt: now, AvailableAt: now, MaxAttempts: 1}
	s.runs[r.ID] = r
	s.events[r.ID] = append(s.events[r.ID], RunEvent{ID: newID("evt"), RunID: r.ID, Type: "run.created", Message: "Run queued", CreatedAt: now})
	return r, nil
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
			s.events[id] = append(s.events[id], RunEvent{ID: newID("evt"), RunID: id, Type: "run.failed", Message: run.Summary, CreatedAt: now})
			failed++
		}
		s.runs[id] = run
	}
	return requeued, failed, nil
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
	if len(correlationIDs) > 0 {
		step.CorrelationIDs = cloneMap(correlationIDs)
	}
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

func (s *MemoryStore) RetryRunStep(id string) (RunStep, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	step, ok := s.steps[id]
	if !ok {
		return RunStep{}, ErrNotFound
	}
	if step.Status != StepFailed && step.Status != StepCanceled && step.Status != StepTimedOut {
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
	if input.RunID == "" || input.Name == "" || input.Kind == "" || input.URI == "" {
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

func (s *MemoryStore) RunDetail(id string) (RunDetail, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.runs[id]
	if !ok {
		return RunDetail{}, ErrNotFound
	}
	return RunDetail{Run: r, Steps: s.listRunStepsLocked(id), Events: append([]RunEvent(nil), s.events[id]...), Artifacts: append([]Artifact(nil), s.artifacts[id]...)}, nil
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

func isTerminalStepStatus(status RunStepStatus) bool {
	return status == StepSucceeded || status == StepFailed || status == StepCanceled || status == StepTimedOut
}

func cloneMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func openPilotIssueStateKey(repo, issue string) string {
	return repo + "\x00" + issue
}
