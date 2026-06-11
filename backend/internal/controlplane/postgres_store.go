package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type PostgresStore struct {
	db  *sql.DB
	now func() time.Time
}

func NewPostgresStore(db *sql.DB, now func() time.Time) *PostgresStore {
	if now == nil {
		now = time.Now
	}
	return &PostgresStore{db: db, now: now}
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
	_, err = s.db.ExecContext(context.Background(), `
		insert into triggers(id, workflow_id, type, name, description, enabled, schedule, inputs, next_run_at)
		values($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9)
		on conflict(id) do update set workflow_id=excluded.workflow_id, type=excluded.type, name=excluded.name, description=excluded.description, enabled=excluded.enabled, schedule=excluded.schedule, inputs=excluded.inputs, next_run_at=excluded.next_run_at`,
		t.ID, t.WorkflowID, t.Type, t.Name, t.Description, t.Enabled, t.Schedule, string(inputs), t.NextRunAt)
	return mapPgErr(err)
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

func (s *PostgresStore) ListTriggers() []Trigger {
	rows, err := s.db.QueryContext(context.Background(), `select id, workflow_id, type, name, description, enabled, schedule, inputs, next_run_at from triggers order by id`)
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

func (s *PostgresStore) Trigger(id string) (Trigger, bool) {
	row := s.db.QueryRowContext(context.Background(), `select id, workflow_id, type, name, description, enabled, schedule, inputs, next_run_at from triggers where id=$1`, id)
	t, err := scanTriggerRow(row)
	return t, err == nil
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
	r := Run{ID: newID("run"), WorkflowID: input.WorkflowID, TriggerID: input.TriggerID, Status: RunQueued, Inputs: cloneMap(input.Inputs), CreatedAt: now}
	triggerID := sql.NullString{String: input.TriggerID, Valid: input.TriggerID != ""}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return Run{}, err
	}
	if _, err := tx.ExecContext(context.Background(), `insert into runs(id, workflow_id, trigger_id, status, inputs, created_at, summary) values($1,$2,$3,$4,$5::jsonb,$6,'')`, r.ID, r.WorkflowID, triggerID, r.Status, string(inputs), r.CreatedAt); err != nil {
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
	row := tx.QueryRowContext(context.Background(), `update runs set status=$2, started_at=$3 where id=$1 returning id, workflow_id, coalesce(trigger_id,''), status, inputs, created_at, started_at, finished_at, summary`, id, RunRunning, now)
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

func (s *PostgresStore) FinishRun(id string, status RunStatus, summary string) (Run, error) {
	now := s.now().UTC()
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return Run{}, err
	}
	row := tx.QueryRowContext(context.Background(), `update runs set status=$2, finished_at=$3, summary=$4 where id=$1 returning id, workflow_id, coalesce(trigger_id,''), status, inputs, created_at, started_at, finished_at, summary`, id, status, now, summary)
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

func (s *PostgresStore) AddEvent(runID, typ, message string) (RunEvent, error) {
	now := s.now().UTC()
	e := RunEvent{ID: newID("evt"), RunID: runID, Type: typ, Message: message, CreatedAt: now}
	_, err := s.db.ExecContext(context.Background(), `insert into run_events(id, run_id, type, message, created_at) values($1,$2,$3,$4,$5)`, e.ID, e.RunID, e.Type, e.Message, e.CreatedAt)
	return e, mapPgErr(err)
}

func (s *PostgresStore) ListRuns() []Run {
	rows, err := s.db.QueryContext(context.Background(), `select id, workflow_id, coalesce(trigger_id,''), status, inputs, created_at, started_at, finished_at, summary from runs order by created_at desc, id desc`)
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
	r, err := scanRunRow(s.db.QueryRowContext(context.Background(), `select id, workflow_id, coalesce(trigger_id,''), status, inputs, created_at, started_at, finished_at, summary from runs where id=$1`, id))
	if err != nil {
		return RunDetail{}, mapScanErr(err)
	}
	detail := RunDetail{Run: r}
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

type scanner interface{ Scan(dest ...any) error }

func scanTrigger(rows *sql.Rows) (Trigger, bool) {
	t, err := scanTriggerRow(rows)
	return t, err == nil
}

func scanTriggerRow(row scanner) (Trigger, error) {
	var t Trigger
	var inputs []byte
	var next sql.NullTime
	if err := row.Scan(&t.ID, &t.WorkflowID, &t.Type, &t.Name, &t.Description, &t.Enabled, &t.Schedule, &inputs, &next); err != nil {
		return Trigger{}, err
	}
	if next.Valid {
		t.NextRunAt = &next.Time
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
	var started, finished sql.NullTime
	if err := row.Scan(&r.ID, &r.WorkflowID, &r.TriggerID, &r.Status, &inputs, &r.CreatedAt, &started, &finished, &r.Summary); err != nil {
		return Run{}, err
	}
	if started.Valid {
		r.StartedAt = &started.Time
	}
	if finished.Valid {
		r.FinishedAt = &finished.Time
	}
	_ = json.Unmarshal(inputs, &r.Inputs)
	return r, nil
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
