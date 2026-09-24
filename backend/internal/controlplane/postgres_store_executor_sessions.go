package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// ListExecutorSessionSteps reads retained session-bearing run steps newest
// first. Only three run-input keys are projected (repository affinity and the
// repository slug); no other input, output, summary, or error is selected.
// The created_at bound and ordering use run_steps_created_at_id_idx
// (migration 028), and the scan stops after Limit+1 matching rows.
func (s *PostgresStore) ListExecutorSessionSteps(ctx context.Context, input ExecutorSessionStepsInput) (ExecutorSessionStepPage, error) {
	input, err := normalizeExecutorSessionStepsInput(input)
	if err != nil {
		return ExecutorSessionStepPage{}, err
	}
	args := []any{input.RetainedSince}
	sessionFilter := ""
	if input.SessionID != "" {
		args = append(args, input.SessionID)
		sessionFilter = fmt.Sprintf(" and (s.correlation_ids->>'opencode_session_id' = $%d or s.correlation_ids->>'ocq_session_id' = $%d)", len(args), len(args))
	}
	args = append(args, openPilotRepositoryIDInputKey, openPilotExecutorGenerationInputKey, input.Limit+1)
	repositoryArg, generationArg, limitArg := len(args)-2, len(args)-1, len(args)
	query := fmt.Sprintf(`select s.id, s.run_id, s.position, s.name, s.executor, s.status, s.correlation_ids, s.created_at, s.started_at, s.finished_at,
       r.workflow_id, r.status, r.lease_expires_at,
       coalesce(case when jsonb_typeof(r.inputs) = 'object' then r.inputs->>$%d end, ''),
       coalesce(case when jsonb_typeof(r.inputs) = 'object' then r.inputs->>$%d end, ''),
       coalesce(case when jsonb_typeof(r.inputs) = 'object' then r.inputs->>'repo' end, '')
  from run_steps s join runs r on r.id = s.run_id
 where s.created_at >= $1
   and jsonb_typeof(s.correlation_ids) = 'object'
   and (coalesce(s.correlation_ids->>'opencode_session_id', '') <> '' or coalesce(s.correlation_ids->>'ocq_session_id', '') <> '')%s
 order by s.created_at desc, s.id desc
 limit $%d`, repositoryArg, generationArg, sessionFilter, limitArg)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return ExecutorSessionStepPage{}, err
	}
	defer rows.Close()
	page := ExecutorSessionStepPage{Records: []ExecutorSessionStepRecord{}}
	for rows.Next() {
		var record ExecutorSessionStepRecord
		var correlation []byte
		var started, finished, lease sql.NullTime
		if err := rows.Scan(&record.StepID, &record.RunID, &record.StepPosition, &record.StepName, &record.StepExecutor, &record.StepStatus, &correlation, &record.StepCreatedAt, &started, &finished,
			&record.WorkflowID, &record.RunStatus, &lease, &record.RepositoryID, &record.ExecutorGeneration, &record.Repo); err != nil {
			return ExecutorSessionStepPage{}, err
		}
		record.StepCreatedAt = record.StepCreatedAt.UTC()
		record.StepStartedAt = executorSessionNullTime(started)
		record.StepFinishedAt = executorSessionNullTime(finished)
		record.RunLeaseExpiresAt = executorSessionNullTime(lease)
		_ = json.Unmarshal(correlation, &record.CorrelationIDs)
		page.Records = append(page.Records, record)
	}
	if err := rows.Err(); err != nil {
		return ExecutorSessionStepPage{}, err
	}
	if len(page.Records) > input.Limit {
		page.Records = page.Records[:input.Limit]
		page.Truncated = true
	}
	return page, nil
}

func executorSessionNullTime(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	return cloneExecutorSessionTime(&value.Time)
}
