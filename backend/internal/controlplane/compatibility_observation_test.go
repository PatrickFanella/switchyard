package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCompatibilityReportRequiresCompleteExactCoverage(t *testing.T) {
	store := NewStore(nil)
	identity := CompatibilityObservationIdentity{SourceRevision: "0123456789abcdef0123456789abcdef01234567", DeploymentID: "production-a"}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for offset := 0; offset < 90; offset++ {
		day := start.AddDate(0, 0, offset)
		if err := store.RecordCompatibilityObservation(context.Background(), day, identity, day, day.AddDate(0, 0, 1)); err != nil {
			t.Fatal(err)
		}
	}
	attemptAt := start.AddDate(0, 0, 9).Add(time.Hour)
	if err := store.RecordCompatibilityUsage(context.Background(), "ocq", attemptAt); err != nil {
		t.Fatal(err)
	}
	input := CompatibilityReportInput{Start: start.In(time.FixedZone("west", -7*60*60)), End: start.AddDate(0, 0, 89), GeneratedAt: start.AddDate(0, 0, 90), Identity: identity}
	report, err := store.CompatibilityReport(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Observations) != 180 || report.Observations[19].Backend != "ocq" || report.Observations[19].Attempts != 1 {
		t.Fatalf("observations = %+v", report.Observations)
	}
	if report.QualificationEligible {
		t.Fatal("memory report must remain diagnostic")
	}
	again, err := store.CompatibilityReport(context.Background(), input)
	if err != nil || report.CanonicalSHA256 != again.CanonicalSHA256 {
		t.Fatalf("digest changed: %q %q err=%v", report.CanonicalSHA256, again.CanonicalSHA256, err)
	}
	ocqKey := compatibilityUsageKey(start, "ocq")
	ocq := store.compatibilityUsage[ocqKey]
	ocq.DeploymentID = "different-deployment"
	store.compatibilityUsage[ocqKey] = ocq
	if _, err := store.CompatibilityReport(context.Background(), input); err != ErrCompatibilityCoverageIncomplete {
		t.Fatalf("mismatched daily identities error = %v", err)
	}
	ocq.DeploymentID = identity.DeploymentID
	store.compatibilityUsage[ocqKey] = ocq
	ocq.SourceRevision = "abcdef0123456789abcdef0123456789abcdef01"
	store.compatibilityUsage[ocqKey] = ocq
	if _, err := store.CompatibilityReport(context.Background(), input); err != ErrCompatibilityCoverageIncomplete {
		t.Fatalf("mismatched report revision error = %v", err)
	}
	ocq.SourceRevision = identity.SourceRevision
	store.compatibilityUsage[ocqKey] = ocq

	delete(store.compatibilityUsage, compatibilityUsageKey(start.AddDate(0, 0, 20), "llama-line"))
	if _, err := store.CompatibilityReport(context.Background(), input); err != ErrCompatibilityCoverageIncomplete {
		t.Fatalf("missing backend error = %v", err)
	}
}

type flakyCompatibilityStore struct {
	*MemoryStore
	failures int
}

func (s *flakyCompatibilityStore) RecordCompatibilityObservation(ctx context.Context, day time.Time, identity CompatibilityObservationIdentity, startedAt, completedAt time.Time) error {
	if s.failures > 0 {
		s.failures--
		return fmt.Errorf("temporary database error")
	}
	return s.MemoryStore.RecordCompatibilityObservation(ctx, day, identity, startedAt, completedAt)
}

func (s *flakyCompatibilityStore) RecordCompatibilityHeartbeat(ctx context.Context, identity CompatibilityObservationIdentity, at time.Time, maxGap time.Duration) (compatibilityHeartbeat, error) {
	if s.failures > 0 {
		s.failures--
		return compatibilityHeartbeat{}, fmt.Errorf("temporary database error")
	}
	return s.MemoryStore.RecordCompatibilityHeartbeat(ctx, identity, at, maxGap)
}

func TestCompatibilityObserverRetriesAndExposesHealth(t *testing.T) {
	store := &flakyCompatibilityStore{MemoryStore: NewStore(nil), failures: 2}
	identity := CompatibilityObservationIdentity{SourceRevision: "0123456789abcdef0123456789abcdef01234567", DeploymentID: "production-a"}
	observer, err := NewCompatibilityObserver(store, identity, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	waits := 0
	if err := observer.tickWithRetry(t.Context(), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), func(time.Duration) bool { waits++; return true }); err != nil {
		t.Fatal(err)
	}
	status := observer.Status()
	if !status.Healthy || status.ObservedGaps != 0 || status.TransientRetryErrors != 2 || !status.GapStartedAt.IsZero() || waits != 2 || status.LastSuccessAt.IsZero() {
		t.Fatalf("status=%+v waits=%d", status, waits)
	}
}

