package controlplane

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

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
	ctx := s.operationContext()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorkflowDefinition{}, err
	}
	var currentVersion int
	var createdAt time.Time
	err = tx.QueryRowContext(ctx, `select version, created_at from workflow_definitions where id=$1 for update`, definition.ID).Scan(&currentVersion, &createdAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return WorkflowDefinition{}, err
	}
	definition.Version = currentVersion + 1
	if currentVersion == 0 {
		definition.Version = 1
		createdAt = s.now().UTC()
	}
	definition.CreatedAt = createdAt
	definition.UpdatedAt = s.now().UTC()
	row := tx.QueryRowContext(ctx, `
		insert into workflow_definitions(id, name, description, status, version, owns, uses, input_shape, pipeline, created_at, updated_at)
		values($1,$2,$3,$4,$5,$6::jsonb,$7::jsonb,$8::jsonb,$9::jsonb,$10,$11)
		on conflict(id) do update set name=excluded.name, description=excluded.description, status=excluded.status, version=excluded.version, owns=excluded.owns, uses=excluded.uses, input_shape=excluded.input_shape, pipeline=excluded.pipeline, updated_at=excluded.updated_at
		returning `+workflowDefinitionColumns,
		definition.ID, definition.Name, definition.Description, definition.Status, definition.Version, string(owns), string(uses), string(inputShape), string(pipeline), definition.CreatedAt, definition.UpdatedAt)
	saved, err := scanWorkflowDefinitionRow(row)
	if err != nil {
		_ = tx.Rollback()
		return WorkflowDefinition{}, mapScanErr(err)
	}
	if _, err := tx.ExecContext(ctx, `insert into workflow_definition_versions(id, version, name, description, status, owns, uses, input_shape, pipeline, created_at, updated_at) values($1,$2,$3,$4,$5,$6::jsonb,$7::jsonb,$8::jsonb,$9::jsonb,$10,$11)`, saved.ID, saved.Version, saved.Name, saved.Description, saved.Status, string(owns), string(uses), string(inputShape), string(pipeline), saved.CreatedAt, saved.UpdatedAt); err != nil {
		_ = tx.Rollback()
		return WorkflowDefinition{}, err
	}
	if err := tx.Commit(); err != nil {
		return WorkflowDefinition{}, err
	}
	return saved, nil
}

func (s *PostgresStore) WorkflowDefinition(id string) (WorkflowDefinition, error) {
	row := s.db.QueryRowContext(s.operationContext(), `select `+workflowDefinitionColumns+` from workflow_definitions where id=$1`, id)
	definition, err := scanWorkflowDefinitionRow(row)
	return definition, mapScanErr(err)
}

