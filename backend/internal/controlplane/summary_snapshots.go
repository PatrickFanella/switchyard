package controlplane

import (
	"context"
	"sort"
	"time"
)

// OperatorRunSnapshot is a bounded projection: aggregates are calculated in
// storage and only the latest rows needed by the UI are returned.
type OperatorRunSnapshot struct {
	Queued, Running, Failed24H, Succeeded24H int
	Recent                                   []Run
}

type operatorSummaryStore interface {
	OperatorRunSnapshot(context.Context, time.Time, int) (OperatorRunSnapshot, error)
}

func operatorRunSnapshot(ctx context.Context, store Store, now time.Time, limit int) (OperatorRunSnapshot, error) {
	if source, ok := store.(operatorSummaryStore); ok {
		return source.OperatorRunSnapshot(ctx, now, limit)
	}
	runs := store.ListRuns()
	snapshot := OperatorRunSnapshot{Recent: []Run{}}
	for _, run := range runs {
		accumulateOperatorRun(&snapshot, run, now)
		if len(snapshot.Recent) < limit {
			snapshot.Recent = append(snapshot.Recent, run)
		}
	}
	return snapshot, nil
}

func accumulateOperatorRun(snapshot *OperatorRunSnapshot, run Run, now time.Time) {
	switch run.Status {
	case RunQueued:
		snapshot.Queued++
	case RunRunning:
		snapshot.Running++
	}
	reference := run.CreatedAt
	if run.FinishedAt != nil {
		reference = *run.FinishedAt
	}
	if !reference.Before(now.Add(-24 * time.Hour)) {
		switch run.Status {
		case RunFailed, RunTimedOut:
			snapshot.Failed24H++
		case RunSucceeded:
			snapshot.Succeeded24H++
		}
	}
}

func (s *MemoryStore) OperatorRunSnapshot(_ context.Context, now time.Time, limit int) (OperatorRunSnapshot, error) {
	if limit < 1 {
		limit = 10
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	all := make([]Run, 0, len(s.runs))
	for _, run := range s.runs {
		all = append(all, run)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].CreatedAt.After(all[j].CreatedAt) })
	snapshot := OperatorRunSnapshot{Recent: []Run{}}
	for _, run := range all {
		accumulateOperatorRun(&snapshot, run, now)
		if len(snapshot.Recent) < limit {
			snapshot.Recent = append(snapshot.Recent, run)
		}
	}
	return snapshot, nil
}

func (s *PostgresStore) OperatorRunSnapshot(ctx context.Context, now time.Time, limit int) (OperatorRunSnapshot, error) {
	if limit < 1 || limit > 100 {
		limit = 10
	}
	snapshot := OperatorRunSnapshot{Recent: []Run{}}
	if err := s.db.QueryRowContext(ctx, `select count(*) filter(where status=$1), count(*) filter(where status=$2), count(*) filter(where status in ($3,$4) and coalesce(finished_at,created_at) >= $5), count(*) filter(where status=$6 and coalesce(finished_at,created_at) >= $5) from runs`, RunQueued, RunRunning, RunFailed, RunTimedOut, now.UTC().Add(-24*time.Hour), RunSucceeded).Scan(&snapshot.Queued, &snapshot.Running, &snapshot.Failed24H, &snapshot.Succeeded24H); err != nil {
		return OperatorRunSnapshot{}, err
	}
	rows, err := s.db.QueryContext(ctx, `select id, workflow_id, coalesce(trigger_id,''), status, inputs, created_at, queued_at, available_at, started_at, finished_at, claimed_at, claimed_by, lease_expires_at, attempt_count, max_attempts, summary, pipeline_id, pipeline_version, pipeline_snapshot from runs order by created_at desc, id desc limit $1`, limit)
	if err != nil {
		return OperatorRunSnapshot{}, err
	}
	defer rows.Close()
	for rows.Next() {
		run, err := scanRunRow(rows)
		if err != nil {
			return OperatorRunSnapshot{}, err
		}
		snapshot.Recent = append(snapshot.Recent, run)
	}
	return snapshot, rows.Err()
}
