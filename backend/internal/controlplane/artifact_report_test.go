package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestArtifactReportMeasuresCreatedRetainedAndPrunedBytes(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	store := NewStore(func() time.Time { return now })
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	old, err := store.CreateRun(CreateRunInput{WorkflowID: "devlog"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddArtifact(CreateArtifactInput{RunID: old.ID, Name: "old", Kind: "text", URI: "data:text/plain,hello"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(old.ID, RunSucceeded, "done"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(24 * time.Hour)
	active, err := store.CreateRun(CreateRunInput{WorkflowID: "devlog"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddArtifact(CreateArtifactInput{RunID: active.ID, Name: "active", Kind: "text", URI: "data:text/plain,new"}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(24 * time.Hour)
	result, err := store.PruneRetention(now.Add(-36 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if result.Artifacts != 1 || result.ArtifactBytes != 5 {
		t.Fatalf("prune result = %+v", result)
	}
	report, err := store.ArtifactReport(context.Background(), ArtifactReportInput{Start: utcDay(now).AddDate(0, 0, -2), End: now})
	if err != nil {
		t.Fatal(err)
	}
	if report.Retained != 1 || report.RetainedBytes != 3 || report.CreatedTotal != 2 || report.CreatedBytesTotal != 8 || report.PrunedTotal != 1 || report.PrunedBytesTotal != 5 {
		t.Fatalf("report = %+v", report)
	}
	if len(report.Buckets) != 3 || report.Buckets[0].Created != 1 || report.Buckets[1].CreatedBytes != 3 || report.Buckets[2].PrunedBytes != 5 || report.Buckets[2].NetBytes != -5 {
		t.Fatalf("buckets = %+v", report.Buckets)
	}
}

func TestArtifactReportEmptyStateAndBounds(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	store := NewStore(func() time.Time { return now })
	report, err := store.ArtifactReport(context.Background(), ArtifactReportInput{Start: utcDay(now).AddDate(0, 0, -1), End: now})
	if err != nil {
		t.Fatal(err)
	}
	if report.Retained != 0 || report.CreatedTotal != 0 || len(report.Buckets) != 2 || report.Buckets[0].Date == "" {
		t.Fatalf("empty report = %+v", report)
	}
	_, err = store.ArtifactReport(context.Background(), ArtifactReportInput{Start: utcDay(now).AddDate(0, 0, -90), End: now})
	if err != ErrInvalid {
		t.Fatalf("91-day report error = %v", err)
	}
}

func TestArtifactRetentionUntilIsMetadataNotPruneTrigger(t *testing.T) {
	t.Setenv("SWITCHYARD_RETENTION", "1h")
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	store := NewStore(func() time.Time { return now })
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	run, _ := store.CreateRun(CreateRunInput{WorkflowID: "devlog"})
	artifact, err := store.AddArtifact(CreateArtifactInput{RunID: run.ID, Name: "evidence", Kind: "text", URI: "data:text/plain,x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(run.ID, RunSucceeded, "done"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	if result, err := store.PruneRetention(run.CreatedAt.Add(-time.Second)); err != nil || result.Artifacts != 0 {
		t.Fatalf("metadata unexpectedly pruned artifact: result=%+v err=%v", result, err)
	}
	report, err := store.ArtifactReport(context.Background(), ArtifactReportInput{Start: utcDay(now), End: now})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.RetentionUntil == nil || report.DeclaredRetentionExpired != 1 || report.Retained != 1 || !strings.Contains(report.RetentionSemantics, "terminal run") {
		t.Fatalf("retention metadata report = %+v artifact=%+v", report, artifact)
	}
}

func TestArtifactReportEndpointAuthBoundsAndListenerIsolation(t *testing.T) {
	t.Setenv("SWITCHYARD_CONTROL_READ_TOKEN", "read-secret")
	store := NewStore(nil)
	api := NewAPI(store, NewRunner(store))
	request := httptest.NewRequest(http.MethodGet, "/api/artifacts/report?days=2", nil)
	unauthorized := httptest.NewRecorder()
	api.ControlPlaneHandler().ServeHTTP(unauthorized, request)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/artifacts/report?days=2", nil)
	request.Header.Set("Authorization", "Bearer read-secret")
	authorized := httptest.NewRecorder()
	api.ControlPlaneHandler().ServeHTTP(authorized, request)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized status = %d body=%s", authorized.Code, authorized.Body.String())
	}
	var envelope struct {
		ArtifactReport ArtifactReport `json:"artifact_report"`
	}
	if err := json.NewDecoder(authorized.Body).Decode(&envelope); err != nil || len(envelope.ArtifactReport.Buckets) != 2 {
		t.Fatalf("report response = %+v err=%v", envelope, err)
	}
	bad := httptest.NewRecorder()
	api.routes().ServeHTTP(bad, httptest.NewRequest(http.MethodGet, "/api/artifacts/report?days=91", nil))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad days status = %d", bad.Code)
	}
	isolated := httptest.NewRecorder()
	api.ModelIngressHandler().ServeHTTP(isolated, httptest.NewRequest(http.MethodGet, "/api/artifacts/report", nil))
	if isolated.Code != http.StatusNotFound {
		t.Fatalf("model listener status = %d", isolated.Code)
	}
}

func TestArtifactMetricsHaveFixedNamesValuesAndNoLabels(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	store := NewStore(func() time.Time { return now })
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	run, _ := store.CreateRun(CreateRunInput{WorkflowID: "devlog"})
	if _, err := store.AddArtifact(CreateArtifactInput{RunID: run.ID, Name: "x", Kind: "text", URI: "data:text/plain,abc"}); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	NewAPI(store, NewRunner(store)).routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, expected := range []string{"switchyard_artifacts_retained 1", "switchyard_artifact_bytes_retained 3", "switchyard_artifacts_retained_bytes_unmeasured 0", "switchyard_artifacts_created_total 1", "switchyard_artifact_bytes_created_total 3", "switchyard_artifacts_pruned_total 0", "switchyard_artifact_bytes_pruned_total 0", "switchyard_artifacts_pruned_bytes_unmeasured_total 0"} {
		if !strings.Contains(recorder.Body.String(), expected) {
			t.Errorf("metrics missing %q", expected)
		}
	}
	for _, line := range strings.Split(recorder.Body.String(), "\n") {
		if strings.HasPrefix(line, "switchyard_artifact") && strings.Contains(line, "{") {
			t.Errorf("artifact metric has high-cardinality labels: %s", line)
		}
	}
}

func TestPostgresArtifactReportQueryIsBoundedAndAggregateOnly(t *testing.T) {
	source, err := os.ReadFile("artifact_report.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, contract := range []string{"measurement_date between $1::date and $2::date", "Format(time.DateOnly)", "count(*)", "sum(byte_size)", "storage_class<>'inline-data-uri'", "maxArtifactReportDays", "= 90", "LevelRepeatableRead"} {
		if !strings.Contains(text, contract) {
			t.Errorf("Postgres report query missing %q", contract)
		}
	}
	pruneSource, err := os.ReadFile("postgres_store_model_broker.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, contract := range []string{"BeginTx(ctx, nil)", "limit $7 for update skip locked", "limit $6 for update skip locked", "retentionBatchSize = 500", "delete from runs where id in (", "returning byte_size, storage_class", "pruned_bytes_unmeasured"} {
		if !strings.Contains(string(pruneSource), contract) {
			t.Errorf("Postgres prune accounting missing %q", contract)
		}
	}
}
