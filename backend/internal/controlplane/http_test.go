package controlplane_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/PatrickFanella/switchyard/internal/controlplane"
)

func TestAPIWorkflowDefinitionCRUD(t *testing.T) {
	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	body := bytes.NewBufferString(`{"id":"wrong-id","name":"Custom Digest","description":"Daily custom digest","status":"draft","input_shape":{"type":"object","properties":{"topic":{"type":"string"}},"required":["topic"]},"pipeline":{"id":"custom-digest-pipeline","workflow_id":"wrong-id","name":"Custom Digest Pipeline","steps":[{"id":"generate","position":1,"name":"custom.generate","executor":"model","profile":"content-writer","model":"${model}"}]}}`)
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/workflow-definitions/custom-digest", body)
	if err != nil {
		t.Fatalf("new PUT request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT workflow definition error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d body = %s", resp.StatusCode, mustReadBody(t, resp.Body))
	}
	var putBody struct {
		WorkflowDefinition controlplane.WorkflowDefinition `json:"workflow_definition"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&putBody); err != nil {
		t.Fatalf("decode PUT response: %v", err)
	}
	if putBody.WorkflowDefinition.ID != "custom-digest" || putBody.WorkflowDefinition.Pipeline.WorkflowID != "custom-digest" {
		t.Fatalf("workflow definition = %+v", putBody.WorkflowDefinition)
	}

	resp, err = http.Get(srv.URL + "/api/workflow-definitions")
	if err != nil {
		t.Fatalf("GET workflow definitions error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET list status = %d", resp.StatusCode)
	}
	var listBody struct {
		WorkflowDefinitions []controlplane.WorkflowDefinition `json:"workflow_definitions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listBody); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listBody.WorkflowDefinitions) != 1 || listBody.WorkflowDefinitions[0].ID != "custom-digest" {
		t.Fatalf("workflow definitions = %+v", listBody.WorkflowDefinitions)
	}

	resp, err = http.Get(srv.URL + "/api/workflow-definitions/custom-digest")
	if err != nil {
		t.Fatalf("GET workflow definition error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d", resp.StatusCode)
	}
	var getBody struct {
		WorkflowDefinition controlplane.WorkflowDefinition `json:"workflow_definition"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&getBody); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	if getBody.WorkflowDefinition.Name != "Custom Digest" || getBody.WorkflowDefinition.InputShape.Type != "object" {
		t.Fatalf("workflow definition = %+v", getBody.WorkflowDefinition)
	}

	body = bytes.NewBufferString(`{"name":"Updated Digest","description":"Updated description","status":"draft","pipeline":{"id":"updated-pipeline","workflow_id":"wrong-id","name":"Updated Pipeline","steps":[{"id":"generate","position":1,"name":"custom.generate","executor":"model","profile":"content-writer","model":"${model}"}]}}`)
	req, err = http.NewRequest(http.MethodPut, srv.URL+"/api/workflow-definitions/custom-digest", body)
	if err != nil {
		t.Fatalf("new update request: %v", err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT update workflow definition error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update status = %d body = %s", resp.StatusCode, mustReadBody(t, resp.Body))
	}
	if err := json.NewDecoder(resp.Body).Decode(&putBody); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	if putBody.WorkflowDefinition.Name != "Updated Digest" || putBody.WorkflowDefinition.ID != "custom-digest" {
		t.Fatalf("updated workflow definition = %+v", putBody.WorkflowDefinition)
	}

	resp, err = http.Get(srv.URL + "/api/workflow-definitions/custom-digest")
	if err != nil {
		t.Fatalf("GET updated workflow definition error = %v", err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&getBody); err != nil {
		t.Fatalf("decode updated GET response: %v", err)
	}
	if getBody.WorkflowDefinition.Name != "Updated Digest" || getBody.WorkflowDefinition.Status != "draft" {
		t.Fatalf("updated workflow definition = %+v", getBody.WorkflowDefinition)
	}
}

func TestAPIWorkflowDefinitionWritesAreDraftOnlyAndValidated(t *testing.T) {
	store := controlplane.NewStore(nil)
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	for name, body := range map[string]string{
		"active status":     `{"name":"Custom Digest","description":"Daily custom digest","status":"active","pipeline":{"id":"custom-digest-pipeline","workflow_id":"custom-digest","name":"Custom Digest Pipeline","steps":[{"id":"generate","position":1,"name":"custom.generate","executor":"model"}]}}`,
		"duplicate step id": `{"name":"Custom Digest","description":"Daily custom digest","status":"draft","pipeline":{"id":"custom-digest-pipeline","workflow_id":"custom-digest","name":"Custom Digest Pipeline","steps":[{"id":"generate","position":1,"name":"custom.generate","executor":"model"},{"id":"generate","position":2,"name":"custom.generate-again","executor":"internal"}]}}`,
		"http executor":     `{"name":"Custom Digest","description":"Daily custom digest","status":"draft","pipeline":{"id":"custom-digest-pipeline","workflow_id":"custom-digest","name":"Custom Digest Pipeline","steps":[{"id":"call","position":1,"name":"custom.call","executor":"http"}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/workflow-definitions/custom-digest", bytes.NewBufferString(body))
			if err != nil {
				t.Fatalf("new PUT request: %v", err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("PUT workflow definition error = %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d body = %s", resp.StatusCode, mustReadBody(t, resp.Body))
			}
		})
	}
}

func TestAPICORSDoesNotAdvertiseWorkflowDefinitionPUT(t *testing.T) {
	store := controlplane.NewStore(nil)
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodOptions, srv.URL+"/api/workflow-definitions/custom-digest", nil)
	if err != nil {
		t.Fatalf("new OPTIONS request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("OPTIONS workflow definition error = %v", err)
	}
	defer resp.Body.Close()
	if methods := resp.Header.Get("Access-Control-Allow-Methods"); strings.Contains(methods, "PUT") {
		t.Fatalf("Access-Control-Allow-Methods = %q", methods)
	}
}

func TestAPIWorkflowDefinitionRejectsInvalidJSON(t *testing.T) {
	store := controlplane.NewStore(nil)
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/workflow-definitions/custom-digest", bytes.NewBufferString(`{"name":`))
	if err != nil {
		t.Fatalf("new PUT request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT invalid workflow definition error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestAPIWorkflowDefinitionNotFound(t *testing.T) {
	store := controlplane.NewStore(nil)
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/workflow-definitions/missing")
	if err != nil {
		t.Fatalf("GET missing workflow definition error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func mustReadBody(t *testing.T, r io.Reader) string {
	t.Helper()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(data)
}

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

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/triggers/manual-devlog/run", bytes.NewReader(nil))
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
	if body.Run.WorkflowID != "devlog" || body.Run.Status != controlplane.RunSucceeded {
		t.Fatalf("run = %+v", body.Run)
	}
}

func TestAPIRunsOpenPilotWorkflow(t *testing.T) {
	store := controlplane.NewStore(nil)
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	api := controlplane.NewAPI(store, controlplane.NewRunner(store, controlplane.MapExecutorRegistry{}))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/api/workflows/open-pilot/run", "application/json", bytes.NewBufferString(`{"repo":"OWNER/REPO","issue":"123"}`))
	if err != nil {
		t.Fatalf("POST workflow error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body struct {
		Run controlplane.Run `json:"run"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	if body.Run.WorkflowID != "open-pilot" || body.Run.Inputs["repo"] != "OWNER/REPO" || body.Run.Inputs["issue"] != "123" {
		t.Fatalf("run = %+v", body.Run)
	}
}

func TestAPIRejectsOpenPilotManualTriggerWithoutInputs(t *testing.T) {
	store := controlplane.NewStore(nil)
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	api := controlplane.NewAPI(store, controlplane.NewRunner(store, controlplane.MapExecutorRegistry{}))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/triggers/manual-open-pilot/run", bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST trigger run error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if runs := store.ListRuns(); len(runs) != 0 {
		t.Fatalf("runs = %+v, want none", runs)
	}
}

func TestAPIRetryOpenPilotPausedRunRequiresLifecycleResume(t *testing.T) {
	store := controlplane.NewStore(nil)
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	run, err := store.CreateRun(controlplane.CreateRunInput{WorkflowID: "open-pilot", TriggerID: "manual-open-pilot", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	if _, err := store.UpsertOpenPilotIssueState(controlplane.OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "123", Phase: controlplane.OpenPilotPhaseRequested}); err != nil {
		t.Fatalf("UpsertOpenPilotIssueState() error = %v", err)
	}
	if _, err := store.TransitionOpenPilotIssueState("OWNER/REPO", "123", controlplane.OpenPilotPhasePaused); err != nil {
		t.Fatalf("TransitionOpenPilotIssueState() error = %v", err)
	}
	api := controlplane.NewAPI(store, controlplane.NewRunner(store, controlplane.MapExecutorRegistry{}))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/runs/"+run.ID+"/retry", nil)
	if err != nil {
		t.Fatalf("new retry request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST retry error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("retry status = %d, want 409", resp.StatusCode)
	}
	if runs := store.ListRuns(); len(runs) != 1 {
		t.Fatalf("runs = %+v, want original only", runs)
	}
}

type fakeCanceler struct{ called bool }

func (f *fakeCanceler) Cancel(context.Context, controlplane.Run) controlplane.CancelResult {
	f.called = true
	return controlplane.CancelResult{Events: []controlplane.WorkflowExecutionEvent{{Type: "fake.cancel", Message: "called"}}}
}

func TestAPIUpdatesTriggerEnabled(t *testing.T) {
	store := controlplane.NewStore(nil)
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodPatch, srv.URL+"/api/triggers/daily-changemaker-native", bytes.NewBufferString(`{"enabled":true}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH trigger error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	trigger, ok := store.Trigger("daily-changemaker-native")
	if !ok || !trigger.Enabled {
		t.Fatalf("trigger = %+v ok=%v", trigger, ok)
	}
}

func TestAPIPipelinesListsStaticPipelines(t *testing.T) {
	store := controlplane.NewStore(nil)
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/pipelines")
	if err != nil {
		t.Fatalf("GET pipelines error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body struct {
		Pipelines []controlplane.Pipeline `json:"pipelines"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode pipelines: %v", err)
	}
	if len(body.Pipelines) != 2 || body.Pipelines[0].ID == "" || len(body.Pipelines[0].Steps) == 0 {
		t.Fatalf("pipelines = %+v", body.Pipelines)
	}
}

func TestAPIAgentProfilesListsNativeProfiles(t *testing.T) {
	store := controlplane.NewStore(nil)
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/agent-profiles")
	if err != nil {
		t.Fatalf("GET agent profiles error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body struct {
		Profiles []controlplane.AgentProfile `json:"profiles"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode profiles: %v", err)
	}
	if len(body.Profiles) != 3 || body.Profiles[0].ID == "" || len(body.Profiles[0].AllowedExecutors) == 0 {
		t.Fatalf("profiles = %+v", body.Profiles)
	}
}

func TestAPIModelBrokerStatusReportsNativeBroker(t *testing.T) {
	upstreamHealthy := true
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/broker/status" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"queue_depth":       2,
			"max_depth":         8,
			"in_flight_client":  "",
			"connected_clients": 3,
			"upstreams": []map[string]any{
				{
					"name":          "local-ollama",
					"url":           "http://ollama:11434",
					"healthy":       upstreamHealthy,
					"queue_depth":   0,
					"max_depth":     4,
					"last_probe_at": "2026-06-14T12:00:00Z",
				},
				{
					"name":         "ocq-switchyard",
					"url":          "http://ocq:8088",
					"healthy":      upstreamHealthy,
					"queue_depth":  2,
					"max_depth":    4,
					"in_flight_id": "req-2",
				},
			},
		}); err != nil {
			t.Fatalf("encode status: %v", err)
		}
	}))
	t.Cleanup(broker.Close)
	t.Setenv("MODEL_BROKER_BACKEND", "llama-line")
	t.Setenv("LLAMA_LINE_BASE_URL", broker.URL)

	store := controlplane.NewStore(nil)
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/model-broker/status")
	if err != nil {
		t.Fatalf("GET model broker status error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body struct {
		ModelBroker controlplane.ModelBrokerStatus `json:"model_broker"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if body.ModelBroker.ID != "switchyard-model-broker" || body.ModelBroker.Status != "healthy" || !body.ModelBroker.Configured {
		t.Fatalf("model broker = %+v", body.ModelBroker)
	}
	if body.ModelBroker.Backend != "switchyard-model-broker" || body.ModelBroker.TransitionalBackend != "llama-line" {
		t.Fatalf("model broker backend = %+v", body.ModelBroker)
	}
	if body.ModelBroker.QueueDepth != 2 || body.ModelBroker.MaxDepth != 8 || !body.ModelBroker.ActiveRequest || body.ModelBroker.ConnectedClients != 3 {
		t.Fatalf("model broker queue = %+v", body.ModelBroker)
	}
	if len(body.ModelBroker.InFlightRequestIDs) != 1 || body.ModelBroker.InFlightRequestIDs[0] != "req-2" {
		t.Fatalf("in-flight IDs = %+v", body.ModelBroker.InFlightRequestIDs)
	}
	if len(body.ModelBroker.Upstreams) != 2 || body.ModelBroker.Upstreams[1].Name != "ocq-switchyard" || body.ModelBroker.Upstreams[1].Status != "healthy" || body.ModelBroker.Upstreams[1].InFlightID != "req-2" {
		t.Fatalf("upstreams = %+v", body.ModelBroker.Upstreams)
	}
	if body.ModelBroker.Upstreams[1].LastProbeAt != nil {
		t.Fatalf("missing last_probe_at should stay absent: %+v", body.ModelBroker.Upstreams[1])
	}
}

func TestAPILlamaLineCompatChatCompletionsUsesNativeBroker(t *testing.T) {
	opencode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/session":
			if r.Method == http.MethodGet {
				_ = json.NewEncoder(w).Encode([]map[string]string{{"id": "session-1", "title": "compat"}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "session-1"})
		case "/session/session-1/message":
			_ = json.NewEncoder(w).Encode(map[string]any{"parts": []map[string]string{{"type": "text", "text": "native answer"}}})
		default:
			t.Fatalf("unexpected opencode path %s", r.URL.Path)
		}
	}))
	t.Cleanup(opencode.Close)
	t.Setenv("OPENCODE_SERVER_URL", opencode.URL)
	t.Setenv("OPENCODE_SERVER_USERNAME", "")
	t.Setenv("OPENCODE_SERVER_PASSWORD", "")

	store := controlplane.NewStore(nil)
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", bytes.NewBufferString(`{"model":"openai/gpt-5.4","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatalf("post compat chat: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %s", resp.Status)
	}
	var body struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Choices) != 1 || body.Choices[0].Message.Content != "native answer" {
		t.Fatalf("body = %+v", body)
	}
}

func TestAPILlamaLineCompatGenerateRoutesOllamaModelToOllama(t *testing.T) {
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Fatalf("unexpected ollama path %s", r.URL.Path)
		}
		var input struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode ollama request: %v", err)
		}
		if input.Model != "llama3.2" {
			t.Fatalf("model = %q", input.Model)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"role": "assistant", "content": "ollama answer"}, "done": true})
	}))
	t.Cleanup(ollama.Close)
	t.Setenv("OLLAMA_BASE_URL", ollama.URL)

	store := controlplane.NewStore(nil)
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/api/generate", "application/json", bytes.NewBufferString(`{"model":"ollama/llama3.2","prompt":"hello"}`))
	if err != nil {
		t.Fatalf("post compat generate: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %s", resp.Status)
	}
	var body struct {
		Response string `json:"response"`
		Done     bool   `json:"done"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Response != "ollama answer" || !body.Done {
		t.Fatalf("body = %+v", body)
	}
}

func TestAPILlamaLineCompatSetsCorrelationHeaders(t *testing.T) {
	upstreamRequestID := make(chan string, 1)
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamRequestID <- r.Header.Get("X-Request-ID")
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"role": "assistant", "content": "header answer"}, "done": true})
	}))
	t.Cleanup(ollama.Close)
	t.Setenv("OLLAMA_BASE_URL", ollama.URL)

	store := controlplane.NewStore(nil)
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", bytes.NewBufferString(`{"model":"ollama/llama3.2","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "compat-header-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST chat completions: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %s", resp.Status)
	}
	if resp.Header.Get("X-Request-ID") != "compat-header-1" || resp.Header.Get("X-Llama-Line-Request-ID") != "compat-header-1" || resp.Header.Get("X-OCQ-Request-ID") != "compat-header-1" {
		t.Fatalf("headers = X-Request-ID:%q X-Llama-Line-Request-ID:%q X-OCQ-Request-ID:%q", resp.Header.Get("X-Request-ID"), resp.Header.Get("X-Llama-Line-Request-ID"), resp.Header.Get("X-OCQ-Request-ID"))
	}
	if resp.Header.Get("X-OCQ-Status") != "completed" {
		t.Fatalf("X-OCQ-Status = %q", resp.Header.Get("X-OCQ-Status"))
	}
	select {
	case got := <-upstreamRequestID:
		if got != "compat-header-1" {
			t.Fatalf("upstream X-Request-ID = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not receive request")
	}
}

func TestAPILlamaLineCompatStreamingUsesBufferedSSEStatusEvents(t *testing.T) {
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"role": "assistant", "content": "stream answer"}, "done": true})
	}))
	t.Cleanup(ollama.Close)
	t.Setenv("OLLAMA_BASE_URL", ollama.URL)

	store := controlplane.NewStore(nil)
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", bytes.NewBufferString(`{"model":"ollama/llama3.2","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Llama-Line-Request-ID", "compat-stream-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST streaming chat completions: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %s", resp.Status)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("Content-Type = %q", resp.Header.Get("Content-Type"))
	}
	if resp.Header.Get("X-OCQ-Streaming-Mode") != "buffered" || resp.Header.Get("X-OCQ-Status") != "running" {
		t.Fatalf("stream headers = mode:%q status:%q", resp.Header.Get("X-OCQ-Streaming-Mode"), resp.Header.Get("X-OCQ-Status"))
	}
	body := mustReadBody(t, resp.Body)
	for _, want := range []string{"event: status", `"status":"queued"`, `"status":"running"`, `"status":"completed"`, "stream answer", "data: [DONE]"} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream body missing %q: %s", want, body)
		}
	}
}

