package controlplane

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

const overseerLockID int64 = 7031270040

type overseerPersistence interface {
	load(context.Context) (OpenPilotOverseerState, error)
	save(context.Context, *OpenPilotOverseerState) error
	cycle(context.Context, func(context.Context) error) error
}

type memoryOverseerPersistence struct {
	mu      sync.Mutex
	cycleMu sync.Mutex
	state   OpenPilotOverseerState
}

func cloneOverseerState(state OpenPilotOverseerState) OpenPilotOverseerState {
	data, _ := json.Marshal(state)
	var copy OpenPilotOverseerState
	_ = json.Unmarshal(data, &copy)
	return copy
}

func (s *memoryOverseerPersistence) load(ctx context.Context) (OpenPilotOverseerState, error) {
	if err := ctx.Err(); err != nil {
		return OpenPilotOverseerState{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneOverseerState(s.state), nil
}

func (s *memoryOverseerPersistence) save(ctx context.Context, state *OpenPilotOverseerState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if state.Revision != s.state.Revision {
		return ErrConflict
	}
	state.Revision++
	s.state = cloneOverseerState(*state)
	return nil
}

func (s *memoryOverseerPersistence) cycle(ctx context.Context, fn func(context.Context) error) error {
	if !s.cycleMu.TryLock() {
		return ErrConflict
	}
	defer s.cycleMu.Unlock()
	return fn(ctx)
}

type postgresOverseerPersistence struct{ db *sql.DB }

func (s *postgresOverseerPersistence) load(ctx context.Context) (OpenPilotOverseerState, error) {
	var data []byte
	var state OpenPilotOverseerState
	if err := s.db.QueryRowContext(ctx, `select revision,state from open_pilot_overseer where singleton`).Scan(&state.Revision, &data); err != nil {
		return state, err
	}
	revision := state.Revision
	if err := json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	state.Revision = revision
	return state, nil
}

func (s *postgresOverseerPersistence) save(ctx context.Context, state *OpenPilotOverseerState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `update open_pilot_overseer set revision=revision+1,state=$1,updated_at=clock_timestamp() where singleton and revision=$2`, data, state.Revision)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	state.Revision++
	return nil
}

func (s *postgresOverseerPersistence) cycle(ctx context.Context, fn func(context.Context) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var acquired bool
	if err = conn.QueryRowContext(ctx, `select pg_try_advisory_lock($1)`, overseerLockID).Scan(&acquired); err != nil {
		return err
	}
	if !acquired {
		return ErrConflict
	}
	defer func() {
		// A bounded context independent of a canceled cycle releases only our lock.
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		if _, err := conn.ExecContext(unlockCtx, `select pg_advisory_unlock($1)`, overseerLockID); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	cycleCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Verify the exact lock before every side effect, including ledger saves.
	cycleCtx = context.WithValue(cycleCtx, overseerGuardKey{}, func() error {
		var held bool
		err := conn.QueryRowContext(cycleCtx, `select exists(select 1 from pg_locks where locktype='advisory' and pid=pg_backend_pid() and classid=$1 and objid=$2 and objsubid=1 and granted)`, overseerLockID>>32, overseerLockID&0xffffffff).Scan(&held)
		if err != nil || !held {
			cancel()
			return fmt.Errorf("overseer ownership lost: %w", ErrConflict)
		}
		return nil
	})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-cycleCtx.Done():
				return
			case <-ticker.C:
				probeCtx, stopProbe := context.WithTimeout(cycleCtx, time.Second)
				var held bool
				err := conn.QueryRowContext(probeCtx, `select exists(select 1 from pg_locks where locktype='advisory' and pid=pg_backend_pid() and classid=$1 and objid=$2 and objsubid=1 and granted)`, overseerLockID>>32, overseerLockID&0xffffffff).Scan(&held)
				stopProbe()
				if err != nil || !held {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-stopped }()
	return fn(cycleCtx)
}

type overseerGuardKey struct{}

func guardOverseerCycle(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if guard, ok := ctx.Value(overseerGuardKey{}).(func() error); ok {
		return guard()
	}
	return nil
}

func newOverseerPersistence(store Store) overseerPersistence {
	switch s := store.(type) {
	case *PostgresStore:
		return &postgresOverseerPersistence{db: s.db}
	case *MemoryStore:
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.overseerPersistence == nil {
			s.overseerPersistence = &memoryOverseerPersistence{state: defaultOverseerState()}
		}
		return s.overseerPersistence
	default:
		return &memoryOverseerPersistence{state: defaultOverseerState()}
	}
}

func (s *MemoryStore) ListOpenPilotOverseerIssueStates(ctx context.Context) ([]OpenPilotIssueState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.openPilotIssueStates) > 10000 {
		return nil, fmt.Errorf("overseer lifecycle scan exceeds 10000 states")
	}
	out := make([]OpenPilotIssueState, 0, len(s.openPilotIssueStates))
	for _, state := range s.openPilotIssueStates {
		out = append(out, cloneOpenPilotIssueState(state))
	}
	return out, nil
}

func (s *PostgresStore) ListOpenPilotOverseerIssueStates(ctx context.Context) ([]OpenPilotIssueState, error) {
	rows, err := s.db.QueryContext(ctx, `select `+openPilotIssueStateColumns+` from open_pilot_issue_states order by repo,issue,id limit 10001`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OpenPilotIssueState{}
	for rows.Next() {
		state, err := scanOpenPilotIssueStateRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, state)
		if len(out) > 10000 {
			return nil, fmt.Errorf("overseer lifecycle scan exceeds 10000 states")
		}
	}
	return out, rows.Err()
}

func (s *MemoryStore) RequeueOpenPilotOverseerRun(ctx context.Context, expected OpenPilotIssueState, attempt int, at time.Time) (Run, error) {
	if err := ctx.Err(); err != nil {
		return Run{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.openPilotIssueStates[openPilotIssueStateKey(expected.Repo, expected.Issue)]
	if !ok || current.RunID != expected.RunID || !current.UpdatedAt.Equal(expected.UpdatedAt) || current.Phase != OpenPilotPhaseFailed || current.RepairAttempts >= current.MaxRepairAttempts {
		return Run{}, ErrConflict
	}
	run, ok := s.runs[current.RunID]
	if !ok || run.WorkflowID != string(WorkflowOpenPilot) || run.AttemptCount != attempt || attempt >= run.MaxAttempts || (run.Status != RunFailed && run.Status != RunTimedOut) {
		return Run{}, ErrConflict
	}
	for _, other := range s.runs {
		if other.ID != run.ID && other.WorkflowID == string(WorkflowOpenPilot) && (other.Status == RunQueued || other.Status == RunRunning) && strings.EqualFold(other.Inputs["repo"], firstNonEmpty(current.CanonicalRepo, current.Repo)) && other.Inputs["issue"] == firstNonEmpty(current.CanonicalIssue, current.Issue) {
			return Run{}, ErrConflict
		}
	}
	run.Status = RunQueued
	run.AvailableAt = at.UTC()
	run.FinishedAt = nil
	run.ClaimedAt = nil
	run.ClaimedBy = ""
	run.LeaseExpiresAt = nil
	run.Summary = "Native overseer recovered confirmed transient failure within existing attempt budget"
	s.runs[run.ID] = run
	s.events[run.ID] = append(s.events[run.ID], RunEvent{ID: newID("evt"), RunID: run.ID, Type: "open-pilot.overseer.requeued", Message: run.Summary, CreatedAt: at.UTC()})
	return run, nil
}

func (s *PostgresStore) RequeueOpenPilotOverseerRun(ctx context.Context, expected OpenPilotIssueState, attempt int, at time.Time) (Run, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback()
	current, err := scanOpenPilotIssueStateRow(tx.QueryRowContext(ctx, `select `+openPilotIssueStateColumns+` from open_pilot_issue_states where repo=$1 and issue=$2 for update`, expected.Repo, expected.Issue))
	if err != nil {
		return Run{}, err
	}
	if current.RunID != expected.RunID || !current.UpdatedAt.Equal(expected.UpdatedAt) || current.Phase != OpenPilotPhaseFailed || current.RepairAttempts >= current.MaxRepairAttempts {
		return Run{}, ErrConflict
	}
	var active bool
	if err := tx.QueryRowContext(ctx, `select exists(select 1 from runs where id<>$1 and workflow_id=$2 and status in ($3,$4) and lower(inputs->>'repo')=lower($5) and inputs->>'issue'=$6)`, current.RunID, string(WorkflowOpenPilot), RunQueued, RunRunning, firstNonEmpty(current.CanonicalRepo, current.Repo), firstNonEmpty(current.CanonicalIssue, current.Issue)).Scan(&active); err != nil {
		return Run{}, err
	}
	if active {
		return Run{}, ErrConflict
	}
	summary := "Native overseer recovered confirmed transient failure within existing attempt budget"
	run, err := scanRunRow(tx.QueryRowContext(ctx, `update runs set status=$2,available_at=$3,finished_at=null,claimed_at=null,claimed_by='',lease_expires_at=null,summary=$4 where id=$1 and workflow_id=$5 and attempt_count=$6 and attempt_count<max_attempts and status in ($7,$8) returning `+postgresRunColumns, current.RunID, RunQueued, at.UTC(), summary, string(WorkflowOpenPilot), attempt, RunFailed, RunTimedOut))
	if err != nil {
		return Run{}, ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `insert into run_events(id,run_id,type,message,created_at) values($1,$2,$3,$4,$5)`, newID("evt"), run.ID, "open-pilot.overseer.requeued", summary, at.UTC()); err != nil {
		return Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, err
	}
	return run, nil
}
