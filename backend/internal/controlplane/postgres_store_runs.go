package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

func (s *PostgresStore) CreateRun(input CreateRunInput) (Run, error) {
	return s.CreateRunContext(s.operationContext(), input)
}

func (s *PostgresStore) CreateRunContext(ctx context.Context, input CreateRunInput) (Run, error) {
	if input.WorkflowID == "" {
		return Run{}, ErrInvalid
	}
	inputs, err := json.Marshal(input.Inputs)
	if err != nil {
		return Run{}, err
	}
	now := s.now().UTC()
	maxAttempts := input.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	r := Run{ID: newID("run"), WorkflowID: input.WorkflowID, TriggerID: input.TriggerID, Status: RunQueued, Inputs: cloneMap(input.Inputs), CreatedAt: now, QueuedAt: now, AvailableAt: now, MaxAttempts: maxAttempts}
	var pipelineSnapshot any
	if input.PipelineSnapshot != nil {
		snapshot := clonePipeline(*input.PipelineSnapshot)
		r.PipelineID = snapshot.ID
		r.PipelineVersion = pipelineVersion(snapshot)
		r.PipelineSnapshot = &snapshot
		encoded, marshalErr := json.Marshal(snapshot)
		if marshalErr != nil {
			return Run{}, marshalErr
		}
		pipelineSnapshot = string(encoded)
	}
	triggerID := sql.NullString{String: input.TriggerID, Valid: input.TriggerID != ""}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, err
	}
	if _, err := tx.ExecContext(ctx, `insert into runs(id, workflow_id, trigger_id, status, inputs, created_at, queued_at, available_at, attempt_count, max_attempts, summary, pipeline_id, pipeline_version, pipeline_snapshot) values($1,$2,$3,$4,$5::jsonb,$6,$7,$8,$9,$10,'',$11,$12,$13::jsonb)`, r.ID, r.WorkflowID, triggerID, r.Status, string(inputs), r.CreatedAt, r.QueuedAt, r.AvailableAt, r.AttemptCount, r.MaxAttempts, r.PipelineID, r.PipelineVersion, pipelineSnapshot); err != nil {
		_ = tx.Rollback()
		return Run{}, mapPgErr(err)
	}
	if _, err := tx.ExecContext(ctx, `insert into run_events(id, run_id, type, message, created_at) values($1,$2,$3,$4,$5)`, newID("evt"), r.ID, "run.created", "Run queued", now); err != nil {
		_ = tx.Rollback()
		return Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, err
	}
	return r, nil
}

func (s *PostgresStore) StartRun(id string) (Run, error) {
	now := s.now().UTC()
	tx, err := s.db.BeginTx(s.operationContext(), nil)
	if err != nil {
		return Run{}, err
	}
	row := tx.QueryRowContext(s.operationContext(), `update runs set status=$2, started_at=coalesce(started_at,$3), attempt_count=attempt_count+1 where id=$1 returning id, workflow_id, coalesce(trigger_id,''), status, inputs, created_at, queued_at, available_at, started_at, finished_at, claimed_at, claimed_by, lease_expires_at, attempt_count, max_attempts, summary, pipeline_id, pipeline_version, pipeline_snapshot`, id, RunRunning, now)
	r, err := scanRunRow(row)
	if err != nil {
		_ = tx.Rollback()
		return Run{}, mapScanErr(err)
	}
	if _, err := tx.ExecContext(s.operationContext(), `insert into run_events(id, run_id, type, message, created_at) values($1,$2,$3,$4,$5)`, newID("evt"), id, "run.started", "Run started", now); err != nil {
		_ = tx.Rollback()
		return Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, err
	}
	return r, nil
}

