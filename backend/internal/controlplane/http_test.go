package controlplane_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

func TestAPIWorkflowDefinitionActivationVersionsAndRollback(t *testing.T) {
	store := controlplane.NewStore(nil)
	if err := store.UpsertWorkflow(controlplane.Workflow{ID: "versioned", Kind: "versioned", Name: "Versioned"}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(controlplane.NewAPI(store, controlplane.NewRunner(store)).Handler())
	t.Cleanup(srv.Close)
	put := func(name, summary string) controlplane.WorkflowDefinition {
		body := `{"name":"` + name + `","status":"draft","pipeline":{"id":"versioned-pipeline","workflow_id":"versioned","name":"Versioned Pipeline","steps":[{"id":"prepare","position":1,"name":"versioned.prepare","executor":"internal","inputs":{"summary":"` + summary + `"}}]}}`
		req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/workflow-definitions/versioned", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT status = %d body=%s", resp.StatusCode, mustReadBody(t, resp.Body))
		}
		var envelope struct {
			Definition controlplane.WorkflowDefinition `json:"workflow_definition"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Definition
	}
	first := put("First", "first")
	if first.Version != 1 || first.Status != "draft" {
		t.Fatalf("first = %+v", first)
	}
	activate, err := http.NewRequest(http.MethodPost, srv.URL+"/api/workflow-definitions/versioned/activate", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(activate)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("activate status = %d", resp.StatusCode)
	}
	second := put("Second", "second")
	if second.Version != 2 || second.Status != "draft" {
		t.Fatalf("second = %+v", second)
	}
	rollback, err := http.NewRequest(http.MethodPost, srv.URL+"/api/workflow-definitions/versioned/rollback", strings.NewReader(`{"version":1}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err = http.DefaultClient.Do(rollback)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rollback status = %d body=%s", resp.StatusCode, mustReadBody(t, resp.Body))
	}
	var rollbackEnvelope struct {
		Definition controlplane.WorkflowDefinition `json:"workflow_definition"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rollbackEnvelope); err != nil {
		t.Fatal(err)
	}
	if rollbackEnvelope.Definition.Version != 3 || rollbackEnvelope.Definition.Status != "active" || rollbackEnvelope.Definition.Name != "First" {
		t.Fatalf("rollback = %+v", rollbackEnvelope.Definition)
	}
	versions, err := store.ListWorkflowDefinitionVersions("versioned")
	if err != nil || len(versions) != 3 {
		t.Fatalf("versions = %+v err=%v", versions, err)
	}
}

func TestAPIModelBrokerObservabilitySummarizesLedger(t *testing.T) {
	store := controlplane.NewStore(time.Now)
	req, err := store.CreateModelBrokerRequest(controlplane.CreateModelBrokerRequestInput{ID: "req-1", Model: "qwen3.5:latest", Path: "/api/generate", Client: "test", RequestBody: `{"prompt":"redacted"}`})
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	if _, err := store.StartModelBrokerRequest(req.ID); err != nil {
		t.Fatalf("start request: %v", err)
	}
	if _, err := store.FinishModelBrokerRequest(req.ID, controlplane.FinishModelBrokerRequestInput{Status: controlplane.ModelBrokerRequestCompleted, ResponseBody: `{"response":"redacted"}`, CorrelationIDs: map[string]string{"trace_id": "abc"}}); err != nil {
		t.Fatalf("finish request: %v", err)
	}
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/observability/model-broker")
	if err != nil {
		t.Fatalf("GET observability error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.StatusCode, mustReadBody(t, resp.Body))
	}
	body := mustReadBody(t, resp.Body)
	if !strings.Contains(body, "req-1") || !strings.Contains(body, "completed") || !strings.Contains(body, "trace_id") {
		t.Fatalf("observability body missing summary fields: %s", body)
	}
	if strings.Contains(body, "request_body") || strings.Contains(body, "prompt") || strings.Contains(body, "response_body") {
		t.Fatalf("observability body exposed request or response body: %s", body)
	}
}

func TestAPIModelCatalogListsOllamaAndOpenCodeModels(t *testing.T) {
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Fatalf("ollama path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"models":[{"model":"qwen3.5:latest","name":"qwen3.5:latest"}]}`))
	}))
	t.Cleanup(ollama.Close)
	opencode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/config/providers" {
			t.Fatalf("opencode path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"providers":[{"id":"openai","name":"OpenAI","source":"api","key":"secret-key-must-not-leak","models":{"gpt-5.5":{"id":"gpt-5.5","name":"GPT 5.5","providerID":"openai"}}}]}`))
	}))
	t.Cleanup(opencode.Close)
	t.Setenv("OLLAMA_BASE_URL", ollama.URL)
	t.Setenv("OPENCODE_SERVER_URL", opencode.URL)

	api := controlplane.NewAPI(controlplane.NewStore(nil), controlplane.NewRunner(controlplane.NewStore(nil)))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/models")
	if err != nil {
		t.Fatalf("GET /api/models error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.StatusCode, mustReadBody(t, resp.Body))
	}
	body := mustReadBody(t, resp.Body)
	if !strings.Contains(body, "qwen3.5:latest") || !strings.Contains(body, "gpt-5.5") {
		t.Fatalf("body missing models: %s", body)
	}
	if strings.Contains(body, "secret-key-must-not-leak") {
		t.Fatalf("body leaked provider secret: %s", body)
	}
}

func TestAPIModelPullPullsOllamaModels(t *testing.T) {
	var pulled string
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/pull" || r.Method != http.MethodPost {
			t.Fatalf("ollama request = %s %s", r.Method, r.URL.Path)
		}
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode pull body: %v", err)
		}
		pulled, _ = input["name"].(string)
		_, _ = w.Write([]byte(`{"status":"success"}`))
	}))
	t.Cleanup(ollama.Close)
	t.Setenv("OLLAMA_BASE_URL", ollama.URL)

	api := controlplane.NewAPI(controlplane.NewStore(nil), controlplane.NewRunner(controlplane.NewStore(nil)))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/api/models/pull", "application/json", strings.NewReader(`{"models":["ollama/qwen3.5:latest"]}`))
	if err != nil {
		t.Fatalf("POST /api/models/pull error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.StatusCode, mustReadBody(t, resp.Body))
	}
	if pulled != "qwen3.5:latest" {
		t.Fatalf("pulled = %q", pulled)
	}
}

