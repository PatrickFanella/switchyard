package controlplane

import (
	"context"
	"os"
	"sort"
	"strings"
	"time"
)

// CompatibilityUsageDaily is deliberately aggregate-only so retention of runs
// and broker ledgers cannot hide whether a deprecated route remains in use.
type CompatibilityUsageDaily struct {
	Date           time.Time `json:"date"`
	Backend        string    `json:"backend"`
	Attempts       int64     `json:"attempts"`
	FirstAttemptAt time.Time `json:"first_attempt_at"`
	LastAttemptAt  time.Time `json:"last_attempt_at"`
}

type CompatibilityRetirementStatus struct {
	Backends []CompatibilityRetirementBackend `json:"backends"`
}

type CompatibilityRetirementBackend struct {
	Backend            string    `json:"backend"`
	Attempts90D        int64     `json:"attempts_90d"`
	FirstObservedAt    time.Time `json:"first_observed_at,omitempty"`
	LastObservedAt     time.Time `json:"last_observed_at,omitempty"`
	ConfiguredSelector string    `json:"configured_selector"`
	EffectiveSelector  string    `json:"effective_selector"`
	Eligibility        string    `json:"eligibility"`
}

type compatibilityUsageStore interface {
	RecordCompatibilityUsage(context.Context, string, time.Time) error
	CompatibilityRetirementStatus(context.Context, time.Time) (CompatibilityRetirementStatus, error)
}

func recordCompatibilityUsage(store Store, decision BackendDecision) error {
	if decision.DeprecatedBackend == "" {
		return nil
	}
	usage, ok := store.(compatibilityUsageStore)
	if !ok {
		return nil
	}
	return usage.RecordCompatibilityUsage(context.Background(), decision.DeprecatedBackend, time.Now().UTC())
}

func compatibilityRetirementStatus(store Store, now time.Time) (CompatibilityRetirementStatus, error) {
	usage, ok := store.(compatibilityUsageStore)
	if !ok {
		return CompatibilityRetirementStatus{Backends: []CompatibilityRetirementBackend{}}, nil
	}
	return usage.CompatibilityRetirementStatus(context.Background(), now)
}

func retirementStatus(rows []CompatibilityUsageDaily, now time.Time) CompatibilityRetirementStatus {
	now = now.UTC()
	cutoff := now.AddDate(0, 0, -90)
	byBackend := map[string]CompatibilityRetirementBackend{}
	for _, known := range []string{"llama-line", "ocq"} {
		byBackend[known] = compatibilityRetirementBackend(known)
	}
	for _, row := range rows {
		entry := byBackend[row.Backend]
		if entry.Backend == "" {
			entry = compatibilityRetirementBackend(row.Backend)
		}
		if !row.Date.Before(cutoff) {
			entry.Attempts90D += row.Attempts
		}
		if entry.FirstObservedAt.IsZero() || row.FirstAttemptAt.Before(entry.FirstObservedAt) {
			entry.FirstObservedAt = row.FirstAttemptAt
		}
		if row.LastAttemptAt.After(entry.LastObservedAt) {
			entry.LastObservedAt = row.LastAttemptAt
		}
		byBackend[row.Backend] = entry
	}
	out := CompatibilityRetirementStatus{Backends: make([]CompatibilityRetirementBackend, 0, len(byBackend))}
	for _, entry := range byBackend {
		if entry.Attempts90D > 0 {
			entry.Eligibility = "in_use"
		}
		out.Backends = append(out.Backends, entry)
	}
	sort.Slice(out.Backends, func(i, j int) bool { return strings.Compare(out.Backends[i].Backend, out.Backends[j].Backend) < 0 })
	return out
}

func compatibilityRetirementBackend(backend string) CompatibilityRetirementBackend {
	configured, effective := "", "native"
	switch backend {
	case "llama-line":
		configured = strings.TrimSpace(os.Getenv("MODEL_BROKER_BACKEND"))
		effective = effectiveModelBrokerSelector(configured)
	case "ocq":
		configured = strings.TrimSpace(os.Getenv("OPENCODE_EXECUTOR_BACKEND"))
		effective = effectiveOpenCodeExecutorSelector(configured)
	}
	return CompatibilityRetirementBackend{Backend: backend, ConfiguredSelector: configured, EffectiveSelector: effective, Eligibility: "eligible_pending_two_release_evidence"}
}

func (s *MemoryStore) RecordCompatibilityUsage(_ context.Context, backend string, at time.Time) error {
	backend = strings.TrimSpace(backend)
	if backend == "" {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recordCompatibilityUsageLocked(backend, at)
}

func (s *MemoryStore) recordCompatibilityUsageLocked(backend string, at time.Time) error {
	day := time.Date(at.UTC().Year(), at.UTC().Month(), at.UTC().Day(), 0, 0, 0, 0, time.UTC)
	key := day.Format("2006-01-02") + "|" + backend
	row := s.compatibilityUsage[key]
	if row.Attempts == 0 {
		row.FirstAttemptAt = at.UTC()
	}
	row.Date, row.Backend, row.Attempts, row.LastAttemptAt = day, backend, row.Attempts+1, at.UTC()
	s.compatibilityUsage[key] = row
	return nil
}

func (s *MemoryStore) CompatibilityRetirementStatus(_ context.Context, now time.Time) (CompatibilityRetirementStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows := make([]CompatibilityUsageDaily, 0, len(s.compatibilityUsage))
	for _, row := range s.compatibilityUsage {
		rows = append(rows, row)
	}
	return retirementStatus(rows, now), nil
}

func (s *PostgresStore) RecordCompatibilityUsage(ctx context.Context, backend string, at time.Time) error {
	backend = strings.TrimSpace(backend)
	if backend == "" {
		return ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, `insert into compatibility_usage_daily(usage_date, backend, attempts, first_attempt_at, last_attempt_at) values($1,$2,1,$3,$3) on conflict(usage_date, backend) do update set attempts=compatibility_usage_daily.attempts+1, first_attempt_at=least(compatibility_usage_daily.first_attempt_at, excluded.first_attempt_at), last_attempt_at=greatest(compatibility_usage_daily.last_attempt_at, excluded.last_attempt_at)`, at.UTC().Truncate(24*time.Hour), backend, at.UTC())
	return err
}

func (s *PostgresStore) CompatibilityRetirementStatus(ctx context.Context, now time.Time) (CompatibilityRetirementStatus, error) {
	rows, err := s.db.QueryContext(ctx, `select usage_date::timestamptz, backend, attempts, first_attempt_at, last_attempt_at from compatibility_usage_daily order by backend, usage_date`)
	if err != nil {
		return CompatibilityRetirementStatus{}, err
	}
	defer rows.Close()
	items := []CompatibilityUsageDaily{}
	for rows.Next() {
		var item CompatibilityUsageDaily
		if err := rows.Scan(&item.Date, &item.Backend, &item.Attempts, &item.FirstAttemptAt, &item.LastAttemptAt); err != nil {
			return CompatibilityRetirementStatus{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return CompatibilityRetirementStatus{}, err
	}
	return retirementStatus(items, now), nil
}