func (s *PostgresStore) ClaimNextRun(workerID string, lease time.Duration) (Run, error) {
	if workerID == "" || lease <= 0 {
		return Run{}, ErrInvalid
	}
	now := s.now().UTC()
	expires := now.Add(lease).UTC()
	tx, err := s.db.BeginTx(s.operationContext(), nil)
	if err != nil {
		return Run{}, err
	}
	row := tx.QueryRowContext(s.operationContext(), `
		with next_run as (
			select id from runs
			where status=$1 and available_at <= $2
			order by created_at, id
			for update skip locked
			limit 1
		)
		update runs set status=$3, started_at=coalesce(started_at,$2), claimed_at=$2, claimed_by=$4, lease_expires_at=$5, attempt_count=attempt_count+1
		where id in (select id from next_run)
			returning id, workflow_id, coalesce(trigger_id,''), status, inputs, created_at, queued_at, available_at, started_at, finished_at, claimed_at, claimed_by, lease_expires_at, attempt_count, max_attempts, summary, pipeline_id, pipeline_version, pipeline_snapshot`, RunQueued, now, RunRunning, workerID, expires)
	r, err := scanRunRow(row)
	if err != nil {
		_ = tx.Rollback()
		return Run{}, mapScanErr(err)
	}
	if _, err := tx.ExecContext(s.operationContext(), `insert into run_events(id, run_id, type, message, created_at) values($1,$2,$3,$4,$5)`, newID("evt"), r.ID, "run.started", "Run claimed by "+workerID, now); err != nil {
		_ = tx.Rollback()
		return Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, err
	}
	return r, nil
}

func (s *PostgresStore) ExtendRunLease(id string, workerID string, lease time.Duration) (Run, error) {
	if id == "" || workerID == "" || lease <= 0 {
		return Run{}, ErrInvalid
	}
	now := s.now().UTC()
	expires := now.Add(lease).UTC()
	tx, err := s.db.BeginTx(s.operationContext(), nil)
	if err != nil {
		return Run{}, err
	}
	row := tx.QueryRowContext(s.operationContext(), `update runs set claimed_at=$3, lease_expires_at=$4 where id=$1 and status=$5 and claimed_by=$2 returning id, workflow_id, coalesce(trigger_id,''), status, inputs, created_at, queued_at, available_at, started_at, finished_at, claimed_at, claimed_by, lease_expires_at, attempt_count, max_attempts, summary, pipeline_id, pipeline_version, pipeline_snapshot`, id, workerID, now, expires, RunRunning)
	r, err := scanRunRow(row)
	if err != nil {
		_ = tx.Rollback()
		return Run{}, mapScanErr(err)
	}
	if _, err := tx.ExecContext(s.operationContext(), `insert into run_events(id, run_id, type, message, created_at) values($1,$2,$3,$4,$5)`, newID("evt"), id, "run.lease.extended", "Lease extended by "+workerID, now); err != nil {
		_ = tx.Rollback()
		return Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, err
	}
	return r, nil
}

