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
		if _, present, _ := runAttemptFenceFromContext(ctx); present && errors.Is(err, sql.ErrNoRows) {
			return rollback(ErrStaleRunAttempt)
		}
		return rollback(mapScanErr(err))
	}
	if fence, present, fenceErr := runAttemptFenceFromContext(ctx); present {
		if fenceErr != nil || state.RunID != fence.RunID || request.ParentRunID != fence.RunID || request.ExpectedWorkerID != fence.WorkerID || request.ExpectedAttemptCount != fence.Attempt {
			return rollback(ErrStaleRunAttempt)
		}
	}
	if state.RunID != request.ParentRunID {
		child, err := scanRunRow(tx.QueryRowContext(ctx, `select `+postgresRunColumns+` from runs where id=$1`, state.RunID))
		if err != nil {
			return rollback(mapScanErr(err))
		}
		if err := validateOpenPilotRunStateTarget(child, state, request.Target); err != nil {
			return rollback(err)
		}
		if child.Inputs[openPilotCIRepairParentRunIDKey] != request.ParentRunID {
			return rollback(ErrConflict)
		}
		parent, err := scanRunRow(tx.QueryRowContext(ctx, `select `+postgresRunColumns+` from runs where id=$1`, request.ParentRunID))
		if err != nil {
			return rollback(mapScanErr(err))
		}
		if err := validateOpenPilotRunAffinityTarget(parent, state, request.Target); err != nil {
			return rollback(err)
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
	if err := validateOpenPilotRunStateTarget(parent, state, request.Target); err != nil {
		return rollback(err)
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

func (s *PostgresStore) ReconcileOpenPilotCIRepairTerminal(runID string, gates ...*OpenPilotAdmissionGate) (bool, error) {
	gate := firstOpenPilotGate(gates)
	if gate == nil {
		return false, ErrInvalid
	}
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
	if _, err := gate.ValidateRunState(run, state); err != nil {
		return rollback(err)
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

func (s *PostgresStore) ReconcileOpenPilotMergeTerminal(runID string, gates ...*OpenPilotAdmissionGate) (bool, error) {
	gate := firstOpenPilotGate(gates)
	if gate == nil {
		return false, ErrInvalid
	}
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
	if _, err := gate.ValidateRunState(run, state); err != nil {
		return rollback(err)
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

func (s *PostgresStore) ReconcileOpenPilotMergeTerminals(limit int, gates ...*OpenPilotAdmissionGate) (int, error) {
	gate := firstOpenPilotGate(gates)
	if gate == nil || limit < 1 || limit > 100 {
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
	reconciled := 0
	for _, state := range states {
		run, err := scanRunRow(tx.QueryRowContext(ctx, `select `+postgresRunColumns+` from runs where id=$1 for update`, state.RunID))
		if err != nil {
			return rollback(mapScanErr(err))
		}
		if _, err := gate.ValidateRunState(run, state); err != nil {
			continue
		}
		setOpenPilotMergeTerminalDiagnostic(&state, run.Summary, s.now().UTC())
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

func (s *PostgresStore) ReconcileOpenPilotCIRepairTerminals(limit int, gates ...*OpenPilotAdmissionGate) (int, error) {
	gate := firstOpenPilotGate(gates)
	if gate == nil || limit < 1 || limit > 100 {
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
		if _, err := gate.ValidateRunState(run, state); err != nil {
			continue
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

func (s *PostgresStore) OpenPilotIssueStateForTarget(target OpenPilotTarget) (OpenPilotIssueState, error) {
	if err := validateOpenPilotTarget(target); err != nil {
		return OpenPilotIssueState{}, err
	}
	rows, err := s.db.QueryContext(s.operationContext(), `select `+openPilotIssueStateColumns+` from open_pilot_issue_states where canonical_issue=$1 and (lower(canonical_repo)=any($2::text[]) or coalesce(canonical_candidates,array[]::text[]) && $2::text[]) order by id limit 2`, target.Issue, openPilotTargetCandidates(target))
	if err != nil {
		return OpenPilotIssueState{}, err
	}
	defer rows.Close()
	var matched OpenPilotIssueState
	matches := 0
	for rows.Next() {
		matched, err = scanOpenPilotIssueStateRow(rows)
		if err != nil {
			return OpenPilotIssueState{}, err
		}
		matches++
	}
	if err := rows.Err(); err != nil {
		return OpenPilotIssueState{}, err
	}
	if matches > 1 {
		return OpenPilotIssueState{}, ErrConflict
	}
	if matches == 0 {
		return OpenPilotIssueState{}, ErrNotFound
	}
	return matched, nil
}

func (s *PostgresStore) BindOpenPilotIssueStateAffinity(repo, issue string, target OpenPilotTarget) (OpenPilotIssueState, error) {
	if err := validateOpenPilotTarget(target); err != nil {
		return OpenPilotIssueState{}, err
	}
	ctx := s.operationContext()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OpenPilotIssueState{}, err
	}
	rollback := func(err error) (OpenPilotIssueState, error) { _ = tx.Rollback(); return OpenPilotIssueState{}, err }
	state, err := scanOpenPilotIssueStateRow(tx.QueryRowContext(ctx, `select `+openPilotIssueStateColumns+` from open_pilot_issue_states where repo=$1 and issue=$2 for update`, repo, issue))
	if err != nil {
		return rollback(mapScanErr(err))
	}
	if err := lockPostgresOpenPilotCandidates(ctx, tx, target); err != nil {
		return rollback(err)
	}
	if err := postgresOpenPilotCanonicalCollision(ctx, tx, state.ID, target); err != nil {
		return rollback(err)
	}
	if state.RepositoryID != "" {
		if state.RepositoryID != target.RepositoryID || state.ExecutorGeneration != target.ExecutorGeneration || state.CanonicalRepo != target.RepositorySlug || state.CanonicalIssue != target.Issue {
			return rollback(ErrConflict)
		}
		state, err = scanOpenPilotIssueStateRow(tx.QueryRowContext(ctx, `update open_pilot_issue_states set canonical_candidates=(select array_agg(distinct lower(value) order by lower(value)) from unnest(coalesce(canonical_candidates,array[]::text[]) || $2::text[]) value) where id=$1 returning `+openPilotIssueStateColumns, state.ID, openPilotTargetCandidates(target)))
		if err != nil {
			return rollback(mapScanErr(err))
		}
		if err := tx.Commit(); err != nil {
			return OpenPilotIssueState{}, err
		}
		return state, nil
	}
	state, err = scanOpenPilotIssueStateRow(tx.QueryRowContext(ctx, `update open_pilot_issue_states set repository_id=$3,executor_generation=$4,canonical_repo=$5,canonical_issue=$6,canonical_candidates=(select array_agg(distinct lower(value) order by lower(value)) from unnest(coalesce(canonical_candidates,array[]::text[]) || $7::text[]) value),updated_at=$8 where repo=$1 and issue=$2 and repository_id is null and executor_generation is null returning `+openPilotIssueStateColumns, repo, issue, target.RepositoryID, target.ExecutorGeneration, target.RepositorySlug, target.Issue, openPilotTargetCandidates(target), s.now().UTC()))
	if err != nil {
		return rollback(mapScanErr(err))
	}
	if err := tx.Commit(); err != nil {
		return OpenPilotIssueState{}, err
	}
	return state, nil
}

func (s *PostgresStore) AdoptOpenPilotRunAffinity(runID, expectedWorkerID string, expectedAttemptCount int, gate *OpenPilotAdmissionGate) (Run, OpenPilotIssueState, error) {
	if runID == "" || expectedWorkerID == "" || expectedAttemptCount < 1 {
		return Run{}, OpenPilotIssueState{}, ErrInvalid
	}
	ctx := s.operationContext()
	fence, fencePresent, fenceErr := runAttemptFenceFromContext(ctx)
	if fencePresent && (fenceErr != nil || fence.RunID != runID || fence.WorkerID != expectedWorkerID || fence.Attempt != expectedAttemptCount) {
		return Run{}, OpenPilotIssueState{}, ErrStaleRunAttempt
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, OpenPilotIssueState{}, err
	}
	if err := lockPostgresOpenPilotStateForRun(ctx, tx, runID); err != nil {
		_ = tx.Rollback()
		return Run{}, OpenPilotIssueState{}, err
	}
	if fencePresent {
		if err := s.lockRunAttemptFenceTx(tx, runID); err != nil {
			_ = tx.Rollback()
			return Run{}, OpenPilotIssueState{}, err
		}
	}
	run, err := scanRunRow(tx.QueryRowContext(ctx, `select `+postgresRunColumns+` from runs where id=$1 for update`, runID))
	if err != nil {
		_ = tx.Rollback()
		return Run{}, OpenPilotIssueState{}, mapScanErr(err)
	}
	if run.WorkflowID == string(WorkflowOpenPilot) && run.Status != RunQueued && run.Status != RunRunning {
		_ = tx.Rollback()
		return Run{}, OpenPilotIssueState{}, ErrConflict
	}
	var claimLive bool
	if err := tx.QueryRowContext(ctx, `select status=$2 and claimed_by=$3 and attempt_count=$4 and lease_expires_at is not null and lease_expires_at > clock_timestamp() from runs where id=$1`, runID, RunRunning, expectedWorkerID, expectedAttemptCount).Scan(&claimLive); err != nil {
		_ = tx.Rollback()
		return Run{}, OpenPilotIssueState{}, mapScanErr(err)
	}
	if !claimLive {
		_ = tx.Rollback()
		return Run{}, OpenPilotIssueState{}, ErrConflict
	}
	run, state, adoptErr := adoptPostgresOpenPilotRunAffinity(ctx, tx, run, gate, s.now().UTC())
	if adoptErr != nil && !errors.Is(adoptErr, ErrConflict) {
		_ = tx.Rollback()
		return Run{}, OpenPilotIssueState{}, adoptErr
	}
	if err := tx.Commit(); err != nil {
		return Run{}, OpenPilotIssueState{}, err
	}
	return run, state, adoptErr
}

func lockPostgresOpenPilotStateForRun(ctx context.Context, tx *sql.Tx, runID string) error {
	count, err := lockPostgresOpenPilotStatesForRun(ctx, tx, runID)
	if err != nil {
		return err
	}
	if count > 1 {
		return ErrConflict
	}
	return nil
}

func lockPostgresOpenPilotStatesForRun(ctx context.Context, tx *sql.Tx, runID string) (int, error) {
	rows, err := tx.QueryContext(ctx, `select id from open_pilot_issue_states where run_id=$1 order by id limit 2 for update`, runID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return count, nil
}

func quarantinePostgresDuplicateOpenPilotRunStates(ctx context.Context, tx *sql.Tx, run Run, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `update runs set status=$2,summary=$3,finished_at=$4,claimed_at=null,claimed_by='',lease_expires_at=null where id=$1`, run.ID, RunPaused, openPilotAffinityAdoptionDiagnostic, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update open_pilot_issue_states set phase=$2,last_error=$3,failing_gate='repository_affinity',operator_summary=$3,recommended_next_action='manual_review',updated_at=$4 where run_id=$1`, run.ID, OpenPilotPhasePaused, openPilotAffinityAdoptionDiagnostic, now); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `insert into run_events(id,run_id,type,message,created_at) values($1,$2,'run.paused',$3,$4)`, newID("evt"), run.ID, openPilotAffinityAdoptionDiagnostic, now)
	return err
}

func adoptPostgresOpenPilotRunAffinity(ctx context.Context, tx *sql.Tx, run Run, gate *OpenPilotAdmissionGate, now time.Time) (Run, OpenPilotIssueState, error) {
	if run.WorkflowID != string(WorkflowOpenPilot) {
		return run, OpenPilotIssueState{}, nil
	}
	state, err := scanOpenPilotIssueStateRow(tx.QueryRowContext(ctx, `select `+openPilotIssueStateColumns+` from open_pilot_issue_states where repo=$1 and issue=$2 for update`, run.Inputs["repo"], run.Inputs["issue"]))
	stateFound := err == nil
	unsafe := gate == nil || (!stateFound && errors.Is(err, sql.ErrNoRows))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Run{}, OpenPilotIssueState{}, err
	}
	unsafe = unsafe || state.RunID != run.ID || state.Repo != run.Inputs["repo"] || state.Issue != run.Inputs["issue"]
	var target OpenPilotTarget
	if !unsafe {
		target, err = gate.Resolve(state.Repo, state.Issue)
		unsafe = err != nil
	}
	stateNull := state.RepositoryID == "" && state.ExecutorGeneration == ""
	runNull := run.Inputs[openPilotRepositoryIDInputKey] == "" && run.Inputs[openPilotExecutorGenerationInputKey] == ""
	stateExact := !stateNull && state.RepositoryID == target.RepositoryID && state.ExecutorGeneration == target.ExecutorGeneration && state.CanonicalRepo == target.RepositorySlug && state.CanonicalIssue == target.Issue
	runExact := !runNull && run.Inputs[openPilotRepositoryIDInputKey] == target.RepositoryID && run.Inputs[openPilotExecutorGenerationInputKey] == target.ExecutorGeneration
	if !unsafe && ((stateExact && runExact) || (stateNull && runNull)) {
		if err := lockPostgresOpenPilotCandidates(ctx, tx, target); err != nil {
			return Run{}, OpenPilotIssueState{}, err
		}
		if err := postgresOpenPilotCanonicalCollision(ctx, tx, state.ID, target); err != nil {
			if !errors.Is(err, ErrConflict) {
				return Run{}, OpenPilotIssueState{}, err
			}
			unsafe = true
		}
	}
	if !unsafe && (state.Repo != target.RepositorySlug || state.Issue != target.Issue) {
		var canonicalStateID string
		err = tx.QueryRowContext(ctx, `select id from open_pilot_issue_states where repo=$1 and issue=$2`, target.RepositorySlug, target.Issue).Scan(&canonicalStateID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Run{}, OpenPilotIssueState{}, err
		}
		unsafe = err == nil && canonicalStateID != state.ID
	}
	if unsafe || (!stateExact && !stateNull) || (!runExact && !runNull) {
		return quarantinePostgresOpenPilotRunAffinity(ctx, tx, run, state, stateFound, now)
	}
	state, err = scanOpenPilotIssueStateRow(tx.QueryRowContext(ctx, `update open_pilot_issue_states set repo=$2,issue=$3,repository_id=$4,executor_generation=$5,canonical_repo=$2,canonical_issue=$3,canonical_candidates=(select array_agg(distinct lower(value) order by lower(value)) from unnest(coalesce(canonical_candidates,array[]::text[]) || $6::text[]) value),legacy_repo=coalesce(legacy_repo,repo),legacy_issue=coalesce(legacy_issue,issue),updated_at=$7 where id=$1 returning `+openPilotIssueStateColumns, state.ID, target.RepositorySlug, target.Issue, target.RepositoryID, target.ExecutorGeneration, openPilotTargetCandidates(target), now))
	if err != nil {
		return Run{}, OpenPilotIssueState{}, mapScanErr(err)
	}
	run.Inputs = cloneMap(run.Inputs)
	run.Inputs["repo"], run.Inputs["issue"] = target.RepositorySlug, target.Issue
	setOpenPilotAffinityInputs(run.Inputs, target)
	encodedInputs, err := json.Marshal(run.Inputs)
	if err != nil {
		return Run{}, OpenPilotIssueState{}, err
	}
	run, err = scanRunRow(tx.QueryRowContext(ctx, `update runs set inputs=$2::jsonb where id=$1 and status in ($3,$4) returning `+postgresRunColumns, run.ID, string(encodedInputs), RunQueued, RunRunning))
	if err != nil {
		return Run{}, OpenPilotIssueState{}, mapScanErr(err)
	}
	return run, state, nil
}

func quarantinePostgresOpenPilotRunAffinity(ctx context.Context, tx *sql.Tx, run Run, state OpenPilotIssueState, stateFound bool, now time.Time) (Run, OpenPilotIssueState, error) {
	var err error
	run, err = scanRunRow(tx.QueryRowContext(ctx, `update runs set status=$2,summary=$3,finished_at=$4,claimed_at=null,claimed_by='',lease_expires_at=null where id=$1 returning `+postgresRunColumns, run.ID, RunPaused, openPilotAffinityAdoptionDiagnostic, now))
	if err != nil {
		return Run{}, OpenPilotIssueState{}, err
	}
	if stateFound {
		state, err = scanOpenPilotIssueStateRow(tx.QueryRowContext(ctx, `update open_pilot_issue_states set phase=$2,last_error=$3,failing_gate='repository_affinity',operator_summary=$3,recommended_next_action='manual_review',updated_at=$4 where id=$1 returning `+openPilotIssueStateColumns, state.ID, OpenPilotPhasePaused, openPilotAffinityAdoptionDiagnostic, now))
		if err != nil {
			return Run{}, OpenPilotIssueState{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `insert into run_events(id,run_id,type,message,created_at) values($1,$2,'run.paused',$3,$4)`, newID("evt"), run.ID, openPilotAffinityAdoptionDiagnostic, now); err != nil {
		return Run{}, OpenPilotIssueState{}, err
	}
	return run, state, ErrConflict
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
	if fence, present, fenceErr := runAttemptFenceFromContext(s.operationContext()); present {
		if fenceErr != nil || existingPtr == nil || existing.RunID != fence.RunID || input.RunID != fence.RunID {
			_ = tx.Rollback()
			return OpenPilotIssueState{}, ErrStaleRunAttempt
		}
		if err := s.lockRunAttemptFenceTx(tx, fence.RunID); err != nil {
			_ = tx.Rollback()
			return OpenPilotIssueState{}, err
		}
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
			insert into open_pilot_issue_states(id, repo, issue, repository_id, executor_generation, canonical_repo, canonical_issue, legacy_repo, legacy_issue, phase, run_id, last_step_name, branch, base, worktree_path, pull_request_url, pull_request_id, pull_request_number, merge_commit_sha, issue_closed, gate_status, resume_inputs, repair_attempts, max_repair_attempts, last_error, failing_gate, operator_summary, recommended_next_action, created_at, updated_at)
			values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21::jsonb,$22::jsonb,$23,$24,$25,$26,$27,$28,$29,$30)
			returning `+openPilotIssueStateColumns+``,
			state.ID, state.Repo, state.Issue, nullString(state.RepositoryID), nullString(state.ExecutorGeneration), nullString(state.CanonicalRepo), nullString(state.CanonicalIssue), nullString(state.LegacyRepo), nullString(state.LegacyIssue), state.Phase, state.RunID, state.LastStepName, state.Branch, state.Base, state.WorktreePath, state.PullRequestURL, nullInt64(state.PullRequestID), nullInt64(state.PullRequestNum), state.MergeCommitSHA, state.IssueClosed, string(gateStatus), string(resumeInputs), state.RepairAttempts, state.MaxRepairAttempts, state.LastError, nullString(state.FailingGate), nullString(state.OperatorSummary), nullString(state.RecommendedNextAction), state.CreatedAt, state.UpdatedAt)
	} else {
		row = tx.QueryRowContext(s.operationContext(), `
			update open_pilot_issue_states set repository_id=$3, executor_generation=$4, canonical_repo=$5, canonical_issue=$6, legacy_repo=$7, legacy_issue=$8, phase=$9, run_id=$10, last_step_name=$11, branch=$12, base=$13, worktree_path=$14, pull_request_url=$15, pull_request_id=$16, pull_request_number=$17, merge_commit_sha=$18, issue_closed=$19, gate_status=$20::jsonb, resume_inputs=$21::jsonb, repair_attempts=$22, max_repair_attempts=$23, last_error=$24, failing_gate=$25, operator_summary=$26, recommended_next_action=$27, updated_at=$28
			where repo=$1 and issue=$2
			returning `+openPilotIssueStateColumns+``,
			state.Repo, state.Issue, nullString(state.RepositoryID), nullString(state.ExecutorGeneration), nullString(state.CanonicalRepo), nullString(state.CanonicalIssue), nullString(state.LegacyRepo), nullString(state.LegacyIssue), state.Phase, state.RunID, state.LastStepName, state.Branch, state.Base, state.WorktreePath, state.PullRequestURL, nullInt64(state.PullRequestID), nullInt64(state.PullRequestNum), state.MergeCommitSHA, state.IssueClosed, string(gateStatus), string(resumeInputs), state.RepairAttempts, state.MaxRepairAttempts, state.LastError, nullString(state.FailingGate), nullString(state.OperatorSummary), nullString(state.RecommendedNextAction), state.UpdatedAt)
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
		if _, present, _ := runAttemptFenceFromContext(s.operationContext()); present {
			_ = tx.Rollback()
			return OpenPilotIssueState{}, ErrStaleRunAttempt
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
	if fence, present, fenceErr := runAttemptFenceFromContext(s.operationContext()); present {
		if fenceErr != nil || existing.RunID != fence.RunID {
			_ = tx.Rollback()
			return OpenPilotIssueState{}, ErrStaleRunAttempt
		}
		if err := s.lockRunAttemptFenceTx(tx, fence.RunID); err != nil {
			_ = tx.Rollback()
			return OpenPilotIssueState{}, err
		}
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
	if input.OpenPilotTarget == nil {
		return rollback(ErrInvalid)
	}
	if err := validateOpenPilotTarget(*input.OpenPilotTarget); err != nil {
		return rollback(err)
	}
	state, err := scanOpenPilotIssueStateRow(tx.QueryRowContext(ctx, `select `+openPilotIssueStateColumns+` from open_pilot_issue_states where repo=$1 and issue=$2 for update`, repo, issue))
	if err != nil {
		return rollback(mapScanErr(err))
	}
	if err := lockPostgresOpenPilotCandidates(ctx, tx, *input.OpenPilotTarget); err != nil {
		return rollback(err)
	}
	if target := input.OpenPilotTarget; target != nil {
		if state.RepositoryID != target.RepositoryID || state.ExecutorGeneration != target.ExecutorGeneration || state.CanonicalRepo != target.RepositorySlug || state.CanonicalIssue != target.Issue {
			return rollback(ErrConflict)
		}
		state.RepositoryID, state.ExecutorGeneration = target.RepositoryID, target.ExecutorGeneration
		state.CanonicalRepo, state.CanonicalIssue = target.RepositorySlug, target.Issue
		if err := postgresOpenPilotCanonicalCollision(ctx, tx, state.ID, *target); err != nil {
			return rollback(err)
		}
	}
	prior, err := scanRunRow(tx.QueryRowContext(ctx, `select `+postgresRunColumns+` from runs where id=$1 for update`, state.RunID))
	if err != nil {
		return rollback(mapScanErr(err))
	}
	if prior.Status == RunQueued || prior.Status == RunRunning {
		return rollback(ErrInvalid)
	}
	if state.Phase == OpenPilotPhaseRequested {
		var hasSteps bool
		if err := tx.QueryRowContext(ctx, `select exists(select 1 from run_steps where run_id=$1)`, prior.ID).Scan(&hasSteps); err != nil {
			return rollback(err)
		}
		if prior.Status != RunCanceled || hasSteps {
			return rollback(ErrInvalid)
		}
	}
	if err := validateOpenPilotRunStateTarget(prior, state, *input.OpenPilotTarget); err != nil {
		return rollback(err)
	}
	var active bool
	if err := tx.QueryRowContext(ctx, `select exists(select 1 from runs where workflow_id=$1 and status in ($2,$3) and inputs->>$4=$5 and (regexp_replace(inputs->>'issue','^0+','')=$6 or inputs->>'issue'=$6))`, string(WorkflowOpenPilot), RunQueued, RunRunning, openPilotRepositoryIDInputKey, input.OpenPilotTarget.RepositoryID, input.OpenPilotTarget.Issue).Scan(&active); err != nil {
		return rollback(err)
	}
	if active {
		return rollback(ErrInvalid)
	}
	inputs, err := safeOpenPilotResumeInputs(state, input.PipelineSnapshot)
	if err != nil {
		return rollback(err)
	}
	for name, value := range input.Inputs {
		if name != "retry_from_step" && name != "retry_from_step_position" && name != "repo" && name != "issue" && !strings.HasPrefix(name, "resume.open-pilot.ci-repair.") {
			inputs[name] = value
		}
	}
	stripOpenPilotCIRepairInputs(inputs)
	inputs["repo"], inputs["issue"] = input.OpenPilotTarget.RepositorySlug, input.OpenPilotTarget.Issue
	setOpenPilotAffinityInputs(inputs, *input.OpenPilotTarget)
	inputs[openPilotLifecycleResumeInputKey] = "true"
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
	state, err = scanOpenPilotIssueStateRow(tx.QueryRowContext(ctx, `update open_pilot_issue_states set phase=$3, run_id=$4, resume_inputs=$5::jsonb, repository_id=$6, executor_generation=$7, canonical_repo=$8, canonical_issue=$9, canonical_candidates=(select array_agg(distinct lower(value) order by lower(value)) from unnest(coalesce(canonical_candidates,array[]::text[]) || $10::text[]) value), last_error='', failing_gate='', operator_summary='', recommended_next_action='', updated_at=$11 where repo=$1 and issue=$2 returning `+openPilotIssueStateColumns, repo, issue, phase, run.ID, string(resumeJSON), nullString(state.RepositoryID), nullString(state.ExecutorGeneration), nullString(state.CanonicalRepo), nullString(state.CanonicalIssue), openPilotTargetCandidates(*input.OpenPilotTarget), now))
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
	if input.OpenPilotTarget != nil {
		target := input.OpenPilotTarget
		if err := validateOpenPilotTarget(*target); err != nil {
			return rollback(err)
		}
		canonicalRepo, repoErr := ParseOpenPilotRepositorySlug(repo)
		canonicalIssue, issueErr := ParseOpenPilotIssue(issue)
		if repoErr != nil || issueErr != nil || !strings.EqualFold(target.RepositorySlug, canonicalRepo) || target.Issue != canonicalIssue {
			return rollback(ErrInvalid)
		}
		if err := lockPostgresOpenPilotCandidates(ctx, tx, *target); err != nil {
			return rollback(err)
		}
		var collision bool
		if err := tx.QueryRowContext(ctx, `select exists(select 1 from open_pilot_issue_states where canonical_issue=$2 and (lower(canonical_repo)=any($1::text[]) or canonical_candidates && $1::text[]))`, openPilotTargetCandidates(*target), target.Issue).Scan(&collision); err != nil {
			return rollback(err)
		}
		if collision {
			return rollback(ErrConflict)
		}
	} else {
		return rollback(ErrInvalid)
	}
	input.Inputs = cloneMap(input.Inputs)
	if input.Inputs == nil {
		input.Inputs = map[string]string{}
	}
	input.Inputs["repo"], input.Inputs["issue"] = repo, issue
	setOpenPilotAffinityInputs(input.Inputs, *input.OpenPilotTarget)
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
	state := OpenPilotIssueState{ID: newID("opi"), Repo: repo, Issue: issue, Phase: OpenPilotPhaseRequested, RunID: run.ID, ResumeInputs: cloneMap(input.Inputs), MaxRepairAttempts: 2, CreatedAt: now, UpdatedAt: now, LegacyRepo: repo, LegacyIssue: issue}
	if target := input.OpenPilotTarget; target != nil {
		state.RepositoryID, state.ExecutorGeneration = target.RepositoryID, target.ExecutorGeneration
		state.CanonicalRepo, state.CanonicalIssue = target.RepositorySlug, target.Issue
	}
	gateJSON, _ := marshalOpenPilotJSONMap(nil)
	state, err = scanOpenPilotIssueStateRow(tx.QueryRowContext(ctx, `insert into open_pilot_issue_states(id,repo,issue,repository_id,executor_generation,canonical_repo,canonical_issue,canonical_candidates,legacy_repo,legacy_issue,phase,run_id,last_step_name,branch,base,worktree_path,pull_request_url,pull_request_id,pull_request_number,merge_commit_sha,issue_closed,gate_status,resume_inputs,repair_attempts,max_repair_attempts,last_error,failing_gate,operator_summary,recommended_next_action,created_at,updated_at) values($1,$2,$3,$4,$5,$6,$7,$8::text[],$9,$10,$11,$12,'','','','','',null,null,'',false,$13::jsonb,$14::jsonb,0,2,'',null,null,null,$15,$15) on conflict (repo,issue) do nothing returning `+openPilotIssueStateColumns, state.ID, repo, issue, nullString(state.RepositoryID), nullString(state.ExecutorGeneration), nullString(state.CanonicalRepo), nullString(state.CanonicalIssue), openPilotTargetCandidates(*input.OpenPilotTarget), state.LegacyRepo, state.LegacyIssue, state.Phase, run.ID, string(gateJSON), string(resumeJSON), now))
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

func (s *PostgresStore) ListOpenPilotIssueStatesForAudit(limit int) ([]OpenPilotIssueState, error) {
	if limit < 1 || limit > maxOpenPilotAuditStates+1 {
		return nil, ErrInvalid
	}
	rows, err := s.db.QueryContext(s.operationContext(), `select `+openPilotIssueStateColumns+` from open_pilot_issue_states order by repo, issue, id limit $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]OpenPilotIssueState, 0, limit)
	for rows.Next() {
		state, err := scanOpenPilotIssueStateRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, state)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return out, nil
}

func openPilotCanonicalLockKey(repo, issue string) string {
	return strings.ToLower(repo) + "#" + issue
}

func lockPostgresOpenPilotCandidates(ctx context.Context, tx *sql.Tx, target OpenPilotTarget) error {
	for _, candidate := range openPilotTargetCandidates(target) {
		if _, err := tx.ExecContext(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, openPilotCanonicalLockKey(candidate, target.Issue)); err != nil {
			return err
		}
	}
	return nil
}

func postgresOpenPilotCanonicalCollision(ctx context.Context, tx *sql.Tx, excludeID string, target OpenPilotTarget) error {
	var collision bool
	if err := tx.QueryRowContext(ctx, `select exists(select 1 from open_pilot_issue_states where id<>$1 and canonical_issue=$3 and (lower(canonical_repo)=any($2::text[]) or canonical_candidates && $2::text[]))`, excludeID, openPilotTargetCandidates(target), target.Issue).Scan(&collision); err != nil {
		return err
	}
	if collision {
		return ErrConflict
	}
	return nil
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
	var repositoryID, executorGeneration, canonicalRepo, canonicalIssue, legacyRepo, legacyIssue sql.NullString
	var runID, lastStepName, branch, base, worktreePath, pullRequestURL, mergeCommitSHA, lastError, failingGate, operatorSummary, recommendedNextAction sql.NullString
	if err := row.Scan(&state.ID, &state.Repo, &state.Issue, &repositoryID, &executorGeneration, &canonicalRepo, &canonicalIssue, &legacyRepo, &legacyIssue, &state.Phase, &runID, &lastStepName, &branch, &base, &worktreePath, &pullRequestURL, &pullRequestID, &pullRequestNum, &mergeCommitSHA, &state.IssueClosed, &gateStatusBytes, &resumeInputsBytes, &state.RepairAttempts, &state.MaxRepairAttempts, &lastError, &failingGate, &operatorSummary, &recommendedNextAction, &state.CreatedAt, &state.UpdatedAt); err != nil {
		return OpenPilotIssueState{}, err
	}
	state.RepositoryID, state.ExecutorGeneration = repositoryID.String, executorGeneration.String
	state.CanonicalRepo, state.CanonicalIssue = canonicalRepo.String, canonicalIssue.String
	state.LegacyRepo, state.LegacyIssue = legacyRepo.String, legacyIssue.String
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
