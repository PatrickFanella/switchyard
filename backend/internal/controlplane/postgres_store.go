package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type PostgresStore struct {
	db  *sql.DB
	now func() time.Time
}

const openPilotIssueStateColumns = "id, repo, issue, phase, run_id, last_step_name, branch, base, worktree_path, pull_request_url, pull_request_id, pull_request_number, merge_commit_sha, issue_closed, gate_status, resume_inputs, repair_attempts, max_repair_attempts, last_error, failing_gate, operator_summary, recommended_next_action, created_at, updated_at"
const workflowDefinitionColumns = "id, name, description, status, owns, uses, input_shape, pipeline, created_at, updated_at"
const modelBrokerRequestColumns = "id, status, model, path, client, request_body, response_body, error, correlation_ids, created_at, started_at, finished_at, updated_at, cancel_requested_at"

func NewPostgresStore(db *sql.DB, now func() time.Time) *PostgresStore {
	if now == nil {
		now = time.Now
	}
	return &PostgresStore{db: db, now: now}
}

func (s *PostgresStore) UpsertWorkflowDefinition(definition WorkflowDefinition) (WorkflowDefinition, error) {
	definition, err := normalizeWorkflowDefinition(definition)
	if err != nil {
		return WorkflowDefinition{}, err
	}
	owns, err := json.Marshal(definition.Owns)
	if err != nil {
		return WorkflowDefinition{}, err
	}
	uses, err := json.Marshal(definition.Uses)
	if err != nil {
		return WorkflowDefinition{}, err
	}
	inputShape, err := json.Marshal(definition.InputShape)
	if err != nil {
		return WorkflowDefinition{}, err
	}
	pipeline, err := json.Marshal(definition.Pipeline)
	if err != nil {
		return WorkflowDefinition{}, err
	}
	row := s.db.QueryRowContext(context.Background(), `
		insert into workflow_definitions(id, name, description, status, owns, uses, input_shape, pipeline, created_at, updated_at)
		values($1,$2,$3,$4,$5::jsonb,$6::jsonb,$7::jsonb,$8::jsonb, now(), now())
		on conflict(id) do update set name=excluded.name, description=excluded.description, status=excluded.status, owns=excluded.owns, uses=excluded.uses, input_shape=excluded.input_shape, pipeline=excluded.pipeline, updated_at=now()
		returning `+workflowDefinitionColumns,
		definition.ID, definition.Name, definition.Description, definition.Status, string(owns), string(uses), string(inputShape), string(pipeline))
	saved, err := scanWorkflowDefinitionRow(row)
	return saved, mapScanErr(err)
}

func (s *PostgresStore) WorkflowDefinition(id string) (WorkflowDefinition, error) {
	row := s.db.QueryRowContext(context.Background(), `select `+workflowDefinitionColumns+` from workflow_definitions where id=$1`, id)
	definition, err := scanWorkflowDefinitionRow(row)
	return definition, mapScanErr(err)
}