func TestAPIPreservesOllamaToolCalls(t *testing.T) {
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" || r.Method != http.MethodPost {
			t.Fatalf("ollama request = %s %s", r.Method, r.URL.Path)
		}
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode ollama request: %v", err)
		}
		if _, ok := input["tools"].([]any); !ok {
			t.Fatalf("tools were not forwarded: %#v", input)
		}
		if options, ok := input["options"].(map[string]any); !ok || options["temperature"] == nil {
			t.Fatalf("options were not forwarded: %#v", input)
		}
		_, _ = w.Write([]byte(`{"model":"qwen3:14b","message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","function":{"name":"add_two_numbers","arguments":{"a":41,"b":1}}}]},"done":true}`))
	}))
	t.Cleanup(ollama.Close)
	t.Setenv("OLLAMA_BASE_URL", ollama.URL)

	api := controlplane.NewAPI(controlplane.NewStore(nil), controlplane.NewRunner(controlplane.NewStore(nil)))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	body := `{"model":"ollama/qwen3:14b","stream":false,"tools":[{"type":"function","function":{"name":"add_two_numbers","parameters":{"type":"object"}}}],"messages":[{"role":"user","content":"Use add_two_numbers to add 41 and 1."}],"options":{"temperature":0}}`
	resp, err := http.Post(srv.URL+"/api/chat", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/chat error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.StatusCode, mustReadBody(t, resp.Body))
	}
	responseBody := mustReadBody(t, resp.Body)
	if !strings.Contains(responseBody, "tool_calls") || !strings.Contains(responseBody, "add_two_numbers") {
		t.Fatalf("response did not preserve tool calls: %s", responseBody)
	}
}

