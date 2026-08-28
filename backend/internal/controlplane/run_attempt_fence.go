package controlplane

import (
	"context"
	"fmt"
)

type RunAttemptFence struct {
	RunID    string
	WorkerID string
	Attempt  int
}

type runAttemptFenceContextKey struct{}

func withRunAttemptFence(ctx context.Context, fence RunAttemptFence) context.Context {
	return context.WithValue(ctx, runAttemptFenceContextKey{}, fence)
}

func runAttemptFenceFromContext(ctx context.Context) (RunAttemptFence, bool, error) {
	value := ctx.Value(runAttemptFenceContextKey{})
	if value == nil {
		return RunAttemptFence{}, false, nil
	}
	fence, ok := value.(RunAttemptFence)
	if !ok || fence.RunID == "" || fence.WorkerID == "" || fence.Attempt < 1 {
		return RunAttemptFence{}, true, ErrStaleRunAttempt
	}
	return fence, true, nil
}

type memoryStoreContext struct {
	*MemoryStore
	fence        RunAttemptFence
	fencePresent bool
	fenceErr     error
}

type runAttemptStoreValidator interface {
	validateRunAttemptFence() error
}

func validateAttemptStore(store Store) error {
	if validator, ok := store.(runAttemptStoreValidator); ok {
		return validator.validateRunAttemptFence()
	}
	return nil
}

func (s *memoryStoreContext) validateRunAttemptFence() error {
	s.MemoryStore.mu.Lock()
	defer s.MemoryStore.mu.Unlock()
	return s.MemoryStore.validateRunAttemptFenceLocked(s.fence, s.fencePresent, s.fenceErr, s.fence.RunID)
}

func (s *memoryStoreContext) AdoptOpenPilotRunAffinity(runID, expectedWorkerID string, expectedAttemptCount int, gate *OpenPilotAdmissionGate) (Run, OpenPilotIssueState, error) {
	if runID == "" || expectedWorkerID == "" || expectedAttemptCount < 1 {
		return Run{}, OpenPilotIssueState{}, ErrInvalid
	}
	s.MemoryStore.mu.Lock()
	defer s.MemoryStore.mu.Unlock()
	if err := s.MemoryStore.validateRunAttemptFenceLocked(s.fence, s.fencePresent, s.fenceErr, runID); err != nil {
		return Run{}, OpenPilotIssueState{}, err
	}
	if s.fencePresent && (s.fence.WorkerID != expectedWorkerID || s.fence.Attempt != expectedAttemptCount) {
		return Run{}, OpenPilotIssueState{}, ErrStaleRunAttempt
	}
	run := s.MemoryStore.runs[runID]
	if run.Status != RunRunning || run.ClaimedBy != expectedWorkerID || run.AttemptCount != expectedAttemptCount || run.LeaseExpiresAt == nil || !run.LeaseExpiresAt.After(s.MemoryStore.now().UTC()) {
		return Run{}, OpenPilotIssueState{}, ErrConflict
	}
	return s.MemoryStore.adoptOpenPilotRunAffinityLocked(runID, gate, s.MemoryStore.now().UTC())
}

