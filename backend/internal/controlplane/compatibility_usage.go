package controlplane

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxCompatibilityReportDays = 90
	compatibilityReportVersion = "switchyard.compatibility-observation/v1"
)

var ErrCompatibilityCoverageIncomplete = errors.New("compatibility observation coverage incomplete")

var compatibilityBackends = []string{"llama-line", "ocq"}
var safeCompatibilityIdentity = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// CompatibilityUsageDaily is aggregate-only. Observation timestamps prove that
// a zero is measured; an absent row or an unfinished row never means zero use.
type CompatibilityUsageDaily struct {
	Date                   time.Time  `json:"date"`
	Backend                string     `json:"backend"`
	Attempts               int64      `json:"attempts"`
	FirstAttemptAt         *time.Time `json:"first_attempt_at,omitempty"`
	LastAttemptAt          *time.Time `json:"last_attempt_at,omitempty"`
	SourceRevision         string     `json:"source_revision"`
	DeploymentID           string     `json:"deployment_id"`
	ObservationStartedAt   *time.Time `json:"observation_started_at,omitempty"`
	ObservationCompletedAt *time.Time `json:"observation_completed_at,omitempty"`
	ObservationSegment     string     `json:"observation_segment"`
	ClosedAt               *time.Time `json:"-"`
}

type CompatibilityObservationIdentity struct {
	SourceRevision string `json:"source_revision"`
	DeploymentID   string `json:"deployment_id"`
	SegmentID      string `json:"segment_id,omitempty"`
}

type CompatibilityReportInput struct {
	Start       time.Time
	End         time.Time
	GeneratedAt time.Time
	Identity    CompatibilityObservationIdentity
}

type CompatibilityObservation struct {
	Date                   string `json:"date"`
	Backend                string `json:"backend"`
	Attempts               int64  `json:"attempts"`
	SourceRevision         string `json:"source_revision"`
	DeploymentID           string `json:"deployment_id"`
	ObservationStartedAt   string `json:"observation_started_at"`
	ObservationCompletedAt string `json:"observation_completed_at"`
	ObservationSegment     string `json:"observation_segment"`
}

