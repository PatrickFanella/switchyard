package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const postgresRunColumns = `id, workflow_id, coalesce(trigger_id,''), status, inputs, created_at, queued_at, available_at, started_at, finished_at, claimed_at, claimed_by, lease_expires_at, attempt_count, max_attempts, summary, pipeline_id, pipeline_version, pipeline_snapshot`

func (s *PostgresStore) HandleOpenPilotCIFailure(request OpenPilotCIRepairRequest) (OpenPilotCIRepairResult, error) {
	if err := validateOpenPilotCIRepairRequest(request); err != nil {
		return OpenPilotCIRepairResult{}, err
	}
	artifactInput, err := openPilotCIRepairArtifactInput(request)
	if err != nil {
		return OpenPilotCIRepairResult{}, err
	}
	ctx := s.operationContext()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OpenPilotCIRepairResult{}, err
	}
	rollback := func(err error) (OpenPilotCIRepairResult, error) {
		_ = tx.Rollback()
		return OpenPilotCIRepairResult{}, err
	}
	state, err := scanOpenPilotIssueStateRow(tx.QueryRowContext(ctx, `select `+openPilotIssueStateColumns+` from open_pilot_issue_states where repo=$1 and issue=$2 for update`, request.Repo, request.Issue))
	if err != nil {
		return rollback(mapScanErr(err))
	}
	if state.RunID != request.ParentRunID {
		child, err := scanRunRow(tx.QueryRowContext(ctx, `select `+postgresRunColumns+` from runs where id=$1`, state.RunID))
		if err != nil {
			return rollback(mapScanErr(err))
		}
		if child.Inputs[openPilotCIRepairParentRunIDKey] != request.ParentRunID {
			return rollback(ErrConflict)
		}
		parent, err := scanRunRow(tx.QueryRowContext(ctx, `select `+postgresRunColumns+` from runs where id=$1`, request.ParentRunID))
		if err != nil {
			return rollback(mapScanErr(err))
		}
		attempt, err := openPilotCIRepairCount(child.Inputs)
		if err != nil {
			return rollback(err)
		}
		if err := tx.Commit(); err != nil {
			return OpenPilotCIRepairResult{}, err
		}
		childCopy := cloneRunForOpenPilotRepair(child)
		return OpenPilotCIRepairResult{State: state, ParentRun: parent, ChildRun: &childCopy, Attempt: attempt, Queued: true, Replayed: true}, nil
	}
	parent, err := scanRunRow(tx.QueryRowContext(ctx, `select `+postgresRunColumns+` from runs where id=$1 for update`, request.ParentRunID))
	if err != nil {
		return rollback(mapScanErr(err))
	}
	if replay, ok := exhaustedOpenPilotCIRepairReplay(state, parent, request); ok {
		if err := tx.Commit(); err != nil {
			return OpenPilotCIRepairResult{}, err
		}
		return replay, nil
	}
	if parent.Status != RunRunning || parent.ClaimedBy != request.ExpectedWorkerID || parent.AttemptCount != request.ExpectedAttemptCount {
		return rollback(ErrConflict)
	}
	var databaseNow time.Time
	if err := tx.QueryRowContext(ctx, `select clock_timestamp()`).Scan(&databaseNow); err != nil {
		return rollback(err)
	}
	if parent.LeaseExpiresAt == nil || !parent.LeaseExpiresAt.After(databaseNow) {
		return rollback(ErrConflict)
	}
	count, err := openPilotCIRepairCount(state.ResumeInputs)
	if err != nil {
		return rollback(err)
	}
	now := s.now().UTC()
	queued := count < openPilotCIRepairMaxAttempts
	var child Run
	var childInputs map[string]string
	if queued {
		child, childInputs, err = buildOpenPilotCIRepairChild(state, parent, request, count+1, now)
		if err != nil {
			return rollback(err)
		}
	}
	summary := openPilotCIRepairSummary(count+1, queued)
	parent, err = scanRunRow(tx.QueryRowContext(ctx, `update runs set status=$2, finished_at=$3, claimed_at=null, claimed_by='', lease_expires_at=null, max_attempts=attempt_count, summary=$4 where id=$1 returning `+postgresRunColumns, parent.ID, RunFailed, now, summary))
	if err != nil {
		return rollback(mapScanErr(err))
	}
	stepRows, err := tx.QueryContext(ctx, `update run_steps set status=$2, error=$3, finished_at=$4 where run_id=$1 and status=$5 returning id`, parent.ID, StepFailed, summary, now, StepRunning)
	if err != nil {
		return rollback(err)
	}
	stepIDs := []string{}
	for stepRows.Next() {
		var id string
		if err := stepRows.Scan(&id); err != nil {
			stepRows.Close()
			return rollback(err)
		}
		stepIDs = append(stepIDs, id)
	}
	if err := stepRows.Close(); err != nil {
		return rollback(err)
	}
	for _, stepID := range stepIDs {
		if _, err := tx.ExecContext(ctx, `insert into run_events(id,run_id,run_step_id,type,message,created_at) values($1,$2,$3,$4,$5,$6)`, newID("evt"), parent.ID, stepID, "step.failed", summary, now); err != nil {
			return rollback(err)
		}
	}
	for _, eventType := range []string{"open-pilot.ci-repair.handled", "run.failed"} {
		if _, err := tx.ExecContext(ctx, `insert into run_events(id,run_id,type,message,created_at) values($1,$2,$3,$4,$5)`, newID("evt"), parent.ID, eventType, summary, now); err != nil {
			return rollback(err)
		}
	}
	if err := insertPostgresOpenPilotCIRepairArtifact(ctx, tx, artifactInput, now); err != nil {
		return rollback(err)
	}
	if !queued {
		setOpenPilotCIRepairDiagnostics(&state, parent.ID, request.Feedback, count, false, now)
		state.ResumeInputs[openPilotCIRepairExhaustedRunIDKey] = parent.ID
		state, err = updatePostgresOpenPilotCIRepairState(ctx, tx, state, nil)
		if err != nil {
			return rollback(err)
		}
		if err := tx.Commit(); err != nil {
			return OpenPilotCIRepairResult{}, err
		}
		return OpenPilotCIRepairResult{State: state, ParentRun: parent, Attempt: count, Exhausted: true}, nil
	}
	encodedInputs, err := json.Marshal(childInputs)
	if err != nil {
		return rollback(err)
	}
	encodedSnapshot, err := json.Marshal(child.PipelineSnapshot)
	if err != nil {
		return rollback(err)
	}
	triggerID := sql.NullString{String: child.TriggerID, Valid: child.TriggerID != ""}
	if _, err := tx.ExecContext(ctx, `insert into runs(id,workflow_id,trigger_id,status,inputs,created_at,queued_at,available_at,attempt_count,max_attempts,summary,pipeline_id,pipeline_version,pipeline_snapshot) values($1,$2,$3,$4,$5::jsonb,$6,$6,$6,0,$7,'',$8,$9,$10::jsonb)`, child.ID, child.WorkflowID, triggerID, child.Status, string(encodedInputs), now, child.MaxAttempts, child.PipelineID, child.PipelineVersion, string(encodedSnapshot)); err != nil {
		return rollback(mapPgErr(err))
	}
	if _, err := tx.ExecContext(ctx, `insert into run_events(id,run_id,type,message,created_at) values($1,$2,$3,$4,$5)`, newID("evt"), child.ID, "run.created", summary, now); err != nil {
		return rollback(err)
	}
	setOpenPilotCIRepairDiagnostics(&state, child.ID, request.Feedback, count+1, true, now)
	state.ResumeInputs = cloneMap(childInputs)
	state, err = updatePostgresOpenPilotCIRepairState(ctx, tx, state, childInputs)
	if err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return OpenPilotCIRepairResult{}, err
	}
	childCopy := cloneRunForOpenPilotRepair(child)
	return OpenPilotCIRepairResult{State: state, ParentRun: parent, ChildRun: &childCopy, Attempt: count + 1, Queued: true}, nil
}