func (s *PostgresStore) validateRunAttemptFence() error {
	tx, err := s.db.BeginTx(s.operationContext(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	fence, present, fenceErr := runAttemptFenceFromContext(s.operationContext())
	if !present {
		return nil
	}
	if fenceErr != nil {
		return ErrStaleRunAttempt
	}
	return s.lockRunAttemptFenceTx(tx, fence.RunID)
}

func (s *MemoryStore) validateRunAttemptFenceLocked(fence RunAttemptFence, present bool, fenceErr error, runID string) error {
	if !present {
		return nil
	}
	if fenceErr != nil || fence.RunID != runID {
		return ErrStaleRunAttempt
	}
	run, ok := s.runs[runID]
	now := s.now().UTC()
	if !ok || run.Status != RunRunning || run.ClaimedBy != fence.WorkerID || run.AttemptCount != fence.Attempt || run.LeaseExpiresAt == nil || !run.LeaseExpiresAt.After(now) {
		return ErrStaleRunAttempt
	}
	return nil
}

func staleFenceTargetError(target string) error {
	return fmt.Errorf("%w: mutation target %s", ErrStaleRunAttempt, target)
}

func (s *memoryStoreContext) preflight(runID string) error {
	if !s.fencePresent {
		return nil
	}
	if s.fenceErr != nil || (runID != "" && s.fence.RunID != runID) {
		return ErrStaleRunAttempt
	}
	return nil
}

func (s *memoryStoreContext) CreateRunStep(input CreateRunStepInput) (RunStep, error) {
	if err := s.preflight(input.RunID); err != nil {
		return RunStep{}, err
	}
	return s.MemoryStore.createRunStep(s.fence, s.fencePresent, s.fenceErr, input)
}
func (s *memoryStoreContext) StartRunStep(id string) (RunStep, error) {
	if err := s.preflight(""); err != nil {
		return RunStep{}, err
	}
	return s.MemoryStore.startRunStep(s.fence, s.fencePresent, s.fenceErr, id)
}
func (s *memoryStoreContext) UpdateRunStep(id string, input UpdateRunStepInput) (RunStep, error) {
	if err := s.preflight(""); err != nil {
		return RunStep{}, err
	}
	return s.MemoryStore.updateRunStep(s.fence, s.fencePresent, s.fenceErr, id, input)
}
func (s *memoryStoreContext) FinishRunStep(id string, status RunStepStatus, summary, errText string, ids map[string]string) (RunStep, error) {
	if err := s.preflight(""); err != nil {
		return RunStep{}, err
	}
	return s.MemoryStore.finishRunStep(s.fence, s.fencePresent, s.fenceErr, id, status, summary, errText, ids)
}
func (s *memoryStoreContext) MergeRunStepCorrelationIDs(id string, ids map[string]string) (RunStep, error) {
	if err := s.preflight(""); err != nil {
		return RunStep{}, err
	}
	return s.MemoryStore.mergeRunStepCorrelationIDs(s.fence, s.fencePresent, s.fenceErr, id, ids)
}
func (s *memoryStoreContext) RecordRunStepBackendDecision(runID, stepID string, decision BackendDecision) error {
	if err := s.preflight(runID); err != nil {
		return err
	}
	return s.MemoryStore.recordRunStepBackendDecision(s.fence, s.fencePresent, s.fenceErr, runID, stepID, decision)
}
func (s *memoryStoreContext) RetryRunStep(id string) (RunStep, error) {
	if err := s.preflight(""); err != nil {
		return RunStep{}, err
	}
	return s.MemoryStore.retryRunStep(s.fence, s.fencePresent, s.fenceErr, id)
}
func (s *memoryStoreContext) RecoverRunStep(id string) (RunStep, error) {
	if err := s.preflight(""); err != nil {
		return RunStep{}, err
	}
	return s.MemoryStore.recoverRunStep(s.fence, s.fencePresent, s.fenceErr, id)
}
func (s *memoryStoreContext) CancelRunStep(id string) (RunStep, error) {
	if err := s.preflight(""); err != nil {
		return RunStep{}, err
	}
	return s.MemoryStore.cancelRunStep(s.fence, s.fencePresent, s.fenceErr, id)
}
func (s *memoryStoreContext) AddEvent(runID, typ, message string) (RunEvent, error) {
	if err := s.preflight(runID); err != nil {
		return RunEvent{}, err
	}
	return s.MemoryStore.addEvent(s.fence, s.fencePresent, s.fenceErr, runID, typ, message)
}
func (s *memoryStoreContext) AddArtifact(input CreateArtifactInput) (Artifact, error) {
	if err := s.preflight(input.RunID); err != nil {
		return Artifact{}, err
	}
	return s.MemoryStore.addArtifact(s.fence, s.fencePresent, s.fenceErr, input)
}
func (s *memoryStoreContext) FinishRun(id string, status RunStatus, summary string) (Run, error) {
	if err := s.preflight(id); err != nil {
		return Run{}, err
	}
	return s.MemoryStore.finishRunFenced(s.fence, s.fencePresent, s.fenceErr, id, "", 0, status, summary)
}
func (s *memoryStoreContext) FinishClaimedRun(id, workerID string, attempt int, status RunStatus, summary string) (Run, error) {
	if err := s.preflight(id); err != nil || (s.fencePresent && (workerID != s.fence.WorkerID || attempt != s.fence.Attempt)) {
		return Run{}, ErrStaleRunAttempt
	}
	return s.MemoryStore.finishRunFenced(s.fence, s.fencePresent, s.fenceErr, id, workerID, attempt, status, summary)
}
func (s *memoryStoreContext) UpsertOpenPilotIssueState(state OpenPilotIssueState) (OpenPilotIssueState, error) {
	if err := s.preflight(state.RunID); err != nil {
		return OpenPilotIssueState{}, err
	}
	return s.MemoryStore.upsertOpenPilotIssueState(s.fence, s.fencePresent, s.fenceErr, state)
}
func (s *memoryStoreContext) TransitionOpenPilotIssueState(repo, issue string, phase OpenPilotPhase) (OpenPilotIssueState, error) {
	if err := s.preflight(""); err != nil {
		return OpenPilotIssueState{}, err
	}
	return s.MemoryStore.transitionOpenPilotIssueState(s.fence, s.fencePresent, s.fenceErr, repo, issue, phase)
}
func (s *memoryStoreContext) HandleOpenPilotCIFailure(request OpenPilotCIRepairRequest) (OpenPilotCIRepairResult, error) {
	if err := s.preflight(request.ParentRunID); err != nil {
		return OpenPilotCIRepairResult{}, err
	}
	return s.MemoryStore.handleOpenPilotCIFailure(s.fence, s.fencePresent, s.fenceErr, request)
}