type CompatibilityReport struct {
	Version               string                     `json:"version"`
	SourceRevision        string                     `json:"source_revision"`
	SourceIdentity        string                     `json:"source_identity"`
	SourceSegment         string                     `json:"source_segment"`
	WindowStart           string                     `json:"window_start"`
	WindowEnd             string                     `json:"window_end"`
	GeneratedAt           time.Time                  `json:"generated_at"`
	Observations          []CompatibilityObservation `json:"observations"`
	CanonicalSHA256       string                     `json:"canonical_sha256"`
	QualificationEligible bool                       `json:"qualification_eligible"`
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

type compatibilityObservationStore interface {
	RecordCompatibilityHeartbeat(context.Context, CompatibilityObservationIdentity, time.Time, time.Duration) (compatibilityHeartbeat, error)
	CompatibilityReport(context.Context, CompatibilityReportInput) (CompatibilityReport, error)
}

type compatibilityHeartbeat struct {
	ObservedAt   time.Time
	GapStartedAt time.Time
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
	for _, known := range compatibilityBackends {
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
		if row.FirstAttemptAt != nil && (entry.FirstObservedAt.IsZero() || row.FirstAttemptAt.Before(entry.FirstObservedAt)) {
			entry.FirstObservedAt = *row.FirstAttemptAt
		}
		if row.LastAttemptAt != nil && row.LastAttemptAt.After(entry.LastObservedAt) {
			entry.LastObservedAt = *row.LastAttemptAt
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
	sort.Slice(out.Backends, func(i, j int) bool { return out.Backends[i].Backend < out.Backends[j].Backend })
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
	return CompatibilityRetirementBackend{Backend: backend, ConfiguredSelector: configured, EffectiveSelector: effective, Eligibility: "observation_coverage_required"}
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
	at = at.UTC()
	day := utcDay(at)
	key := compatibilityUsageKey(day, backend)
	row := s.compatibilityUsage[key]
	if row.Attempts == 0 {
		first := at
		row.FirstAttemptAt = &first
	}
	last := at
	row.Date, row.Backend, row.Attempts, row.LastAttemptAt = day, backend, row.Attempts+1, &last
	s.compatibilityUsage[key] = row
	return nil
}

func (s *MemoryStore) RecordCompatibilityObservation(_ context.Context, day time.Time, identity CompatibilityObservationIdentity, startedAt, completedAt time.Time) error {
	identity.SourceRevision = strings.TrimSpace(identity.SourceRevision)
	identity.DeploymentID = strings.TrimSpace(identity.DeploymentID)
	if identity.SegmentID == "" {
		identity.SegmentID = identity.DeploymentID
	}
	if !validSourceRevision(identity.SourceRevision) || !validCompatibilityIdentity(identity.DeploymentID) || (identity.SegmentID != "" && !validCompatibilityIdentity(identity.SegmentID)) || startedAt.IsZero() || completedAt.Before(startedAt) {
		return ErrInvalid
	}
	day = utcDay(day)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, backend := range compatibilityBackends {
		key := compatibilityUsageKey(day, backend)
		row := s.compatibilityUsage[key]
		started, completed := startedAt.UTC(), completedAt.UTC()
		if row.SourceRevision == identity.SourceRevision && row.DeploymentID == identity.DeploymentID && row.ObservationCompletedAt != nil && !started.After(*row.ObservationCompletedAt) {
			if row.ObservationStartedAt != nil && row.ObservationStartedAt.Before(started) {
				started = *row.ObservationStartedAt
			}
			if row.ObservationCompletedAt != nil && row.ObservationCompletedAt.After(completed) {
				completed = *row.ObservationCompletedAt
			}
		}
		row.Date, row.Backend = day, backend
		row.SourceRevision, row.DeploymentID = identity.SourceRevision, identity.DeploymentID
		row.ObservationSegment = identity.SegmentID
		row.ObservationStartedAt, row.ObservationCompletedAt = &started, &completed
		s.compatibilityUsage[key] = row
	}
	return nil
}

func (s *MemoryStore) RecordCompatibilityHeartbeat(ctx context.Context, identity CompatibilityObservationIdentity, observedAt time.Time, maxGap time.Duration) (compatibilityHeartbeat, error) {
	observedAt = observedAt.UTC()
	if observedAt.IsZero() || maxGap <= 0 {
		return compatibilityHeartbeat{}, ErrInvalid
	}
	identity.SourceRevision = strings.TrimSpace(identity.SourceRevision)
	identity.DeploymentID = strings.TrimSpace(identity.DeploymentID)
	if identity.SegmentID == "" {
		identity.SegmentID = identity.DeploymentID
	}
	s.mu.RLock()
	var last, previousSegmentLast time.Time
	for _, row := range s.compatibilityUsage {
		if row.SourceRevision != identity.SourceRevision || row.DeploymentID != identity.DeploymentID || row.ObservationCompletedAt == nil {
			continue
		}
		if row.ObservationSegment == identity.SegmentID && row.ObservationCompletedAt.After(last) {
			last = *row.ObservationCompletedAt
		} else if row.ObservationSegment != identity.SegmentID && row.ObservationCompletedAt.After(previousSegmentLast) {
			previousSegmentLast = *row.ObservationCompletedAt
		}
	}
	s.mu.RUnlock()
	result := compatibilityHeartbeat{ObservedAt: observedAt}
	if last.IsZero() && !previousSegmentLast.IsZero() {
		if observedAt.Before(previousSegmentLast) || observedAt.Sub(previousSegmentLast) > maxGap {
			result.GapStartedAt = previousSegmentLast
		}
		return result, s.RecordCompatibilityObservation(ctx, observedAt, identity, observedAt, observedAt)
	}
	if last.IsZero() || observedAt.Before(last) || observedAt.Sub(last) > maxGap {
		if !last.IsZero() {
			result.GapStartedAt = last
		}
		return result, s.RecordCompatibilityObservation(ctx, observedAt, identity, observedAt, observedAt)
	}
	lastDay, day := utcDay(last), utcDay(observedAt)
	if day.Equal(lastDay) {
		return result, s.RecordCompatibilityObservation(ctx, day, identity, last, observedAt)
	}
	if !day.Equal(lastDay.AddDate(0, 0, 1)) {
		result.GapStartedAt = last
		return result, s.RecordCompatibilityObservation(ctx, day, identity, observedAt, observedAt)
	}
	if err := s.RecordCompatibilityObservation(ctx, lastDay, identity, last, day); err != nil {
		return compatibilityHeartbeat{}, err
	}
	return result, s.RecordCompatibilityObservation(ctx, day, identity, day, observedAt)
}

func (s *MemoryStore) CompatibilityRetirementStatus(_ context.Context, now time.Time) (CompatibilityRetirementStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return retirementStatus(compatibilityRows(s.compatibilityUsage), now), nil
}

func (s *MemoryStore) CompatibilityReport(_ context.Context, input CompatibilityReportInput) (CompatibilityReport, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	report, err := buildCompatibilityReport(compatibilityRows(s.compatibilityUsage), input)
	if err == nil {
		report.QualificationEligible = false
		report.CanonicalSHA256, err = compatibilityReportDigest(report)
	}
	return report, err
}

func compatibilityRows(source map[string]CompatibilityUsageDaily) []CompatibilityUsageDaily {
	rows := make([]CompatibilityUsageDaily, 0, len(source))
	for _, row := range source {
		rows = append(rows, row)
	}
	return rows
}

func compatibilityUsageKey(day time.Time, backend string) string {
	return utcDay(day).Format(time.DateOnly) + "|" + backend
}

func compatibilityDeploymentID() string {
	return strings.TrimSpace(os.Getenv("SWITCHYARD_DEPLOYMENT_ID"))
}

func validSourceRevision(revision string) bool {
	if len(revision) != 40 || revision != strings.ToLower(revision) {
		return false
	}
	_, err := hex.DecodeString(revision)
	return err == nil
}

func validCompatibilityIdentity(value string) bool {
	return safeCompatibilityIdentity.MatchString(value)
}

type CompatibilityObserverLease struct {
	conn *sql.Conn
}

func (s *PostgresStore) AcquireCompatibilityObserverLease(ctx context.Context, identity CompatibilityObservationIdentity) (*CompatibilityObserverLease, CompatibilityObservationIdentity, error) {
	identity.SourceRevision = strings.TrimSpace(identity.SourceRevision)
	identity.DeploymentID = strings.TrimSpace(identity.DeploymentID)
	if !validSourceRevision(identity.SourceRevision) || !validCompatibilityIdentity(identity.DeploymentID) {
		return nil, CompatibilityObservationIdentity{}, ErrInvalid
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, CompatibilityObservationIdentity{}, err
	}
	var acquired bool
	if err := conn.QueryRowContext(ctx, `select pg_try_advisory_lock($1,$2)`, compatibilityDayLockNamespace, int64(-1)).Scan(&acquired); err != nil || !acquired {
		_ = conn.Close()
		if err != nil {
			return nil, CompatibilityObservationIdentity{}, err
		}
		return nil, CompatibilityObservationIdentity{}, fmt.Errorf("compatibility observer lease held by another deployment: %w", ErrInvalid)
	}
	identity.SegmentID = newID("segment")
	if _, err := conn.ExecContext(ctx, `insert into compatibility_observer_fence(singleton,deployment_id,segment_id,fence) values(true,$1,$2,1) on conflict(singleton) do update set deployment_id=excluded.deployment_id,segment_id=excluded.segment_id,fence=compatibility_observer_fence.fence+1`, identity.DeploymentID, identity.SegmentID); err != nil {
		_, _ = conn.ExecContext(context.Background(), `select pg_advisory_unlock($1,$2)`, compatibilityDayLockNamespace, int64(-1))
		_ = conn.Close()
		return nil, CompatibilityObservationIdentity{}, err
	}
	return &CompatibilityObserverLease{conn: conn}, identity, nil
}

func (l *CompatibilityObserverLease) Close() error {
	if l == nil || l.conn == nil {
		return nil
	}
	_, unlockErr := l.conn.ExecContext(context.Background(), `select pg_advisory_unlock($1,$2)`, compatibilityDayLockNamespace, int64(-1))
	closeErr := l.conn.Close()
	l.conn = nil
	return errors.Join(unlockErr, closeErr)
}

func (s *PostgresStore) RecordCompatibilityUsage(ctx context.Context, backend string, at time.Time) error {
	backend = strings.TrimSpace(backend)
	if backend == "" {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err = recordCompatibilityAttemptTx(ctx, tx, backend); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

const compatibilityAttemptUpsertSQL = `insert into compatibility_usage_daily(usage_date, backend, attempts, first_attempt_at, last_attempt_at) values($1::date,$2,1,$3,$3) on conflict(usage_date, backend) do update set attempts=compatibility_usage_daily.attempts+1, first_attempt_at=least(compatibility_usage_daily.first_attempt_at, excluded.first_attempt_at), last_attempt_at=greatest(compatibility_usage_daily.last_attempt_at, excluded.last_attempt_at)`

const compatibilityDayLockNamespace = int64(0x53575944)

func lockCompatibilityDay(ctx context.Context, tx *sql.Tx, day time.Time) error {
	_, err := tx.ExecContext(ctx, `select pg_advisory_xact_lock($1,$2)`, compatibilityDayLockNamespace, utcDay(day).Unix()/86400)
	return err
}

func recordCompatibilityAttemptTx(ctx context.Context, tx *sql.Tx, backend string) (time.Time, error) {
	var clockHint time.Time
	if err := tx.QueryRowContext(ctx, `select clock_timestamp()`).Scan(&clockHint); err != nil {
		return time.Time{}, err
	}
	clockHint = clockHint.UTC()
	if err := lockCompatibilityDay(ctx, tx, clockHint); err != nil {
		return time.Time{}, err
	}
	var attemptedAt time.Time
	if err := tx.QueryRowContext(ctx, `select clock_timestamp()`).Scan(&attemptedAt); err != nil {
		return time.Time{}, err
	}
	attemptedAt = attemptedAt.UTC()
	if actualDay := utcDay(attemptedAt); actualDay.After(utcDay(clockHint)) {
		if err := lockCompatibilityDay(ctx, tx, actualDay); err != nil {
			return time.Time{}, err
		}
	}
	var closed bool
	if err := tx.QueryRowContext(ctx, `select exists(select 1 from compatibility_observation_daily where usage_date=$1::date and backend=$2 and closed_at is not null)`, attemptedAt.Format(time.DateOnly), backend).Scan(&closed); err != nil {
		return time.Time{}, err
	}
	if closed {
		return time.Time{}, ErrCompatibilityCoverageIncomplete
	}
	result, err := tx.ExecContext(ctx, compatibilityAttemptUpsertSQL, attemptedAt.Format(time.DateOnly), backend, attemptedAt)
	if err != nil {
		return time.Time{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return time.Time{}, err
	}
	if changed != 1 {
		return time.Time{}, ErrCompatibilityCoverageIncomplete
	}
	return attemptedAt, nil
}

func (s *PostgresStore) RecordCompatibilityHeartbeat(ctx context.Context, identity CompatibilityObservationIdentity, _ time.Time, maxGap time.Duration) (compatibilityHeartbeat, error) {
	identity.SourceRevision = strings.TrimSpace(identity.SourceRevision)
	identity.DeploymentID = strings.TrimSpace(identity.DeploymentID)
	if !validSourceRevision(identity.SourceRevision) || !validCompatibilityIdentity(identity.DeploymentID) || !validCompatibilityIdentity(identity.SegmentID) || maxGap <= 0 {
		return compatibilityHeartbeat{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return compatibilityHeartbeat{}, err
	}
	defer tx.Rollback()
	var clockHint time.Time
	if err := tx.QueryRowContext(ctx, `select clock_timestamp()`).Scan(&clockHint); err != nil {
		return compatibilityHeartbeat{}, err
	}
	clockHint = clockHint.UTC()
	for _, day := range []time.Time{utcDay(clockHint).AddDate(0, 0, -1), utcDay(clockHint)} {
		if err := lockCompatibilityDay(ctx, tx, day); err != nil {
			return compatibilityHeartbeat{}, err
		}
	}
	var observedAt time.Time
	if err := tx.QueryRowContext(ctx, `select clock_timestamp()`).Scan(&observedAt); err != nil {
		return compatibilityHeartbeat{}, err
	}
	observedAt = observedAt.UTC()
	if day := utcDay(observedAt); day.After(utcDay(clockHint)) {
		if err := lockCompatibilityDay(ctx, tx, day); err != nil {
			return compatibilityHeartbeat{}, err
		}
	}
	var fencedDeployment, fencedSegment string
	if err := tx.QueryRowContext(ctx, `select deployment_id,segment_id from compatibility_observer_fence where singleton for update`).Scan(&fencedDeployment, &fencedSegment); err != nil {
		return compatibilityHeartbeat{}, err
	}
	if fencedDeployment != identity.DeploymentID || fencedSegment != identity.SegmentID {
		return compatibilityHeartbeat{}, fmt.Errorf("compatibility observer lease lost: %w", ErrInvalid)
	}
	var last sql.NullTime
	if err := tx.QueryRowContext(ctx, `select max(observation_completed_at) from compatibility_observation_daily where source_revision=$1 and deployment_id=$2 and observation_segment=$3`, identity.SourceRevision, identity.DeploymentID, identity.SegmentID).Scan(&last); err != nil {
		return compatibilityHeartbeat{}, err
	}
	result := compatibilityHeartbeat{ObservedAt: observedAt}
	if !last.Valid {
		var previousSegmentLast sql.NullTime
		if err := tx.QueryRowContext(ctx, `select max(observation_completed_at) from compatibility_observation_daily where source_revision=$1 and deployment_id=$2 and observation_segment<>$3`, identity.SourceRevision, identity.DeploymentID, identity.SegmentID).Scan(&previousSegmentLast); err != nil {
			return compatibilityHeartbeat{}, err
		}
		if previousSegmentLast.Valid {
			previous := previousSegmentLast.Time.UTC()
			if observedAt.Before(previous) || observedAt.Sub(previous) > maxGap {
				result.GapStartedAt = previous
			}
			if err := recordCompatibilityObservationTx(ctx, tx, identity, observedAt, observedAt, observedAt); err != nil {
				return compatibilityHeartbeat{}, err
			}
			if err := tx.Commit(); err != nil {
				return compatibilityHeartbeat{}, err
			}
			return result, nil
		}
	}
	if !last.Valid || observedAt.Before(last.Time) || observedAt.Sub(last.Time) > maxGap {
		if last.Valid {
			result.GapStartedAt = last.Time.UTC()
		}
		if err := recordCompatibilityObservationTx(ctx, tx, identity, observedAt, observedAt, observedAt); err != nil {
			return compatibilityHeartbeat{}, err
		}
	} else {
		lastAt := last.Time.UTC()
		lastDay, day := utcDay(lastAt), utcDay(observedAt)
		switch {
		case day.Equal(lastDay):
			if err := recordCompatibilityObservationTx(ctx, tx, identity, lastAt, observedAt, observedAt); err != nil {
				return compatibilityHeartbeat{}, err
			}
		case day.Equal(lastDay.AddDate(0, 0, 1)):
			if err := recordCompatibilityObservationTx(ctx, tx, identity, lastAt, day, observedAt); err != nil {
				return compatibilityHeartbeat{}, err
			}
			if err := recordCompatibilityObservationTx(ctx, tx, identity, day, observedAt, observedAt); err != nil {
				return compatibilityHeartbeat{}, err
			}
		default:
			result.GapStartedAt = lastAt
			if err := recordCompatibilityObservationTx(ctx, tx, identity, observedAt, observedAt, observedAt); err != nil {
				return compatibilityHeartbeat{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return compatibilityHeartbeat{}, err
	}
	return result, nil
}

func recordCompatibilityObservationTx(ctx context.Context, tx *sql.Tx, identity CompatibilityObservationIdentity, startedAt, completedAt, closedAt time.Time) error {
	day := utcDay(completedAt)
	if completedAt.Equal(day) && startedAt.Before(day) {
		day = day.AddDate(0, 0, -1)
	}
	for _, backend := range compatibilityBackends {
		if _, err := tx.ExecContext(ctx, `insert into compatibility_observation_daily(usage_date,backend,source_revision,deployment_id,observation_segment,observation_started_at,observation_completed_at,closed_at) values($1::date,$2,$3,$4,$5,$6::timestamptz,$7::timestamptz,case when $7::timestamptz >= ($1::date::timestamp at time zone 'UTC') + interval '1 day' then $8::timestamptz end) on conflict(usage_date,backend) do update set source_revision=excluded.source_revision,deployment_id=excluded.deployment_id,observation_segment=excluded.observation_segment,observation_started_at=case when compatibility_observation_daily.source_revision=excluded.source_revision and compatibility_observation_daily.deployment_id=excluded.deployment_id and compatibility_observation_daily.observation_segment=excluded.observation_segment and excluded.observation_started_at <= compatibility_observation_daily.observation_completed_at then least(compatibility_observation_daily.observation_started_at,excluded.observation_started_at) else excluded.observation_started_at end,observation_completed_at=case when compatibility_observation_daily.source_revision=excluded.source_revision and compatibility_observation_daily.deployment_id=excluded.deployment_id and compatibility_observation_daily.observation_segment=excluded.observation_segment and excluded.observation_started_at <= compatibility_observation_daily.observation_completed_at then greatest(compatibility_observation_daily.observation_completed_at,excluded.observation_completed_at) else excluded.observation_completed_at end,closed_at=case when excluded.observation_completed_at >= (excluded.usage_date::timestamp at time zone 'UTC') + interval '1 day' then $8::timestamptz end where compatibility_observation_daily.closed_at is null`, day.Format(time.DateOnly), backend, identity.SourceRevision, identity.DeploymentID, identity.SegmentID, startedAt, completedAt, closedAt); err != nil {
			return err
		}
	}
	return nil
}

func (s *PostgresStore) CompatibilityRetirementStatus(ctx context.Context, now time.Time) (CompatibilityRetirementStatus, error) {
	items, err := s.compatibilityUsageRows(ctx, time.Time{}, time.Time{})
	if err != nil {
		return CompatibilityRetirementStatus{}, err
	}
	return retirementStatus(items, now), nil
}

func (s *PostgresStore) CompatibilityReport(ctx context.Context, input CompatibilityReportInput) (CompatibilityReport, error) {
	if err := validateCompatibilityReportInput(input); err != nil {
		return CompatibilityReport{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CompatibilityReport{}, err
	}
	defer tx.Rollback()
	for day := utcDay(input.Start); !day.After(utcDay(input.End)); day = day.AddDate(0, 0, 1) {
		if err := lockCompatibilityDay(ctx, tx, day); err != nil {
			return CompatibilityReport{}, err
		}
	}
	if err := tx.QueryRowContext(ctx, `select clock_timestamp()`).Scan(&input.GeneratedAt); err != nil {
		return CompatibilityReport{}, err
	}
	input.GeneratedAt = input.GeneratedAt.UTC()
	items, err := compatibilityObservationRowsQuery(ctx, tx, utcDay(input.Start), utcDay(input.End))
	if err != nil {
		return CompatibilityReport{}, err
	}
	for _, item := range items {
		if item.ClosedAt == nil {
			return CompatibilityReport{}, ErrCompatibilityCoverageIncomplete
		}
	}
	report, err := buildCompatibilityReport(items, input)
	if err != nil {
		return CompatibilityReport{}, err
	}
	report.QualificationEligible = true
	report.CanonicalSHA256, err = compatibilityReportDigest(report)
	return report, err
}

func (s *PostgresStore) compatibilityUsageRows(ctx context.Context, start, end time.Time) ([]CompatibilityUsageDaily, error) {
	return compatibilityUsageRowsQuery(ctx, s.db, start, end)
}

type compatibilityQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func compatibilityUsageRowsQuery(ctx context.Context, queryer compatibilityQueryer, start, end time.Time) ([]CompatibilityUsageDaily, error) {
	query := `select usage_date::text,backend,attempts,first_attempt_at,last_attempt_at from compatibility_usage_daily`
	args := []any{}
	if !start.IsZero() || !end.IsZero() {
		query += ` where usage_date between $1::date and $2::date`
		args = append(args, start.Format(time.DateOnly), end.Format(time.DateOnly))
	}
	query += ` order by usage_date,backend`
	rows, err := queryer.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []CompatibilityUsageDaily{}
	for rows.Next() {
		var item CompatibilityUsageDaily
		var date string
		var first, last sql.NullTime
		if err := rows.Scan(&date, &item.Backend, &item.Attempts, &first, &last); err != nil {
			return nil, err
		}
		item.Date, err = time.Parse(time.DateOnly, date)
		if err != nil {
			return nil, err
		}
		item.FirstAttemptAt = nullableTime(first)
		item.LastAttemptAt = nullableTime(last)
		items = append(items, item)
	}
	return items, rows.Err()
}

func compatibilityObservationRowsQuery(ctx context.Context, queryer compatibilityQueryer, start, end time.Time) ([]CompatibilityUsageDaily, error) {
	query := `select o.usage_date::text,o.backend,coalesce(u.attempts,0),u.first_attempt_at,u.last_attempt_at,o.source_revision,o.deployment_id,o.observation_started_at,o.observation_completed_at,o.observation_segment,o.closed_at from compatibility_observation_daily o left join compatibility_usage_daily u using(usage_date,backend)`
	args := []any{}
	if !start.IsZero() || !end.IsZero() {
		query += ` where o.usage_date between $1::date and $2::date`
		args = append(args, start.Format(time.DateOnly), end.Format(time.DateOnly))
	}
	query += ` order by o.usage_date,o.backend`
	rows, err := queryer.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []CompatibilityUsageDaily{}
	for rows.Next() {
		var item CompatibilityUsageDaily
		var date string
		var first, last, started, completed, closed sql.NullTime
		if err := rows.Scan(&date, &item.Backend, &item.Attempts, &first, &last, &item.SourceRevision, &item.DeploymentID, &started, &completed, &item.ObservationSegment, &closed); err != nil {
			return nil, err
		}
		item.Date, err = time.Parse(time.DateOnly, date)
		if err != nil {
			return nil, err
		}
		item.FirstAttemptAt = nullableTime(first)
		item.LastAttemptAt = nullableTime(last)
		item.ObservationStartedAt = nullableTime(started)
		item.ObservationCompletedAt = nullableTime(completed)
		item.ClosedAt = nullableTime(closed)
		items = append(items, item)
	}
	return items, rows.Err()
}

func nullableTime(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	value.Time = value.Time.UTC()
	return &value.Time
}

func buildCompatibilityReport(rows []CompatibilityUsageDaily, input CompatibilityReportInput) (CompatibilityReport, error) {
	start, end, generated := utcDay(input.Start), utcDay(input.End), input.GeneratedAt.UTC()
	input.Identity.SourceRevision = strings.TrimSpace(input.Identity.SourceRevision)
	input.Identity.DeploymentID = strings.TrimSpace(input.Identity.DeploymentID)
	if validateCompatibilityReportInput(input) != nil || input.GeneratedAt.IsZero() || generated.Before(end.AddDate(0, 0, 1)) {
		return CompatibilityReport{}, ErrInvalid
	}
	byKey := make(map[string]CompatibilityUsageDaily, len(rows))
	for _, row := range rows {
		byKey[compatibilityUsageKey(row.Date, row.Backend)] = row
	}
	report := CompatibilityReport{Version: compatibilityReportVersion, SourceRevision: input.Identity.SourceRevision, SourceIdentity: input.Identity.DeploymentID, WindowStart: start.Format(time.DateOnly), WindowEnd: end.Format(time.DateOnly), GeneratedAt: generated, Observations: make([]CompatibilityObservation, 0, (int(end.Sub(start)/(24*time.Hour))+1)*len(compatibilityBackends))}
	reportSegment := ""
	for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
		dayRevision, dayDeployment, daySegment := "", "", ""
		for _, backend := range compatibilityBackends {
			row, ok := byKey[compatibilityUsageKey(day, backend)]
			if !ok || row.Attempts < 0 || row.SourceRevision != input.Identity.SourceRevision || row.DeploymentID != input.Identity.DeploymentID || !validCompatibilityIdentity(row.ObservationSegment) || row.ObservationStartedAt == nil || row.ObservationCompletedAt == nil || row.ObservationStartedAt.After(day) || row.ObservationCompletedAt.Before(day.AddDate(0, 0, 1)) {
				return CompatibilityReport{}, ErrCompatibilityCoverageIncomplete
			}
			if dayRevision == "" {
				dayRevision, dayDeployment, daySegment = row.SourceRevision, row.DeploymentID, row.ObservationSegment
			} else if row.SourceRevision != dayRevision || row.DeploymentID != dayDeployment || row.ObservationSegment != daySegment {
				return CompatibilityReport{}, ErrCompatibilityCoverageIncomplete
			}
			if reportSegment == "" {
				reportSegment = row.ObservationSegment
			} else if reportSegment != row.ObservationSegment {
				return CompatibilityReport{}, ErrCompatibilityCoverageIncomplete
			}
			report.Observations = append(report.Observations, CompatibilityObservation{Date: day.Format(time.DateOnly), Backend: backend, Attempts: row.Attempts, SourceRevision: row.SourceRevision, DeploymentID: row.DeploymentID, ObservationStartedAt: row.ObservationStartedAt.UTC().Format(time.RFC3339Nano), ObservationCompletedAt: row.ObservationCompletedAt.UTC().Format(time.RFC3339Nano), ObservationSegment: row.ObservationSegment})
		}
	}
	report.SourceSegment = reportSegment
	report.CanonicalSHA256, _ = compatibilityReportDigest(report)
	return report, nil
}

func validateCompatibilityReportInput(input CompatibilityReportInput) error {
	if input.Start.IsZero() || input.End.IsZero() || !validSourceRevision(strings.TrimSpace(input.Identity.SourceRevision)) || !validCompatibilityIdentity(strings.TrimSpace(input.Identity.DeploymentID)) {
		return ErrInvalid
	}
	start, end := utcDay(input.Start), utcDay(input.End)
	if end.Before(start) || end.After(start.AddDate(0, 0, maxCompatibilityReportDays-1)) {
		return ErrInvalid
	}
	return nil
}

func compatibilityReportDigest(report CompatibilityReport) (string, error) {
	canonical, err := json.Marshal(struct {
		Version               string                     `json:"version"`
		SourceRevision        string                     `json:"source_revision"`
		SourceIdentity        string                     `json:"source_identity"`
		SourceSegment         string                     `json:"source_segment"`
		WindowStart           string                     `json:"window_start"`
		WindowEnd             string                     `json:"window_end"`
		GeneratedAt           time.Time                  `json:"generated_at"`
		Observations          []CompatibilityObservation `json:"observations"`
		QualificationEligible bool                       `json:"qualification_eligible"`
	}{report.Version, report.SourceRevision, report.SourceIdentity, report.SourceSegment, report.WindowStart, report.WindowEnd, report.GeneratedAt, report.Observations, report.QualificationEligible})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

// CompatibilityObserver owns zero-use heartbeats. It only closes a UTC day
// when it observed both sides of that boundary within the allowed heartbeat
// gap. Startup records the current instant and never backfills earlier time.
type CompatibilityObserver struct {
	store    compatibilityObservationStore
	identity CompatibilityObservationIdentity
	maxGap   time.Duration
	mu       sync.Mutex
	health   CompatibilityObserverHealth
}

type CompatibilityObserverHealth struct {
	Healthy              bool      `json:"healthy"`
	LastSuccessAt        time.Time `json:"last_success_at,omitempty"`
	LastError            string    `json:"last_error,omitempty"`
	ConsecutiveFailures  int       `json:"consecutive_failures"`
	FailureStartedAt     time.Time `json:"failure_started_at,omitempty"`
	TransientRetryErrors int       `json:"transient_retry_errors"`
	GapStartedAt         time.Time `json:"gap_started_at,omitempty"`
	LastGapStartedAt     time.Time `json:"last_gap_started_at,omitempty"`
	ObservedGaps         int       `json:"observed_gaps"`
}

func NewCompatibilityObserver(store Store, identity CompatibilityObservationIdentity, heartbeatInterval time.Duration) (*CompatibilityObserver, error) {
	observations, ok := store.(compatibilityObservationStore)
	if !ok || !validSourceRevision(strings.TrimSpace(identity.SourceRevision)) || strings.TrimSpace(identity.DeploymentID) == "" || heartbeatInterval <= 0 {
		return nil, ErrInvalid
	}
	if identity.SegmentID == "" {
		identity.SegmentID = identity.DeploymentID
	}
	if !validCompatibilityIdentity(identity.DeploymentID) || !validCompatibilityIdentity(identity.SegmentID) {
		return nil, ErrInvalid
	}
	return &CompatibilityObserver{store: observations, identity: identity, maxGap: heartbeatInterval * 3, health: CompatibilityObserverHealth{Healthy: true}}, nil
}

func (o *CompatibilityObserver) Status() CompatibilityObserverHealth {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.health
}

func (o *CompatibilityObserver) recordResult(result compatibilityHeartbeat, failedAt time.Time, err error) {
	if err != nil {
		o.health.Healthy = false
		o.health.LastError = RedactForLog(err.Error())
		o.health.ConsecutiveFailures++
		if o.health.FailureStartedAt.IsZero() {
			o.health.FailureStartedAt = failedAt.UTC()
		}
		return
	}
	o.health.LastSuccessAt = result.ObservedAt.UTC()
	o.health.LastError = ""
	o.health.ConsecutiveFailures = 0
	o.health.FailureStartedAt = time.Time{}
	if result.GapStartedAt.IsZero() {
		o.health.Healthy = true
		o.health.GapStartedAt = time.Time{}
		return
	}
	o.health.Healthy = false
	o.health.GapStartedAt = result.GapStartedAt.UTC()
	o.health.LastGapStartedAt = result.GapStartedAt.UTC()
	o.health.ObservedGaps++
}

func (o *CompatibilityObserver) Tick(ctx context.Context, at time.Time) error {
	result, err := o.store.RecordCompatibilityHeartbeat(ctx, o.identity, at.UTC(), o.maxGap)
	o.mu.Lock()
	o.recordResult(result, at, err)
	o.mu.Unlock()
	return err
}

func (o *CompatibilityObserver) Run(ctx context.Context, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case at := <-ticker.C:
			o.tickWithRetry(ctx, at, func(delay time.Duration) bool {
				select {
				case <-ctx.Done():
					return false
				case <-time.After(delay):
					return true
				}
			})
		}
	}
}

func (o *CompatibilityObserver) tickWithRetry(ctx context.Context, at time.Time, wait func(time.Duration) bool) error {
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		var result compatibilityHeartbeat
		result, err = o.store.RecordCompatibilityHeartbeat(ctx, o.identity, at.UTC(), o.maxGap)
		if err == nil {
			o.mu.Lock()
			o.recordResult(result, at, nil)
			o.mu.Unlock()
			return nil
		}
		o.mu.Lock()
		o.health.TransientRetryErrors++
		o.mu.Unlock()
		if attempt == 4 {
			break
		}
		if !wait(time.Duration(1<<attempt) * 100 * time.Millisecond) {
			break
		}
	}
	o.mu.Lock()
	o.recordResult(compatibilityHeartbeat{}, at, err)
	o.mu.Unlock()
	return err
}