func TestAPILlamaLineCompatAdminInflightCancel(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Fatalf("unexpected ollama path %s", r.URL.Path)
		}
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"role": "assistant", "content": "late answer"}, "done": true})
	}))
	t.Cleanup(ollama.Close)
	t.Setenv("OLLAMA_BASE_URL", ollama.URL)

	store := controlplane.NewStore(nil)
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	result := make(chan int, 1)
	go func() {
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/generate", bytes.NewBufferString(`{"model":"ollama/llama3.2","prompt":"hello"}`))
		if err != nil {
			result <- 0
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Request-ID", "compat-cancel-1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			result <- 0
			return
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		result <- resp.StatusCode
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("ollama request did not start")
	}

	var inflightBody struct {
		Active   bool `json:"active_request"`
		Inflight []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"inflight"`
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		resp, err := http.Get(srv.URL + "/admin/inflight")
		if err != nil {
			t.Fatalf("GET inflight: %v", err)
		}
		if err := json.NewDecoder(resp.Body).Decode(&inflightBody); err != nil {
			resp.Body.Close()
			t.Fatalf("decode inflight: %v", err)
		}
		resp.Body.Close()
		if len(inflightBody.Inflight) == 1 {
			break
		}
	}
	if !inflightBody.Active || len(inflightBody.Inflight) != 1 || inflightBody.Inflight[0].ID != "compat-cancel-1" || inflightBody.Inflight[0].Status != "running" {
		t.Fatalf("inflight = %+v", inflightBody)
	}

	cancelResp, err := http.Post(srv.URL+"/admin/inflight/cancel", "application/json", bytes.NewBufferString(`{"request_id":"compat-cancel-1"}`))
	if err != nil {
		t.Fatalf("POST cancel: %v", err)
	}
	defer cancelResp.Body.Close()
	if cancelResp.StatusCode != http.StatusAccepted {
		t.Fatalf("cancel status = %s", cancelResp.Status)
	}

	resp, err := http.Get(srv.URL + "/admin/inflight")
	if err != nil {
		t.Fatalf("GET inflight after cancel: %v", err)
	}
	defer resp.Body.Close()
	var after struct {
		Inflight []any `json:"inflight"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&after); err != nil {
		t.Fatalf("decode after: %v", err)
	}
	if len(after.Inflight) != 0 {
		t.Fatalf("after inflight = %+v", after.Inflight)
	}
	close(release)
	select {
	case <-result:
	case <-time.After(2 * time.Second):
		t.Fatal("generate request did not finish after release")
	}
}