func TestAPIOpenAIChatCompletionsConvertsOllamaToolCalls(t *testing.T) {
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"qwen3:14b","message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","function":{"name":"add_two_numbers","arguments":{"a":41,"b":1}}}]},"done":true}`))
	}))
	t.Cleanup(ollama.Close)
	t.Setenv("OLLAMA_BASE_URL", ollama.URL)

	api := controlplane.NewAPI(controlplane.NewStore(nil), controlplane.NewRunner(controlplane.NewStore(nil)))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	body := `{"model":"ollama/qwen3:14b","tools":[{"type":"function","function":{"name":"add_two_numbers","parameters":{"type":"object"}}}],"messages":[{"role":"user","content":"Use add_two_numbers to add 41 and 1."}]}`
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /v1/chat/completions error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body = %s", resp.StatusCode, mustReadBody(t, resp.Body))
	}
	responseBody := mustReadBody(t, resp.Body)
	for _, want := range []string{"tool_calls", "finish_reason\":\"tool_calls", "add_two_numbers", "\\\"a\\\":41"} {
		if !strings.Contains(responseBody, want) {
			t.Fatalf("response missing %q: %s", want, responseBody)
		}
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

	resp, err := http.Post(srv.URL+"/api/workflows/open-pilot/run", "application/json", bytes.NewBufferString(`{"repo":"OWNER/REPO","issue":"123","test_command":" make verify ","base_branch":" release "}`))
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
	if body.Run.WorkflowID != "open-pilot" || body.Run.Inputs["repo"] != "owner/repo" || body.Run.Inputs["issue"] != "123" || body.Run.Inputs["test_command"] != "make verify" || body.Run.Inputs["base_branch"] != "release" {
		t.Fatalf("run = %+v", body.Run)
	}
}

func TestAPIRunCreationRejectsCIRepairContextInjection(t *testing.T) {
	store := controlplane.NewStore(nil)
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	api := controlplane.NewAPI(store, controlplane.NewRunner(store, controlplane.MapExecutorRegistry{}))
	api.SetAsyncDispatch(true)
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/api/runs", "application/json", strings.NewReader(`{"workflow_id":"open-pilot","inputs":{"repo":"OWNER/REPO","issue":"123","resume.open-pilot.ci-repair.feedback":"forged"}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || len(store.ListRuns()) != 0 {
		t.Fatalf("status/runs = %d/%+v", resp.StatusCode, store.ListRuns())
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

type fakeCanceler struct {
	called bool
	calls  int
}

func (f *fakeCanceler) Cancel(context.Context, controlplane.Run) controlplane.CancelResult {
	f.called = true
	f.calls++
	return controlplane.CancelResult{Events: []controlplane.WorkflowExecutionEvent{{Type: "fake.cancel", Message: "called"}}}
}

func TestAPIStepCancellationDispatchesUpstreamOnceAndTerminalizesRun(t *testing.T) {
	store := controlplane.NewStore(nil)
	if err := store.UpsertWorkflow(controlplane.Workflow{ID: "test", Kind: "test", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(controlplane.CreateRunInput{WorkflowID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	step, err := store.CreateRunStep(controlplane.CreateRunStepInput{RunID: run.ID, Position: 1, Name: "test.agent", Executor: "opencode", CorrelationIDs: map[string]string{"opencode_session_id": "ses-1"}})
	if err != nil {
		t.Fatal(err)
	}
	canceler := &fakeCanceler{}
	api := controlplane.NewAPI(store, controlplane.NewRunner(store, controlplane.MapExecutorRegistry{}))
	api.SetCancelDispatcher(canceler)
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	for attempt := 0; attempt < 2; attempt++ {
		request, err := http.NewRequest(http.MethodPost, srv.URL+"/api/runs/"+run.ID+"/steps/"+step.ID+"/cancel", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("attempt %d status = %d", attempt, response.StatusCode)
		}
	}
	if canceler.calls != 1 {
		t.Fatalf("upstream cancellation calls = %d, want 1", canceler.calls)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Run.Status != controlplane.RunCanceled || detail.Steps[0].Status != controlplane.StepCanceled {
		t.Fatalf("detail = %+v", detail)
	}
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
	if len(body.Profiles) != 4 || body.Profiles[0].ID != "content-writer" || len(body.Profiles[0].AllowedExecutors) == 0 {
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
	messagePosted := false
	opencode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/session":
			if r.Method == http.MethodGet {
				_ = json.NewEncoder(w).Encode([]map[string]string{{"id": "session-1", "title": "compat"}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "session-1"})
		case r.URL.Path == "/session/session-1/message" && r.Method == http.MethodPost:
			messagePosted = true
			_ = json.NewEncoder(w).Encode(map[string]any{"parts": []map[string]string{{"type": "text", "text": "native answer"}}})
		case r.URL.Path == "/session/session-1/message" && r.Method == http.MethodGet:
			if !messagePosted {
				_ = json.NewEncoder(w).Encode([]map[string]any{{"info": map[string]string{"id": "before", "role": "assistant", "finish": "stop"}}})
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"info": map[string]string{"id": "after", "role": "assistant", "finish": "stop"}, "parts": []map[string]string{{"type": "text", "text": "native answer"}}}})
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

func TestAPILlamaLineCompatChatCompletionsForwardsInlineImagesToOllama(t *testing.T) {
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Fatalf("unexpected ollama path %s", r.URL.Path)
		}
		var input struct {
			Model    string         `json:"model"`
			Format   string         `json:"format"`
			Think    *bool          `json:"think"`
			Options  map[string]any `json:"options"`
			Messages []struct {
				Role    string   `json:"role"`
				Content string   `json:"content"`
				Images  []string `json:"images"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode ollama request: %v", err)
		}
		if input.Model != "gemma4:latest" || len(input.Messages) != 1 {
			t.Fatalf("input = %+v", input)
		}
		if input.Format != "json" || input.Options["num_predict"] != float64(128) || input.Options["temperature"] != 0.2 {
			t.Fatalf("format/options = %q/%+v", input.Format, input.Options)
		}
		if input.Think == nil || *input.Think {
			t.Fatalf("think = %v, want false", input.Think)
		}
		message := input.Messages[0]
		if message.Role != "user" || message.Content != "Classify this thumbnail." || len(message.Images) != 1 || message.Images[0] != "aGVsbG8=" {
			t.Fatalf("message = %+v", message)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]string{"role": "assistant", "content": `{"tags":[]}`},
			"done":    true,
		})
	}))
	t.Cleanup(ollama.Close)
	t.Setenv("OLLAMA_BASE_URL", ollama.URL)

	store := controlplane.NewStore(nil)
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	payload := `{"model":"ollama/gemma4:latest","messages":[{"role":"user","content":[{"type":"text","text":"Classify this thumbnail."},{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,aGVsbG8=","detail":"low"}}]}],"response_format":{"type":"json_object"},"max_tokens":128,"temperature":0.2}`
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("post multimodal compat chat: %v", err)
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
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Choices) != 1 || body.Choices[0].Message.Content != `{"tags":[]}` {
		t.Fatalf("body = %+v", body)
	}
}

func TestAPILlamaLineCompatChatCompletionsRejectsRemoteImageURLs(t *testing.T) {
	store := controlplane.NewStore(nil)
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	payload := `{"model":"ollama/gemma4:latest","messages":[{"role":"user","content":[{"type":"text","text":"Classify this thumbnail."},{"type":"image_url","image_url":{"url":"https://example.com/image.jpg"}}]}]}`
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("post remote-image compat chat: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %s, want 400", resp.Status)
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

func TestAPILlamaLineCompatDuplicateRequestReplaysWithoutGeneration(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"role": "assistant", "content": "original"}, "done": true})
	}))
	t.Cleanup(ollama.Close)
	t.Setenv("OLLAMA_BASE_URL", ollama.URL)

	store := controlplane.NewStore(nil)
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	request := func() (*http.Response, error) {
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", bytes.NewBufferString(`{"model":"ollama/llama3.2","messages":[{"role":"user","content":"hello"}]}`))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Request-ID", "duplicate-sync-1")
		return http.DefaultClient.Do(req)
	}
	firstResult := make(chan *http.Response, 1)
	firstErr := make(chan error, 1)
	go func() {
		resp, err := request()
		firstResult <- resp
		firstErr <- err
	}()
	<-started

	duplicate, err := request()
	if err != nil {
		t.Fatal(err)
	}
	defer duplicate.Body.Close()
	if duplicate.StatusCode != http.StatusConflict || duplicate.Header.Get("Location") != "/api/requests/duplicate-sync-1/result" {
		t.Fatalf("duplicate status=%d location=%q", duplicate.StatusCode, duplicate.Header.Get("Location"))
	}
	var replay struct {
		ID       string `json:"id"`
		Status   string `json:"status"`
		Replayed bool   `json:"replayed"`
	}
	if err := json.NewDecoder(duplicate.Body).Decode(&replay); err != nil || replay.ID != "duplicate-sync-1" || replay.Status != controlplane.ModelBrokerRequestRunning || !replay.Replayed {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	select {
	case <-started:
		t.Fatal("duplicate reached upstream generator")
	default:
	}
	close(release)
	first := <-firstResult
	if err := <-firstErr; err != nil {
		t.Fatal(err)
	}
	defer first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("original status=%s body=%s", first.Status, mustReadBody(t, first.Body))
	}
	stored, err := store.ModelBrokerRequest("duplicate-sync-1")
	if err != nil || stored.Status != controlplane.ModelBrokerRequestCompleted || !strings.Contains(stored.ResponseBody, "original") {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
}

func TestAPILlamaLineCompatStreamingUsesNativeSSEChunksAndStatusEvents(t *testing.T) {
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"stream "},"done":false}` + "\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"answer"},"done":true}` + "\n"))
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
	if resp.Header.Get("X-OCQ-Streaming-Mode") != "native" || resp.Header.Get("X-OCQ-Status") != "running" {
		t.Fatalf("stream headers = mode:%q status:%q", resp.Header.Get("X-OCQ-Streaming-Mode"), resp.Header.Get("X-OCQ-Status"))
	}
	body := mustReadBody(t, resp.Body)
	for _, want := range []string{`"object":"chat.completion.chunk"`, `"content":"stream "`, `"content":"answer"`, "data: [DONE]"} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream body missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "event: status") || strings.Contains(body, `"status":"queued"`) {
		t.Fatalf("OpenAI-compatible stream contains nonstandard lifecycle events: %s", body)
	}
}

type cancellationStreamGenerator struct {
	started chan struct{}
}

func (g *cancellationStreamGenerator) Generate(context.Context, controlplane.ModelRequest) (controlplane.ModelResponse, error) {
	return controlplane.ModelResponse{}, errors.New("unexpected buffered generation")
}

func (g *cancellationStreamGenerator) Stream(ctx context.Context, request controlplane.ModelRequest, emit func(controlplane.ModelStreamEvent) error) (controlplane.ModelResponse, error) {
	if err := request.PreAttempt(controlplane.BackendDecision{Backend: "test"}); err != nil {
		return controlplane.ModelResponse{}, err
	}
	if err := emit(controlplane.ModelStreamEvent{Type: "content", Content: "before cancel"}); err != nil {
		return controlplane.ModelResponse{}, err
	}
	close(g.started)
	<-ctx.Done()
	return controlplane.ModelResponse{}, ctx.Err()
}

func TestAPILlamaLineCompatStreamingCancellationUsesCanceledEnvelope(t *testing.T) {
	store := controlplane.NewStore(nil)
	generator := &cancellationStreamGenerator{started: make(chan struct{})}
	service := controlplane.NewModelService(store, generator, controlplane.ModelServiceOptions{DisableStart: true})
	t.Cleanup(func() { _ = service.Close(context.Background()) })
	api := controlplane.NewAPIWithModelService(store, controlplane.NewRunner(store), service)
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", bytes.NewBufferString(`{"model":"test","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "compat-stream-cancel")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	select {
	case <-generator.started:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not start")
	}
	if _, err := service.Cancel(t.Context(), "compat-stream-cancel"); err != nil {
		t.Fatal(err)
	}
	body := mustReadBody(t, resp.Body)
	for _, want := range []string{`"status":"canceled"`, `"error":"request canceled"`, "data: [DONE]"} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream body missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, context.Canceled.Error()) {
		t.Fatalf("stream exposed raw cancellation error: %s", body)
	}
	stored, err := service.Get(t.Context(), "compat-stream-cancel")
	if err != nil || stored.Status != controlplane.ModelBrokerRequestCanceled {
		t.Fatalf("stored=%+v err=%v", stored, err)
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

func TestAPILlamaLineCompatAsyncRequestPersistsAndPollsResult(t *testing.T) {
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"role": "assistant", "content": "async answer"}, "done": true})
	}))
	t.Cleanup(ollama.Close)
	t.Setenv("OLLAMA_BASE_URL", ollama.URL)

	store := controlplane.NewStore(nil)
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/generate?async=true", bytes.NewBufferString(`{"model":"ollama/llama3.2","prompt":"hello"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "async-ledger-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST async generate: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("async status = %s", resp.Status)
	}
	if resp.Header.Get("Location") != "/api/requests/async-ledger-1/result" {
		t.Fatalf("Location = %q", resp.Header.Get("Location"))
	}

	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		resultResp, err := http.Get(srv.URL + "/api/requests/async-ledger-1/result")
		if err != nil {
			t.Fatalf("GET result: %v", err)
		}
		body := mustReadBody(t, resultResp.Body)
		resultResp.Body.Close()
		if resultResp.StatusCode == http.StatusOK {
			if !strings.Contains(body, "async answer") {
				t.Fatalf("result body = %s", body)
			}
			return
		}
		if resultResp.StatusCode != http.StatusAccepted {
			t.Fatalf("result status = %s body = %s", resultResp.Status, body)
		}
	}
	t.Fatal("async result did not complete")
}