func (s *PostgresStore) ListWorkflowDefinitions() ([]WorkflowDefinition, error) {
	rows, err := s.db.QueryContext(context.Background(), `select `+workflowDefinitionColumns+` from workflow_definitions order by id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]WorkflowDefinition, 0)
	for rows.Next() {
		definition, err := scanWorkflowDefinitionRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, definition)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func OpenPostgres(ctx context.Context, databaseURL string) (*sql.DB, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func (s *PostgresStore) UpsertWorkflow(w Workflow) error {
	if w.ID == "" || w.Kind == "" || w.Name == "" {
		return ErrInvalid
	}
	owns, err := json.Marshal(w.Owns)
	if err != nil {
		return err
	}
	uses, err := json.Marshal(w.Uses)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(context.Background(), `
		insert into workflows(id, kind, name, description, owns, uses, status)
		values($1,$2,$3,$4,$5::jsonb,$6::jsonb,$7)
		on conflict(id) do update set kind=excluded.kind, name=excluded.name, description=excluded.description, owns=excluded.owns, uses=excluded.uses, status=excluded.status`,
		w.ID, w.Kind, w.Name, w.Description, string(owns), string(uses), w.Status)
	return err
}

func (s *PostgresStore) UpsertTrigger(t Trigger) error {
	if t.ID == "" || t.WorkflowID == "" || t.Type == "" || t.Name == "" {
		return ErrInvalid
	}
	inputs, err := json.Marshal(t.Inputs)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	_, err = s.db.ExecContext(context.Background(), `
		insert into triggers(id, workflow_id, type, name, description, enabled, schedule, inputs, next_run_at, last_run_at, updated_at, last_error)
		values($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9,$10,$11,$12)
		on conflict(id) do update set workflow_id=excluded.workflow_id, type=excluded.type, name=excluded.name, description=excluded.description, schedule=excluded.schedule, inputs=excluded.inputs, updated_at=excluded.updated_at`,
		t.ID, t.WorkflowID, t.Type, t.Name, t.Description, t.Enabled, t.Schedule, string(inputs), t.NextRunAt, t.LastRunAt, now, t.LastError)
	return mapPgErr(err)
}

func (s *PostgresStore) OpenPilotIssueState(repo, issue string) (OpenPilotIssueState, error) {
	row := s.db.QueryRowContext(context.Background(), `select `+openPilotIssueStateColumns+` from open_pilot_issue_states where repo=$1 and issue=$2`, repo, issue)
	state, err := scanOpenPilotIssueStateRow(row)
	return state, mapScanErr(err)
}

func (s *PostgresStore) UpsertOpenPilotIssueState(input OpenPilotIssueState) (OpenPilotIssueState, error) {
	now := s.now().UTC()
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return OpenPilotIssueState{}, err
	}
	row := tx.QueryRowContext(context.Background(), `select `+openPilotIssueStateColumns+` from open_pilot_issue_states where repo=$1 and issue=$2 for update`, input.Repo, input.Issue)
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
		row = tx.QueryRowContext(context.Background(), `
			insert into open_pilot_issue_states(id, repo, issue, phase, run_id, last_step_name, branch, base, worktree_path, pull_request_url, pull_request_id, pull_request_number, merge_commit_sha, issue_closed, gate_status, resume_inputs, repair_attempts, max_repair_attempts, last_error, failing_gate, operator_summary, recommended_next_action, created_at, updated_at)
			values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15::jsonb,$16::jsonb,$17,$18,$19,$20,$21,$22,$23,$24)
			returning `+openPilotIssueStateColumns+``,
			state.ID, state.Repo, state.Issue, state.Phase, state.RunID, state.LastStepName, state.Branch, state.Base, state.WorktreePath, state.PullRequestURL, nullInt64(state.PullRequestID), nullInt64(state.PullRequestNum), state.MergeCommitSHA, state.IssueClosed, string(gateStatus), string(resumeInputs), state.RepairAttempts, state.MaxRepairAttempts, state.LastError, nullString(state.FailingGate), nullString(state.OperatorSummary), nullString(state.RecommendedNextAction), state.CreatedAt, state.UpdatedAt)
	} else {
		row = tx.QueryRowContext(context.Background(), `
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
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return OpenPilotIssueState{}, err
	}
	row := tx.QueryRowContext(context.Background(), `select `+openPilotIssueStateColumns+` from open_pilot_issue_states where repo=$1 and issue=$2 for update`, repo, issue)
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
		row = tx.QueryRowContext(context.Background(), `insert into open_pilot_issue_states(id, repo, issue, phase, run_id, last_step_name, branch, base, worktree_path, pull_request_url, pull_request_id, pull_request_number, merge_commit_sha, issue_closed, gate_status, resume_inputs, repair_attempts, max_repair_attempts, last_error, failing_gate, operator_summary, recommended_next_action, created_at, updated_at) values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15::jsonb,$16::jsonb,$17,$18,$19,$20,$21,$22,$23,$24) returning `+openPilotIssueStateColumns+``, state.ID, state.Repo, state.Issue, state.Phase, "", "", "", "", "", "", nil, nil, "", false, string(gateStatus), string(resumeInputs), state.RepairAttempts, state.MaxRepairAttempts, "", nullString(state.FailingGate), nullString(state.OperatorSummary), nullString(state.RecommendedNextAction), state.CreatedAt, state.UpdatedAt)
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
	row = tx.QueryRowContext(context.Background(), `update open_pilot_issue_states set phase=$3, max_repair_attempts=$4, updated_at=$5 where repo=$1 and issue=$2 returning `+openPilotIssueStateColumns+``, repo, issue, next, existing.MaxRepairAttempts, now)
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
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return OpenPilotIssueState{}, err
	}
	row := tx.QueryRowContext(context.Background(), `select `+openPilotIssueStateColumns+` from open_pilot_issue_states where repo=$1 and issue=$2 for update`, repo, issue)
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
	row = tx.QueryRowContext(context.Background(), `update open_pilot_issue_states set phase=$3, resume_inputs=$4::jsonb, last_error='', failing_gate='', operator_summary='', recommended_next_action='', max_repair_attempts=$5, updated_at=$6 where repo=$1 and issue=$2 and phase in ('agent_completed','ai_reviewed','merged','failed','paused') returning `+openPilotIssueStateColumns+``, repo, issue, claimPhase, string(resumeJSON), existing.MaxRepairAttempts, now)
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