func (s *PostgresStore) RecoverLeasedRuns(at time.Time) (int, int, error) {
	now := at.UTC()
	tx, err := s.db.BeginTx(s.operationContext(), nil)
	if err != nil {
		return 0, 0, err
	}
	requeueRows, err := tx.QueryContext(s.operationContext(), `update runs set status=$2, available_at=$3, claimed_at=null, claimed_by='', lease_expires_at=null where status=$1 and lease_expires_at is not null and lease_expires_at <= $3 and attempt_count < max_attempts returning id`, RunRunning, RunQueued, now)
	if err != nil {
		_ = tx.Rollback()
		return 0, 0, err
	}
	requeuedIDs := []string{}
	for requeueRows.Next() {
		var id string
		if err := requeueRows.Scan(&id); err == nil {
			requeuedIDs = append(requeuedIDs, id)
		}
	}
	requeueRows.Close()
	failRows, err := tx.QueryContext(s.operationContext(), `update runs set status=$2, finished_at=$3, summary='Worker lease expired', claimed_at=null, claimed_by='', lease_expires_at=null where status=$1 and lease_expires_at is not null and lease_expires_at <= $3 and attempt_count >= max_attempts returning id`, RunRunning, RunFailed, now)
	if err != nil {
		_ = tx.Rollback()
		return 0, 0, err
	}
	failedIDs := []string{}
	for failRows.Next() {
		var id string
		if err := failRows.Scan(&id); err == nil {
			failedIDs = append(failedIDs, id)
		}
	}
	failRows.Close()
	for _, id := range requeuedIDs {
		if _, err := tx.ExecContext(s.operationContext(), `insert into run_events(id, run_id, type, message, created_at) values($1,$2,$3,$4,$5)`, newID("evt"), id, "run.recovered", "Worker lease expired; run requeued", now); err != nil {
			_ = tx.Rollback()
			return 0, 0, err
		}
	}
	for _, id := range failedIDs {
		stepRows, err := tx.QueryContext(s.operationContext(), `update run_steps set status=$2, error='Worker lease expired', finished_at=$3 where run_id=$1 and status=$4 returning id`, id, StepFailed, now, StepRunning)
		if err != nil {
			_ = tx.Rollback()
			return 0, 0, err
		}
		stepIDs := []string{}
		for stepRows.Next() {
			var stepID string
			if err := stepRows.Scan(&stepID); err != nil {
				stepRows.Close()
				_ = tx.Rollback()
				return 0, 0, err
			}
			stepIDs = append(stepIDs, stepID)
		}
		stepRows.Close()
		for _, stepID := range stepIDs {
			if _, err := tx.ExecContext(s.operationContext(), `insert into run_events(id, run_id, run_step_id, type, message, created_at) values($1,$2,$3,$4,$5,$6)`, newID("evt"), id, stepID, "step.failed", "Worker lease expired", now); err != nil {
				_ = tx.Rollback()
				return 0, 0, err
			}
		}
		if _, err := tx.ExecContext(s.operationContext(), `insert into run_events(id, run_id, type, message, created_at) values($1,$2,$3,$4,$5)`, newID("evt"), id, "run.failed", "Worker lease expired", now); err != nil {
			_ = tx.Rollback()
			return 0, 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return len(requeuedIDs), len(failedIDs), nil
}

func (s *PostgresStore) RequeueFailedRun(id string, availableAt time.Time, summary string) (Run, error) {
	now := s.now().UTC()
	tx, err := s.db.BeginTx(s.operationContext(), nil)
	if err != nil {
		return Run{}, err
	}
	row := tx.QueryRowContext(s.operationContext(), `update runs set status=$2, available_at=$3, finished_at=null, summary=$4, claimed_at=null, claimed_by='', lease_expires_at=null where id=$1 and status in ($5,$6) and attempt_count < max_attempts returning id, workflow_id, coalesce(trigger_id,''), status, inputs, created_at, queued_at, available_at, started_at, finished_at, claimed_at, claimed_by, lease_expires_at, attempt_count, max_attempts, summary, pipeline_id, pipeline_version, pipeline_snapshot`, id, RunQueued, availableAt.UTC(), summary, RunFailed, RunTimedOut)
	run, err := scanRunRow(row)
	if err != nil {
		_ = tx.Rollback()
		return Run{}, mapScanErr(err)
	}
	if _, err := tx.ExecContext(s.operationContext(), `insert into run_events(id, run_id, type, message, created_at) values($1,$2,$3,$4,$5)`, newID("evt"), id, "run.retry.scheduled", summary, now); err != nil {
		_ = tx.Rollback()
		return Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, err
	}
	return run, nil
}

func (s *PostgresStore) FinishRun(id string, status RunStatus, summary string) (Run, error) {
	return s.finishRun(id, "", status, summary)
}

func (s *PostgresStore) FinishClaimedRun(id string, workerID string, status RunStatus, summary string) (Run, error) {
	if workerID == "" {
		return Run{}, ErrInvalid
	}
	return s.finishRun(id, workerID, status, summary)
}

func (s *PostgresStore) finishRun(id string, workerID string, status RunStatus, summary string) (Run, error) {
	now := s.now().UTC()
	tx, err := s.db.BeginTx(s.operationContext(), nil)
	if err != nil {
		return Run{}, err
	}
	query := `update runs set status=$2, finished_at=$3, summary=$4, claimed_at=null, claimed_by='', lease_expires_at=null where id=$1`
	args := []any{id, status, now, summary}
	if workerID != "" {
		query += ` and status=$5 and claimed_by=$6`
		args = append(args, RunRunning, workerID)
	}
	query += ` returning id, workflow_id, coalesce(trigger_id,''), status, inputs, created_at, queued_at, available_at, started_at, finished_at, claimed_at, claimed_by, lease_expires_at, attempt_count, max_attempts, summary, pipeline_id, pipeline_version, pipeline_snapshot`
	row := tx.QueryRowContext(s.operationContext(), query, args...)
	r, err := scanRunRow(row)
	if err != nil {
		_ = tx.Rollback()
		return Run{}, mapScanErr(err)
	}
	if stepStatus, terminal := terminalStepStatusForRun(status); terminal {
		stepRows, err := tx.QueryContext(s.operationContext(), `update run_steps set status=$2, error=$3, finished_at=$4 where run_id=$1 and status=$5 returning id`, id, stepStatus, summary, now, StepRunning)
		if err != nil {
			_ = tx.Rollback()
			return Run{}, err
		}
		stepIDs := []string{}
		for stepRows.Next() {
			var stepID string
			if err := stepRows.Scan(&stepID); err != nil {
				stepRows.Close()
				_ = tx.Rollback()
				return Run{}, err
			}
			stepIDs = append(stepIDs, stepID)
		}
		stepRows.Close()
		for _, stepID := range stepIDs {
			if _, err := tx.ExecContext(s.operationContext(), `insert into run_events(id, run_id, run_step_id, type, message, created_at) values($1,$2,$3,$4,$5,$6)`, newID("evt"), id, stepID, "step."+string(stepStatus), summary, now); err != nil {
				_ = tx.Rollback()
				return Run{}, err
			}
		}
	}
	if _, err := tx.ExecContext(s.operationContext(), `insert into run_events(id, run_id, type, message, created_at) values($1,$2,$3,$4,$5)`, newID("evt"), id, "run."+string(status), summary, now); err != nil {
		_ = tx.Rollback()
		return Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, err
	}
	return r, nil
}

func (s *PostgresStore) CreateRunStep(input CreateRunStepInput) (RunStep, error) {
	if input.RunID == "" || input.Position < 1 || input.Name == "" || input.Executor == "" {
		return RunStep{}, ErrInvalid
	}
	inputs, err := json.Marshal(input.Inputs)
	if err != nil {
		return RunStep{}, err
	}
	correlationIDs, err := json.Marshal(input.CorrelationIDs)
	if err != nil {
		return RunStep{}, err
	}
	now := s.now().UTC()
	step := RunStep{ID: newID("step"), RunID: input.RunID, Position: input.Position, Name: input.Name, Executor: input.Executor, Model: input.Model, Status: StepQueued, Inputs: cloneMap(input.Inputs), CorrelationIDs: cloneMap(input.CorrelationIDs), CreatedAt: now}
	tx, err := s.db.BeginTx(s.operationContext(), nil)
	if err != nil {
		return RunStep{}, err
	}
	if _, err := tx.ExecContext(s.operationContext(), `insert into run_steps(id, run_id, position, name, executor, model, status, inputs, correlation_ids, created_at) values($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9::jsonb,$10)`, step.ID, step.RunID, step.Position, step.Name, step.Executor, step.Model, step.Status, string(inputs), string(correlationIDs), step.CreatedAt); err != nil {
		_ = tx.Rollback()
		return RunStep{}, mapPgErr(err)
	}
	if _, err := tx.ExecContext(s.operationContext(), `insert into run_events(id, run_id, run_step_id, type, message, created_at) values($1,$2,$3,$4,$5,$6)`, newID("evt"), step.RunID, step.ID, "step.created", step.Name, now); err != nil {
		_ = tx.Rollback()
		return RunStep{}, err
	}
	if err := tx.Commit(); err != nil {
		return RunStep{}, err
	}
	return step, nil
}

func (s *PostgresStore) StartRunStep(id string) (RunStep, error) {
	now := s.now().UTC()
	tx, err := s.db.BeginTx(s.operationContext(), nil)
	if err != nil {
		return RunStep{}, err
	}
	row := tx.QueryRowContext(s.operationContext(), `update run_steps set status=$2, attempt_count=attempt_count+1, started_at=$3, finished_at=null, error='', output_summary='' where id=$1 and status=$4 returning id, run_id, position, name, executor, model, status, inputs, output_summary, error, attempt_count, correlation_ids, created_at, started_at, finished_at`, id, StepRunning, now, StepQueued)
	step, err := scanRunStepRow(row)
	if err != nil {
		_ = tx.Rollback()
		return RunStep{}, mapScanErr(err)
	}
	if _, err := tx.ExecContext(s.operationContext(), `insert into run_events(id, run_id, run_step_id, type, message, created_at) values($1,$2,$3,$4,$5,$6)`, newID("evt"), step.RunID, step.ID, "step.started", step.Name, now); err != nil {
		_ = tx.Rollback()
		return RunStep{}, err
	}
	if err := tx.Commit(); err != nil {
		return RunStep{}, err
	}
	return step, nil
}

func (s *PostgresStore) UpdateRunStep(id string, input UpdateRunStepInput) (RunStep, error) {
	inputs, err := json.Marshal(input.Inputs)
	if err != nil {
		return RunStep{}, err
	}
	row := s.db.QueryRowContext(s.operationContext(), `update run_steps set inputs=$2::jsonb where id=$1 returning id, run_id, position, name, executor, model, status, inputs, output_summary, error, attempt_count, correlation_ids, created_at, started_at, finished_at`, id, string(inputs))
	step, err := scanRunStepRow(row)
	if err != nil {
		return RunStep{}, mapScanErr(err)
	}
	return step, nil
}

func (s *PostgresStore) FinishRunStep(id string, status RunStepStatus, outputSummary string, errText string, correlationIDs map[string]string) (RunStep, error) {
	if !isTerminalStepStatus(status) {
		return RunStep{}, ErrInvalid
	}
	encodedCorrelationIDs, err := json.Marshal(mergeStringMaps(nil, correlationIDs))
	if err != nil {
		return RunStep{}, err
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(s.operationContext(), nil)
	if err != nil {
		return RunStep{}, err
	}
	row := tx.QueryRowContext(s.operationContext(), `update run_steps set status=$2, output_summary=$3, error=$4, correlation_ids=correlation_ids || $5::jsonb, finished_at=$6 where id=$1 and status in ($7,$8) returning id, run_id, position, name, executor, model, status, inputs, output_summary, error, attempt_count, correlation_ids, created_at, started_at, finished_at`, id, status, outputSummary, errText, string(encodedCorrelationIDs), now, StepRunning, StepQueued)
	step, err := scanRunStepRow(row)
	if err != nil {
		_ = tx.Rollback()
		return RunStep{}, mapScanErr(err)
	}
	message := outputSummary
	if message == "" {
		message = errText
	}
	if message == "" {
		message = step.Name
	}
	if _, err := tx.ExecContext(s.operationContext(), `insert into run_events(id, run_id, run_step_id, type, message, created_at) values($1,$2,$3,$4,$5,$6)`, newID("evt"), step.RunID, step.ID, "step."+string(status), message, now); err != nil {
		_ = tx.Rollback()
		return RunStep{}, err
	}
	if err := tx.Commit(); err != nil {
		return RunStep{}, err
	}
	return step, nil
}

func (s *PostgresStore) MergeRunStepCorrelationIDs(id string, correlationIDs map[string]string) (RunStep, error) {
	encoded, err := json.Marshal(mergeStringMaps(nil, correlationIDs))
	if err != nil {
		return RunStep{}, err
	}
	step, err := scanRunStepRow(s.db.QueryRowContext(s.operationContext(), `update run_steps set correlation_ids=correlation_ids || $2::jsonb where id=$1 returning id, run_id, position, name, executor, model, status, inputs, output_summary, error, attempt_count, correlation_ids, created_at, started_at, finished_at`, id, string(encoded)))
	return step, mapScanErr(err)
}

func (s *PostgresStore) RecordRunStepBackendDecision(runID, stepID string, decision BackendDecision) error {
	encoded, err := json.Marshal(decision.CorrelationIDs())
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(s.operationContext(), nil)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(s.operationContext(), `update run_steps set correlation_ids=correlation_ids || $3::jsonb where id=$1 and run_id=$2`, stepID, runID, string(encoded))
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		_ = tx.Rollback()
		if err != nil {
			return err
		}
		return ErrNotFound
	}
	if decision.DeprecatedBackend != "" {
		if _, err := tx.ExecContext(s.operationContext(), `insert into run_events(id, run_id, run_step_id, type, message, created_at) values($1,$2,$3,$4,$5,$6)`, newID("evt"), runID, stepID, DeprecatedBackendAttemptedEvent, decision.DeprecatedBackend, s.now().UTC()); err != nil {
			_ = tx.Rollback()
			return err
		}
		if _, err := tx.ExecContext(s.operationContext(), `insert into compatibility_usage_daily(usage_date, backend, attempts, first_attempt_at, last_attempt_at) values($1,$2,1,$3,$3) on conflict(usage_date, backend) do update set attempts=compatibility_usage_daily.attempts+1, first_attempt_at=least(compatibility_usage_daily.first_attempt_at, excluded.first_attempt_at), last_attempt_at=greatest(compatibility_usage_daily.last_attempt_at, excluded.last_attempt_at)`, s.now().UTC().Format("2006-01-02"), decision.DeprecatedBackend, s.now().UTC()); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *PostgresStore) RetryRunStep(id string) (RunStep, error) {
	return s.transitionRunStep(id, []RunStepStatus{StepFailed, StepCanceled, StepTimedOut, StepPaused}, StepQueued, "step.retry")
}

func (s *PostgresStore) RecoverRunStep(id string) (RunStep, error) {
	return s.transitionRunStep(id, []RunStepStatus{StepRunning}, StepQueued, "step.recovered")
}

func (s *PostgresStore) CancelRunStep(id string) (RunStep, error) {
	return s.transitionRunStep(id, []RunStepStatus{StepQueued, StepRunning}, StepCanceled, "step.canceled")
}

func (s *PostgresStore) transitionRunStep(id string, allowed []RunStepStatus, next RunStepStatus, eventType string) (RunStep, error) {
	now := s.now().UTC()
	allowedStrings := make([]string, len(allowed))
	for i, status := range allowed {
		allowedStrings[i] = string(status)
	}
	finished := any(nil)
	errorText := ""
	if next == StepCanceled {
		finished = now
		errorText = "Canceled by operator"
	}
	tx, err := s.db.BeginTx(s.operationContext(), nil)
	if err != nil {
		return RunStep{}, err
	}
	row := tx.QueryRowContext(s.operationContext(), `update run_steps set status=$2, started_at=case when $2=$6 then null else started_at end, finished_at=$3, error=$4, output_summary=case when $2=$6 then '' else output_summary end where id=$1 and status = any($5) returning id, run_id, position, name, executor, model, status, inputs, output_summary, error, attempt_count, correlation_ids, created_at, started_at, finished_at`, id, next, finished, errorText, allowedStrings, StepQueued)
	step, err := scanRunStepRow(row)
	if err != nil {
		_ = tx.Rollback()
		return RunStep{}, mapScanErr(err)
	}
	if _, err := tx.ExecContext(s.operationContext(), `insert into run_events(id, run_id, run_step_id, type, message, created_at) values($1,$2,$3,$4,$5,$6)`, newID("evt"), step.RunID, step.ID, eventType, step.Name, now); err != nil {
		_ = tx.Rollback()
		return RunStep{}, err
	}
	if err := tx.Commit(); err != nil {
		return RunStep{}, err
	}
	return step, nil
}

func (s *PostgresStore) ListRunSteps(runID string) []RunStep {
	rows, err := s.db.QueryContext(s.operationContext(), `select id, run_id, position, name, executor, model, status, inputs, output_summary, error, attempt_count, correlation_ids, created_at, started_at, finished_at from run_steps where run_id=$1 order by position`, runID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []RunStep{}
	for rows.Next() {
		if step, ok := scanRunStep(rows); ok {
			out = append(out, step)
		}
	}
	return out
}

func (s *PostgresStore) RunStep(id string) (RunStep, error) {
	step, err := scanRunStepRow(s.db.QueryRowContext(s.operationContext(), `select id, run_id, position, name, executor, model, status, inputs, output_summary, error, attempt_count, correlation_ids, created_at, started_at, finished_at from run_steps where id=$1`, id))
	return step, mapScanErr(err)
}

func (s *PostgresStore) AddEvent(runID, typ, message string) (RunEvent, error) {
	now := s.now().UTC()
	e := RunEvent{ID: newID("evt"), RunID: runID, Type: typ, Message: message, CreatedAt: now}
	_, err := s.db.ExecContext(s.operationContext(), `insert into run_events(id, run_id, type, message, created_at) values($1,$2,$3,$4,$5)`, e.ID, e.RunID, e.Type, e.Message, e.CreatedAt)
	return e, mapPgErr(err)
}

func (s *PostgresStore) AddArtifact(input CreateArtifactInput) (Artifact, error) {
	if err := validateArtifactInput(input); err != nil {
		return Artifact{}, ErrInvalid
	}
	now := s.now().UTC()
	metadata, err := artifactMetadata(input.URI, now)
	if err != nil {
		return Artifact{}, ErrInvalid
	}
	artifact := Artifact{ID: newID("art"), RunID: input.RunID, Name: input.Name, Kind: input.Kind, URI: input.URI, StorageClass: metadata.StorageClass, ContentType: metadata.ContentType, ByteSize: metadata.ByteSize, SHA256: metadata.SHA256, RetentionUntil: metadata.RetentionUntil, CreatedAt: now}
	tx, err := s.db.BeginTx(s.operationContext(), nil)
	if err != nil {
		return Artifact{}, err
	}
	if _, err := tx.ExecContext(s.operationContext(), `insert into artifacts(id, run_id, name, kind, uri, storage_class, content_type, byte_size, sha256, retention_until, created_at) values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, artifact.ID, artifact.RunID, artifact.Name, artifact.Kind, artifact.URI, artifact.StorageClass, artifact.ContentType, artifact.ByteSize, artifact.SHA256, artifact.RetentionUntil, artifact.CreatedAt); err != nil {
		_ = tx.Rollback()
		return Artifact{}, mapPgErr(err)
	}
	if _, err := tx.ExecContext(s.operationContext(), `update artifact_lifecycle_totals set created_count=created_count+1, created_bytes=created_bytes+$1 where singleton=true`, artifact.ByteSize); err != nil {
		_ = tx.Rollback()
		return Artifact{}, err
	}
	if _, err := tx.ExecContext(s.operationContext(), `insert into artifact_lifecycle_daily(measurement_date, created_count, created_bytes) values(($1::timestamptz at time zone 'UTC')::date,1,$2) on conflict (measurement_date) do update set created_count=artifact_lifecycle_daily.created_count+1, created_bytes=artifact_lifecycle_daily.created_bytes+excluded.created_bytes`, now, artifact.ByteSize); err != nil {
		_ = tx.Rollback()
		return Artifact{}, err
	}
	if _, err := tx.ExecContext(s.operationContext(), `insert into run_events(id, run_id, type, message, created_at) values($1,$2,$3,$4,$5)`, newID("evt"), artifact.RunID, "artifact.created", artifact.Name+" ("+artifact.Kind+")", now); err != nil {
		_ = tx.Rollback()
		return Artifact{}, err
	}
	if err := tx.Commit(); err != nil {
		return Artifact{}, err
	}
	return artifact, nil
}

func (s *PostgresStore) ListRuns() []Run {
	rows, err := s.db.QueryContext(s.operationContext(), `select id, workflow_id, coalesce(trigger_id,''), status, inputs, created_at, queued_at, available_at, started_at, finished_at, claimed_at, claimed_by, lease_expires_at, attempt_count, max_attempts, summary, pipeline_id, pipeline_version, pipeline_snapshot from runs order by created_at desc, id desc`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		if r, ok := scanRun(rows); ok {
			out = append(out, r)
		}
	}
	return out
}

func (s *PostgresStore) ListRunsPage(input ListRunsPageInput) (RunPage, error) {
	return s.ListRunsPageContext(s.operationContext(), input)
}

func (s *PostgresStore) ListRunsPageContext(ctx context.Context, input ListRunsPageInput) (RunPage, error) {
	if input.Limit < 1 || input.Limit > 100 {
		return RunPage{}, ErrInvalid
	}
	query := `select id, workflow_id, coalesce(trigger_id,''), status, inputs, created_at, queued_at, available_at, started_at, finished_at, claimed_at, claimed_by, lease_expires_at, attempt_count, max_attempts, summary, pipeline_id, pipeline_version, pipeline_snapshot from runs`
	args := []any{}
	if input.BeforeCreated != nil {
		query += ` where (created_at, id) < ($1, $2)`
		args = append(args, input.BeforeCreated.UTC(), input.BeforeID)
	}
	args = append(args, input.Limit+1)
	query += fmt.Sprintf(` order by created_at desc, id desc limit $%d`, len(args))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return RunPage{}, err
	}
	defer rows.Close()
	page := RunPage{Runs: []Run{}}
	for rows.Next() {
		run, err := scanRunRow(rows)
		if err != nil {
			return RunPage{}, err
		}
		page.Runs = append(page.Runs, run)
	}
	if err := rows.Err(); err != nil {
		return RunPage{}, err
	}
	if len(page.Runs) > input.Limit {
		page.HasMore = true
		page.Runs = page.Runs[:input.Limit]
	}
	return page, nil
}

func (s *PostgresStore) RunDetail(id string) (RunDetail, error) {
	return s.RunDetailContext(s.operationContext(), id)
}

func (s *PostgresStore) RunDetailContext(ctx context.Context, id string) (RunDetail, error) {
	r, err := scanRunRow(s.db.QueryRowContext(ctx, `select id, workflow_id, coalesce(trigger_id,''), status, inputs, created_at, queued_at, available_at, started_at, finished_at, claimed_at, claimed_by, lease_expires_at, attempt_count, max_attempts, summary, pipeline_id, pipeline_version, pipeline_snapshot from runs where id=$1`, id))
	if err != nil {
		return RunDetail{}, mapScanErr(err)
	}
	detail := RunDetail{Run: r, Steps: []RunStep{}, Events: []RunEvent{}, Artifacts: []Artifact{}}
	rows, err := s.db.QueryContext(ctx, `select id, run_id, position, name, executor, model, status, inputs, output_summary, error, attempt_count, correlation_ids, created_at, started_at, finished_at from run_steps where run_id=$1 order by position`, id)
	if err != nil {
		return RunDetail{}, err
	}
	for rows.Next() {
		step, err := scanRunStepRow(rows)
		if err != nil {
			rows.Close()
			return RunDetail{}, err
		}
		detail.Steps = append(detail.Steps, step)
	}
	rows.Close()
	rows, err = s.db.QueryContext(ctx, `select id, run_id, type, message, created_at from run_events where run_id=$1 order by created_at, id`, id)
	if err != nil {
		return RunDetail{}, err
	}
	for rows.Next() {
		var e RunEvent
		if err := rows.Scan(&e.ID, &e.RunID, &e.Type, &e.Message, &e.CreatedAt); err == nil {
			detail.Events = append(detail.Events, e)
		}
	}
	rows.Close()
	rows, err = s.db.QueryContext(ctx, `select id, run_id, name, kind, uri, storage_class, content_type, byte_size, sha256, retention_until, created_at from artifacts where run_id=$1 order by created_at, id`, id)
	if err != nil {
		return RunDetail{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var a Artifact
		var retention sql.NullTime
		if err := rows.Scan(&a.ID, &a.RunID, &a.Name, &a.Kind, &a.URI, &a.StorageClass, &a.ContentType, &a.ByteSize, &a.SHA256, &retention, &a.CreatedAt); err == nil {
			if retention.Valid {
				value := retention.Time.UTC()
				a.RetentionUntil = &value
			}
			detail.Artifacts = append(detail.Artifacts, a)
		}
	}
	return detail, nil
}

func scanRun(rows *sql.Rows) (Run, bool) {
	r, err := scanRunRow(rows)
	return r, err == nil
}

func scanRunRow(row scanner) (Run, error) {
	var r Run
	var inputs, pipelineSnapshot []byte
	var started, finished, claimed, lease sql.NullTime
	if err := row.Scan(&r.ID, &r.WorkflowID, &r.TriggerID, &r.Status, &inputs, &r.CreatedAt, &r.QueuedAt, &r.AvailableAt, &started, &finished, &claimed, &r.ClaimedBy, &lease, &r.AttemptCount, &r.MaxAttempts, &r.Summary, &r.PipelineID, &r.PipelineVersion, &pipelineSnapshot); err != nil {
		return Run{}, err
	}
	if started.Valid {
		r.StartedAt = &started.Time
	}
	if finished.Valid {
		r.FinishedAt = &finished.Time
	}
	if claimed.Valid {
		r.ClaimedAt = &claimed.Time
	}
	if lease.Valid {
		r.LeaseExpiresAt = &lease.Time
	}
	_ = json.Unmarshal(inputs, &r.Inputs)
	if len(pipelineSnapshot) > 0 {
		var snapshot Pipeline
		if err := json.Unmarshal(pipelineSnapshot, &snapshot); err != nil {
			return Run{}, fmt.Errorf("decode pipeline snapshot for run %s: %w", r.ID, err)
		}
		r.PipelineSnapshot = &snapshot
	}
	return r, nil
}

func scanRunStep(rows *sql.Rows) (RunStep, bool) {
	step, err := scanRunStepRow(rows)
	return step, err == nil
}

func scanRunStepRow(row scanner) (RunStep, error) {
	var step RunStep
	var inputs, correlationIDs []byte
	var started, finished sql.NullTime
	if err := row.Scan(&step.ID, &step.RunID, &step.Position, &step.Name, &step.Executor, &step.Model, &step.Status, &inputs, &step.OutputSummary, &step.Error, &step.AttemptCount, &correlationIDs, &step.CreatedAt, &started, &finished); err != nil {
		return RunStep{}, err
	}
	if started.Valid {
		step.StartedAt = &started.Time
	}
	if finished.Valid {
		step.FinishedAt = &finished.Time
	}
	_ = json.Unmarshal(inputs, &step.Inputs)
	_ = json.Unmarshal(correlationIDs, &step.CorrelationIDs)
	return step, nil
}