func TestAPILlamaLineCompatRecoversQueuedAsyncRequestOnStartup(t *testing.T) {
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"role": "assistant", "content": "recovered answer"}, "done": true})
	}))
	t.Cleanup(ollama.Close)
	t.Setenv("OLLAMA_BASE_URL", ollama.URL)

	store := controlplane.NewStore(nil)
	_, err := store.CreateModelBrokerRequest(controlplane.CreateModelBrokerRequestInput{ID: "recover-queued-1", Model: "ollama/llama3.2", Path: "/api/generate", Client: "test", RequestBody: `{"model":"ollama/llama3.2","messages":[{"role":"user","content":"recover"}],"path":"/api/generate","async":true}`})
	if err != nil {
		t.Fatalf("create queued request: %v", err)
	}
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		resultResp, err := http.Get(srv.URL + "/api/requests/recover-queued-1/result")
		if err != nil {
			t.Fatalf("GET result: %v", err)
		}
		body := mustReadBody(t, resultResp.Body)
		resultResp.Body.Close()
		if resultResp.StatusCode == http.StatusOK {
			if !strings.Contains(body, "recovered answer") {
				t.Fatalf("result body = %s", body)
			}
			return
		}
	}
	t.Fatal("recovered queued request did not complete")
}

func TestAPILlamaLineCompatIdentifiesClientFromBearerKey(t *testing.T) {
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"response": "hello", "done": true})
	}))
	t.Cleanup(ollama.Close)
	t.Setenv("OLLAMA_BASE_URL", ollama.URL)
	t.Setenv("MODEL_BROKER_CLIENT_KEYS", "subcorp=subcorp-secret:50")

	store := controlplane.NewStore(nil)
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/generate", strings.NewReader(`{"model":"ollama/llama3.2","prompt":"hello"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer subcorp-secret")
	req.Header.Set("X-Request-ID", "client-key-request")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/generate: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %s", resp.Status)
	}

	row, err := store.ModelBrokerRequest("client-key-request")
	if err != nil {
		t.Fatalf("model broker request: %v", err)
	}
	if row.Client != "subcorp" || row.ClientPriority != 50 {
		t.Fatalf("client identity = %q/%d, want subcorp/50", row.Client, row.ClientPriority)
	}
}

func TestModelBrokerQueueClaimsByPriority(t *testing.T) {
	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if _, err := store.CreateModelBrokerRequest(controlplane.CreateModelBrokerRequestInput{ID: "low", Model: "qwen3:14b", Path: "/api/generate", Client: "low-client", Priority: 1, RequestBody: `{"prompt":"low"}`}); err != nil {
		t.Fatalf("create low: %v", err)
	}
	if _, err := store.CreateModelBrokerRequest(controlplane.CreateModelBrokerRequestInput{ID: "high", Model: "qwen3:14b", Path: "/api/generate", Client: "high-client", Priority: 99, RequestBody: `{"prompt":"high"}`}); err != nil {
		t.Fatalf("create high: %v", err)
	}
	claimed, err := store.ClaimNextModelBrokerRequest()
	if err != nil {
		t.Fatalf("ClaimNextModelBrokerRequest() error = %v", err)
	}
	if claimed.ID != "high" || claimed.ClientPriority != 99 || claimed.Status != controlplane.ModelBrokerRequestRunning {
		t.Fatalf("claimed = %+v, want high priority running request", claimed)
	}
}

func TestModelBrokerAsyncQueueRejectsRequestsAtCapacity(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
		_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"role": "assistant", "content": "ok"}, "done": true})
	}))
	t.Cleanup(func() {
		close(release)
		ollama.Close()
	})
	t.Setenv("OLLAMA_BASE_URL", ollama.URL)
	t.Setenv("MODEL_BROKER_QUEUE_MAX_DEPTH", "1")
	store := controlplane.NewStore(nil)
	srv := httptest.NewServer(controlplane.NewAPI(store, controlplane.NewRunner(store)).Handler())
	t.Cleanup(srv.Close)

	submit := func(id string) *http.Response {
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/generate?async=true", strings.NewReader(`{"model":"ollama/llama3.2","prompt":"hello"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Request-ID", id)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	first := submit("capacity-first")
	first.Body.Close()
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("first status = %d", first.StatusCode)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first queued request did not start")
	}
	second := submit("capacity-second")
	defer second.Body.Close()
	if second.StatusCode != http.StatusTooManyRequests || second.Header.Get("Retry-After") == "" {
		t.Fatalf("second status = %d Retry-After=%q", second.StatusCode, second.Header.Get("Retry-After"))
	}
}

func TestAPILlamaLineCompatRecoveryLeavesFreshRunningRequest(t *testing.T) {
	t.Setenv("MODEL_BROKER_RECOVERY_STALE_AFTER", "1000000h")
	store := controlplane.NewStore(nil)
	req, err := store.CreateModelBrokerRequest(controlplane.CreateModelBrokerRequestInput{ID: "fresh-running", Model: "qwen3.5:latest", Path: "/api/generate", Client: "test", RequestBody: `{"model":"qwen3.5:latest","messages":[{"role":"user","content":"hello"}]}`})
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	if _, err := store.StartModelBrokerRequest(req.ID); err != nil {
		t.Fatalf("start request: %v", err)
	}

	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	row, err := store.ModelBrokerRequest(req.ID)
	if err != nil {
		t.Fatalf("load request: %v", err)
	}
	if row.Status != controlplane.ModelBrokerRequestRunning {
		t.Fatalf("status = %s", row.Status)
	}
}