func TestAPILlamaLineCompatHistorySearchAndLogsUseDurableLedger(t *testing.T) {
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Fatalf("unexpected ollama path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"role": "assistant", "content": "durable answer"}, "done": true})
	}))
	t.Cleanup(ollama.Close)
	t.Setenv("OLLAMA_BASE_URL", ollama.URL)

	store := controlplane.NewStore(nil)
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/generate", bytes.NewBufferString(`{"model":"ollama/llama3.2","prompt":"durable prompt"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "durable-ledger-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST generate: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %s", resp.Status)
	}

	// Recreate the API over the same store to verify completed requests are not just in process memory.
	srv.Close()
	api = controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv = httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	historyResp, err := http.Get(srv.URL + "/api/history/durable-ledger-1")
	if err != nil {
		t.Fatalf("GET history item: %v", err)
	}
	defer historyResp.Body.Close()
	if historyResp.StatusCode != http.StatusOK {
		t.Fatalf("history status = %s", historyResp.Status)
	}
	var history controlplane.ModelBrokerRequest
	if err := json.NewDecoder(historyResp.Body).Decode(&history); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if history.ID != "durable-ledger-1" || history.Status != "completed" || !strings.Contains(history.ResponseBody, "durable answer") {
		t.Fatalf("history = %+v", history)
	}

	searchResp, err := http.Get(srv.URL + "/api/search?q=durable%20prompt")
	if err != nil {
		t.Fatalf("GET search: %v", err)
	}
	defer searchResp.Body.Close()
	var search struct {
		Count int `json:"count"`
	}
	if err := json.NewDecoder(searchResp.Body).Decode(&search); err != nil {
		t.Fatalf("decode search: %v", err)
	}
	if search.Count != 1 {
		t.Fatalf("search count = %d", search.Count)
	}

	logsResp, err := http.Get(srv.URL + "/api/logs")
	if err != nil {
		t.Fatalf("GET logs: %v", err)
	}
	defer logsResp.Body.Close()
	var logs struct {
		Count int `json:"count"`
	}
	if err := json.NewDecoder(logsResp.Body).Decode(&logs); err != nil {
		t.Fatalf("decode logs: %v", err)
	}
	if logs.Count != 1 {
		t.Fatalf("logs count = %d", logs.Count)
	}
}

func TestAPILlamaLineCompatLegacyAdminAndUIAliases(t *testing.T) {
	store := controlplane.NewStore(nil)
	_, err := store.CreateModelBrokerRequest(controlplane.CreateModelBrokerRequestInput{ID: "legacy-alias-1", Model: "llama3.2", Path: "/api/generate", Client: "test", RequestBody: `{"prompt":"alias"}`})
	if err != nil {
		t.Fatalf("create ledger row: %v", err)
	}
	_, _ = store.FinishModelBrokerRequest("legacy-alias-1", controlplane.FinishModelBrokerRequestInput{Status: "completed", ResponseBody: `{"content":"ok"}`, CorrelationIDs: map[string]string{"llama_line_request_id": "legacy-alias-1"}})
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	for _, path := range []string{"/admin/logs/recent", "/admin/audit/recent", "/admin/search?q=alias", "/admin/stats/summary", "/admin/feedback", "/ui/api/status", "/ui/api/history", "/ui/api/logs", "/ui/api/search?q=alias", "/ui/api/config"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %s", path, resp.Status)
		}
	}
	resp, err := http.Get(srv.URL + "/ui/")
	if err != nil {
		t.Fatalf("GET /ui/: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("ui response = %s %q", resp.Status, resp.Header.Get("Content-Type"))
	}
}