func (s *PostgresStore) ListWorkflowDefinitions() ([]WorkflowDefinition, error) {
	rows, err := s.db.QueryContext(s.operationContext(), `select `+workflowDefinitionColumns+` from workflow_definitions order by id`)
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

func (s *PostgresStore) ListWorkflowDefinitionVersions(id string) ([]WorkflowDefinition, error) {
	rows, err := s.db.QueryContext(s.operationContext(), `select `+workflowDefinitionColumns+` from workflow_definition_versions where id=$1 order by version desc`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WorkflowDefinition{}
	for rows.Next() {
		definition, err := scanWorkflowDefinitionRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, definition)
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	return out, rows.Err()
}

func (s *PostgresStore) ActivateWorkflowDefinition(id string) (WorkflowDefinition, error) {
	ctx := s.operationContext()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorkflowDefinition{}, err
	}
	row := tx.QueryRowContext(ctx, `update workflow_definitions set status='active', updated_at=now() where id=$1 returning `+workflowDefinitionColumns, id)
	definition, err := scanWorkflowDefinitionRow(row)
	if err != nil {
		_ = tx.Rollback()
		return WorkflowDefinition{}, mapScanErr(err)
	}
	if _, err := tx.ExecContext(ctx, `update workflow_definition_versions set status='active', updated_at=$3 where id=$1 and version=$2`, id, definition.Version, definition.UpdatedAt); err != nil {
		_ = tx.Rollback()
		return WorkflowDefinition{}, err
	}
	if err := tx.Commit(); err != nil {
		return WorkflowDefinition{}, err
	}
	return definition, nil
}

func (s *PostgresStore) RollbackWorkflowDefinition(id string, version int) (WorkflowDefinition, error) {
	if version < 1 {
		return WorkflowDefinition{}, ErrInvalid
	}
	target, err := scanWorkflowDefinitionRow(s.db.QueryRowContext(s.operationContext(), `select `+workflowDefinitionColumns+` from workflow_definition_versions where id=$1 and version=$2`, id, version))
	if err != nil {
		return WorkflowDefinition{}, mapScanErr(err)
	}
	target.Status = "active"
	return s.UpsertWorkflowDefinition(target)
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
	_, err = s.db.ExecContext(s.operationContext(), `
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
	_, err = s.db.ExecContext(s.operationContext(), `
		insert into triggers(id, workflow_id, type, name, description, enabled, schedule, inputs, next_run_at, last_run_at, updated_at, last_error)
		values($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9,$10,$11,$12)
		on conflict(id) do update set workflow_id=excluded.workflow_id, type=excluded.type, name=excluded.name, description=excluded.description, schedule=excluded.schedule, inputs=excluded.inputs, updated_at=excluded.updated_at`,
		t.ID, t.WorkflowID, t.Type, t.Name, t.Description, t.Enabled, t.Schedule, string(inputs), t.NextRunAt, t.LastRunAt, now, t.LastError)
	return mapPgErr(err)
}

func (s *PostgresStore) ListWorkflows() []Workflow {
	rows, err := s.db.QueryContext(s.operationContext(), `select id, kind, name, description, owns, uses, status from workflows order by id`)
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
	if err := row.Scan(&definition.ID, &definition.Name, &definition.Description, &definition.Status, &definition.Version, &ownsBytes, &usesBytes, &inputShapeBytes, &pipelineBytes, &definition.CreatedAt, &definition.UpdatedAt); err != nil {
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
	rows, err := s.db.QueryContext(s.operationContext(), `select id, workflow_id, type, name, description, enabled, schedule, inputs, next_run_at, last_run_at, updated_at, last_error from triggers order by id`)
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

func (s *PostgresStore) Trigger(id string) (Trigger, bool) {
	row := s.db.QueryRowContext(s.operationContext(), `select id, workflow_id, type, name, description, enabled, schedule, inputs, next_run_at, last_run_at, updated_at, last_error from triggers where id=$1`, id)
	t, err := scanTriggerRow(row)
	return t, err == nil
}

func (s *PostgresStore) SetTriggerEnabled(id string, enabled bool) (Trigger, error) {
	now := s.now().UTC()
	row := s.db.QueryRowContext(s.operationContext(), `update triggers set enabled=$2, updated_at=$3 where id=$1 returning id, workflow_id, type, name, description, enabled, schedule, inputs, next_run_at, last_run_at, updated_at, last_error`, id, enabled, now)
	t, err := scanTriggerRow(row)
	return t, mapScanErr(err)
}

func (s *PostgresStore) UpdateTriggerScheduleState(id string, lastRunAt *time.Time, nextRunAt *time.Time, lastError string) (Trigger, error) {
	now := s.now().UTC()
	row := s.db.QueryRowContext(s.operationContext(), `update triggers set last_run_at=coalesce($2,last_run_at), next_run_at=coalesce($3,next_run_at), last_error=$4, updated_at=$5 where id=$1 returning id, workflow_id, type, name, description, enabled, schedule, inputs, next_run_at, last_run_at, updated_at, last_error`, id, lastRunAt, nextRunAt, lastError, now)
	t, err := scanTriggerRow(row)
	return t, mapScanErr(err)
}