func TestAPIModelBrokerFeedbackIsPersistedAndRedacted(t *testing.T) {
	store := controlplane.NewStore(nil)
	if _, err := store.CreateModelBrokerRequest(controlplane.CreateModelBrokerRequestInput{ID: "feedback-request", Model: "test", RequestBody: `{}`}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(controlplane.NewAPI(store, controlplane.NewRunner(store)).Handler())
	t.Cleanup(srv.Close)
	resp, err := http.Post(srv.URL+"/admin/feedback", "application/json", strings.NewReader(`{"request_id":"feedback-request","rating":1,"comment":"Bearer super-secret-token"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST status = %d", resp.StatusCode)
	}
	resp, err = http.Get(srv.URL + "/admin/feedback")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := mustReadBody(t, resp.Body)
	if !strings.Contains(body, "feedback-request") || !strings.Contains(body, "[REDACTED]") || strings.Contains(body, "super-secret-token") {
		t.Fatalf("feedback body = %s", body)
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
	target := controlplane.OpenPilotTarget{RepositoryID: "legacy", RepositorySlug: "owner/repo", Issue: "123", ExecutorGeneration: "legacy"}
	_, prior, err := store.ClaimAndCreateOpenPilotInitialRun("OWNER/REPO", "123", controlplane.CreateRunInput{WorkflowID: "open-pilot", OpenPilotTarget: &target})
	if err != nil {
		t.Fatalf("CreateRun prior error = %v", err)
	}
	if _, err := store.FinishRun(prior.ID, controlplane.RunFailed, "CI failed"); err != nil {
		t.Fatalf("FinishRun prior error = %v", err)
	}

	seed, err := store.UpsertOpenPilotIssueState(controlplane.OpenPilotIssueState{
		Repo:           "OWNER/REPO",
		Issue:          "123",
		Phase:          controlplane.OpenPilotPhaseRequested,
		RunID:          prior.ID,
		LastStepName:   "open-pilot.ai-review",
		PullRequestURL: "https://gitea.test/owner/repo/pulls/1",
		PullRequestID:  1,
		PullRequestNum: 1,
		GateStatus:     controlplane.OpenPilotGateStatus{"ai_review": "approved", "ci_checks": "pending"},
		ResumeInputs: map[string]string{
			"retry_from_step": "open-pilot.ai-review", "retry_from_step_position": "9", "resume.open-pilot.prompt": "prompt",
			"resume.open-pilot.ci-repair.count": "1", "resume.open-pilot.ci-repair.feedback": "verify failed", "resume.open-pilot.ci-repair.original_prompt": "original task",
			"resume.open-pilot.ci-repair.parent_run_id": "parent", "resume.open-pilot.ci-repair.repo": "control-repo", "resume.open-pilot.ci-repair.pull_request_id": "99",
		},
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
	if resumeBody.Run.WorkflowID != "open-pilot" || resumeBody.Run.Inputs["repo"] != "owner/repo" || resumeBody.Run.Inputs["issue"] != "123" || resumeBody.Run.Inputs["retry_from_step"] != "open-pilot.ai-review" || resumeBody.Run.Inputs["retry_from_step_position"] != "9" || resumeBody.Run.Inputs["resume.open-pilot.prompt"] != "prompt" {
		t.Fatalf("resume body = %+v", resumeBody)
	}
	if resumeBody.Run.Inputs["resume.open-pilot.ci-repair.count"] != "1" || resumeBody.Run.Inputs["resume.open-pilot.ci-repair.feedback"] != "verify failed" || resumeBody.Run.Inputs["resume.open-pilot.ci-repair.original_prompt"] != "original task" {
		t.Fatalf("resume repair context = %+v", resumeBody.Run.Inputs)
	}
	for _, key := range []string{"resume.open-pilot.ci-repair.parent_run_id", "resume.open-pilot.ci-repair.repo", "resume.open-pilot.ci-repair.pull_request_id"} {
		if resumeBody.Run.Inputs[key] != "" {
			t.Fatalf("resume retained control key %q: %+v", key, resumeBody.Run.Inputs)
		}
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
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("agent completed with test command status = %d, want 400", resp.StatusCode)
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
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("agent completed without test command status = %d, want 400", resp.StatusCode)
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
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("failed verify resume status = %d, want 400", resp.StatusCode)
	}
	aiTarget := controlplane.OpenPilotTarget{RepositoryID: "legacy", RepositorySlug: "owner/repo", Issue: "129", ExecutorGeneration: "legacy"}
	_, priorAI, err := store.ClaimAndCreateOpenPilotInitialRun("OWNER/REPO", "129", controlplane.CreateRunInput{WorkflowID: "open-pilot", OpenPilotTarget: &aiTarget})
	if err != nil {
		t.Fatalf("CreateRun prior AI error = %v", err)
	}
	if _, err := store.FinishRun(priorAI.ID, controlplane.RunFailed, "CI failed"); err != nil {
		t.Fatalf("FinishRun prior AI error = %v", err)
	}
	if _, err := store.UpsertOpenPilotIssueState(controlplane.OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "129", Phase: controlplane.OpenPilotPhaseRequested, RunID: priorAI.ID, PullRequestID: 42, PullRequestNum: 42, PullRequestURL: "https://git.example.test/OWNER/REPO/pulls/42", GateStatus: controlplane.OpenPilotGateStatus{"ai_review": "approved", "ci_checks": "pending"}}); err != nil {
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
	if aiReviewedBody.Run.Inputs["retry_from_step"] != "open-pilot.ci-checks" || aiReviewedBody.Run.Inputs["retry_from_step_position"] != "10" || aiReviewedBody.Run.Inputs["resume.open-pilot.pr-plan.pull_request_number"] != "42" {
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
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("merged resume status = %d, want 400", resp.StatusCode)
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
	if conflictResp.StatusCode != http.StatusBadRequest {
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

func TestAPIOpenPilotLifecycleResolvesCanonicalLegacyIdentity(t *testing.T) {
	store := controlplane.NewStore(nil)
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	api := controlplane.NewAPI(store, controlplane.NewRunner(store, controlplane.MapExecutorRegistry{}))
	api.SetAsyncDispatch(true)
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	seed := func(repo, issue string, phase controlplane.OpenPilotPhase, runID string) controlplane.OpenPilotIssueState {
		t.Helper()
		state, err := store.UpsertOpenPilotIssueState(controlplane.OpenPilotIssueState{Repo: repo, Issue: issue, Phase: controlplane.OpenPilotPhaseRequested, RunID: runID})
		if err != nil {
			t.Fatal(err)
		}
		if phase != controlplane.OpenPilotPhaseRequested {
			state, err = store.TransitionOpenPilotIssueState(repo, issue, phase)
			if err != nil {
				t.Fatal(err)
			}
		}
		return state
	}

	visible := seed("OWNER/REPO", "00130", controlplane.OpenPilotPhaseRequested, "")
	resp, err := http.Get(srv.URL + "/api/open-pilot/state?repo=owner/repo&issue=130")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("canonical lookup status = %d", resp.StatusCode)
	}
	var body struct {
		State controlplane.OpenPilotIssueState `json:"state"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.State.ID != visible.ID {
		t.Fatalf("canonical lookup = %+v, %v", body.State, err)
	}

	active, err := store.CreateRun(controlplane.CreateRunInput{WorkflowID: "open-pilot", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "00131"}})
	if err != nil {
		t.Fatal(err)
	}
	seed("OWNER/REPO", "00131", controlplane.OpenPilotPhaseRequested, active.ID)
	resp, err = http.Post(srv.URL+"/api/open-pilot/state/pause?repo=owner/repo&issue=131", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("canonical pause status = %d", resp.StatusCode)
	}
	if detail, err := store.RunDetail(active.ID); err != nil || detail.Run.Status != controlplane.RunCanceled {
		t.Fatalf("paused run = %+v, %v", detail.Run, err)
	}

	resumeTarget := controlplane.OpenPilotTarget{RepositoryID: "legacy", RepositorySlug: "owner/repo", Issue: "132", ExecutorGeneration: "legacy"}
	_, prior, err := store.ClaimAndCreateOpenPilotInitialRun("OWNER/REPO", "00132", controlplane.CreateRunInput{WorkflowID: "open-pilot", OpenPilotTarget: &resumeTarget})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(prior.ID, controlplane.RunFailed, "failed"); err != nil {
		t.Fatal(err)
	}
	resumable := seed("OWNER/REPO", "00132", controlplane.OpenPilotPhasePaused, prior.ID)
	resumable.LastStepName = "open-pilot.ai-review"
	resumable.PullRequestID = 32
	resumable.PullRequestNum = 32
	resumable.PullRequestURL = "https://git.example.test/OWNER/REPO/pulls/32"
	if _, err := store.UpsertOpenPilotIssueState(resumable); err != nil {
		t.Fatal(err)
	}
	resp, err = http.Post(srv.URL+"/api/open-pilot/state/resume?repo=owner/repo&issue=132", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("canonical resume status = %d", resp.StatusCode)
	}

	seed("OWNER/REPO", "00133", controlplane.OpenPilotPhaseRequested, "")
	seed("owner/repo", "0133", controlplane.OpenPilotPhaseRequested, "")
	resp, err = http.Get(srv.URL + "/api/open-pilot/state?repo=owner/repo&issue=133")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("ambiguous lookup status = %d", resp.StatusCode)
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
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("retry status = %d", resp.StatusCode)
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

func TestAPIStepActionRejectsCrossRunStepID(t *testing.T) {
	store := controlplane.NewStore(nil)
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	runOne, err := store.CreateRun(controlplane.CreateRunInput{WorkflowID: "devlog"})
	if err != nil {
		t.Fatalf("CreateRun(one) error = %v", err)
	}
	runTwo, err := store.CreateRun(controlplane.CreateRunInput{WorkflowID: "devlog"})
	if err != nil {
		t.Fatalf("CreateRun(two) error = %v", err)
	}
	step, err := store.CreateRunStep(controlplane.CreateRunStepInput{RunID: runOne.ID, Position: 1, Name: "devlog.test", Executor: "internal"})
	if err != nil {
		t.Fatalf("CreateRunStep() error = %v", err)
	}

	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	for _, action := range []string{"retry", "cancel"} {
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/runs/"+runTwo.ID+"/steps/"+step.ID+"/"+action, nil)
		if err != nil {
			t.Fatalf("new %s request: %v", action, err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", action, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", action, resp.StatusCode)
		}
	}
	saved, err := store.RunStep(step.ID)
	if err != nil {
		t.Fatalf("RunStep() error = %v", err)
	}
	if saved.Status != controlplane.StepQueued {
		t.Fatalf("step status = %s, want queued", saved.Status)
	}
}

func TestAPIRetryOpenPilotAIReviewRequiresLifecycleResume(t *testing.T) {
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
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("retry status = %d, want 409", resp.StatusCode)
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
	if len(body.Queues) != 0 {
		t.Fatalf("queues = %+v", body.Queues)
	}
	if len(body.Sessions) != 0 {
		t.Fatalf("sessions = %+v", body.Sessions)
	}
	if len(body.Logs) != 0 {
		t.Fatalf("logs = %+v", body.Logs)
	}
	if len(body.Prompts) != 0 {
		t.Fatalf("prompts = %+v", body.Prompts)
	}
	if body.Config.Environment != "unknown" || len(body.Config.Features) == 0 {
		t.Fatalf("config = %+v", body.Config)
	}
	if len(body.Agents) != 0 || len(body.Skills) != 0 {
		t.Fatalf("agents/skills = %+v %+v", body.Agents, body.Skills)
	}
	if body.Health.Status != "healthy" || len(body.Health.Services) != 1 {
		t.Fatalf("health = %+v", body.Health)
	}
	if len(body.LlamaLine.Requests) != 0 || body.LlamaLine.Stats.ActiveRequests != 0 || len(body.LlamaLine.Upstreams) != 0 {
		t.Fatalf("llama_line = %+v", body.LlamaLine)
	}
	if len(body.OCQ.Requests) != 0 || len(body.OCQ.Sessions) != 0 || body.OCQ.Settings.Provider != "unconfigured" {
		t.Fatalf("ocq = %+v", body.OCQ)
	}
	if len(body.Blacktower.Tasks) != 0 || len(body.Blacktower.Reports) != 0 || body.Blacktower.SchedulerHealth.Status != "unconfigured" {
		t.Fatalf("blacktower = %+v", body.Blacktower)
	}
	if len(body.Changemaker.Posts) != 0 || body.Changemaker.Service.Healthy || body.Changemaker.RunControl.Enabled {
		t.Fatalf("changemaker = %+v", body.Changemaker)
	}
}

func TestAPIListenerRouteIsolation(t *testing.T) {
	store := controlplane.NewStore(nil)
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	control := httptest.NewServer(api.ControlPlaneHandler())
	t.Cleanup(control.Close)
	model := httptest.NewServer(api.ModelIngressHandler())
	t.Cleanup(model.Close)

	for _, path := range []string{"/api/runs", "/api/triggers", "/api/workflow-definitions", "/admin/queue", "/api/history"} {
		resp, err := http.Get(model.URL + path)
		if err != nil {
			t.Fatalf("GET model ingress %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("model ingress %s status = %d, want 404", path, resp.StatusCode)
		}
	}

	resp, err := http.Get(control.URL + "/v1/models")
	if err != nil {
		t.Fatalf("GET control inference route: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("control inference status = %d, want 404", resp.StatusCode)
	}

	resp, err = http.Get(model.URL + "/v1/models")
	if err != nil {
		t.Fatalf("GET model list: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("model list status = %d, want 200", resp.StatusCode)
	}
}

func TestAPIRunsUsesBoundedCursorPagination(t *testing.T) {
	now := time.Unix(100, 0)
	store := controlplane.NewStore(func() time.Time {
		now = now.Add(time.Second)
		return now
	})
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := store.CreateRun(controlplane.CreateRunInput{WorkflowID: "devlog"}); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(controlplane.NewAPI(store, controlplane.NewRunner(store)).Handler())
	t.Cleanup(srv.Close)
	var first struct {
		Runs       []controlplane.Run `json:"runs"`
		NextCursor string             `json:"next_cursor"`
	}
	resp, err := http.Get(srv.URL + "/api/runs?limit=2")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(resp.Body).Decode(&first); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(first.Runs) != 2 || first.NextCursor == "" {
		t.Fatalf("first page = %+v", first)
	}
	var second struct {
		Runs       []controlplane.Run `json:"runs"`
		NextCursor string             `json:"next_cursor"`
	}
	resp, err = http.Get(srv.URL + "/api/runs?limit=2&cursor=" + first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&second); err != nil {
		t.Fatal(err)
	}
	if len(second.Runs) != 1 || second.NextCursor != "" || second.Runs[0].ID == first.Runs[0].ID || second.Runs[0].ID == first.Runs[1].ID {
		t.Fatalf("second page = %+v", second)
	}
}

func TestControlPlaneRequiresAuthenticationAndEnforcesReadRole(t *testing.T) {
	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "admin-secret")
	t.Setenv("SWITCHYARD_CONTROL_READ_TOKEN", "read-secret")
	store := controlplane.NewStore(nil)
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	srv := httptest.NewServer(controlplane.NewAPI(store, controlplane.NewRunner(store)).ControlPlaneHandler())
	t.Cleanup(srv.Close)

	request := func(method, path, token string) *http.Response {
		req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(`{"workflow_id":"devlog"}`))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}
	if got := request(http.MethodGet, "/api/runs", "").StatusCode; got != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", got)
	}
	if got := request(http.MethodGet, "/api/runs", "read-secret").StatusCode; got != http.StatusOK {
		t.Fatalf("read token GET status = %d", got)
	}
	if got := request(http.MethodPost, "/api/runs", "read-secret").StatusCode; got != http.StatusForbidden {
		t.Fatalf("read token POST status = %d", got)
	}
	if got := request(http.MethodPost, "/api/runs", "admin-secret").StatusCode; got != http.StatusCreated {
		t.Fatalf("admin token POST status = %d", got)
	}
}

func TestControlPlaneReverseProxyAuthentication(t *testing.T) {
	const proxyToken = "proxy-hop-token-with-at-least-32-bytes"
	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "admin-secret")
	t.Setenv("SWITCHYARD_CONTROL_READ_TOKEN", "read-secret")
	t.Setenv("SWITCHYARD_REVERSE_PROXY_AUTH", "true")
	t.Setenv("SWITCHYARD_REVERSE_PROXY_TOKEN", proxyToken)
	store := controlplane.NewStore(nil)
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	srv := httptest.NewServer(controlplane.NewAPI(store, controlplane.NewRunner(store)).ControlPlaneHandler())
	t.Cleanup(srv.Close)

	tests := []struct {
		name          string
		remoteUser    string
		forwardedUser string
		proxyToken    string
		bearerToken   string
		duplicate     string
		wantStatus    int
	}{
		{name: "identity without token", remoteUser: "operator@example.test", wantStatus: http.StatusUnauthorized},
		{name: "identity with malformed token", remoteUser: "operator@example.test", proxyToken: "short", wantStatus: http.StatusUnauthorized},
		{name: "identity with wrong token", remoteUser: "operator@example.test", proxyToken: strings.Repeat("x", 40), wantStatus: http.StatusUnauthorized},
		{name: "valid remote user", remoteUser: "operator@example.test", proxyToken: proxyToken, wantStatus: http.StatusOK},
		{name: "valid forwarded user", forwardedUser: "operator@example.test", proxyToken: proxyToken, wantStatus: http.StatusOK},
		{name: "matching identity headers", remoteUser: "operator@example.test", forwardedUser: "operator@example.test", proxyToken: proxyToken, wantStatus: http.StatusOK},
		{name: "conflicting identity headers", remoteUser: "operator@example.test", forwardedUser: "attacker@example.test", proxyToken: proxyToken, wantStatus: http.StatusUnauthorized},
		{name: "duplicate remote user", remoteUser: "operator@example.test", proxyToken: proxyToken, duplicate: "Remote-User", wantStatus: http.StatusUnauthorized},
		{name: "duplicate forwarded user", forwardedUser: "operator@example.test", proxyToken: proxyToken, duplicate: "X-Forwarded-User", wantStatus: http.StatusUnauthorized},
		{name: "duplicate hop token", remoteUser: "operator@example.test", proxyToken: proxyToken, duplicate: "X-Switchyard-Proxy-Token", wantStatus: http.StatusUnauthorized},
		{name: "embedded whitespace identity", remoteUser: "operator name", proxyToken: proxyToken, wantStatus: http.StatusUnauthorized},
		{name: "non ASCII identity", remoteUser: "operátor", proxyToken: proxyToken, wantStatus: http.StatusUnauthorized},
		{name: "bidi identity", remoteUser: "operator\u202eevil", proxyToken: proxyToken, wantStatus: http.StatusUnauthorized},
		{name: "identity too long", remoteUser: strings.Repeat("a", 257), proxyToken: proxyToken, wantStatus: http.StatusUnauthorized},
		{name: "token without identity", proxyToken: proxyToken, wantStatus: http.StatusUnauthorized},
		{name: "bearer remains valid", bearerToken: "admin-secret", wantStatus: http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/runs", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Remote-User", test.remoteUser)
			req.Header.Set("X-Forwarded-User", test.forwardedUser)
			req.Header.Set("X-Switchyard-Proxy-Token", test.proxyToken)
			if test.duplicate != "" {
				req.Header.Add(test.duplicate, "duplicate-value")
			}
			if test.bearerToken != "" {
				req.Header.Set("Authorization", "Bearer "+test.bearerToken)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", resp.StatusCode, test.wantStatus, body)
			}
			responseText := string(body)
			for name, values := range resp.Header {
				responseText += name + strings.Join(values, "")
			}
			if strings.Contains(responseText, proxyToken) || (test.proxyToken != "" && strings.Contains(responseText, test.proxyToken)) {
				t.Fatal("proxy token leaked in response")
			}
		})
	}

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want 200", resp.StatusCode)
	}
}