func insertPostgresOpenPilotCIRepairArtifact(ctx context.Context, tx *sql.Tx, input CreateArtifactInput, now time.Time) error {
	metadata, err := artifactMetadata(input.URI, now)
	if err != nil {
		return err
	}
	artifact := Artifact{ID: newID("art"), RunID: input.RunID, Name: input.Name, Kind: input.Kind, URI: input.URI, StorageClass: metadata.StorageClass, ContentType: metadata.ContentType, ByteSize: metadata.ByteSize, SHA256: metadata.SHA256, RetentionUntil: metadata.RetentionUntil, CreatedAt: now}
	if err := enableArtifactTriggerLifecycleAccounting(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `insert into artifacts(id,run_id,name,kind,uri,storage_class,content_type,byte_size,sha256,retention_until,created_at) values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, artifact.ID, artifact.RunID, artifact.Name, artifact.Kind, artifact.URI, artifact.StorageClass, artifact.ContentType, artifact.ByteSize, artifact.SHA256, artifact.RetentionUntil, artifact.CreatedAt); err != nil {
		return mapPgErr(err)
	}
	_, err = tx.ExecContext(ctx, `insert into run_events(id,run_id,type,message,created_at) values($1,$2,$3,$4,$5)`, newID("evt"), artifact.RunID, "artifact.created", artifact.Name+" ("+artifact.Kind+")", now)
	return err
}

func (s *PostgresStore) ReconcileOpenPilotCIRepairTerminal(runID string) (bool, error) {
	ctx := s.operationContext()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	rollback := func(err error) (bool, error) { _ = tx.Rollback(); return false, err }
	state, err := scanOpenPilotIssueStateRow(tx.QueryRowContext(ctx, `select `+openPilotIssueStateColumns+` from open_pilot_issue_states where run_id=$1 and phase in ('requested','validated','branch_prepared','agent_completed','verified','committed','pushed','pr_created','ai_reviewed','ci_passed','merge_ready') for update`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	if err != nil {
		return rollback(err)
	}
	run, err := scanRunRow(tx.QueryRowContext(ctx, `select `+postgresRunColumns+` from runs where id=$1 for update`, runID))
	if err != nil {
		return rollback(mapScanErr(err))
	}
	if run.Inputs[openPilotCIRepairParentRunIDKey] == "" || (run.Status != RunFailed && run.Status != RunTimedOut && run.Status != RunCanceled) {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	setOpenPilotCIRepairTerminalDiagnostic(&state, run.Summary, s.now().UTC())
	if _, err := updatePostgresOpenPilotCIRepairState(ctx, tx, state, nil); err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *PostgresStore) ReconcileOpenPilotMergeTerminal(runID string) (bool, error) {
	ctx := s.operationContext()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	rollback := func(err error) (bool, error) { _ = tx.Rollback(); return false, err }
	state, err := scanOpenPilotIssueStateRow(tx.QueryRowContext(ctx, `select `+openPilotIssueStateColumns+` from open_pilot_issue_states where run_id=$1 and phase='merge_ready' for update`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	if err != nil {
		return rollback(err)
	}
	run, err := scanRunRow(tx.QueryRowContext(ctx, `select `+postgresRunColumns+` from runs where id=$1 for update`, runID))
	if err != nil {
		return rollback(mapScanErr(err))
	}
	if run.WorkflowID != string(WorkflowOpenPilot) || (run.Status != RunFailed && run.Status != RunTimedOut) || run.AttemptCount < run.MaxAttempts {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	setOpenPilotMergeTerminalDiagnostic(&state, run.Summary, s.now().UTC())
	if _, err := updatePostgresOpenPilotCIRepairState(ctx, tx, state, nil); err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *PostgresStore) ReconcileOpenPilotMergeTerminals(limit int) (int, error) {
	if limit < 1 || limit > 100 {
		return 0, ErrInvalid
	}
	ctx := s.operationContext()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	rollback := func(err error) (int, error) { _ = tx.Rollback(); return 0, err }
	rows, err := tx.QueryContext(ctx, `select `+openPilotIssueStateColumns+` from open_pilot_issue_states s where phase='merge_ready' and exists (select 1 from runs r where r.id=s.run_id and r.workflow_id=$1 and r.status in ($2,$3) and r.attempt_count >= r.max_attempts) order by updated_at,id limit $4 for update`, WorkflowOpenPilot, RunFailed, RunTimedOut, limit)
	if err != nil {
		return rollback(err)
	}
	states := make([]OpenPilotIssueState, 0, limit)
	for rows.Next() {
		state, err := scanOpenPilotIssueStateRow(rows)
		if err != nil {
			rows.Close()
			return rollback(err)
		}
		states = append(states, state)
	}
	if err := rows.Close(); err != nil {
		return rollback(err)
	}
	for _, state := range states {
		run, err := scanRunRow(tx.QueryRowContext(ctx, `select `+postgresRunColumns+` from runs where id=$1 for update`, state.RunID))
		if err != nil {
			return rollback(mapScanErr(err))
		}
		setOpenPilotMergeTerminalDiagnostic(&state, run.Summary, s.now().UTC())
		if _, err := updatePostgresOpenPilotCIRepairState(ctx, tx, state, nil); err != nil {
			return rollback(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(states), nil
}

func (s *PostgresStore) ReconcileOpenPilotCIRepairTerminals(limit int) (int, error) {
	if limit < 1 || limit > 100 {
		return 0, ErrInvalid
	}
	ctx := s.operationContext()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	rollback := func(err error) (int, error) { _ = tx.Rollback(); return 0, err }
	rows, err := tx.QueryContext(ctx, `select `+openPilotIssueStateColumns+` from open_pilot_issue_states s where phase in ('requested','validated','branch_prepared','agent_completed','verified','committed','pushed','pr_created','ai_reviewed','ci_passed','merge_ready') and exists (select 1 from runs r where r.id=s.run_id and r.status in ($1,$2,$3) and coalesce(r.inputs->>$4,'') <> '') order by updated_at,id limit $5 for update`, RunFailed, RunTimedOut, RunCanceled, openPilotCIRepairParentRunIDKey, limit)
	if err != nil {
		return rollback(err)
	}
	states := make([]OpenPilotIssueState, 0, limit)
	for rows.Next() {
		state, err := scanOpenPilotIssueStateRow(rows)
		if err != nil {
			rows.Close()
			return rollback(err)
		}
		states = append(states, state)
	}
	if err := rows.Close(); err != nil {
		return rollback(err)
	}
	reconciled := 0
	for _, state := range states {
		run, err := scanRunRow(tx.QueryRowContext(ctx, `select `+postgresRunColumns+` from runs where id=$1 for update`, state.RunID))
		if err != nil {
			return rollback(mapScanErr(err))
		}
		if run.Inputs[openPilotCIRepairParentRunIDKey] == "" || (run.Status != RunFailed && run.Status != RunTimedOut && run.Status != RunCanceled) {
			continue
		}
		setOpenPilotCIRepairTerminalDiagnostic(&state, run.Summary, s.now().UTC())
		if _, err := updatePostgresOpenPilotCIRepairState(ctx, tx, state, nil); err != nil {
			return rollback(err)
		}
		reconciled++
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return reconciled, nil
}

func updatePostgresOpenPilotCIRepairState(ctx context.Context, tx *sql.Tx, state OpenPilotIssueState, resumeInputs map[string]string) (OpenPilotIssueState, error) {
	if resumeInputs == nil {
		resumeInputs = state.ResumeInputs
	}
	resumeJSON, err := marshalOpenPilotJSONMap(resumeInputs)
	if err != nil {
		return OpenPilotIssueState{}, err
	}
	gateJSON, err := marshalOpenPilotJSONMap(map[string]string(state.GateStatus))
	if err != nil {
		return OpenPilotIssueState{}, err
	}
	row := tx.QueryRowContext(ctx, `update open_pilot_issue_states set phase=$3,run_id=$4,last_step_name=$5,resume_inputs=$6::jsonb,gate_status=$7::jsonb,last_error=$8,failing_gate=$9,operator_summary=$10,recommended_next_action=$11,updated_at=$12 where repo=$1 and issue=$2 returning `+openPilotIssueStateColumns, state.Repo, state.Issue, state.Phase, state.RunID, state.LastStepName, string(resumeJSON), string(gateJSON), state.LastError, nullString(state.FailingGate), nullString(state.OperatorSummary), nullString(state.RecommendedNextAction), state.UpdatedAt)
	saved, err := scanOpenPilotIssueStateRow(row)
	return saved, mapScanErr(err)
}

func (s *PostgresStore) OpenPilotIssueState(repo, issue string) (OpenPilotIssueState, error) {
	row := s.db.QueryRowContext(s.operationContext(), `select `+openPilotIssueStateColumns+` from open_pilot_issue_states where repo=$1 and issue=$2`, repo, issue)
	state, err := scanOpenPilotIssueStateRow(row)
	return state, mapScanErr(err)
}

func (s *PostgresStore) UpsertOpenPilotIssueState(input OpenPilotIssueState) (OpenPilotIssueState, error) {
	now := s.now().UTC()
	tx, err := s.db.BeginTx(s.operationContext(), nil)
	if err != nil {
		return OpenPilotIssueState{}, err
	}
	row := tx.QueryRowContext(s.operationContext(), `select `+openPilotIssueStateColumns+` from open_pilot_issue_states where repo=$1 and issue=$2 for update`, input.Repo, input.Issue)
	existing, err := scanOpenPilotIssueStateRow(row)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return OpenPilotIssueState{}, err
	}
	var existingPtr *OpenPilotIssueState
	if err == nil {
		existingPtr = &existing
	}
	state, err := validateOpenPilotIssueStateForUpsert(input, existingPtr)
	if err != nil {
		_ = tx.Rollback()
		return OpenPilotIssueState{}, err
	}
	if existingPtr == nil {
		if state.ID == "" {
			state.ID = newID("opi")
		}
		if state.CreatedAt.IsZero() {
			state.CreatedAt = now
		}
	} else {
		state.ID = existing.ID
		state.CreatedAt = existing.CreatedAt
	}
	state.UpdatedAt = now
	gateStatus, err := marshalOpenPilotJSONMap(map[string]string(state.GateStatus))
	if err != nil {
		_ = tx.Rollback()
		return OpenPilotIssueState{}, err
	}
	resumeInputs, err := marshalOpenPilotJSONMap(state.ResumeInputs)
	if err != nil {
		_ = tx.Rollback()
		return OpenPilotIssueState{}, err
	}
	if existingPtr == nil {
		row = tx.QueryRowContext(s.operationContext(), `
			insert into open_pilot_issue_states(id, repo, issue, phase, run_id, last_step_name, branch, base, worktree_path, pull_request_url, pull_request_id, pull_request_number, merge_commit_sha, issue_closed, gate_status, resume_inputs, repair_attempts, max_repair_attempts, last_error, failing_gate, operator_summary, recommended_next_action, created_at, updated_at)
			values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15::jsonb,$16::jsonb,$17,$18,$19,$20,$21,$22,$23,$24)
			returning `+openPilotIssueStateColumns+``,
			state.ID, state.Repo, state.Issue, state.Phase, state.RunID, state.LastStepName, state.Branch, state.Base, state.WorktreePath, state.PullRequestURL, nullInt64(state.PullRequestID), nullInt64(state.PullRequestNum), state.MergeCommitSHA, state.IssueClosed, string(gateStatus), string(resumeInputs), state.RepairAttempts, state.MaxRepairAttempts, state.LastError, nullString(state.FailingGate), nullString(state.OperatorSummary), nullString(state.RecommendedNextAction), state.CreatedAt, state.UpdatedAt)
	} else {
		row = tx.QueryRowContext(s.operationContext(), `
			update open_pilot_issue_states set phase=$3, run_id=$4, last_step_name=$5, branch=$6, base=$7, worktree_path=$8, pull_request_url=$9, pull_request_id=$10, pull_request_number=$11, merge_commit_sha=$12, issue_closed=$13, gate_status=$14::jsonb, resume_inputs=$15::jsonb, repair_attempts=$16, max_repair_attempts=$17, last_error=$18, failing_gate=$19, operator_summary=$20, recommended_next_action=$21, updated_at=$22
			where repo=$1 and issue=$2
			returning `+openPilotIssueStateColumns+``,
			state.Repo, state.Issue, state.Phase, state.RunID, state.LastStepName, state.Branch, state.Base, state.WorktreePath, state.PullRequestURL, nullInt64(state.PullRequestID), nullInt64(state.PullRequestNum), state.MergeCommitSHA, state.IssueClosed, string(gateStatus), string(resumeInputs), state.RepairAttempts, state.MaxRepairAttempts, state.LastError, nullString(state.FailingGate), nullString(state.OperatorSummary), nullString(state.RecommendedNextAction), state.UpdatedAt)
	}
	saved, err := scanOpenPilotIssueStateRow(row)
	if err != nil {
		_ = tx.Rollback()
		return OpenPilotIssueState{}, mapScanErr(err)
	}
	if err := tx.Commit(); err != nil {
		return OpenPilotIssueState{}, err
	}
	return saved, nil
}

func (s *PostgresStore) TransitionOpenPilotIssueState(repo, issue string, next OpenPilotPhase) (OpenPilotIssueState, error) {
	if repo == "" || issue == "" || next == "" {
		return OpenPilotIssueState{}, ErrInvalid
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(s.operationContext(), nil)
	if err != nil {
		return OpenPilotIssueState{}, err
	}
	row := tx.QueryRowContext(s.operationContext(), `select `+openPilotIssueStateColumns+` from open_pilot_issue_states where repo=$1 and issue=$2 for update`, repo, issue)
	existing, err := scanOpenPilotIssueStateRow(row)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			_ = tx.Rollback()
			return OpenPilotIssueState{}, err
		}
		if err := ValidateOpenPilotTransition("", next); err != nil {
			_ = tx.Rollback()
			return OpenPilotIssueState{}, err
		}
		state := OpenPilotIssueState{
			ID:                newID("opi"),
			Repo:              repo,
			Issue:             issue,
			Phase:             next,
			RepairAttempts:    0,
			MaxRepairAttempts: 2,
			CreatedAt:         now,
			UpdatedAt:         now,
		}
		gateStatus, err := marshalOpenPilotJSONMap(map[string]string(state.GateStatus))
		if err != nil {
			_ = tx.Rollback()
			return OpenPilotIssueState{}, err
		}
		resumeInputs, err := marshalOpenPilotJSONMap(state.ResumeInputs)
		if err != nil {
			_ = tx.Rollback()
			return OpenPilotIssueState{}, err
		}
		row = tx.QueryRowContext(s.operationContext(), `insert into open_pilot_issue_states(id, repo, issue, phase, run_id, last_step_name, branch, base, worktree_path, pull_request_url, pull_request_id, pull_request_number, merge_commit_sha, issue_closed, gate_status, resume_inputs, repair_attempts, max_repair_attempts, last_error, failing_gate, operator_summary, recommended_next_action, created_at, updated_at) values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15::jsonb,$16::jsonb,$17,$18,$19,$20,$21,$22,$23,$24) returning `+openPilotIssueStateColumns+``, state.ID, state.Repo, state.Issue, state.Phase, "", "", "", "", "", "", nil, nil, "", false, string(gateStatus), string(resumeInputs), state.RepairAttempts, state.MaxRepairAttempts, "", nullString(state.FailingGate), nullString(state.OperatorSummary), nullString(state.RecommendedNextAction), state.CreatedAt, state.UpdatedAt)
		saved, err := scanOpenPilotIssueStateRow(row)
		if err != nil {
			_ = tx.Rollback()
			return OpenPilotIssueState{}, err
		}
		if err := tx.Commit(); err != nil {
			return OpenPilotIssueState{}, err
		}
		return saved, nil
	}
	if err := normalizeOpenPilotRepairCounts(&existing); err != nil {
		_ = tx.Rollback()
		return OpenPilotIssueState{}, err
	}
	if err := ValidateOpenPilotTransition(existing.Phase, next); err != nil {
		_ = tx.Rollback()
		return OpenPilotIssueState{}, err
	}
	row = tx.QueryRowContext(s.operationContext(), `update open_pilot_issue_states set phase=$3, max_repair_attempts=$4, updated_at=$5 where repo=$1 and issue=$2 returning `+openPilotIssueStateColumns+``, repo, issue, next, existing.MaxRepairAttempts, now)
	saved, err := scanOpenPilotIssueStateRow(row)
	if err != nil {
		_ = tx.Rollback()
		return OpenPilotIssueState{}, err
	}
	if err := tx.Commit(); err != nil {
		return OpenPilotIssueState{}, err
	}
	return saved, nil
}

func (s *PostgresStore) ClaimOpenPilotIssueStateForResume(repo, issue string, resumeInputs map[string]string) (OpenPilotIssueState, error) {
	if repo == "" || issue == "" {
		return OpenPilotIssueState{}, ErrInvalid
	}
	now := s.now().UTC()
	resumeJSON, err := marshalOpenPilotJSONMap(resumeInputs)
	if err != nil {
		return OpenPilotIssueState{}, err
	}
	tx, err := s.db.BeginTx(s.operationContext(), nil)
	if err != nil {
		return OpenPilotIssueState{}, err
	}
	row := tx.QueryRowContext(s.operationContext(), `select `+openPilotIssueStateColumns+` from open_pilot_issue_states where repo=$1 and issue=$2 for update`, repo, issue)
	existing, err := scanOpenPilotIssueStateRow(row)
	if err != nil {
		_ = tx.Rollback()
		return OpenPilotIssueState{}, mapScanErr(err)
	}
	if !isOpenPilotResumablePhase(existing.Phase) {
		_ = tx.Rollback()
		return OpenPilotIssueState{}, ErrInvalid
	}
	if err := normalizeOpenPilotRepairCounts(&existing); err != nil {
		_ = tx.Rollback()
		return OpenPilotIssueState{}, err
	}
	claimPhase := openPilotResumeClaimPhase(existing.Phase, resumeInputs)
	row = tx.QueryRowContext(s.operationContext(), `update open_pilot_issue_states set phase=$3, resume_inputs=$4::jsonb, last_error='', failing_gate='', operator_summary='', recommended_next_action='', max_repair_attempts=$5, updated_at=$6 where repo=$1 and issue=$2 and phase in ('agent_completed','ai_reviewed','merged','failed','paused') returning `+openPilotIssueStateColumns+``, repo, issue, claimPhase, string(resumeJSON), existing.MaxRepairAttempts, now)
	saved, err := scanOpenPilotIssueStateRow(row)
	if err != nil {
		_ = tx.Rollback()
		return OpenPilotIssueState{}, mapScanErr(err)
	}
	if err := tx.Commit(); err != nil {
		return OpenPilotIssueState{}, err
	}
	return saved, nil
}

func (s *PostgresStore) ClaimAndCreateOpenPilotResumeRun(repo, issue string, input CreateRunInput) (OpenPilotIssueState, Run, error) {
	if repo == "" || issue == "" || input.WorkflowID != string(WorkflowOpenPilot) {
		return OpenPilotIssueState{}, Run{}, ErrInvalid
	}
	ctx := s.operationContext()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OpenPilotIssueState{}, Run{}, err
	}
	rollback := func(err error) (OpenPilotIssueState, Run, error) {
		_ = tx.Rollback()
		return OpenPilotIssueState{}, Run{}, err
	}
	state, err := scanOpenPilotIssueStateRow(tx.QueryRowContext(ctx, `select `+openPilotIssueStateColumns+` from open_pilot_issue_states where repo=$1 and issue=$2 for update`, repo, issue))
	if err != nil {
		return rollback(mapScanErr(err))
	}
	var priorStatus RunStatus
	if err := tx.QueryRowContext(ctx, `select status from runs where id=$1`, state.RunID).Scan(&priorStatus); err != nil {
		return rollback(mapScanErr(err))
	}
	if priorStatus == RunQueued || priorStatus == RunRunning {
		return rollback(ErrInvalid)
	}
	var active bool
	if err := tx.QueryRowContext(ctx, `select exists(select 1 from runs where workflow_id=$1 and status in ($2,$3) and inputs->>'repo'=$4 and inputs->>'issue'=$5)`, string(WorkflowOpenPilot), RunQueued, RunRunning, repo, issue).Scan(&active); err != nil {
		return rollback(err)
	}
	if active {
		return rollback(ErrInvalid)
	}
	inputs, err := safeOpenPilotResumeInputs(state)
	if err != nil {
		return rollback(err)
	}
	for name, value := range input.Inputs {
		if name != "retry_from_step" && name != "retry_from_step_position" && name != "repo" && name != "issue" {
			inputs[name] = value
		}
	}
	stripOpenPilotCIRepairInputs(inputs)
	input.Inputs = inputs
	encodedInputs, err := json.Marshal(inputs)
	if err != nil {
		return rollback(err)
	}
	now := s.now().UTC()
	maxAttempts := input.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	run := Run{ID: newID("run"), WorkflowID: input.WorkflowID, TriggerID: input.TriggerID, Status: RunQueued, Inputs: cloneMap(inputs), CreatedAt: now, QueuedAt: now, AvailableAt: now, MaxAttempts: maxAttempts}
	var pipelineSnapshot any
	if input.PipelineSnapshot != nil {
		snapshot := clonePipeline(*input.PipelineSnapshot)
		run.PipelineID, run.PipelineVersion, run.PipelineSnapshot = snapshot.ID, pipelineVersion(snapshot), &snapshot
		encoded, marshalErr := json.Marshal(snapshot)
		if marshalErr != nil {
			return rollback(marshalErr)
		}
		pipelineSnapshot = string(encoded)
	}
	triggerID := sql.NullString{String: input.TriggerID, Valid: input.TriggerID != ""}
	if _, err := tx.ExecContext(ctx, `insert into runs(id, workflow_id, trigger_id, status, inputs, created_at, queued_at, available_at, attempt_count, max_attempts, summary, pipeline_id, pipeline_version, pipeline_snapshot) values($1,$2,$3,$4,$5::jsonb,$6,$7,$8,$9,$10,'',$11,$12,$13::jsonb)`, run.ID, run.WorkflowID, triggerID, run.Status, string(encodedInputs), now, now, now, 0, maxAttempts, run.PipelineID, run.PipelineVersion, pipelineSnapshot); err != nil {
		return rollback(mapPgErr(err))
	}
	if _, err := tx.ExecContext(ctx, `insert into run_events(id, run_id, type, message, created_at) values($1,$2,$3,$4,$5)`, newID("evt"), run.ID, "run.created", "Open Pilot resume queued", now); err != nil {
		return rollback(err)
	}
	phase := openPilotResumeClaimPhase(state.Phase, inputs)
	resumeJSON, err := marshalOpenPilotJSONMap(inputs)
	if err != nil {
		return rollback(err)
	}
	state, err = scanOpenPilotIssueStateRow(tx.QueryRowContext(ctx, `update open_pilot_issue_states set phase=$3, run_id=$4, resume_inputs=$5::jsonb, last_error='', failing_gate='', operator_summary='', recommended_next_action='', updated_at=$6 where repo=$1 and issue=$2 returning `+openPilotIssueStateColumns, repo, issue, phase, run.ID, string(resumeJSON), now))
	if err != nil {
		return rollback(mapScanErr(err))
	}
	if err := tx.Commit(); err != nil {
		return OpenPilotIssueState{}, Run{}, err
	}
	return state, run, nil
}

func (s *PostgresStore) ClaimAndCreateOpenPilotInitialRun(repo, issue string, input CreateRunInput) (OpenPilotIssueState, Run, error) {
	if repo == "" || issue == "" || input.WorkflowID != string(WorkflowOpenPilot) {
		return OpenPilotIssueState{}, Run{}, ErrInvalid
	}
	ctx := s.operationContext()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OpenPilotIssueState{}, Run{}, err
	}
	rollback := func(err error) (OpenPilotIssueState, Run, error) {
		_ = tx.Rollback()
		return OpenPilotIssueState{}, Run{}, err
	}
	input.Inputs = cloneMap(input.Inputs)
	input.Inputs["repo"], input.Inputs["issue"] = repo, issue
	encodedInputs, err := json.Marshal(input.Inputs)
	if err != nil {
		return rollback(err)
	}
	now := s.now().UTC()
	maxAttempts := input.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	run := Run{ID: newID("run"), WorkflowID: input.WorkflowID, TriggerID: input.TriggerID, Status: RunQueued, Inputs: cloneMap(input.Inputs), CreatedAt: now, QueuedAt: now, AvailableAt: now, MaxAttempts: maxAttempts}
	var pipelineSnapshot any
	if input.PipelineSnapshot != nil {
		snapshot := clonePipeline(*input.PipelineSnapshot)
		run.PipelineID, run.PipelineVersion, run.PipelineSnapshot = snapshot.ID, pipelineVersion(snapshot), &snapshot
		encoded, marshalErr := json.Marshal(snapshot)
		if marshalErr != nil {
			return rollback(marshalErr)
		}
		pipelineSnapshot = string(encoded)
	}
	triggerID := sql.NullString{String: input.TriggerID, Valid: input.TriggerID != ""}
	if _, err := tx.ExecContext(ctx, `insert into runs(id, workflow_id, trigger_id, status, inputs, created_at, queued_at, available_at, attempt_count, max_attempts, summary, pipeline_id, pipeline_version, pipeline_snapshot) values($1,$2,$3,$4,$5::jsonb,$6,$7,$8,0,$9,'',$10,$11,$12::jsonb)`, run.ID, run.WorkflowID, triggerID, run.Status, string(encodedInputs), now, now, now, maxAttempts, run.PipelineID, run.PipelineVersion, pipelineSnapshot); err != nil {
		return rollback(mapPgErr(err))
	}
	resumeJSON, err := marshalOpenPilotJSONMap(input.Inputs)
	if err != nil {
		return rollback(err)
	}
	state := OpenPilotIssueState{ID: newID("opi"), Repo: repo, Issue: issue, Phase: OpenPilotPhaseRequested, RunID: run.ID, ResumeInputs: cloneMap(input.Inputs), MaxRepairAttempts: 2, CreatedAt: now, UpdatedAt: now}
	gateJSON, _ := marshalOpenPilotJSONMap(nil)
	state, err = scanOpenPilotIssueStateRow(tx.QueryRowContext(ctx, `insert into open_pilot_issue_states(id,repo,issue,phase,run_id,last_step_name,branch,base,worktree_path,pull_request_url,pull_request_id,pull_request_number,merge_commit_sha,issue_closed,gate_status,resume_inputs,repair_attempts,max_repair_attempts,last_error,failing_gate,operator_summary,recommended_next_action,created_at,updated_at) values($1,$2,$3,$4,$5,'','','','','',null,null,'',false,$6::jsonb,$7::jsonb,0,2,'',null,null,null,$8,$8) on conflict (repo,issue) do nothing returning `+openPilotIssueStateColumns, state.ID, repo, issue, state.Phase, run.ID, string(gateJSON), string(resumeJSON), now))
	if errors.Is(err, sql.ErrNoRows) {
		return rollback(ErrAlreadyClaimed)
	}
	if err != nil {
		return rollback(mapPgErr(err))
	}
	if _, err := tx.ExecContext(ctx, `insert into run_events(id,run_id,type,message,created_at) values($1,$2,$3,$4,$5)`, newID("evt"), run.ID, "run.created", "Verified Open Pilot issue queued", now); err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return OpenPilotIssueState{}, Run{}, err
	}
	return state, run, nil
}

func (s *PostgresStore) ListOpenPilotIssueStates() []OpenPilotIssueState {
	rows, err := s.db.QueryContext(s.operationContext(), `select `+openPilotIssueStateColumns+` from open_pilot_issue_states order by repo, issue`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []OpenPilotIssueState{}
	for rows.Next() {
		if state, ok := scanOpenPilotIssueState(rows); ok {
			out = append(out, state)
		}
	}
	return out
}

func nullInt64(v int) any {
	if v == 0 {
		return nil
	}
	return int64(v)
}

func nullString(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

func marshalOpenPilotJSONMap(in map[string]string) ([]byte, error) {
	if len(in) == 0 {
		return []byte("{}"), nil
	}
	return json.Marshal(in)
}

func scanOpenPilotIssueState(rows *sql.Rows) (OpenPilotIssueState, bool) {
	state, err := scanOpenPilotIssueStateRow(rows)
	return state, err == nil
}

func scanOpenPilotIssueStateRow(row scanner) (OpenPilotIssueState, error) {
	var state OpenPilotIssueState
	var gateStatusBytes []byte
	var resumeInputsBytes []byte
	var pullRequestID, pullRequestNum sql.NullInt64
	var runID, lastStepName, branch, base, worktreePath, pullRequestURL, mergeCommitSHA, lastError, failingGate, operatorSummary, recommendedNextAction sql.NullString
	if err := row.Scan(&state.ID, &state.Repo, &state.Issue, &state.Phase, &runID, &lastStepName, &branch, &base, &worktreePath, &pullRequestURL, &pullRequestID, &pullRequestNum, &mergeCommitSHA, &state.IssueClosed, &gateStatusBytes, &resumeInputsBytes, &state.RepairAttempts, &state.MaxRepairAttempts, &lastError, &failingGate, &operatorSummary, &recommendedNextAction, &state.CreatedAt, &state.UpdatedAt); err != nil {
		return OpenPilotIssueState{}, err
	}
	if runID.Valid {
		state.RunID = runID.String
	}
	if lastStepName.Valid {
		state.LastStepName = lastStepName.String
	}
	if branch.Valid {
		state.Branch = branch.String
	}
	if base.Valid {
		state.Base = base.String
	}
	if worktreePath.Valid {
		state.WorktreePath = worktreePath.String
	}
	if pullRequestURL.Valid {
		state.PullRequestURL = pullRequestURL.String
	}
	if pullRequestID.Valid {
		state.PullRequestID = int(pullRequestID.Int64)
	}
	if pullRequestNum.Valid {
		state.PullRequestNum = int(pullRequestNum.Int64)
	}
	if mergeCommitSHA.Valid {
		state.MergeCommitSHA = mergeCommitSHA.String
	}
	if lastError.Valid {
		state.LastError = lastError.String
	}
	if failingGate.Valid {
		state.FailingGate = failingGate.String
	}
	if operatorSummary.Valid {
		state.OperatorSummary = operatorSummary.String
	}
	if recommendedNextAction.Valid {
		state.RecommendedNextAction = recommendedNextAction.String
	}
	if err := json.Unmarshal(gateStatusBytes, &state.GateStatus); err != nil {
		return OpenPilotIssueState{}, err
	}
	if err := json.Unmarshal(resumeInputsBytes, &state.ResumeInputs); err != nil {
		return OpenPilotIssueState{}, err
	}
	return state, nil
}