func TestAPIOCQCompatSurfacesUseNativeState(t *testing.T) {
	opencode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session" {
			t.Fatalf("unexpected opencode path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "ses-native", "title": "Native Session", "status": "active"}})
	}))
	t.Cleanup(opencode.Close)
	t.Setenv("OPENCODE_SERVER_URL", opencode.URL)

	store := controlplane.NewStore(nil)
	_, err := store.CreateModelBrokerRequest(controlplane.CreateModelBrokerRequestInput{ID: "ocq-compat-1", Model: "openai/gpt", Path: "/v1/chat/completions", Client: "test", RequestBody: `{"prompt":"ocq"}`})
	if err != nil {
		t.Fatalf("create ledger row: %v", err)
	}
	_, _ = store.FinishModelBrokerRequest("ocq-compat-1", controlplane.FinishModelBrokerRequestInput{Status: "completed", ResponseBody: `{"content":"ok"}`, CorrelationIDs: map[string]string{"opencode_request_id": "ocq-compat-1", "opencode_session_id": "ses-native"}})
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	for _, path := range []string{"/ocq/sessions", "/ocq/sessions/ses-native", "/ocq/requests", "/ocq/requests/ocq-compat-1", "/ocq/settings", "/ocq/audit", "/ocq/logs"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body := mustReadBody(t, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %s body = %s", path, resp.Status, body)
		}
	}

	abortResp, err := http.Post(srv.URL+"/ocq/requests/ocq-compat-1/abort", "application/json", nil)
	if err != nil {
		t.Fatalf("POST abort: %v", err)
	}
	defer abortResp.Body.Close()
	if abortResp.StatusCode != http.StatusAccepted {
		t.Fatalf("abort status = %s", abortResp.Status)
	}
}

func TestAPIModelBrokerStatusDegradesWithoutUpstreams(t *testing.T) {
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"queue_depth": 0, "max_depth": 0, "upstreams": []map[string]any{}}); err != nil {
			t.Fatalf("encode status: %v", err)
		}
	}))
	t.Cleanup(broker.Close)
	t.Setenv("MODEL_BROKER_BACKEND", "llama-line")
	t.Setenv("LLAMA_LINE_BASE_URL", broker.URL)

	store := controlplane.NewStore(nil)
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/model-broker/status")
	if err != nil {
		t.Fatalf("GET model broker status error = %v", err)
	}
	defer resp.Body.Close()
	var body struct {
		ModelBroker controlplane.ModelBrokerStatus `json:"model_broker"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if body.ModelBroker.Status != "degraded" {
		t.Fatalf("status = %q, want degraded", body.ModelBroker.Status)
	}
}

func TestAPIModelBrokerStatusReportsUnconfigured(t *testing.T) {
	t.Setenv("LLAMA_LINE_BASE_URL", "")
	t.Setenv("MODEL_BROKER_BACKEND", "llama-line")
	store := controlplane.NewStore(nil)
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/model-broker/status")
	if err != nil {
		t.Fatalf("GET model broker status error = %v", err)
	}
	defer resp.Body.Close()
	var body struct {
		ModelBroker controlplane.ModelBrokerStatus `json:"model_broker"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if body.ModelBroker.Configured || body.ModelBroker.Status != "not_configured" || body.ModelBroker.Error == "" {
		t.Fatalf("model broker = %+v", body.ModelBroker)
	}
}

