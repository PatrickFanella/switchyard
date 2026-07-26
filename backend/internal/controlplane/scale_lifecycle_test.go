package controlplane

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestInlineArtifactLifecycleMetadata(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	store := NewStore(func() time.Time { return now })
	if err := store.UpsertWorkflow(Workflow{ID: "artifact", Kind: WorkflowDevlog, Name: "Artifact"}); err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: "artifact"})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := store.AddArtifact(CreateArtifactInput{RunID: run.ID, Name: "report.txt", Kind: "text", URI: "data:text/plain;charset=utf-8,hello%20world"})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.StorageClass != "inline-data-uri" || artifact.ContentType != "text/plain" || artifact.ByteSize != 11 || len(artifact.SHA256) != 64 || artifact.RetentionUntil == nil {
		t.Fatalf("artifact metadata = %+v", artifact)
	}
	if _, err := store.AddArtifact(CreateArtifactInput{RunID: run.ID, Name: "remote", Kind: "text", URI: "https://example.invalid/report"}); err == nil {
		t.Fatal("remote artifact URI accepted")
	}
}

func TestCompatibilityRetirementStatusIsDurableAndReadOnly(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	store := NewStore(func() time.Time { return now })
	if err := store.RecordCompatibilityUsage(context.Background(), "ocq", now); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordCompatibilityUsage(context.Background(), "ocq", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	status, err := store.CompatibilityRetirementStatus(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Backends) != 2 || status.Backends[1].Backend != "ocq" || status.Backends[1].Attempts90D != 2 || status.Backends[1].Eligibility != "in_use" {
		t.Fatalf("status = %+v", status)
	}
	api := NewAPI(store, NewRunner(store))
	rec := httptest.NewRecorder()
	api.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/compatibility/retirement", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestOperatorRunSnapshotIsBounded(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	store := NewStore(func() time.Time { return now })
	if err := store.UpsertWorkflow(Workflow{ID: "snapshot", Kind: WorkflowDevlog, Name: "Snapshot"}); err != nil {
		t.Fatal(err)
	}
	for range 15 {
		if _, err := store.CreateRun(CreateRunInput{WorkflowID: "snapshot"}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := store.OperatorRunSnapshot(context.Background(), now, 5)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Queued != 15 || len(snapshot.Recent) != 5 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}
