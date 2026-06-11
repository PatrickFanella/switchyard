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

func TestAPIAdaptersEndpointShape(t *testing.T) {
	api := controlplane.NewAPI(controlplane.NewStore(nil), controlplane.NewRunner(controlplane.NewStore(nil)))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/adapters")
	if err != nil {
		t.Fatalf("GET adapters error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var body struct {
		Queues      []controlplane.AdapterQueue     `json:"queues"`
		Sessions    []controlplane.AdapterSession   `json:"sessions"`
		Logs        []controlplane.AdapterLog       `json:"logs"`
		Prompts     []controlplane.AdapterPrompt    `json:"prompts"`
		Config      controlplane.AdapterConfig      `json:"config"`
		Agents      []controlplane.AdapterAgent     `json:"agents"`
		Skills      []controlplane.AdapterSkill     `json:"skills"`
		Health      controlplane.AdapterHealth      `json:"health"`
		LlamaLine   controlplane.AdapterLlamaLine   `json:"llama_line"`
		OCQ         controlplane.AdapterOCQ         `json:"ocq"`
		Blacktower  controlplane.AdapterBlacktower  `json:"blacktower"`
		Changemaker controlplane.AdapterChangemaker `json:"changemaker"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode adapters: %v", err)
	}
	if len(body.Queues) != 1 || body.Queues[0].Status != "healthy" {
		t.Fatalf("queues = %+v", body.Queues)
	}
	if len(body.Sessions) != 1 || body.Sessions[0].Surface != "sessions" {
		t.Fatalf("sessions = %+v", body.Sessions)
	}
	if len(body.Logs) != 1 || body.Logs[0].Level != "info" {
		t.Fatalf("logs = %+v", body.Logs)
	}
	if len(body.Prompts) != 1 || body.Prompts[0].Name == "" {
		t.Fatalf("prompts = %+v", body.Prompts)
	}
	if body.Config.Environment != "development" || len(body.Config.Features) == 0 {
		t.Fatalf("config = %+v", body.Config)
	}
	if len(body.Agents) != 1 || len(body.Skills) != 1 {
		t.Fatalf("agents/skills = %+v %+v", body.Agents, body.Skills)
	}
	if body.Health.Status != "healthy" || len(body.Health.Services) != 1 {
		t.Fatalf("health = %+v", body.Health)
	}
	if len(body.LlamaLine.Requests) != 1 || body.LlamaLine.Stats.ActiveRequests == 0 || len(body.LlamaLine.Upstreams) != 1 {
		t.Fatalf("llama_line = %+v", body.LlamaLine)
	}
	if len(body.OCQ.Requests) != 1 || len(body.OCQ.Sessions) != 1 || body.OCQ.Settings.Provider == "" {
		t.Fatalf("ocq = %+v", body.OCQ)
	}
	if len(body.Blacktower.Tasks) != 1 || len(body.Blacktower.Reports) != 1 || body.Blacktower.SchedulerHealth.Status != "healthy" {
		t.Fatalf("blacktower = %+v", body.Blacktower)
	}
	if len(body.Changemaker.Posts) != 1 || !body.Changemaker.Service.Healthy || !body.Changemaker.RunControl.Enabled {
		t.Fatalf("changemaker = %+v", body.Changemaker)
	}
}