func TestControlPlaneReverseProxyAuthenticationFailsClosedForInvalidConfiguredToken(t *testing.T) {
	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "")
	t.Setenv("SWITCHYARD_CONTROL_READ_TOKEN", "")
	t.Setenv("SWITCHYARD_REVERSE_PROXY_AUTH", "true")
	t.Setenv("SWITCHYARD_REVERSE_PROXY_TOKEN", "short-configured-secret")
	store := controlplane.NewStore(nil)
	srv := httptest.NewServer(controlplane.NewAPI(store, controlplane.NewRunner(store)).ControlPlaneHandler())
	t.Cleanup(srv.Close)
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/runs", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Remote-User", "operator@example.test")
	req.Header.Set("X-Switchyard-Proxy-Token", "short-configured-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if strings.Contains(string(body), "short-configured-secret") {
		t.Fatal("configured proxy token leaked in error")
	}
	for name, values := range resp.Header {
		if strings.Contains(name+strings.Join(values, ""), "short-configured-secret") {
			t.Fatal("configured proxy token leaked in response headers")
		}
	}
}

func TestControlPlaneDoesNotTrustProxyHeadersWhenDisabled(t *testing.T) {
	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "admin-secret")
	t.Setenv("SWITCHYARD_REVERSE_PROXY_AUTH", "false")
	t.Setenv("SWITCHYARD_REVERSE_PROXY_TOKEN", "proxy-hop-token-with-at-least-32-bytes")
	store := controlplane.NewStore(nil)
	srv := httptest.NewServer(controlplane.NewAPI(store, controlplane.NewRunner(store)).ControlPlaneHandler())
	t.Cleanup(srv.Close)
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/runs", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Remote-User", "operator@example.test")
	req.Header.Set("X-Switchyard-Proxy-Token", "proxy-hop-token-with-at-least-32-bytes")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestControlPlaneTrustsForwardedClientIPOnlyWithValidHopToken(t *testing.T) {
	const proxyToken = "proxy-hop-token-with-at-least-32-bytes"
	for _, test := range []struct {
		name           string
		hopToken       string
		forwarded      []string
		duplicateToken bool
		wantSecond     int
	}{
		{name: "missing", forwarded: []string{"192.0.2.1", "192.0.2.2"}, wantSecond: http.StatusTooManyRequests},
		{name: "wrong", hopToken: "wrong-proxy-hop-token-at-least-32-bytes", forwarded: []string{"192.0.2.1", "192.0.2.2"}, wantSecond: http.StatusTooManyRequests},
		{name: "valid IPv4", hopToken: proxyToken, forwarded: []string{"192.0.2.1", "192.0.2.2"}, wantSecond: http.StatusOK},
		{name: "valid IPv6", hopToken: proxyToken, forwarded: []string{"2001:db8::1", "2001:db8::2"}, wantSecond: http.StatusOK},
		{name: "list rejected", hopToken: proxyToken, forwarded: []string{"192.0.2.1, 198.51.100.1", "192.0.2.2, 198.51.100.2"}, wantSecond: http.StatusTooManyRequests},
		{name: "invalid rejected", hopToken: proxyToken, forwarded: []string{"not-an-ip", "also-not-an-ip"}, wantSecond: http.StatusTooManyRequests},
		{name: "duplicate hop token rejected", hopToken: proxyToken, duplicateToken: true, forwarded: []string{"192.0.2.1", "192.0.2.2"}, wantSecond: http.StatusTooManyRequests},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "admin-secret")
			t.Setenv("SWITCHYARD_REVERSE_PROXY_AUTH", "true")
			t.Setenv("SWITCHYARD_REVERSE_PROXY_TOKEN", proxyToken)
			t.Setenv("SWITCHYARD_RATE_LIMIT_PER_SECOND", "1")
			store := controlplane.NewStore(nil)
			srv := httptest.NewServer(controlplane.NewAPI(store, controlplane.NewRunner(store)).ControlPlaneHandler())
			t.Cleanup(srv.Close)
			request := func(forwardedFor string) *http.Response {
				req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/runs", nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer admin-secret")
				req.Header.Set("X-Forwarded-For", forwardedFor)
				req.Header.Set("X-Switchyard-Proxy-Token", test.hopToken)
				if test.duplicateToken {
					req.Header.Add("X-Switchyard-Proxy-Token", proxyToken)
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				return resp
			}
			first := request(test.forwarded[0])
			first.Body.Close()
			if first.StatusCode != http.StatusOK {
				t.Fatalf("first status = %d, want 200", first.StatusCode)
			}
			second := request(test.forwarded[1])
			second.Body.Close()
			if second.StatusCode != test.wantSecond {
				t.Fatalf("second status = %d, want %d", second.StatusCode, test.wantSecond)
			}
		})
	}
}

