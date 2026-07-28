package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type staticModelBrokerStatusProvider struct {
	status ModelBrokerStatus
}

func (p staticModelBrokerStatusProvider) Status(context.Context) ModelBrokerStatus {
	return p.status
}

type staticOpenCodeStatusProvider struct {
	status OpenCodeExecutorStatus
}

func (p staticOpenCodeStatusProvider) Status(context.Context) OpenCodeExecutorStatus {
	return p.status
}

func TestOperatorSummaryReportsRunsAndComponents(t *testing.T) {
	now := time.Now().UTC()
	store := NewStore(func() time.Time { return now })
	if err := store.UpsertWorkflow(Workflow{ID: "open-pilot", Kind: WorkflowOpenPilot, Name: "Open Pilot"}); err != nil {
		t.Fatal(err)
	}
	failed, err := store.CreateRun(CreateRunInput{WorkflowID: "open-pilot"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartRun(failed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(failed.ID, RunFailed, strings.Repeat("failure ", 80)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRun(CreateRunInput{WorkflowID: "open-pilot"}); err != nil {
		t.Fatal(err)
	}

	api := NewAPI(store, NewRunner(store))
	api.modelBroker = staticModelBrokerStatusProvider{status: ModelBrokerStatus{
		Status: "healthy", Configured: true, CheckedAt: now.Add(-time.Second),
	}}
	api.openCode = staticOpenCodeStatusProvider{status: OpenCodeExecutorStatus{
		Status: "healthy", Configured: true, Healthy: true, CheckedAt: now,
	}}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/operator-summary", nil)
	api.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}

	var summary OperatorSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.SchemaVersion != "1.0" || summary.Service.ID != "switchyard" {
		t.Fatalf("identity = %+v schema=%q", summary.Service, summary.SchemaVersion)
	}
	if summary.Status.Level != "degraded" {
		t.Fatalf("status = %+v", summary.Status)
	}
	if summary.Data.Runs.Queued != 1 || summary.Data.Runs.Failed24H != 1 {
		t.Fatalf("runs = %+v", summary.Data.Runs)
	}
	if len(summary.Data.Runs.Recent) != 2 || summary.Data.Runs.Recent[0].WorkflowName != "Open Pilot" {
		t.Fatalf("recent = %+v", summary.Data.Runs.Recent)
	}
	if len(summary.Data.Runs.Recent[0].Summary) > 243 {
		t.Fatalf("summary was not bounded: %d", len(summary.Data.Runs.Recent[0].Summary))
	}
	if len(summary.Alerts) != 1 || summary.Alerts[0].TargetID != failed.ID {
		t.Fatalf("alerts = %+v", summary.Alerts)
	}
	if summary.Capabilities.Actions == nil || len(summary.Capabilities.Actions) != 0 {
		t.Fatalf("actions = %#v", summary.Capabilities.Actions)
	}
}

func TestOperatorSummaryMarksUnavailableComponentPartial(t *testing.T) {
	now := time.Date(2026, 7, 26, 18, 0, 0, 0, time.UTC)
	store := NewStore(func() time.Time { return now })
	api := NewAPI(store, NewRunner(store))
	api.modelBroker = staticModelBrokerStatusProvider{status: ModelBrokerStatus{
		Status: "unreachable", Configured: true, CheckedAt: now, Error: "dial failed",
	}}
	api.openCode = staticOpenCodeStatusProvider{status: OpenCodeExecutorStatus{
		Status: "healthy", Configured: true, Healthy: true, CheckedAt: now,
	}}

	summary := api.buildOperatorSummary(context.Background(), now)
	if !summary.Partial || summary.Status.Level != "degraded" {
		t.Fatalf("summary partial=%v status=%+v", summary.Partial, summary.Status)
	}
	if got := summary.Data.Components[0]; got.Status != "critical" || got.Summary != "dial failed" {
		t.Fatalf("broker component = %+v", got)
	}
}

func TestOperatorSummaryReadTokenCannotMutate(t *testing.T) {
	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "admin-secret")
	t.Setenv("SWITCHYARD_CONTROL_READ_TOKEN", "read-secret")
	store := NewStore(nil)
	api := NewAPI(store, NewRunner(store))
	api.modelBroker = staticModelBrokerStatusProvider{status: ModelBrokerStatus{Status: "healthy", Configured: true, CheckedAt: time.Now().UTC()}}
	api.openCode = staticOpenCodeStatusProvider{status: OpenCodeExecutorStatus{Status: "healthy", Configured: true, Healthy: true, CheckedAt: time.Now().UTC()}}
	server := httptest.NewServer(api.ControlPlaneHandler())
	t.Cleanup(server.Close)

	req, err := http.NewRequest(http.MethodGet, server.URL+"/api/operator-summary", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer read-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("summary status = %d", resp.StatusCode)
	}

	req, err = http.NewRequest(http.MethodPost, server.URL+"/api/runs", strings.NewReader(`{"workflow_id":"open-pilot"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer read-secret")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("mutation status = %d", resp.StatusCode)
	}
}
