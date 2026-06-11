package controlplane_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.subcult.tv/PatrickFanella/switchyard/internal/controlplane"
)

func TestAPIListsWorkflowsAndRunsTrigger(t *testing.T) {
	store := controlplane.NewStore(nil)
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/workflows")
	if err != nil {
		t.Fatalf("GET workflows error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var workflows struct {
		Workflows []controlplane.Workflow `json:"workflows"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&workflows); err != nil {
		t.Fatalf("decode workflows: %v", err)
	}
	if len(workflows.Workflows) != 3 {
		t.Fatalf("len(workflows) = %d, want 3", len(workflows.Workflows))
	}

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/triggers/manual-open-pilot/run", bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST trigger run error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	var body struct {
		Run controlplane.Run `json:"run"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	if body.Run.WorkflowID != "open-pilot" || body.Run.Status != controlplane.RunSucceeded {
		t.Fatalf("run = %+v", body.Run)
	}
}