func TestControlPlaneFailsClosedWithoutAuthenticationConfiguration(t *testing.T) {
	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "")
	t.Setenv("SWITCHYARD_CONTROL_READ_TOKEN", "")
	t.Setenv("SWITCHYARD_REVERSE_PROXY_AUTH", "false")
	store := controlplane.NewStore(nil)
	srv := httptest.NewServer(controlplane.NewAPI(store, controlplane.NewRunner(store)).ControlPlaneHandler())
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/api/runs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

func TestAPICORSAllowsOnlyConfiguredOrigins(t *testing.T) {
	t.Setenv("SWITCHYARD_CORS_ALLOWED_ORIGINS", "https://operator.example")
	store := controlplane.NewStore(nil)
	srv := httptest.NewServer(controlplane.NewAPI(store, controlplane.NewRunner(store)).Handler())
	t.Cleanup(srv.Close)
	request := func(origin string) *http.Response {
		req, err := http.NewRequest(http.MethodOptions, srv.URL+"/api/runs", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", origin)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	allowed := request("https://operator.example")
	allowed.Body.Close()
	if allowed.StatusCode != http.StatusNoContent || allowed.Header.Get("Access-Control-Allow-Origin") != "https://operator.example" {
		t.Fatalf("allowed origin status/header = %d/%q", allowed.StatusCode, allowed.Header.Get("Access-Control-Allow-Origin"))
	}
	denied := request("https://evil.example")
	denied.Body.Close()
	if denied.StatusCode != http.StatusForbidden || denied.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("denied origin status/header = %d/%q", denied.StatusCode, denied.Header.Get("Access-Control-Allow-Origin"))
	}
}

func TestAPIEnforcesBodyPromptAndRateLimits(t *testing.T) {
	t.Run("body", func(t *testing.T) {
		t.Setenv("SWITCHYARD_MAX_REQUEST_BYTES", "1024")
		store := controlplane.NewStore(nil)
		srv := httptest.NewServer(controlplane.NewAPI(store, controlplane.NewRunner(store)).Handler())
		t.Cleanup(srv.Close)
		resp, err := http.Post(srv.URL+"/api/runs", "application/json", strings.NewReader(strings.Repeat("x", 1025)))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	})
	t.Run("prompt", func(t *testing.T) {
		t.Setenv("SWITCHYARD_MAX_PROMPT_BYTES", "1024")
		store := controlplane.NewStore(nil)
		srv := httptest.NewServer(controlplane.NewAPI(store, controlplane.NewRunner(store)).Handler())
		t.Cleanup(srv.Close)
		body := `{"model":"test","messages":[{"role":"user","content":"` + strings.Repeat("x", 1025) + `"}]}`
		resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	})
	t.Run("rate", func(t *testing.T) {
		t.Setenv("SWITCHYARD_RATE_LIMIT_PER_SECOND", "1")
		store := controlplane.NewStore(nil)
		srv := httptest.NewServer(controlplane.NewAPI(store, controlplane.NewRunner(store)).Handler())
		t.Cleanup(srv.Close)
		first, err := http.Get(srv.URL + "/api/runs")
		if err != nil {
			t.Fatal(err)
		}
		first.Body.Close()
		limited := false
		for i := 0; i < 3; i++ {
			response, err := http.Get(srv.URL + "/api/runs")
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode == http.StatusTooManyRequests && response.Header.Get("Retry-After") != "" {
				limited = true
				break
			}
		}
		if !limited {
			t.Fatal("requests were not rate limited")
		}
	})
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
	if len(body.Sessions) != 1 || body.Sessions[0].Surface != "blacktower" {
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

func TestAPIOpenPilotResumeCanceledBeforeExecution(t *testing.T) {
	store := controlplane.NewStore(nil)
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	api := controlplane.NewAPI(store, controlplane.NewRunner(store, controlplane.MapExecutorRegistry{}))
	api.SetAsyncDispatch(true)
	srv := httptest.NewServer(api.Handler())
	defer srv.Close()
	target := controlplane.OpenPilotTarget{RepositoryID: "legacy", RepositorySlug: "owner/repo", Issue: "991", ExecutorGeneration: "legacy"}
	_, prior, err := store.ClaimAndCreateOpenPilotInitialRun("owner/repo", "991", controlplane.CreateRunInput{WorkflowID: "open-pilot", OpenPilotTarget: &target})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.FinishRun(prior.ID, controlplane.RunCanceled, "canceled before execution"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(srv.URL+"/api/open-pilot/state/resume?repo=owner/repo&issue=991", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, b)
	}
	var body struct {
		Run controlplane.Run `json:"run"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Run.Inputs["retry_from_step"] != "open-pilot.validate-issue" {
		t.Fatalf("cursor=%v", body.Run.Inputs)
	}
}