func TestAPIOpenCodeExecutorStatusReportsNativeExecutor(t *testing.T) {
	ocq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-gateway-key" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/healthz":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/ocq/sessions":
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]any{"sessions": []map[string]any{
				{"id": "sess-1", "title": "Issue 123", "status": "active", "user": "operator", "createdAt": "2026-06-14T12:00:00Z", "updatedAt": "2026-06-14T12:03:00Z"},
				{"id": "sess-2", "name": "Review", "status": "completed", "createdAt": 1718376000000},
			}}); err != nil {
				t.Fatalf("encode sessions: %v", err)
			}
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(ocq.Close)
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "ocq")
	t.Setenv("OCQ_BASE_URL", ocq.URL)
	t.Setenv("OCQ_GATEWAY_KEY", "test-gateway-key")

	store := controlplane.NewStore(nil)
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/opencode-executor/status")
	if err != nil {
		t.Fatalf("GET opencode executor status error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body struct {
		OpenCodeExecutor controlplane.OpenCodeExecutorStatus `json:"opencode_executor"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	status := body.OpenCodeExecutor
	if status.ID != "switchyard-opencode-executor" || status.Status != "healthy" || !status.Configured || !status.Healthy {
		t.Fatalf("opencode executor = %+v", status)
	}
	if status.Backend != "switchyard-opencode-executor" || status.TransitionalBackend != "ocq" || status.BaseURL != ocq.URL {
		t.Fatalf("opencode executor backend = %+v", status)
	}
	if status.SessionCount != 2 || status.ActiveSessionCount != 1 || len(status.Sessions) != 2 {
		t.Fatalf("session counts = %+v", status)
	}
	if status.Sessions[0].ID != "sess-1" || status.Sessions[0].Title != "Issue 123" || status.Sessions[0].CreatedAt == nil || status.Sessions[0].UpdatedAt == nil {
		t.Fatalf("first session = %+v", status.Sessions[0])
	}
	if status.Sessions[1].Title != "Review" || status.Sessions[1].CreatedAt != nil || status.Sessions[1].Status != "completed" {
		t.Fatalf("second session = %+v", status.Sessions[1])
	}
}

func TestAPIOpenCodeExecutorStatusFailsWhenSessionsFail(t *testing.T) {
	ocq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/ocq/sessions":
			http.Error(w, "sessions failed", http.StatusBadGateway)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(ocq.Close)
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "ocq")
	t.Setenv("OCQ_BASE_URL", ocq.URL)

	store := controlplane.NewStore(nil)
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/opencode-executor/status")
	if err != nil {
		t.Fatalf("GET opencode executor status error = %v", err)
	}
	defer resp.Body.Close()
	var body struct {
		OpenCodeExecutor controlplane.OpenCodeExecutorStatus `json:"opencode_executor"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if body.OpenCodeExecutor.Status != "failed" || body.OpenCodeExecutor.Healthy || body.OpenCodeExecutor.Error == "" {
		t.Fatalf("opencode executor = %+v", body.OpenCodeExecutor)
	}
}

func TestAPIOpenCodeExecutorStatusFailsWhenOCQAuthFails(t *testing.T) {
	ocq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/ocq/sessions":
			http.Error(w, "forbidden", http.StatusForbidden)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(ocq.Close)
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "ocq")
	t.Setenv("OCQ_BASE_URL", ocq.URL)
	t.Setenv("OCQ_GATEWAY_KEY", "wrong-key")

	store := controlplane.NewStore(nil)
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/opencode-executor/status")
	if err != nil {
		t.Fatalf("GET opencode executor status error = %v", err)
	}
	defer resp.Body.Close()
	var body struct {
		OpenCodeExecutor controlplane.OpenCodeExecutorStatus `json:"opencode_executor"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if body.OpenCodeExecutor.Status != "failed" || body.OpenCodeExecutor.Healthy || !body.OpenCodeExecutor.Configured || body.OpenCodeExecutor.Error == "" {
		t.Fatalf("opencode executor = %+v", body.OpenCodeExecutor)
	}
}

func TestAPIOpenCodeExecutorStatusReportsUnconfigured(t *testing.T) {
	t.Setenv("OCQ_BASE_URL", "")
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "ocq")
	store := controlplane.NewStore(nil)
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/opencode-executor/status")
	if err != nil {
		t.Fatalf("GET opencode executor status error = %v", err)
	}
	defer resp.Body.Close()
	var body struct {
		OpenCodeExecutor controlplane.OpenCodeExecutorStatus `json:"opencode_executor"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if body.OpenCodeExecutor.Configured || body.OpenCodeExecutor.Status != "not_configured" || body.OpenCodeExecutor.Error == "" {
		t.Fatalf("opencode executor = %+v", body.OpenCodeExecutor)
	}
}

func TestAPIOpenPilotStateVisibilityAndResume(t *testing.T) {
	store := controlplane.NewStore(nil)
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	api := controlplane.NewAPI(store, controlplane.NewRunner(store, controlplane.MapExecutorRegistry{}))
	api.SetAsyncDispatch(true)
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	seed, err := store.UpsertOpenPilotIssueState(controlplane.OpenPilotIssueState{
		Repo:              "OWNER/REPO",
		Issue:             "123",
		Phase:             controlplane.OpenPilotPhaseRequested,
		GateStatus:        controlplane.OpenPilotGateStatus{"ai_review": "approved", "ci_checks": "pending"},
		ResumeInputs:      map[string]string{"retry_from_step": "open-pilot.ai-review", "retry_from_step_position": "8", "resume.open-pilot.prompt": "prompt"},
		RepairAttempts:    1,
		MaxRepairAttempts: 2,
	})
	if err != nil {
		t.Fatalf("UpsertOpenPilotIssueState() error = %v", err)
	}
	if _, err := store.TransitionOpenPilotIssueState(seed.Repo, seed.Issue, controlplane.OpenPilotPhasePaused); err != nil {
		t.Fatalf("TransitionOpenPilotIssueState() error = %v", err)
	}
	if _, err := store.UpsertOpenPilotIssueState(controlplane.OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "124", Phase: controlplane.OpenPilotPhaseRequested}); err != nil {
		t.Fatalf("UpsertOpenPilotIssueState() other error = %v", err)
	}
	active, err := store.CreateRun(controlplane.CreateRunInput{WorkflowID: "open-pilot", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "124"}})
	if err != nil {
		t.Fatalf("CreateRun active error = %v", err)
	}

	resp, err := http.Get(srv.URL + "/api/open-pilot/states")
	if err != nil {
		t.Fatalf("GET open-pilot states error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var statesBody struct {
		States []controlplane.OpenPilotIssueState `json:"states"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&statesBody); err != nil {
		t.Fatalf("decode states: %v", err)
	}
	if len(statesBody.States) != 2 {
		t.Fatalf("states = %+v", statesBody.States)
	}

	resp, err = http.Get(srv.URL + "/api/open-pilot/state?repo=OWNER/REPO&issue=123")
	if err != nil {
		t.Fatalf("GET open-pilot state error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var stateBody struct {
		State      controlplane.OpenPilotIssueState `json:"state"`
		NextAction string                           `json:"next_action"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&stateBody); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if stateBody.State.Phase != controlplane.OpenPilotPhasePaused || stateBody.NextAction != "Resume" {
		t.Fatalf("state body = %+v", stateBody)
	}

	pauseReq, err := http.NewRequest(http.MethodPost, srv.URL+"/api/open-pilot/state/pause?repo=OWNER/REPO&issue=124", nil)
	if err != nil {
		t.Fatalf("new pause request: %v", err)
	}
	pauseResp, err := http.DefaultClient.Do(pauseReq)
	if err != nil {
		t.Fatalf("POST pause error = %v", err)
	}
	defer pauseResp.Body.Close()
	if pauseResp.StatusCode != http.StatusOK {
		t.Fatalf("pause status = %d", pauseResp.StatusCode)
	}
	pausedRun, err := store.RunDetail(active.ID)
	if err != nil {
		t.Fatalf("RunDetail active error = %v", err)
	}
	if pausedRun.Run.Status != controlplane.RunCanceled {
		t.Fatalf("active run was not canceled on pause: %+v", pausedRun.Run)
	}
	pausedState, err := store.OpenPilotIssueState("OWNER/REPO", "124")
	if err != nil {
		t.Fatalf("OpenPilotIssueState paused error = %v", err)
	}
	if pausedState.ResumeInputs["retry_from_step"] == "" || pausedState.ResumeInputs["retry_from_step_position"] == "" {
		t.Fatalf("pause did not establish resume cursor: %+v", pausedState.ResumeInputs)
	}

	resumeReq, err := http.NewRequest(http.MethodPost, srv.URL+"/api/open-pilot/state/resume?repo=OWNER/REPO&issue=123", nil)
	if err != nil {
		t.Fatalf("new resume request: %v", err)
	}
	resumeResp, err := http.DefaultClient.Do(resumeReq)
	if err != nil {
		t.Fatalf("POST resume error = %v", err)
	}
	defer resumeResp.Body.Close()
	if resumeResp.StatusCode != http.StatusAccepted {
		t.Fatalf("resume status = %d", resumeResp.StatusCode)
	}
	if resumeResp.Header.Get("Location") == "" {
		t.Fatal("resume Location header missing")
	}
	var resumeBody struct {
		State      controlplane.OpenPilotIssueState `json:"state"`
		Run        controlplane.Run                 `json:"run"`
		NextAction string                           `json:"next_action"`
	}
	if err := json.NewDecoder(resumeResp.Body).Decode(&resumeBody); err != nil {
		t.Fatalf("decode resume: %v", err)
	}
	if resumeBody.Run.WorkflowID != "open-pilot" || resumeBody.Run.Inputs["repo"] != "OWNER/REPO" || resumeBody.Run.Inputs["issue"] != "123" || resumeBody.Run.Inputs["retry_from_step"] != "open-pilot.ai-review" || resumeBody.Run.Inputs["retry_from_step_position"] != "8" || resumeBody.Run.Inputs["resume.open-pilot.prompt"] != "prompt" {
		t.Fatalf("resume body = %+v", resumeBody)
	}
	if resumeBody.State.Phase != controlplane.OpenPilotPhasePRCreated {
		t.Fatalf("resume state = %+v", resumeBody)
	}
	if _, err := store.UpsertOpenPilotIssueState(controlplane.OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "125", Phase: controlplane.OpenPilotPhaseRequested}); err != nil {
		t.Fatalf("seed agent completed with test command requested error = %v", err)
	}
	for _, phase := range []controlplane.OpenPilotPhase{controlplane.OpenPilotPhaseValidated, controlplane.OpenPilotPhaseBranchPrepared, controlplane.OpenPilotPhaseAgentCompleted} {
		if _, err := store.TransitionOpenPilotIssueState("OWNER/REPO", "125", phase); err != nil {
			t.Fatalf("transition issue 125 to %s error = %v", phase, err)
		}
	}
	if saved, err := store.OpenPilotIssueState("OWNER/REPO", "125"); err == nil {
		saved.ResumeInputs = map[string]string{"retry_from_step": "open-pilot.stale", "retry_from_step_position": "4", "test_command": "make verify"}
		if _, err := store.UpsertOpenPilotIssueState(saved); err != nil {
			t.Fatalf("update agent completed with test command error = %v", err)
		}
	} else {
		t.Fatalf("OpenPilotIssueState() issue 125 error = %v", err)
	}
	resp, err = http.Post(srv.URL+"/api/open-pilot/state/resume?repo=OWNER/REPO&issue=125", "application/json", nil)
	if err != nil {
		t.Fatalf("POST resume agent completed with test command error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("agent completed with test command status = %d", resp.StatusCode)
	}
	var agentTestBody struct {
		Run controlplane.Run `json:"run"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&agentTestBody); err != nil {
		t.Fatalf("decode agent completed test resume: %v", err)
	}
	if agentTestBody.Run.Inputs["retry_from_step"] != "open-pilot.verify-command" || agentTestBody.Run.Inputs["retry_from_step_position"] != "5" {
		t.Fatalf("agent completed with test command resume = %+v", agentTestBody.Run.Inputs)
	}
	if _, err := store.UpsertOpenPilotIssueState(controlplane.OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "126", Phase: controlplane.OpenPilotPhaseRequested}); err != nil {
		t.Fatalf("seed agent completed without test command requested error = %v", err)
	}
	for _, phase := range []controlplane.OpenPilotPhase{controlplane.OpenPilotPhaseValidated, controlplane.OpenPilotPhaseBranchPrepared, controlplane.OpenPilotPhaseAgentCompleted} {
		if _, err := store.TransitionOpenPilotIssueState("OWNER/REPO", "126", phase); err != nil {
			t.Fatalf("transition issue 126 to %s error = %v", phase, err)
		}
	}
	if saved, err := store.OpenPilotIssueState("OWNER/REPO", "126"); err == nil {
		saved.ResumeInputs = map[string]string{"retry_from_step": "open-pilot.stale", "retry_from_step_position": "4"}
		if _, err := store.UpsertOpenPilotIssueState(saved); err != nil {
			t.Fatalf("update agent completed without test command error = %v", err)
		}
	} else {
		t.Fatalf("OpenPilotIssueState() issue 126 error = %v", err)
	}
	resp, err = http.Post(srv.URL+"/api/open-pilot/state/resume?repo=OWNER/REPO&issue=126", "application/json", nil)
	if err != nil {
		t.Fatalf("POST resume agent completed without test command error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("agent completed without test command status = %d", resp.StatusCode)
	}
	var agentNoTestBody struct {
		Run controlplane.Run `json:"run"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&agentNoTestBody); err != nil {
		t.Fatalf("decode agent completed no test resume: %v", err)
	}
	if agentNoTestBody.Run.Inputs["retry_from_step"] != "open-pilot.push-branch" || agentNoTestBody.Run.Inputs["retry_from_step_position"] != "6" {
		t.Fatalf("agent completed without test command resume = %+v", agentNoTestBody.Run.Inputs)
	}
	if _, err := store.UpsertOpenPilotIssueState(controlplane.OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "127", Phase: controlplane.OpenPilotPhaseRequested}); err != nil {
		t.Fatalf("seed failed verify requested error = %v", err)
	}
	for _, phase := range []controlplane.OpenPilotPhase{controlplane.OpenPilotPhaseValidated, controlplane.OpenPilotPhaseBranchPrepared, controlplane.OpenPilotPhaseAgentCompleted, controlplane.OpenPilotPhaseVerified} {
		if _, err := store.TransitionOpenPilotIssueState("OWNER/REPO", "127", phase); err != nil {
			t.Fatalf("transition issue 127 to %s error = %v", phase, err)
		}
	}
	if saved, err := store.OpenPilotIssueState("OWNER/REPO", "127"); err == nil {
		saved.Phase = controlplane.OpenPilotPhaseFailed
		saved.LastStepName = "open-pilot.verify-command"
		saved.ResumeInputs = map[string]string{"retry_from_step": "open-pilot.agent-work", "retry_from_step_position": "4", "test_command": "make verify"}
		if _, err := store.UpsertOpenPilotIssueState(saved); err != nil {
			t.Fatalf("update failed verify error = %v", err)
		}
	} else {
		t.Fatalf("OpenPilotIssueState() issue 127 error = %v", err)
	}
	resp, err = http.Post(srv.URL+"/api/open-pilot/state/resume?repo=OWNER/REPO&issue=127", "application/json", nil)
	if err != nil {
		t.Fatalf("POST resume failed verify error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("failed verify resume status = %d", resp.StatusCode)
	}
	var failedVerifyBody struct {
		Run controlplane.Run `json:"run"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&failedVerifyBody); err != nil {
		t.Fatalf("decode failed verify resume: %v", err)
	}
	if failedVerifyBody.Run.Inputs["retry_from_step"] != "open-pilot.verify-command" || failedVerifyBody.Run.Inputs["retry_from_step_position"] != "5" {
		t.Fatalf("failed verify resume = %+v", failedVerifyBody.Run.Inputs)
	}
	if _, err := store.UpsertOpenPilotIssueState(controlplane.OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "129", Phase: controlplane.OpenPilotPhaseRequested, PullRequestNum: 42, PullRequestURL: "https://git.example.test/OWNER/REPO/pulls/42", GateStatus: controlplane.OpenPilotGateStatus{"ai_review": "approved", "ci_checks": "pending"}}); err != nil {
		t.Fatalf("seed ai reviewed requested error = %v", err)
	}
	for _, phase := range []controlplane.OpenPilotPhase{controlplane.OpenPilotPhaseValidated, controlplane.OpenPilotPhaseBranchPrepared, controlplane.OpenPilotPhaseAgentCompleted, controlplane.OpenPilotPhaseVerified, controlplane.OpenPilotPhasePushed, controlplane.OpenPilotPhasePRCreated, controlplane.OpenPilotPhaseAIReviewed} {
		if _, err := store.TransitionOpenPilotIssueState("OWNER/REPO", "129", phase); err != nil {
			t.Fatalf("transition issue 129 to %s error = %v", phase, err)
		}
	}
	resp, err = http.Post(srv.URL+"/api/open-pilot/state/resume?repo=OWNER/REPO&issue=129", "application/json", nil)
	if err != nil {
		t.Fatalf("POST resume ai reviewed error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("ai reviewed resume status = %d", resp.StatusCode)
	}
	var aiReviewedBody struct {
		Run controlplane.Run `json:"run"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&aiReviewedBody); err != nil {
		t.Fatalf("decode ai reviewed resume: %v", err)
	}
	if aiReviewedBody.Run.Inputs["retry_from_step"] != "open-pilot.ci-checks" || aiReviewedBody.Run.Inputs["retry_from_step_position"] != "9" || aiReviewedBody.Run.Inputs["resume.open-pilot.pr-plan.pull_request_number"] != "42" {
		t.Fatalf("ai reviewed resume = %+v", aiReviewedBody.Run.Inputs)
	}
	if _, err := store.UpsertOpenPilotIssueState(controlplane.OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "128", Phase: controlplane.OpenPilotPhaseRequested, GateStatus: controlplane.OpenPilotGateStatus{"merge": "merged"}, MergeCommitSHA: "deadbeef"}); err != nil {
		t.Fatalf("seed merged requested error = %v", err)
	}
	for _, phase := range []controlplane.OpenPilotPhase{controlplane.OpenPilotPhaseValidated, controlplane.OpenPilotPhaseBranchPrepared, controlplane.OpenPilotPhaseAgentCompleted, controlplane.OpenPilotPhaseVerified, controlplane.OpenPilotPhasePushed, controlplane.OpenPilotPhasePRCreated, controlplane.OpenPilotPhaseAIReviewed, controlplane.OpenPilotPhaseCIPassed, controlplane.OpenPilotPhaseMergeReady, controlplane.OpenPilotPhaseMerged} {
		if _, err := store.TransitionOpenPilotIssueState("OWNER/REPO", "128", phase); err != nil {
			t.Fatalf("transition issue 128 to %s error = %v", phase, err)
		}
	}
	resp, err = http.Post(srv.URL+"/api/open-pilot/state/resume?repo=OWNER/REPO&issue=128", "application/json", nil)
	if err != nil {
		t.Fatalf("POST resume merged error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("merged resume status = %d", resp.StatusCode)
	}
	var mergedBody struct {
		Run controlplane.Run `json:"run"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&mergedBody); err != nil {
		t.Fatalf("decode merged resume: %v", err)
	}
	if mergedBody.Run.Inputs["retry_from_step"] != "open-pilot.close-issue" || mergedBody.Run.Inputs["retry_from_step_position"] != "11" || mergedBody.Run.Inputs["resume.open-pilot.merge-pr.merge_commit_sha"] != "deadbeef" {
		t.Fatalf("merged resume = %+v", mergedBody.Run.Inputs)
	}
	_, err = store.CreateRun(controlplane.CreateRunInput{WorkflowID: "open-pilot", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "124"}})
	if err != nil {
		t.Fatalf("CreateRun conflict error = %v", err)
	}
	conflictReq, err := http.NewRequest(http.MethodPost, srv.URL+"/api/open-pilot/state/resume?repo=OWNER/REPO&issue=124", nil)
	if err != nil {
		t.Fatalf("new conflict resume request: %v", err)
	}
	conflictResp, err := http.DefaultClient.Do(conflictReq)
	if err != nil {
		t.Fatalf("POST conflict resume error = %v", err)
	}
	defer conflictResp.Body.Close()
	if conflictResp.StatusCode != http.StatusConflict {
		t.Fatalf("conflict resume status = %d", conflictResp.StatusCode)
	}

	resp, err = http.Get(srv.URL + "/api/open-pilot/state?issue=123")
	if err != nil {
		t.Fatalf("GET open-pilot state missing repo error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing repo status = %d", resp.StatusCode)
	}
}

func TestAPIOpenPilotStateMissingQueryReturnsBadRequest(t *testing.T) {
	api := controlplane.NewAPI(controlplane.NewStore(nil), controlplane.NewRunner(controlplane.NewStore(nil)))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/open-pilot/state")
	if err != nil {
		t.Fatalf("GET open-pilot state error = %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("GET open-pilot state status = %d", resp.StatusCode)
	}

	for _, req := range []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/api/open-pilot/state/pause?repo=OWNER/REPO"},
		{method: http.MethodPost, path: "/api/open-pilot/state/resume?issue=123"},
	} {
		request, err := http.NewRequest(req.method, srv.URL+req.path, nil)
		if err != nil {
			t.Fatalf("new request %s %s: %v", req.method, req.path, err)
		}
		resp, err = http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("%s %s error = %v", req.method, req.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s %s status = %d", req.method, req.path, resp.StatusCode)
		}
	}
}

func TestAPIRetryAndCancelRun(t *testing.T) {
	store := controlplane.NewStore(nil)
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	api := controlplane.NewAPI(store, controlplane.NewRunner(store, controlplane.MapExecutorRegistry{}))
	canceler := &fakeCanceler{}
	api.SetCancelDispatcher(canceler)
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	run, err := store.CreateRun(controlplane.CreateRunInput{WorkflowID: "open-pilot", TriggerID: "manual-open-pilot", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/runs/"+run.ID+"/cancel", nil)
	if err != nil {
		t.Fatalf("new cancel request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST cancel error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel status = %d", resp.StatusCode)
	}
	if !canceler.called {
		t.Fatal("canceler was not called")
	}

	req, err = http.NewRequest(http.MethodPost, srv.URL+"/api/runs/"+run.ID+"/retry", nil)
	if err != nil {
		t.Fatalf("new retry request: %v", err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST retry error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("retry status = %d", resp.StatusCode)
	}
	var body struct {
		Run controlplane.Run `json:"run"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode retry: %v", err)
	}
	if body.Run.ID == run.ID || body.Run.TriggerID != run.TriggerID || body.Run.Status != controlplane.RunSucceeded {
		t.Fatalf("retry run = %+v original = %+v", body.Run, run)
	}
}