func (s *PostgresStore) ListOpenPilotIssueStates() []OpenPilotIssueState {
	rows, err := s.db.QueryContext(context.Background(), `select `+openPilotIssueStateColumns+` from open_pilot_issue_states order by repo, issue`)
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

func (s *PostgresStore) ListWorkflows() []Workflow {
	rows, err := s.db.QueryContext(context.Background(), `select id, kind, name, description, owns, uses, status from workflows order by id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Workflow
	for rows.Next() {
		var w Workflow
		var owns, uses []byte
		if err := rows.Scan(&w.ID, &w.Kind, &w.Name, &w.Description, &owns, &uses, &w.Status); err == nil {
			_ = json.Unmarshal(owns, &w.Owns)
			_ = json.Unmarshal(uses, &w.Uses)
			out = append(out, w)
		}
	}
	return out
}

func scanWorkflowDefinitionRow(row scanner) (WorkflowDefinition, error) {
	var definition WorkflowDefinition
	var ownsBytes, usesBytes, inputShapeBytes, pipelineBytes []byte
	if err := row.Scan(&definition.ID, &definition.Name, &definition.Description, &definition.Status, &ownsBytes, &usesBytes, &inputShapeBytes, &pipelineBytes, &definition.CreatedAt, &definition.UpdatedAt); err != nil {
		return WorkflowDefinition{}, err
	}
	if err := json.Unmarshal(ownsBytes, &definition.Owns); err != nil {
		return WorkflowDefinition{}, err
	}
	if err := json.Unmarshal(usesBytes, &definition.Uses); err != nil {
		return WorkflowDefinition{}, err
	}
	if err := json.Unmarshal(inputShapeBytes, &definition.InputShape); err != nil {
		return WorkflowDefinition{}, err
	}
	if err := json.Unmarshal(pipelineBytes, &definition.Pipeline); err != nil {
		return WorkflowDefinition{}, err
	}
	return definition, nil
}

func (s *PostgresStore) ListTriggers() []Trigger {
	rows, err := s.db.QueryContext(context.Background(), `select id, workflow_id, type, name, description, enabled, schedule, inputs, next_run_at, last_run_at, updated_at, last_error from triggers order by id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Trigger
	for rows.Next() {
		if t, ok := scanTrigger(rows); ok {
			out = append(out, t)
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

func (s *PostgresStore) Trigger(id string) (Trigger, bool) {
	row := s.db.QueryRowContext(context.Background(), `select id, workflow_id, type, name, description, enabled, schedule, inputs, next_run_at, last_run_at, updated_at, last_error from triggers where id=$1`, id)
	t, err := scanTriggerRow(row)
	return t, err == nil
}

func (s *PostgresStore) SetTriggerEnabled(id string, enabled bool) (Trigger, error) {
	now := s.now().UTC()
	row := s.db.QueryRowContext(context.Background(), `update triggers set enabled=$2, updated_at=$3 where id=$1 returning id, workflow_id, type, name, description, enabled, schedule, inputs, next_run_at, last_run_at, updated_at, last_error`, id, enabled, now)
	t, err := scanTriggerRow(row)
	return t, mapScanErr(err)
}

func (s *PostgresStore) UpdateTriggerScheduleState(id string, lastRunAt *time.Time, nextRunAt *time.Time, lastError string) (Trigger, error) {
	now := s.now().UTC()
	row := s.db.QueryRowContext(context.Background(), `update triggers set last_run_at=coalesce($2,last_run_at), next_run_at=coalesce($3,next_run_at), last_error=$4, updated_at=$5 where id=$1 returning id, workflow_id, type, name, description, enabled, schedule, inputs, next_run_at, last_run_at, updated_at, last_error`, id, lastRunAt, nextRunAt, lastError, now)
	t, err := scanTriggerRow(row)
	return t, mapScanErr(err)
}

func (s *PostgresStore) CreateRun(input CreateRunInput) (Run, error) {
	if input.WorkflowID == "" {
		return Run{}, ErrInvalid
	}
	inputs, err := json.Marshal(input.Inputs)
	if err != nil {
		return Run{}, err
	}
	now := s.now().UTC()
	r := Run{ID: newID("run"), WorkflowID: input.WorkflowID, TriggerID: input.TriggerID, Status: RunQueued, Inputs: cloneMap(input.Inputs), CreatedAt: now, QueuedAt: now, AvailableAt: now, MaxAttempts: 1}
	triggerID := sql.NullString{String: input.TriggerID, Valid: input.TriggerID != ""}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return Run{}, err
	}
	if _, err := tx.ExecContext(context.Background(), `insert into runs(id, workflow_id, trigger_id, status, inputs, created_at, queued_at, available_at, attempt_count, max_attempts, summary) values($1,$2,$3,$4,$5::jsonb,$6,$7,$8,$9,$10,'')`, r.ID, r.WorkflowID, triggerID, r.Status, string(inputs), r.CreatedAt, r.QueuedAt, r.AvailableAt, r.AttemptCount, r.MaxAttempts); err != nil {
		_ = tx.Rollback()
		return Run{}, mapPgErr(err)
	}
	if _, err := tx.ExecContext(context.Background(), `insert into run_events(id, run_id, type, message, created_at) values($1,$2,$3,$4,$5)`, newID("evt"), r.ID, "run.created", "Run queued", now); err != nil {
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
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return Run{}, err
	}
	row := tx.QueryRowContext(context.Background(), `update runs set status=$2, started_at=coalesce(started_at,$3), attempt_count=attempt_count+1 where id=$1 returning id, workflow_id, coalesce(trigger_id,''), status, inputs, created_at, queued_at, available_at, started_at, finished_at, claimed_at, claimed_by, lease_expires_at, attempt_count, max_attempts, summary`, id, RunRunning, now)
	r, err := scanRunRow(row)
	if err != nil {
		_ = tx.Rollback()
		return Run{}, mapScanErr(err)
	}
	if _, err := tx.ExecContext(context.Background(), `insert into run_events(id, run_id, type, message, created_at) values($1,$2,$3,$4,$5)`, newID("evt"), id, "run.started", "Run started", now); err != nil {
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
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return Run{}, err
	}
	row := tx.QueryRowContext(context.Background(), `
		with next_run as (
			select id from runs
			where status=$1 and available_at <= $2
			order by created_at, id
			for update skip locked
			limit 1
		)
		update runs set status=$3, started_at=coalesce(started_at,$2), claimed_at=$2, claimed_by=$4, lease_expires_at=$5, attempt_count=attempt_count+1
		where id in (select id from next_run)
		returning id, workflow_id, coalesce(trigger_id,''), status, inputs, created_at, queued_at, available_at, started_at, finished_at, claimed_at, claimed_by, lease_expires_at, attempt_count, max_attempts, summary`, RunQueued, now, RunRunning, workerID, expires)
	r, err := scanRunRow(row)
	if err != nil {
		_ = tx.Rollback()
		return Run{}, mapScanErr(err)
	}
	if _, err := tx.ExecContext(context.Background(), `insert into run_events(id, run_id, type, message, created_at) values($1,$2,$3,$4,$5)`, newID("evt"), r.ID, "run.started", "Run claimed by "+workerID, now); err != nil {
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
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return Run{}, err
	}
	row := tx.QueryRowContext(context.Background(), `update runs set claimed_at=$3, lease_expires_at=$4 where id=$1 and status=$5 and claimed_by=$2 returning id, workflow_id, coalesce(trigger_id,''), status, inputs, created_at, queued_at, available_at, started_at, finished_at, claimed_at, claimed_by, lease_expires_at, attempt_count, max_attempts, summary`, id, workerID, now, expires, RunRunning)
	r, err := scanRunRow(row)
	if err != nil {
		_ = tx.Rollback()
		return Run{}, mapScanErr(err)
	}
	if _, err := tx.ExecContext(context.Background(), `insert into run_events(id, run_id, type, message, created_at) values($1,$2,$3,$4,$5)`, newID("evt"), id, "run.lease.extended", "Lease extended by "+workerID, now); err != nil {
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
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return 0, 0, err
	}
	requeueRows, err := tx.QueryContext(context.Background(), `update runs set status=$2, available_at=$3, claimed_at=null, claimed_by='', lease_expires_at=null where status=$1 and lease_expires_at is not null and lease_expires_at <= $3 and attempt_count < max_attempts returning id`, RunRunning, RunQueued, now)
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
	failRows, err := tx.QueryContext(context.Background(), `update runs set status=$2, finished_at=$3, summary='Worker lease expired', claimed_at=null, claimed_by='', lease_expires_at=null where status=$1 and lease_expires_at is not null and lease_expires_at <= $3 and attempt_count >= max_attempts returning id`, RunRunning, RunFailed, now)
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
		if _, err := tx.ExecContext(context.Background(), `insert into run_events(id, run_id, type, message, created_at) values($1,$2,$3,$4,$5)`, newID("evt"), id, "run.recovered", "Worker lease expired; run requeued", now); err != nil {
			_ = tx.Rollback()
			return 0, 0, err
		}
	}
	for _, id := range failedIDs {
		if _, err := tx.ExecContext(context.Background(), `insert into run_events(id, run_id, type, message, created_at) values($1,$2,$3,$4,$5)`, newID("evt"), id, "run.failed", "Worker lease expired", now); err != nil {
			_ = tx.Rollback()
			return 0, 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return len(requeuedIDs), len(failedIDs), nil
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
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return Run{}, err
	}
	query := `update runs set status=$2, finished_at=$3, summary=$4, claimed_at=null, claimed_by='', lease_expires_at=null where id=$1`
	args := []any{id, status, now, summary}
	if workerID != "" {
		query += ` and status=$5 and claimed_by=$6`
		args = append(args, RunRunning, workerID)
	}
	query += ` returning id, workflow_id, coalesce(trigger_id,''), status, inputs, created_at, queued_at, available_at, started_at, finished_at, claimed_at, claimed_by, lease_expires_at, attempt_count, max_attempts, summary`
	row := tx.QueryRowContext(context.Background(), query, args...)
	r, err := scanRunRow(row)
	if err != nil {
		_ = tx.Rollback()
		return Run{}, mapScanErr(err)
	}
	if _, err := tx.ExecContext(context.Background(), `insert into run_events(id, run_id, type, message, created_at) values($1,$2,$3,$4,$5)`, newID("evt"), id, "run."+string(status), summary, now); err != nil {
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
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return RunStep{}, err
	}
	if _, err := tx.ExecContext(context.Background(), `insert into run_steps(id, run_id, position, name, executor, model, status, inputs, correlation_ids, created_at) values($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9::jsonb,$10)`, step.ID, step.RunID, step.Position, step.Name, step.Executor, step.Model, step.Status, string(inputs), string(correlationIDs), step.CreatedAt); err != nil {
		_ = tx.Rollback()
		return RunStep{}, mapPgErr(err)
	}
	if _, err := tx.ExecContext(context.Background(), `insert into run_events(id, run_id, run_step_id, type, message, created_at) values($1,$2,$3,$4,$5,$6)`, newID("evt"), step.RunID, step.ID, "step.created", step.Name, now); err != nil {
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
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return RunStep{}, err
	}
	row := tx.QueryRowContext(context.Background(), `update run_steps set status=$2, attempt_count=attempt_count+1, started_at=$3, finished_at=null, error='', output_summary='' where id=$1 and status=$4 returning id, run_id, position, name, executor, model, status, inputs, output_summary, error, attempt_count, correlation_ids, created_at, started_at, finished_at`, id, StepRunning, now, StepQueued)
	step, err := scanRunStepRow(row)
	if err != nil {
		_ = tx.Rollback()
		return RunStep{}, mapScanErr(err)
	}
	if _, err := tx.ExecContext(context.Background(), `insert into run_events(id, run_id, run_step_id, type, message, created_at) values($1,$2,$3,$4,$5,$6)`, newID("evt"), step.RunID, step.ID, "step.started", step.Name, now); err != nil {
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
	row := s.db.QueryRowContext(context.Background(), `update run_steps set inputs=$2::jsonb where id=$1 returning id, run_id, position, name, executor, model, status, inputs, output_summary, error, attempt_count, correlation_ids, created_at, started_at, finished_at`, id, string(inputs))
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
	encodedCorrelationIDs, err := json.Marshal(correlationIDs)
	if err != nil {
		return RunStep{}, err
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return RunStep{}, err
	}
	row := tx.QueryRowContext(context.Background(), `update run_steps set status=$2, output_summary=$3, error=$4, correlation_ids=case when $5::jsonb = '{}'::jsonb then correlation_ids else $5::jsonb end, finished_at=$6 where id=$1 and status in ($7,$8) returning id, run_id, position, name, executor, model, status, inputs, output_summary, error, attempt_count, correlation_ids, created_at, started_at, finished_at`, id, status, outputSummary, errText, string(encodedCorrelationIDs), now, StepRunning, StepQueued)
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
	if _, err := tx.ExecContext(context.Background(), `insert into run_events(id, run_id, run_step_id, type, message, created_at) values($1,$2,$3,$4,$5,$6)`, newID("evt"), step.RunID, step.ID, "step."+string(status), message, now); err != nil {
		_ = tx.Rollback()
		return RunStep{}, err
	}
	if err := tx.Commit(); err != nil {
		return RunStep{}, err
	}
	return step, nil
}

func (s *PostgresStore) RetryRunStep(id string) (RunStep, error) {
	return s.transitionRunStep(id, []RunStepStatus{StepFailed, StepCanceled, StepTimedOut}, StepQueued, "step.retry")
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
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return RunStep{}, err
	}
	row := tx.QueryRowContext(context.Background(), `update run_steps set status=$2, started_at=case when $2=$6 then null else started_at end, finished_at=$3, error=$4, output_summary=case when $2=$6 then '' else output_summary end where id=$1 and status = any($5) returning id, run_id, position, name, executor, model, status, inputs, output_summary, error, attempt_count, correlation_ids, created_at, started_at, finished_at`, id, next, finished, errorText, allowedStrings, StepQueued)
	step, err := scanRunStepRow(row)
	if err != nil {
		_ = tx.Rollback()
		return RunStep{}, mapScanErr(err)
	}
	if _, err := tx.ExecContext(context.Background(), `insert into run_events(id, run_id, run_step_id, type, message, created_at) values($1,$2,$3,$4,$5,$6)`, newID("evt"), step.RunID, step.ID, eventType, step.Name, now); err != nil {
		_ = tx.Rollback()
		return RunStep{}, err
	}
	if err := tx.Commit(); err != nil {
		return RunStep{}, err
	}
	return step, nil
}

func (s *PostgresStore) ListRunSteps(runID string) []RunStep {
	rows, err := s.db.QueryContext(context.Background(), `select id, run_id, position, name, executor, model, status, inputs, output_summary, error, attempt_count, correlation_ids, created_at, started_at, finished_at from run_steps where run_id=$1 order by position`, runID)
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
	step, err := scanRunStepRow(s.db.QueryRowContext(context.Background(), `select id, run_id, position, name, executor, model, status, inputs, output_summary, error, attempt_count, correlation_ids, created_at, started_at, finished_at from run_steps where id=$1`, id))
	return step, mapScanErr(err)
}

func (s *PostgresStore) AddEvent(runID, typ, message string) (RunEvent, error) {
	now := s.now().UTC()
	e := RunEvent{ID: newID("evt"), RunID: runID, Type: typ, Message: message, CreatedAt: now}
	_, err := s.db.ExecContext(context.Background(), `insert into run_events(id, run_id, type, message, created_at) values($1,$2,$3,$4,$5)`, e.ID, e.RunID, e.Type, e.Message, e.CreatedAt)
	return e, mapPgErr(err)
}

func (s *PostgresStore) AddArtifact(input CreateArtifactInput) (Artifact, error) {
	if input.RunID == "" || input.Name == "" || input.Kind == "" || input.URI == "" {
		return Artifact{}, ErrInvalid
	}
	now := s.now().UTC()
	artifact := Artifact{ID: newID("art"), RunID: input.RunID, Name: input.Name, Kind: input.Kind, URI: input.URI, CreatedAt: now}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return Artifact{}, err
	}
	if _, err := tx.ExecContext(context.Background(), `insert into artifacts(id, run_id, name, kind, uri, created_at) values($1,$2,$3,$4,$5,$6)`, artifact.ID, artifact.RunID, artifact.Name, artifact.Kind, artifact.URI, artifact.CreatedAt); err != nil {
		_ = tx.Rollback()
		return Artifact{}, mapPgErr(err)
	}
	if _, err := tx.ExecContext(context.Background(), `insert into run_events(id, run_id, type, message, created_at) values($1,$2,$3,$4,$5)`, newID("evt"), artifact.RunID, "artifact.created", artifact.Name+" ("+artifact.Kind+")", now); err != nil {
		_ = tx.Rollback()
		return Artifact{}, err
	}
	if err := tx.Commit(); err != nil {
		return Artifact{}, err
	}
	return artifact, nil
}

func (s *PostgresStore) ListRuns() []Run {
	rows, err := s.db.QueryContext(context.Background(), `select id, workflow_id, coalesce(trigger_id,''), status, inputs, created_at, queued_at, available_at, started_at, finished_at, claimed_at, claimed_by, lease_expires_at, attempt_count, max_attempts, summary from runs order by created_at desc, id desc`)
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

func (s *PostgresStore) RunDetail(id string) (RunDetail, error) {
	r, err := scanRunRow(s.db.QueryRowContext(context.Background(), `select id, workflow_id, coalesce(trigger_id,''), status, inputs, created_at, queued_at, available_at, started_at, finished_at, claimed_at, claimed_by, lease_expires_at, attempt_count, max_attempts, summary from runs where id=$1`, id))
	if err != nil {
		return RunDetail{}, mapScanErr(err)
	}
	detail := RunDetail{Run: r, Steps: s.ListRunSteps(id)}
	rows, err := s.db.QueryContext(context.Background(), `select id, run_id, type, message, created_at from run_events where run_id=$1 order by created_at, id`, id)
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
	rows, err = s.db.QueryContext(context.Background(), `select id, run_id, name, kind, uri, created_at from artifacts where run_id=$1 order by created_at, id`, id)
	if err != nil {
		return RunDetail{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var a Artifact
		if err := rows.Scan(&a.ID, &a.RunID, &a.Name, &a.Kind, &a.URI, &a.CreatedAt); err == nil {
			detail.Artifacts = append(detail.Artifacts, a)
		}
	}
	return detail, nil
}

func (s *PostgresStore) CreateModelBrokerRequest(input CreateModelBrokerRequestInput) (ModelBrokerRequest, error) {
	if strings.TrimSpace(input.ID) == "" || strings.TrimSpace(input.Model) == "" {
		return ModelBrokerRequest{}, ErrInvalid
	}
	body := normalizeJSONText(input.RequestBody)
	if _, err := s.db.ExecContext(context.Background(), `
		insert into model_broker_requests(id, status, model, path, client, request_body, correlation_ids, created_at, updated_at)
		values($1,$2,$3,$4,$5,$6::jsonb,'{}'::jsonb,now(),now())
		on conflict(id) do nothing`, input.ID, ModelBrokerRequestQueued, input.Model, input.Path, input.Client, body); err != nil {
		return ModelBrokerRequest{}, err
	}
	return s.ModelBrokerRequest(input.ID)
}

func (s *PostgresStore) StartModelBrokerRequest(id string) (ModelBrokerRequest, error) {
	row := s.db.QueryRowContext(context.Background(), `update model_broker_requests set status=$2, started_at=coalesce(started_at, now()), updated_at=now() where id=$1 returning `+modelBrokerRequestColumns, id, ModelBrokerRequestRunning)
	req, err := scanModelBrokerRequestRow(row)
	return req, mapScanErr(err)
}

func (s *PostgresStore) FinishModelBrokerRequest(id string, input FinishModelBrokerRequestInput) (ModelBrokerRequest, error) {
	status := strings.TrimSpace(input.Status)
	if status == "" {
		status = ModelBrokerRequestCompleted
	}
	correlation, err := json.Marshal(input.CorrelationIDs)
	if err != nil {
		return ModelBrokerRequest{}, err
	}
	response := normalizeJSONText(input.ResponseBody)
	row := s.db.QueryRowContext(context.Background(), `update model_broker_requests set status=$2, response_body=$3::jsonb, error=$4, correlation_ids=$5::jsonb, finished_at=now(), updated_at=now() where id=$1 returning `+modelBrokerRequestColumns, id, status, response, input.Error, string(correlation))
	req, err := scanModelBrokerRequestRow(row)
	return req, mapScanErr(err)
}

func (s *PostgresStore) CancelModelBrokerRequest(id string) (ModelBrokerRequest, error) {
	row := s.db.QueryRowContext(context.Background(), `update model_broker_requests set status=$2, cancel_requested_at=now(), updated_at=now() where id=$1 and status in ($3,$4,$5) returning `+modelBrokerRequestColumns, id, ModelBrokerRequestCancelRequested, ModelBrokerRequestQueued, ModelBrokerRequestRunning, ModelBrokerRequestCancelRequested)
	req, err := scanModelBrokerRequestRow(row)
	return req, mapScanErr(err)
}

func (s *PostgresStore) CancelAllModelBrokerRequests() (int, error) {
	result, err := s.db.ExecContext(context.Background(), `update model_broker_requests set status=$1, cancel_requested_at=now(), updated_at=now() where status in ($2,$3,$4)`, ModelBrokerRequestCancelRequested, ModelBrokerRequestQueued, ModelBrokerRequestRunning, ModelBrokerRequestCancelRequested)
	if err != nil {
		return 0, err
	}
	count, _ := result.RowsAffected()
	return int(count), nil
}

func (s *PostgresStore) ListModelBrokerRequests(filter ListModelBrokerRequestsFilter) ([]ModelBrokerRequest, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	args := []any{}
	where := []string{}
	if len(filter.Statuses) > 0 {
		placeholders := []string{}
		for _, status := range filter.Statuses {
			args = append(args, status)
			placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
		}
		where = append(where, "status in ("+strings.Join(placeholders, ",")+")")
	}
	if query := strings.TrimSpace(filter.Query); query != "" {
		args = append(args, "%"+strings.ToLower(query)+"%")
		where = append(where, fmt.Sprintf("(lower(id || ' ' || model || ' ' || path || ' ' || client || ' ' || request_body::text || ' ' || coalesce(response_body::text,'') || ' ' || error) like $%d)", len(args)))
	}
	query := `select ` + modelBrokerRequestColumns + ` from model_broker_requests`
	if len(where) > 0 {
		query += ` where ` + strings.Join(where, ` and `)
	}
	args = append(args, limit)
	query += fmt.Sprintf(` order by created_at desc, id desc limit $%d`, len(args))
	rows, err := s.db.QueryContext(context.Background(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ModelBrokerRequest{}
	for rows.Next() {
		req, err := scanModelBrokerRequestRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, req)
	}
	return out, rows.Err()
}

func (s *PostgresStore) ModelBrokerRequest(id string) (ModelBrokerRequest, error) {
	req, err := scanModelBrokerRequestRow(s.db.QueryRowContext(context.Background(), `select `+modelBrokerRequestColumns+` from model_broker_requests where id=$1`, id))
	return req, mapScanErr(err)
}

func normalizeJSONText(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "{}"
	}
	var value any
	if json.Unmarshal([]byte(trimmed), &value) == nil {
		return trimmed
	}
	encoded, _ := json.Marshal(map[string]string{"raw": raw})
	return string(encoded)
}

type scanner interface{ Scan(dest ...any) error }

func scanModelBrokerRequestRow(row scanner) (ModelBrokerRequest, error) {
	var req ModelBrokerRequest
	var requestBody, responseBody, correlation []byte
	var responseBodyNull sql.NullString
	var started, finished, cancelRequested sql.NullTime
	if err := row.Scan(&req.ID, &req.Status, &req.Model, &req.Path, &req.Client, &requestBody, &responseBodyNull, &req.Error, &correlation, &req.CreatedAt, &started, &finished, &req.UpdatedAt, &cancelRequested); err != nil {
		return ModelBrokerRequest{}, err
	}
	req.RequestBody = string(requestBody)
	if responseBodyNull.Valid {
		responseBody = []byte(responseBodyNull.String)
		req.ResponseBody = string(responseBody)
	}
	if started.Valid {
		req.StartedAt = &started.Time
	}
	if finished.Valid {
		req.FinishedAt = &finished.Time
	}
	if cancelRequested.Valid {
		req.CancelRequestedAt = &cancelRequested.Time
	}
	_ = json.Unmarshal(correlation, &req.CorrelationIDs)
	return req, nil
}

func scanTrigger(rows *sql.Rows) (Trigger, bool) {
	t, err := scanTriggerRow(rows)
	return t, err == nil
}

func scanTriggerRow(row scanner) (Trigger, error) {
	var t Trigger
	var inputs []byte
	var next, last, updated sql.NullTime
	if err := row.Scan(&t.ID, &t.WorkflowID, &t.Type, &t.Name, &t.Description, &t.Enabled, &t.Schedule, &inputs, &next, &last, &updated, &t.LastError); err != nil {
		return Trigger{}, err
	}
	if next.Valid {
		t.NextRunAt = &next.Time
	}
	if last.Valid {
		t.LastRunAt = &last.Time
	}
	if updated.Valid {
		t.UpdatedAt = &updated.Time
	}
	_ = json.Unmarshal(inputs, &t.Inputs)
	return t, nil
}

func scanRun(rows *sql.Rows) (Run, bool) {
	r, err := scanRunRow(rows)
	return r, err == nil
}

func scanRunRow(row scanner) (Run, error) {
	var r Run
	var inputs []byte
	var started, finished, claimed, lease sql.NullTime
	if err := row.Scan(&r.ID, &r.WorkflowID, &r.TriggerID, &r.Status, &inputs, &r.CreatedAt, &r.QueuedAt, &r.AvailableAt, &started, &finished, &claimed, &r.ClaimedBy, &lease, &r.AttemptCount, &r.MaxAttempts, &r.Summary); err != nil {
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

func mapScanErr(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func mapPgErr(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("postgres store: %w", err)
}
