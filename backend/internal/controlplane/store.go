package controlplane

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid input")
)

type Store interface {
	UpsertWorkflowDefinition(WorkflowDefinition) (WorkflowDefinition, error)
	WorkflowDefinition(string) (WorkflowDefinition, error)
	ListWorkflowDefinitions() ([]WorkflowDefinition, error)
	UpsertWorkflow(Workflow) error
	UpsertTrigger(Trigger) error
	OpenPilotIssueState(repo, issue string) (OpenPilotIssueState, error)
	UpsertOpenPilotIssueState(OpenPilotIssueState) (OpenPilotIssueState, error)
	TransitionOpenPilotIssueState(repo, issue string, next OpenPilotPhase) (OpenPilotIssueState, error)
	ClaimOpenPilotIssueStateForResume(repo, issue string, resumeInputs map[string]string) (OpenPilotIssueState, error)
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
	CreateModelBrokerRequest(CreateModelBrokerRequestInput) (ModelBrokerRequest, error)
	StartModelBrokerRequest(string) (ModelBrokerRequest, error)
	FinishModelBrokerRequest(string, FinishModelBrokerRequestInput) (ModelBrokerRequest, error)
	CancelModelBrokerRequest(string) (ModelBrokerRequest, error)
	CancelAllModelBrokerRequests() (int, error)
	ListModelBrokerRequests(ListModelBrokerRequestsFilter) ([]ModelBrokerRequest, error)
	ModelBrokerRequest(string) (ModelBrokerRequest, error)
}

type MemoryStore struct {
	mu                   sync.RWMutex
	now                  func() time.Time
	workflowDefinitions  map[string]WorkflowDefinition
	workflows            map[string]Workflow
	triggers             map[string]Trigger
	openPilotIssueStates map[string]OpenPilotIssueState
	runs                 map[string]Run
	steps                map[string]RunStep
	events               map[string][]RunEvent
	artifacts            map[string][]Artifact
	modelBrokerRequests  map[string]ModelBrokerRequest
}