func TestAPIRunStepsListRetryAndCancel(t *testing.T) {
	store := controlplane.NewStore(nil)
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	run, err := store.CreateRun(controlplane.CreateRunInput{WorkflowID: "devlog"})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	failed, err := store.CreateRunStep(controlplane.CreateRunStepInput{RunID: run.ID, Position: 1, Name: "generate", Executor: "model"})
	if err != nil {
		t.Fatalf("CreateRunStep() error = %v", err)
	}
	if _, err := store.StartRunStep(failed.ID); err != nil {
		t.Fatalf("StartRunStep() error = %v", err)
	}
	if _, err := store.FinishRunStep(failed.ID, controlplane.StepFailed, "", "boom", nil); err != nil {
		t.Fatalf("FinishRunStep() error = %v", err)
	}
	queued, err := store.CreateRunStep(controlplane.CreateRunStepInput{RunID: run.ID, Position: 2, Name: "attach", Executor: "internal"})
	if err != nil {
		t.Fatalf("CreateRunStep() error = %v", err)
	}
	api := controlplane.NewAPI(store, controlplane.NewRunner(store, controlplane.MapExecutorRegistry{}))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/runs/" + run.ID + "/steps")
	if err != nil {
		t.Fatalf("GET steps error = %v", err)
	}
	defer resp.Body.Close()
	var listBody struct {
		Steps []controlplane.RunStep `json:"steps"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listBody); err != nil {
		t.Fatalf("decode steps: %v", err)
	}
	if len(listBody.Steps) != 2 || listBody.Steps[0].ID != failed.ID || listBody.Steps[1].ID != queued.ID {
		t.Fatalf("steps = %+v", listBody.Steps)
	}

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/runs/"+run.ID+"/steps/"+failed.ID+"/retry", nil)
	if err != nil {
		t.Fatalf("new retry request: %v", err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST retry error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("retry status = %d", resp.StatusCode)
	}
	var retryBody struct {
		Run  controlplane.Run     `json:"run"`
		Step controlplane.RunStep `json:"step"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&retryBody); err != nil {
		t.Fatalf("decode retry step: %v", err)
	}
	if retryBody.Run.ID == run.ID || retryBody.Run.Inputs["retry_from_step"] != failed.Name || retryBody.Run.Inputs["retry_from_step_position"] != "1" {
		t.Fatalf("retry body = %+v", retryBody)
	}

	req, err = http.NewRequest(http.MethodPost, srv.URL+"/api/runs/"+run.ID+"/steps/"+queued.ID+"/cancel", nil)
	if err != nil {
		t.Fatalf("new cancel request: %v", err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST cancel error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel status = %d", resp.StatusCode)
	}
}

func TestAPIRetryOpenPilotAIReviewUsesRepairCursor(t *testing.T) {
	store := controlplane.NewStore(nil)
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	run, err := store.CreateRun(controlplane.CreateRunInput{WorkflowID: "open-pilot", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	step, err := store.CreateRunStep(controlplane.CreateRunStepInput{RunID: run.ID, Position: 8, Name: "open-pilot.ai-review", Executor: "model"})
	if err != nil {
		t.Fatalf("CreateRunStep() error = %v", err)
	}
	if _, err := store.StartRunStep(step.ID); err != nil {
		t.Fatalf("StartRunStep() error = %v", err)
	}
	if _, err := store.FinishRunStep(step.ID, controlplane.StepFailed, "AI review requested changes", "ai review requested changes", nil); err != nil {
		t.Fatalf("FinishRunStep() error = %v", err)
	}
	if _, err := store.UpsertOpenPilotIssueState(controlplane.OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "123", Phase: controlplane.OpenPilotPhaseValidated, ResumeInputs: map[string]string{
		"retry_from_step":          "open-pilot.agent-work",
		"retry_from_step_position": "4",
		"ai_review.feedback":       "tighten validation",
	}}); err != nil {
		t.Fatalf("UpsertOpenPilotIssueState() error = %v", err)
	}
	api := controlplane.NewAPI(store, controlplane.NewRunner(store, controlplane.MapExecutorRegistry{}))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/runs/"+run.ID+"/steps/"+step.ID+"/retry", nil)
	if err != nil {
		t.Fatalf("new retry request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST retry error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("retry status = %d", resp.StatusCode)
	}
	var retryBody struct {
		Run controlplane.Run `json:"run"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&retryBody); err != nil {
		t.Fatalf("decode retry step: %v", err)
	}
	if retryBody.Run.Inputs["retry_from_step"] != "open-pilot.agent-work" || retryBody.Run.Inputs["retry_from_step_position"] != "4" || retryBody.Run.Inputs["ai_review.feedback"] != "tighten validation" {
		t.Fatalf("retry inputs = %+v", retryBody.Run.Inputs)
	}
}

func TestAPISchedulerStatusDisabledByDefault(t *testing.T) {
	api := controlplane.NewAPI(controlplane.NewStore(nil), controlplane.NewRunner(controlplane.NewStore(nil)))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/scheduler")
	if err != nil {
		t.Fatalf("GET scheduler error = %v", err)
	}
	defer resp.Body.Close()
	var body controlplane.SchedulerStatus
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode scheduler: %v", err)
	}
	if body.Enabled || body.Running {
		t.Fatalf("scheduler = %+v", body)
	}
}

func TestAPIAsyncDispatchEnqueuesRun(t *testing.T) {
	store := controlplane.NewStore(nil)
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	api.SetAsyncDispatch(true)
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/api/runs", "application/json", bytes.NewBufferString(`{"workflow_id":"devlog"}`))
	if err != nil {
		t.Fatalf("POST runs error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if resp.Header.Get("Location") == "" {
		t.Fatal("Location header missing")
	}
	var body struct {
		Run controlplane.Run `json:"run"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	if body.Run.Status != controlplane.RunQueued || body.Run.StartedAt != nil {
		t.Fatalf("run = %+v", body.Run)
	}
}

func TestAPIWorkerStatusDisabledByDefault(t *testing.T) {
	api := controlplane.NewAPI(controlplane.NewStore(nil), controlplane.NewRunner(controlplane.NewStore(nil)))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/workers")
	if err != nil {
		t.Fatalf("GET workers error = %v", err)
	}
	defer resp.Body.Close()
	var body controlplane.WorkerPoolStatus
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode workers: %v", err)
	}
	if body.Enabled || body.Running {
		t.Fatalf("workers = %+v", body)
	}
}

func TestAPIAdaptersEndpointShape(t *testing.T) {
	t.Setenv("LLAMA_LINE_BASE_URL", "")
	t.Setenv("OCQ_BASE_URL", "")
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

func TestAPIAdaptersProbesOCQWhenConfigured(t *testing.T) {
	ocq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/ocq/sessions":
			if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
				t.Fatalf("Authorization = %q", got)
			}
			_, _ = w.Write([]byte(`{"sessions":[{"id":"ses_alpha","title":"Alpha","status":"active","user":"operator","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:01:00Z"}]}`))
		default:
			t.Fatalf("path = %s", r.URL.Path)
		}
	}))
	t.Cleanup(ocq.Close)
	t.Setenv("LLAMA_LINE_BASE_URL", "")
	t.Setenv("OCQ_BASE_URL", ocq.URL)
	t.Setenv("OCQ_GATEWAY_KEY", "test-key")

	api := controlplane.NewAPI(controlplane.NewStore(nil), controlplane.NewRunner(controlplane.NewStore(nil)))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/adapters")
	if err != nil {
		t.Fatalf("GET adapters error = %v", err)
	}
	defer resp.Body.Close()

	var body controlplane.AdapterPayload
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode adapters: %v", err)
	}
	if len(body.OCQ.Sessions) != 1 || body.OCQ.Sessions[0].ID != "ses_alpha" {
		t.Fatalf("ocq sessions = %+v", body.OCQ.Sessions)
	}
	if len(body.Sessions) != 1 || body.Sessions[0].Surface != "ocq" {
		t.Fatalf("sessions = %+v", body.Sessions)
	}
	if body.OCQ.Settings.Workspace != ocq.URL {
		t.Fatalf("settings = %+v", body.OCQ.Settings)
	}
}

