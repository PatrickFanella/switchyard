package controlplane

import (
	"context"
	"sort"
	"time"
)

// ListExecutorSessionSteps returns retained run steps that recorded an
// executor session correlation, newest first by (created_at, id).
func (s *MemoryStore) ListExecutorSessionSteps(_ context.Context, input ExecutorSessionStepsInput) (ExecutorSessionStepPage, error) {
	input, err := normalizeExecutorSessionStepsInput(input)
	if err != nil {
		return ExecutorSessionStepPage{}, err
	}
	s.mu.RLock()
	records := make([]ExecutorSessionStepRecord, 0)
	for _, step := range s.steps {
		if step.CreatedAt.Before(input.RetainedSince) || !executorSessionRecordMatches(step.CorrelationIDs, input.SessionID) {
			continue
		}
		run := s.runs[step.RunID]
		records = append(records, executorSessionRecord(step, run))
	}
	s.mu.RUnlock()
	sort.Slice(records, func(i, j int) bool {
		if !records[i].StepCreatedAt.Equal(records[j].StepCreatedAt) {
			return records[i].StepCreatedAt.After(records[j].StepCreatedAt)
		}
		return records[i].StepID > records[j].StepID
	})
	page := ExecutorSessionStepPage{Records: records}
	if len(page.Records) > input.Limit {
		page.Records = page.Records[:input.Limit]
		page.Truncated = true
	}
	return page, nil
}

func executorSessionRecord(step RunStep, run Run) ExecutorSessionStepRecord {
	record := ExecutorSessionStepRecord{
		StepID: step.ID, StepName: step.Name, StepPosition: step.Position, StepExecutor: step.Executor, StepStatus: step.Status,
		CorrelationIDs: cloneMap(step.CorrelationIDs), StepCreatedAt: step.CreatedAt.UTC(), StepStartedAt: cloneExecutorSessionTime(step.StartedAt), StepFinishedAt: cloneExecutorSessionTime(step.FinishedAt),
		RunID: step.RunID, WorkflowID: run.WorkflowID, RunStatus: run.Status, RunLeaseExpiresAt: cloneExecutorSessionTime(run.LeaseExpiresAt),
		RepositoryID: run.Inputs[openPilotRepositoryIDInputKey], ExecutorGeneration: run.Inputs[openPilotExecutorGenerationInputKey], Repo: run.Inputs["repo"],
	}
	return record
}

func cloneExecutorSessionTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copied := value.UTC()
	return &copied
}