func NewStore(now func() time.Time) *MemoryStore {
	if now == nil {
		now = time.Now
	}
	return &MemoryStore{
		now:                  now,
		workflowDefinitions:  map[string]WorkflowDefinition{},
		workflows:            map[string]Workflow{},
		triggers:             map[string]Trigger{},
		openPilotIssueStates: map[string]OpenPilotIssueState{},
		runs:                 map[string]Run{},
		steps:                map[string]RunStep{},
		events:               map[string][]RunEvent{},
		artifacts:            map[string][]Artifact{},
		modelBrokerRequests:  map[string]ModelBrokerRequest{},
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

func (s *MemoryStore) UpsertWorkflowDefinition(definition WorkflowDefinition) (WorkflowDefinition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	definition, err := normalizeWorkflowDefinition(definition)
	if err != nil {
		return WorkflowDefinition{}, err
	}
	now := s.now().UTC()
	if existing, ok := s.workflowDefinitions[definition.ID]; ok {
		definition.CreatedAt = existing.CreatedAt
	} else {
		definition.CreatedAt = now
	}
	definition.UpdatedAt = now
	s.workflowDefinitions[definition.ID] = cloneWorkflowDefinition(definition)
	return cloneWorkflowDefinition(definition), nil
}

func (s *MemoryStore) WorkflowDefinition(id string) (WorkflowDefinition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	definition, ok := s.workflowDefinitions[id]
	if !ok {
		return WorkflowDefinition{}, ErrNotFound
	}
	return cloneWorkflowDefinition(definition), nil
}

func (s *MemoryStore) ListWorkflowDefinitions() ([]WorkflowDefinition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]WorkflowDefinition, 0, len(s.workflowDefinitions))
	for _, definition := range s.workflowDefinitions {
		out = append(out, cloneWorkflowDefinition(definition))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
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

func (s *MemoryStore) CreateModelBrokerRequest(input CreateModelBrokerRequestInput) (ModelBrokerRequest, error) {
	if strings.TrimSpace(input.ID) == "" || strings.TrimSpace(input.Model) == "" {
		return ModelBrokerRequest{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	if existing, ok := s.modelBrokerRequests[input.ID]; ok {
		return cloneModelBrokerRequest(existing), nil
	}
	req := ModelBrokerRequest{ID: input.ID, Status: ModelBrokerRequestQueued, Model: input.Model, Path: input.Path, Client: input.Client, ClientPriority: input.Priority, RequestBody: input.RequestBody, CorrelationIDs: map[string]string{}, CreatedAt: now, UpdatedAt: now}
	s.modelBrokerRequests[req.ID] = req
	return cloneModelBrokerRequest(req), nil
}

func (s *MemoryStore) StartModelBrokerRequest(id string) (ModelBrokerRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	req, ok := s.modelBrokerRequests[id]
	if !ok {
		return ModelBrokerRequest{}, ErrNotFound
	}
	now := s.now().UTC()
	req.Status = ModelBrokerRequestRunning
	req.StartedAt = &now
	req.UpdatedAt = now
	s.modelBrokerRequests[id] = req
	return cloneModelBrokerRequest(req), nil
}

func (s *MemoryStore) FinishModelBrokerRequest(id string, input FinishModelBrokerRequestInput) (ModelBrokerRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	req, ok := s.modelBrokerRequests[id]
	if !ok {
		return ModelBrokerRequest{}, ErrNotFound
	}
	now := s.now().UTC()
	status := strings.TrimSpace(input.Status)
	if status == "" {
		status = ModelBrokerRequestCompleted
	}
	req.Status = status
	req.ResponseBody = input.ResponseBody
	req.Error = input.Error
	req.CorrelationIDs = cloneMap(input.CorrelationIDs)
	req.FinishedAt = &now
	req.UpdatedAt = now
	s.modelBrokerRequests[id] = req
	return cloneModelBrokerRequest(req), nil
}

func (s *MemoryStore) CancelModelBrokerRequest(id string) (ModelBrokerRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	req, ok := s.modelBrokerRequests[id]
	if !ok {
		return ModelBrokerRequest{}, ErrNotFound
	}
	now := s.now().UTC()
	req.Status = ModelBrokerRequestCancelRequested
	req.CancelRequestedAt = &now
	req.UpdatedAt = now
	s.modelBrokerRequests[id] = req
	return cloneModelBrokerRequest(req), nil
}

func (s *MemoryStore) CancelAllModelBrokerRequests() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	count := 0
	for id, req := range s.modelBrokerRequests {
		if req.Status == ModelBrokerRequestQueued || req.Status == ModelBrokerRequestRunning || req.Status == ModelBrokerRequestCancelRequested {
			req.Status = ModelBrokerRequestCancelRequested
			req.CancelRequestedAt = &now
			req.UpdatedAt = now
			s.modelBrokerRequests[id] = req
			count++
		}
	}
	return count, nil
}

func (s *MemoryStore) ListModelBrokerRequests(filter ListModelBrokerRequestsFilter) ([]ModelBrokerRequest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	statuses := map[string]bool{}
	for _, status := range filter.Statuses {
		statuses[status] = true
	}
	query := strings.ToLower(strings.TrimSpace(filter.Query))
	out := []ModelBrokerRequest{}
	for _, req := range s.modelBrokerRequests {
		if len(statuses) > 0 && !statuses[req.Status] {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(req.ID+" "+req.Model+" "+req.Path+" "+req.Client+" "+req.RequestBody+" "+req.ResponseBody+" "+req.Error), query) {
			continue
		}
		out = append(out, cloneModelBrokerRequest(req))
	}
	sort.Slice(out, func(i, j int) bool {
		if len(statuses) > 0 && out[i].Status != ModelBrokerRequestCompleted && out[j].Status != ModelBrokerRequestCompleted {
			if out[i].ClientPriority != out[j].ClientPriority {
				return out[i].ClientPriority > out[j].ClientPriority
			}
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MemoryStore) ModelBrokerRequest(id string) (ModelBrokerRequest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	req, ok := s.modelBrokerRequests[id]
	if !ok {
		return ModelBrokerRequest{}, ErrNotFound
	}
	return cloneModelBrokerRequest(req), nil
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
	return status == StepSucceeded || status == StepFailed || status == StepCanceled || status == StepTimedOut || status == StepPaused
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

func normalizeWorkflowDefinition(definition WorkflowDefinition) (WorkflowDefinition, error) {
	definition = cloneWorkflowDefinition(definition)
	definition.ID = strings.TrimSpace(definition.ID)
	definition.Name = strings.TrimSpace(definition.Name)
	definition.Description = strings.TrimSpace(definition.Description)
	definition.Status = strings.TrimSpace(definition.Status)
	definition.Pipeline.ID = strings.TrimSpace(definition.Pipeline.ID)
	definition.Pipeline.WorkflowID = strings.TrimSpace(definition.Pipeline.WorkflowID)
	definition.Pipeline.Name = strings.TrimSpace(definition.Pipeline.Name)
	definition.Pipeline.Description = strings.TrimSpace(definition.Pipeline.Description)
	if definition.ID == "" || definition.Name == "" || definition.Pipeline.ID == "" || definition.Pipeline.Name == "" || len(definition.Pipeline.Steps) == 0 {
		return WorkflowDefinition{}, ErrInvalid
	}
	if definition.Status == "" {
		definition.Status = "draft"
	}
	if definition.Owns == nil {
		definition.Owns = []string{}
	}
	if definition.Uses == nil {
		definition.Uses = []string{}
	}
	if definition.Pipeline.WorkflowID == "" {
		definition.Pipeline.WorkflowID = definition.ID
	}
	if definition.Pipeline.WorkflowID != definition.ID {
		return WorkflowDefinition{}, ErrInvalid
	}
	for i := range definition.Pipeline.Steps {
		step := &definition.Pipeline.Steps[i]
		step.ID = strings.TrimSpace(step.ID)
		step.Name = strings.TrimSpace(step.Name)
		step.Executor = strings.TrimSpace(step.Executor)
		step.Profile = strings.TrimSpace(step.Profile)
		step.Model = strings.TrimSpace(step.Model)
		if step.ID == "" || step.Position < 1 || step.Name == "" || step.Executor == "" {
			return WorkflowDefinition{}, ErrInvalid
		}
	}
	return definition, nil
}

func cloneJSONShape(shape JSONShape) JSONShape {
	shape.Required = append([]string(nil), shape.Required...)
	if shape.Properties != nil {
		properties := make(map[string]JSONShapeProperty, len(shape.Properties))
		for key, value := range shape.Properties {
			properties[key] = value
		}
		shape.Properties = properties
	}
	return shape
}

func cloneWorkflowDefinition(definition WorkflowDefinition) WorkflowDefinition {
	definition.Owns = append([]string(nil), definition.Owns...)
	definition.Uses = append([]string(nil), definition.Uses...)
	definition.InputShape = cloneJSONShape(definition.InputShape)
	definition.Pipeline.Steps = append([]PipelineStepDefinition(nil), definition.Pipeline.Steps...)
	for i := range definition.Pipeline.Steps {
		definition.Pipeline.Steps[i].Inputs = cloneMap(definition.Pipeline.Steps[i].Inputs)
		definition.Pipeline.Steps[i].OutputShape = cloneJSONShape(definition.Pipeline.Steps[i].OutputShape)
	}
	return definition
}

func openPilotIssueStateKey(repo, issue string) string {
	return repo + "\x00" + issue
}