func TestAPIAdaptersProbesLlamaLineWhenConfigured(t *testing.T) {
	llama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/broker/status" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"queue_depth":2,
			"max_depth":25,
			"in_flight_client":"operator",
			"connected_clients":3,
			"upstreams":[{"name":"ocq","url":"http://ocq:8088","healthy":true,"last_probe_at":"2026-01-01T00:00:00Z"}]
		}`))
	}))
	t.Cleanup(llama.Close)
	t.Setenv("LLAMA_LINE_BASE_URL", llama.URL)

	api := controlplane.NewAPI(controlplane.NewStore(nil), controlplane.NewRunner(controlplane.NewStore(nil)))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/adapters")
	if err != nil {
		t.Fatalf("GET adapters error = %v", err)
	}
	defer resp.Body.Close()

	var body controlplane.AdapterPayload
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode adapters: %v", err)
	}
	if len(body.Queues) != 1 || body.Queues[0].ID != "llama-line" || body.Queues[0].Depth != 2 {
		t.Fatalf("queues = %+v", body.Queues)
	}
	if len(body.LlamaLine.Upstreams) != 1 || body.LlamaLine.Upstreams[0].Name != "ocq" || body.LlamaLine.Upstreams[0].Status != "healthy" {
		t.Fatalf("upstreams = %+v", body.LlamaLine.Upstreams)
	}
	if body.LlamaLine.Stats.ActiveRequests != 1 || body.LlamaLine.Stats.QueuedRequests != 2 {
		t.Fatalf("stats = %+v", body.LlamaLine.Stats)
	}
}

func TestAPIAdaptersProbesBlacktowerWhenConfigured(t *testing.T) {
	blacktower := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/snapshot" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"tasks":[{"name":"observe","badges":["daily"],"warnings":[],"latestRun":{"id":"run_bt_1","status":"completed","startedAt":"2026-01-01T00:00:00Z","completedAt":"2026-01-01T00:02:00Z","sessionId":"ses_bt_1"}}],
			"selectedTask":{"report":{"path":"/tmp/report.md","updatedAt":"2026-01-01T00:03:00Z","content":"# Scheduler health\n- no failed tasks\n- queue lag nominal"}},
			"health":{"status":"ok","summary":"scheduler ok","tasks":{"total":1,"enabled":1,"invalid":0},"recentFailureCount":0,"checkedAt":"2026-01-01T00:04:00Z"},
			"schedulerEvents":[{"source":"scheduler","severity":"info","text":"scheduler tick","taskName":"observe","timestamp":"2026-01-01T00:05:00Z"}],
			"generatedAt":"2026-01-01T00:06:00Z"
		}`))
	}))
	t.Cleanup(blacktower.Close)
	t.Setenv("BLACKTOWER_BASE_URL", blacktower.URL)

	api := controlplane.NewAPI(controlplane.NewStore(nil), controlplane.NewRunner(controlplane.NewStore(nil)))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/adapters")
	if err != nil {
		t.Fatalf("GET adapters error = %v", err)
	}
	defer resp.Body.Close()

	var body controlplane.AdapterPayload
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode adapters: %v", err)
	}
	if len(body.Blacktower.Tasks) != 1 || body.Blacktower.Tasks[0].Name != "observe" || body.Blacktower.Tasks[0].Progress != 100 {
		t.Fatalf("blacktower tasks = %+v", body.Blacktower.Tasks)
	}
	if len(body.Blacktower.Reports) != 1 || body.Blacktower.Reports[0].Title != "Scheduler health" {
		t.Fatalf("blacktower reports = %+v", body.Blacktower.Reports)
	}
	if body.Blacktower.SchedulerHealth.Status != "healthy" {
		t.Fatalf("scheduler health = %+v", body.Blacktower.SchedulerHealth)
	}
	if len(body.Sessions) < 2 || body.Sessions[len(body.Sessions)-1].Surface != "blacktower" {
		t.Fatalf("sessions = %+v", body.Sessions)
	}
}