func TestCompatibilityObserverReportsActualGapThenClearsActiveHealth(t *testing.T) {
	store := NewStore(nil)
	identity := CompatibilityObservationIdentity{SourceRevision: "0123456789abcdef0123456789abcdef01234567", DeploymentID: "production-a"}
	observer, err := NewCompatibilityObserver(store, identity, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)
	if err := observer.Tick(t.Context(), start); err != nil {
		t.Fatal(err)
	}
	if err := observer.Tick(t.Context(), start.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	gap := observer.Status()
	if gap.Healthy || !gap.GapStartedAt.Equal(start) || !gap.LastGapStartedAt.Equal(start) || gap.ObservedGaps != 1 {
		t.Fatalf("gap status=%+v", gap)
	}
	if err := observer.Tick(t.Context(), start.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	recovered := observer.Status()
	if !recovered.Healthy || !recovered.GapStartedAt.IsZero() || recovered.LastError != "" || recovered.ObservedGaps != 1 || !recovered.LastGapStartedAt.Equal(start) {
		t.Fatalf("recovered status=%+v", recovered)
	}
}

func TestCompatibilityObserverClearsExhaustedFailureAfterCoverageRecovery(t *testing.T) {
	store := &flakyCompatibilityStore{MemoryStore: NewStore(nil)}
	identity := CompatibilityObservationIdentity{SourceRevision: "0123456789abcdef0123456789abcdef01234567", DeploymentID: "production-a"}
	observer, err := NewCompatibilityObserver(store, identity, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)
	if err := observer.Tick(t.Context(), start); err != nil {
		t.Fatal(err)
	}
	store.failures = 5
	if err := observer.tickWithRetry(t.Context(), start.Add(time.Minute), func(time.Duration) bool { return true }); err == nil {
		t.Fatal("exhausted retry unexpectedly succeeded")
	}
	failed := observer.Status()
	if failed.Healthy || failed.LastError == "" || failed.FailureStartedAt.IsZero() || !failed.GapStartedAt.IsZero() || failed.ObservedGaps != 0 {
		t.Fatalf("failure status=%+v", failed)
	}
	if err := observer.Tick(t.Context(), start.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	gap := observer.Status()
	if gap.Healthy || gap.LastError != "" || !gap.FailureStartedAt.IsZero() || !gap.GapStartedAt.Equal(start) || gap.ObservedGaps != 1 {
		t.Fatalf("gap status=%+v", gap)
	}
	if err := observer.Tick(t.Context(), start.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	recovered := observer.Status()
	if !recovered.Healthy || recovered.LastError != "" || !recovered.FailureStartedAt.IsZero() || !recovered.GapStartedAt.IsZero() || recovered.ObservedGaps != 1 || !recovered.LastGapStartedAt.Equal(start) {
		t.Fatalf("recovered status=%+v", recovered)
	}
}

func TestCompatibilityReportRejectsObservationOutsideReportIdentity(t *testing.T) {
	store := NewStore(nil)
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rowIdentity := CompatibilityObservationIdentity{SourceRevision: "0123456789abcdef0123456789abcdef01234567", DeploymentID: "production-a"}
	if err := store.RecordCompatibilityObservation(t.Context(), day, rowIdentity, day, day.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
	_, err := store.CompatibilityReport(t.Context(), CompatibilityReportInput{Start: day, End: day, GeneratedAt: day.AddDate(0, 0, 1), Identity: CompatibilityObservationIdentity{SourceRevision: rowIdentity.SourceRevision, DeploymentID: "production-b"}})
	if err != ErrCompatibilityCoverageIncomplete {
		t.Fatalf("error=%v", err)
	}
}

func TestCompatibilityReportRejectsInvalidRangeBeforeOpeningPostgresTransaction(t *testing.T) {
	store := &PostgresStore{}
	identity := CompatibilityObservationIdentity{SourceRevision: "0123456789abcdef0123456789abcdef01234567", DeploymentID: "production-a"}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for name, input := range map[string]CompatibilityReportInput{
		"reversed": {Start: start, End: start.AddDate(0, 0, -1), Identity: identity},
		"huge":     {Start: start, End: start.AddDate(1000000, 0, 0), Identity: identity},
		"over-90":  {Start: start, End: start.AddDate(0, 0, 90), Identity: identity},
		"identity": {Start: start, End: start, Identity: CompatibilityObservationIdentity{SourceRevision: "bad", DeploymentID: "production-a"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.CompatibilityReport(t.Context(), input); err != ErrInvalid {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestCompatibilityIdentityUsesCanonicalSafeCharset(t *testing.T) {
	identity := CompatibilityObservationIdentity{SourceRevision: "0123456789abcdef0123456789abcdef01234567", DeploymentID: "production&west"}
	if _, err := NewCompatibilityObserver(NewStore(nil), identity, time.Minute); err != ErrInvalid {
		t.Fatalf("unsafe identity error=%v", err)
	}
}

func TestCompatibilityObserverDoesNotBridgeSameDayHeartbeatGap(t *testing.T) {
	store := NewStore(nil)
	identity := CompatibilityObservationIdentity{SourceRevision: "0123456789abcdef0123456789abcdef01234567", DeploymentID: "production-a"}
	observer, err := NewCompatibilityObserver(store, identity, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	if err := observer.Tick(t.Context(), day); err != nil {
		t.Fatal(err)
	}
	for minute := 12 * 60; minute <= 24*60; minute++ {
		if err := observer.Tick(t.Context(), day.Add(time.Duration(minute)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	_, err = store.CompatibilityReport(t.Context(), CompatibilityReportInput{Start: day, End: day, GeneratedAt: day.AddDate(0, 0, 1), Identity: identity})
	if err != ErrCompatibilityCoverageIncomplete {
		t.Fatalf("same-day gap was treated as complete: %v", err)
	}
}

func TestInformationalRetirementStatusDoesNotClaimEligibility(t *testing.T) {
	status, err := NewStore(nil).CompatibilityRetirementStatus(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, backend := range status.Backends {
		if backend.Eligibility != "observation_coverage_required" {
			t.Fatalf("backend %s eligibility = %q", backend.Backend, backend.Eligibility)
		}
	}
}

func TestCompatibilityObserverNeverBackfillsStartupGap(t *testing.T) {
	store := NewStore(nil)
	identity := CompatibilityObservationIdentity{SourceRevision: "0123456789abcdef0123456789abcdef01234567", DeploymentID: "production-a"}
	first, err := NewCompatibilityObserver(store, identity, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for minute := 0; minute <= 24*60; minute++ {
		if err := first.Tick(context.Background(), day.Add(time.Duration(minute)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	input := CompatibilityReportInput{Start: utcDay(day), End: utcDay(day), GeneratedAt: utcDay(day).AddDate(0, 0, 1), Identity: identity}
	if _, err := store.CompatibilityReport(context.Background(), input); err != nil {
		t.Fatalf("continuous boundary was not complete: %v", err)
	}

	restarted, err := NewCompatibilityObserver(store, identity, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	late := day.AddDate(0, 0, 2)
	if err := restarted.Tick(context.Background(), late); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Tick(context.Background(), late.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	missing := CompatibilityReportInput{Start: utcDay(late).AddDate(0, 0, -1), End: utcDay(late).AddDate(0, 0, -1), GeneratedAt: utcDay(late), Identity: identity}
	if _, err := store.CompatibilityReport(context.Background(), missing); err != ErrCompatibilityCoverageIncomplete {
		t.Fatalf("startup fabricated missing day: %v", err)
	}
}

func TestCompatibilityObserverNewSegmentDetectsRestartGapWithoutMergingCoverage(t *testing.T) {
	store := NewStore(nil)
	base := CompatibilityObservationIdentity{SourceRevision: "0123456789abcdef0123456789abcdef01234567", DeploymentID: "production-a"}
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	firstIdentity := base
	firstIdentity.SegmentID = "segment-one"
	first, err := NewCompatibilityObserver(store, firstIdentity, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Tick(t.Context(), day); err != nil {
		t.Fatal(err)
	}
	if err := first.Tick(t.Context(), day.Add(23*time.Hour+59*time.Minute)); err != nil {
		t.Fatal(err)
	}

	secondIdentity := base
	secondIdentity.SegmentID = "segment-two"
	second, err := NewCompatibilityObserver(store, secondIdentity, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Tick(t.Context(), day.AddDate(0, 0, 1).Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	status := second.Status()
	if status.Healthy || !status.GapStartedAt.Equal(day.Add(23*time.Hour+59*time.Minute)) || status.ObservedGaps != 1 {
		t.Fatalf("restart status=%+v", status)
	}
	_, err = store.CompatibilityReport(t.Context(), CompatibilityReportInput{Start: day, End: day, GeneratedAt: day.AddDate(0, 0, 1).Add(time.Hour), Identity: base})
	if err != ErrCompatibilityCoverageIncomplete {
		t.Fatalf("segments merged into qualifying coverage: %v", err)
	}
}

func TestCompatibilityObserverNewSegmentChecksContinuousPreviousHeartbeat(t *testing.T) {
	store := NewStore(nil)
	base := CompatibilityObservationIdentity{SourceRevision: "0123456789abcdef0123456789abcdef01234567", DeploymentID: "production-a"}
	start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	first := base
	first.SegmentID = "segment-one"
	if _, err := store.RecordCompatibilityHeartbeat(t.Context(), first, start, 3*time.Minute); err != nil {
		t.Fatal(err)
	}
	second := base
	second.SegmentID = "segment-two"
	result, err := store.RecordCompatibilityHeartbeat(t.Context(), second, start.Add(time.Minute), 3*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !result.GapStartedAt.IsZero() {
		t.Fatalf("continuous restart reported gap at %s", result.GapStartedAt)
	}
	row := store.compatibilityUsage[compatibilityUsageKey(start, "ocq")]
	if row.ObservationSegment != second.SegmentID || !row.ObservationStartedAt.Equal(start.Add(time.Minute)) {
		t.Fatalf("new segment merged previous coverage: %+v", row)
	}
}

func TestCompatibilityReportAuthAndListenerIsolation(t *testing.T) {
	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "secret")
	revision := "0123456789abcdef0123456789abcdef01234567"
	t.Setenv("SWITCHYARD_SOURCE_REVISION", revision)
	t.Setenv("SWITCHYARD_DEPLOYMENT_ID", "api-production")
	store := NewStore(nil)
	day := utcDay(time.Now().UTC()).AddDate(0, 0, -1)
	if err := store.RecordCompatibilityObservation(t.Context(), day, CompatibilityObservationIdentity{SourceRevision: revision, DeploymentID: "api-production"}, day, day.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
	api := NewAPI(store, NewRunner(store))
	path := fmt.Sprintf("/api/compatibility/report?start=%s&end=%s", day.Format(time.DateOnly), day.Format(time.DateOnly))

	unauthorized := httptest.NewRecorder()
	api.ControlPlaneHandler().ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, path, nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}
	model := httptest.NewRecorder()
	api.ModelIngressHandler().ServeHTTP(model, httptest.NewRequest(http.MethodGet, path, nil))
	if model.Code != http.StatusNotFound {
		t.Fatalf("model ingress status = %d body=%s", model.Code, model.Body.String())
	}

	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	api.ControlPlaneHandler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		var body any
		_ = json.Unmarshal(response.Body.Bytes(), &body)
		t.Fatalf("authenticated status = %d body=%v", response.Code, body)
	}
}

func TestPostgresStoreCompatibilityObservationIntegration(t *testing.T) {
	day := utcDay(time.Now().UTC()).AddDate(0, 0, -1)
	store, db, _ := newPostgresArtifactIntegrationStore(t, day)
	_, _ = db.ExecContext(t.Context(), `delete from compatibility_observation_daily where usage_date between $1::date and $2::date`, day.Format(time.DateOnly), day.AddDate(0, 0, 1).Format(time.DateOnly))
	if _, err := db.ExecContext(t.Context(), `delete from compatibility_usage_daily where usage_date between $1::date and $2::date`, day.Format(time.DateOnly), day.AddDate(0, 0, 1).Format(time.DateOnly)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `delete from compatibility_observation_daily where usage_date between $1::date and $2::date`, day.Format(time.DateOnly), day.AddDate(0, 0, 1).Format(time.DateOnly))
		_, _ = db.ExecContext(context.Background(), `delete from compatibility_usage_daily where usage_date between $1::date and $2::date`, day.Format(time.DateOnly), day.AddDate(0, 0, 1).Format(time.DateOnly))
	})
	if _, err := db.ExecContext(t.Context(), `set timezone='America/Los_Angeles'`); err != nil {
		t.Fatal(err)
	}
	identity := CompatibilityObservationIdentity{SourceRevision: "0123456789abcdef0123456789abcdef01234567", DeploymentID: "postgres-production", SegmentID: "segment-postgres"}
	seedPostgresCompatibilityDay(t, db, day, identity)
	if _, err := db.ExecContext(t.Context(), `insert into compatibility_usage_daily(usage_date,backend,attempts,first_attempt_at,last_attempt_at) values($1::date,'llama-line',1,($1::date::timestamp at time zone 'UTC') + interval '23 hours',($1::date::timestamp at time zone 'UTC') + interval '23 hours')`, day.Format(time.DateOnly)); err != nil {
		t.Fatal(err)
	}
	report, err := store.CompatibilityReport(t.Context(), CompatibilityReportInput{Start: day, End: day, GeneratedAt: time.Date(2001, 2, 3, 0, 0, 0, 0, time.UTC), Identity: identity})
	if err != nil {
		rows, _ := store.compatibilityUsageRows(t.Context(), day, day)
		t.Fatalf("%v rows=%+v", err, rows)
	}
	if len(report.Observations) != 2 || report.Observations[0].Attempts != 1 || report.Observations[1].Attempts != 0 {
		t.Fatalf("report = %+v", report)
	}
	if report.GeneratedAt.Year() == 2001 {
		t.Fatalf("report used caller clock: %s", report.GeneratedAt)
	}
}

func seedPostgresCompatibilityDay(t *testing.T, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, day time.Time, identity CompatibilityObservationIdentity) {
	t.Helper()
	for _, backend := range compatibilityBackends {
		_, err := db.ExecContext(t.Context(), `insert into compatibility_observation_daily(usage_date,backend,source_revision,deployment_id,observation_segment,observation_started_at,observation_completed_at,closed_at) values($1::date,$2,$3,$4,$5,$1::date::timestamp at time zone 'UTC',($1::date + 1)::timestamp at time zone 'UTC',clock_timestamp()) on conflict(usage_date,backend) do update set source_revision=excluded.source_revision,deployment_id=excluded.deployment_id,observation_segment=excluded.observation_segment,observation_started_at=excluded.observation_started_at,observation_completed_at=excluded.observation_completed_at,closed_at=excluded.closed_at`, day.Format(time.DateOnly), backend, identity.SourceRevision, identity.DeploymentID, identity.SegmentID)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestPostgresCompatibilityHeartbeatUsesDatabaseClockIntegration(t *testing.T) {
	dbDay := utcDay(time.Now().UTC())
	store, db, _ := newPostgresArtifactIntegrationStore(t, dbDay)
	callerDay := time.Date(2001, 2, 3, 0, 0, 0, 0, time.UTC)
	_, _ = db.ExecContext(t.Context(), `delete from compatibility_observation_daily where usage_date in ($1::date,$2::date)`, callerDay.Format(time.DateOnly), dbDay.Format(time.DateOnly))
	base := CompatibilityObservationIdentity{SourceRevision: "0123456789abcdef0123456789abcdef01234567", DeploymentID: "clock-production"}
	lease, identity, err := store.AcquireCompatibilityObserverLease(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	result, err := store.RecordCompatibilityHeartbeat(t.Context(), identity, callerDay, 3*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if utcDay(result.ObservedAt) != dbDay {
		t.Fatalf("observed_at=%s want database day %s", result.ObservedAt, dbDay)
	}
	var callerRows, databaseRows int
	if err := db.QueryRowContext(t.Context(), `select count(*) filter(where usage_date=$1::date),count(*) filter(where usage_date=$2::date) from compatibility_observation_daily where deployment_id=$3`, callerDay.Format(time.DateOnly), dbDay.Format(time.DateOnly), identity.DeploymentID).Scan(&callerRows, &databaseRows); err != nil {
		t.Fatal(err)
	}
	if callerRows != 0 || databaseRows != len(compatibilityBackends) {
		t.Fatalf("caller rows=%d database rows=%d", callerRows, databaseRows)
	}
}

func TestPostgresStoreCompatibilityDayClosureSerializesLateAttemptIntegration(t *testing.T) {
	day := utcDay(time.Now().UTC())
	store, db, _ := newPostgresArtifactIntegrationStore(t, day)
	_, _ = db.ExecContext(t.Context(), `delete from compatibility_observation_daily where usage_date=$1::date`, day.Format(time.DateOnly))
	_, _ = db.ExecContext(t.Context(), `delete from compatibility_usage_daily where usage_date=$1::date`, day.Format(time.DateOnly))
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `delete from compatibility_observation_daily where usage_date=$1::date`, day.Format(time.DateOnly))
		_, _ = db.ExecContext(context.Background(), `delete from compatibility_usage_daily where usage_date=$1::date`, day.Format(time.DateOnly))
	})
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := lockCompatibilityDay(t.Context(), tx, day); err != nil {
		t.Fatal(err)
	}
	identity := CompatibilityObservationIdentity{SourceRevision: "0123456789abcdef0123456789abcdef01234567", DeploymentID: "postgres-production", SegmentID: "segment-postgres"}
	if err := recordCompatibilityObservationTx(t.Context(), tx, identity, day, day.AddDate(0, 0, 1), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	attempted := make(chan error, 1)
	go func() { attempted <- store.RecordCompatibilityUsage(context.Background(), "ocq", day) }()
	select {
	case err := <-attempted:
		t.Fatalf("attempt bypassed day lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-attempted; err != ErrCompatibilityCoverageIncomplete {
		t.Fatalf("late attempt error=%v", err)
	}
}

func TestPostgresStoreCompatibilityObserverLeaseFencesTakeoverIntegration(t *testing.T) {
	day := utcDay(time.Now().UTC())
	store, db, _ := newPostgresArtifactIntegrationStore(t, day)
	_, _ = db.ExecContext(t.Context(), `delete from compatibility_observation_daily where usage_date=$1::date`, day.Format(time.DateOnly))
	_, _ = db.ExecContext(t.Context(), `delete from compatibility_usage_daily where usage_date=$1::date`, day.Format(time.DateOnly))
	base := CompatibilityObservationIdentity{SourceRevision: "0123456789abcdef0123456789abcdef01234567", DeploymentID: "lease-production"}
	first, firstIdentity, err := store.AcquireCompatibilityObserverLease(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AcquireCompatibilityObserverLease(t.Context(), base); err == nil {
		t.Fatal("concurrent observer lease acquired")
	}
	blocker, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := lockCompatibilityDay(t.Context(), blocker, day); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	stale := make(chan error, 1)
	go func() {
		_, err := store.RecordCompatibilityHeartbeat(context.Background(), firstIdentity, time.Now(), 3*time.Minute)
		stale <- err
	}()
	second, secondIdentity, err := store.AcquireCompatibilityObserverLease(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if firstIdentity.SegmentID == secondIdentity.SegmentID {
		t.Fatal("takeover reused observation segment")
	}
	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-stale; err == nil {
		t.Fatal("fenced observer wrote after takeover")
	}
	if _, err := store.RecordCompatibilityHeartbeat(t.Context(), secondIdentity, time.Now(), 3*time.Minute); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreObserverLeaseRestartInspectsPreviousSegmentHeartbeatIntegration(t *testing.T) {
	day := utcDay(time.Now().UTC())
	store, db, _ := newPostgresArtifactIntegrationStore(t, day)
	base := CompatibilityObservationIdentity{SourceRevision: "0123456789abcdef0123456789abcdef01234567", DeploymentID: "restart-production"}
	_, _ = db.ExecContext(t.Context(), `delete from compatibility_observation_daily where deployment_id=$1`, base.DeploymentID)
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `delete from compatibility_observation_daily where deployment_id=$1`, base.DeploymentID)
	})
	first, firstIdentity, err := store.AcquireCompatibilityObserverLease(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordCompatibilityHeartbeat(t.Context(), firstIdentity, time.Time{}, 3*time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `update compatibility_observation_daily set observation_started_at=clock_timestamp()-interval '10 minutes',observation_completed_at=clock_timestamp()-interval '10 minutes' where deployment_id=$1 and observation_segment=$2`, base.DeploymentID, firstIdentity.SegmentID); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, secondIdentity, err := store.AcquireCompatibilityObserverLease(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	result, err := store.RecordCompatibilityHeartbeat(t.Context(), secondIdentity, time.Time{}, 3*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if result.GapStartedAt.IsZero() || result.ObservedAt.Sub(result.GapStartedAt) < 9*time.Minute {
		t.Fatalf("restart gap not detected: %+v", result)
	}
	var segment string
	var started time.Time
	if err := db.QueryRowContext(t.Context(), `select observation_segment,observation_started_at from compatibility_observation_daily where deployment_id=$1 and backend='ocq' and usage_date=$2::date`, base.DeploymentID, result.ObservedAt.Format(time.DateOnly)).Scan(&segment, &started); err != nil {
		t.Fatal(err)
	}
	if segment != secondIdentity.SegmentID || started.Before(result.ObservedAt.Add(-time.Second)) {
		t.Fatalf("restart merged segment coverage: segment=%s started=%s observed=%s", segment, started, result.ObservedAt)
	}
}