func TestAPIAdaptersProbesChangemakerWhenConfigured(t *testing.T) {
	changemaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/api/runs/status":
			if got := r.Header.Get("Authorization"); got != "Bearer changemaker-token" {
				t.Fatalf("Authorization = %q", got)
			}
			_, _ = w.Write([]byte(`{"state":"running","startedAt":"2026-01-01T00:00:00Z","logs":["started","collecting changes"]}`))
		case "/api/posts":
			if got := r.Header.Get("Authorization"); got != "Bearer changemaker-token" {
				t.Fatalf("Authorization = %q", got)
			}
			_, _ = w.Write([]byte(`[{"fileName":"2026-01-01-post.md","date":"2026-01-01","title":"Field report"}]`))
		default:
			t.Fatalf("path = %s", r.URL.Path)
		}
	}))
	t.Cleanup(changemaker.Close)
	t.Setenv("CHANGEMAKER_BASE_URL", changemaker.URL)
	t.Setenv("CHANGEMAKER_ADMIN_TOKEN", "changemaker-token")

	api := controlplane.NewAPI(controlplane.NewStore(nil), controlplane.NewRunner(controlplane.NewStore(nil)))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/adapters")
	if err != nil {
		t.Fatalf("GET adapters error = %v", err)
	}
	defer resp.Body.Close()

	var body controlplane.AdapterPayload
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode adapters: %v", err)
	}
	if !body.Changemaker.Service.Healthy || body.Changemaker.Service.Status != "healthy" {
		t.Fatalf("changemaker service = %+v", body.Changemaker.Service)
	}
	if body.Changemaker.RunControl.Mode != "running" || body.Changemaker.RunControl.ActiveRunID == "" {
		t.Fatalf("run control = %+v", body.Changemaker.RunControl)
	}
	if len(body.Changemaker.Posts) != 1 || body.Changemaker.Posts[0].Title != "Field report" {
		t.Fatalf("posts = %+v", body.Changemaker.Posts)
	}
}
