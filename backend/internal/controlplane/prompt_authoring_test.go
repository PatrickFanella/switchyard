package controlplane

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
)

const (
	promptV1 = "Summarize {{topic}} for operators:\n{{steps.fetch.outputs.body}}"
	promptV2 = "Write a short digest about {{topic}}.\nNotes: {{steps.fetch.outputs.body}}"
)

func promptDefinitionBody(t *testing.T, id, prompt string, expectedVersion int) string {
	t.Helper()
	payload := map[string]any{
		"name":             "Prompt digest",
		"expected_version": expectedVersion,
		"input_shape":      JSONShape{Type: "object", Properties: map[string]JSONShapeProperty{"topic": {Type: "string"}}, Required: []string{"topic"}},
		"pipeline": PipelineDefinition{ID: id + "-pipeline", WorkflowID: id, Name: "Prompt digest", Steps: []PipelineStepDefinition{
			{ID: "fetch", Position: 1, Name: "digest.fetch", Executor: "internal", Inputs: map[string]string{"output.body": "{{topic}} notes"}},
			{ID: "write", Position: 2, Name: "digest.write", Executor: "model", Profile: "content-writer", Model: "gpt-5.4", Inputs: map[string]string{"prompt": prompt}},
		}},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

type promptCall struct {
	status int
	body   []byte
}

func promptRequest(t *testing.T, method, url, token, body string) promptCall {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return promptCall{status: resp.StatusCode, body: raw}
}

func decodePromptCall[T any](t *testing.T, call promptCall) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(call.body, &out); err != nil {
		t.Fatalf("decode %q: %v", call.body, err)
	}
	return out
}

func findPromptTemplate(t *testing.T, inventory PromptInventory, workflowID, stepID string) PromptTemplate {
	t.Helper()
	for _, template := range inventory.Templates {
		if template.WorkflowID == workflowID && template.StepID == stepID {
			return template
		}
	}
	t.Fatalf("inventory has no template %s/%s: %+v", workflowID, stepID, inventory.Templates)
	return PromptTemplate{}
}

func promptPolicyStep(t *testing.T, policy *RunModelPolicy, stepID string) RunModelPolicyStep {
	t.Helper()
	if policy == nil {
		t.Fatal("run has no policy")
	}
	for _, step := range policy.Steps {
		if step.StepID == stepID {
			return step
		}
	}
	t.Fatalf("policy has no step %q: %+v", stepID, policy.Steps)
	return RunModelPolicyStep{}
}

func TestPromptAuthoringContractMemory(t *testing.T) {
	exercisePromptAuthoringContract(t, NewStore(nil), "prompt-digest")
}

// exercisePromptAuthoringContract covers the prompt lifecycle through the
// owning workflow-definition store: inventory, preview parity with the
// runner, draft saves that neither activate nor run anything, promotion,
// rollback, and run binding to the effective template version.
func exercisePromptAuthoringContract(t *testing.T, store Store, id string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.UpsertWorkflow(Workflow{ID: id, Kind: WorkflowKind(id), Name: id}); err != nil {
		t.Fatal(err)
	}
	broker := &recordingModelBroker{response: ModelResponse{Content: "# Digest"}}
	runner := NewRunner(store, MapExecutorRegistry{id: genericWorkflowExecutor{env: &EnvWorkflowExecutors{store: store, modelExecutor: broker}}})
	server := httptest.NewServer(NewAPI(store, runner).Handler())
	t.Cleanup(server.Close)
	base := server.URL + "/api/workflow-definitions/" + id
	expect := func(label string, call promptCall, status int) promptCall {
		t.Helper()
		if call.status != status {
			t.Fatalf("%s status = %d (%s), want %d", label, call.status, call.body, status)
		}
		return call
	}
	inventory := func() PromptInventory {
		t.Helper()
		return decodePromptCall[PromptInventory](t, expect("inventory", promptRequest(t, http.MethodGet, server.URL+"/api/prompts", "", ""), http.StatusOK))
	}
	preview := func(body string) PromptPreview {
		t.Helper()
		return decodePromptCall[struct{ Preview PromptPreview }](t, expect("preview", promptRequest(t, http.MethodPost, server.URL+"/api/prompts/preview", "", body), http.StatusOK)).Preview
	}
	newRunPolicy := func() (Run, *RunModelPolicy) {
		t.Helper()
		run, err := runner.EnqueueContext(ctx, CreateRunInput{WorkflowID: id, Inputs: map[string]string{"topic": "Switchyard"}})
		if err != nil {
			t.Fatal(err)
		}
		return run, runner.RunModelPolicy(run, nil)
	}
	sha := promptTemplateSHA256

	expect("save v1", promptRequest(t, http.MethodPut, base, "", promptDefinitionBody(t, id, promptV1, 0)), http.StatusOK)
	expect("activate v1", promptRequest(t, http.MethodPost, base+"/activate", "", `{"version":1}`), http.StatusOK)

	listed := findPromptTemplate(t, inventory(), id, "write")
	if listed.Owner != PromptOwnerWorkflowDefinition || listed.Version != 1 || listed.ActiveVersion != 1 || listed.ChangedSinceActive || listed.Input != "prompt" || listed.TemplateSHA256 != sha(promptV1) || listed.ProfileID != "content-writer" {
		t.Fatalf("v1 template = %+v", listed)
	}
	if len(listed.Problems) != 0 {
		t.Fatalf("v1 problems = %+v", listed.Problems)
	}
	if len(listed.Variables) != 2 || listed.Variables[0].Name != "topic" || !listed.Variables[0].Declared || !listed.Variables[0].Required || listed.Variables[1].Kind != PromptVariableStepOutput || listed.Variables[1].StepID != "fetch" || listed.Variables[1].Output != "body" {
		t.Fatalf("v1 variables = %+v", listed.Variables)
	}
	for _, entry := range inventory().Templates {
		if entry.WorkflowID == id && entry.StepID == "fetch" {
			t.Fatal("internal steps are not prompt templates")
		}
	}

	// The preview renders exactly what the runner sends to the model.
	sample := `{"workflow_id":"` + id + `","step_id":"write","inputs":{"topic":"Switchyard"},"step_outputs":{"fetch":{"body":"Switchyard notes"}}}`
	rendered := preview(sample)
	if !rendered.OK || len(rendered.MissingVariables) != 0 || rendered.Draft || rendered.Version != 1 || rendered.Profile == nil || rendered.Profile.ProfileID != "content-writer" || rendered.Profile.Version != 0 || rendered.Profile.Source != AgentProfileSourceStatic {
		t.Fatalf("preview = %+v", rendered)
	}
	runA, err := runner.RunOnce(ctx, CreateRunInput{WorkflowID: id, Inputs: map[string]string{"topic": "Switchyard"}})
	if err != nil || runA.Status != RunSucceeded {
		t.Fatalf("RunOnce() = %+v, %v", runA, err)
	}
	if len(broker.requests) != 1 || broker.requests[0].Messages[0].Content != rendered.Rendered {
		t.Fatalf("runner prompt differs from preview:\nrunner:  %q\npreview: %q", broker.requests[0].Messages[0].Content, rendered.Rendered)
	}
	if !strings.HasPrefix(rendered.Rendered, "Profile contract:\n") || !strings.Contains(rendered.WorkflowRequest, "Summarize Switchyard for operators:\nSwitchyard notes") {
		t.Fatalf("rendered = %q", rendered.Rendered)
	}
	detailA, err := store.RunDetail(runA.ID)
	if err != nil {
		t.Fatal(err)
	}
	policyA := runner.RunModelPolicy(detailA.Run, detailA.Steps)
	if policyA.DefinitionID != id || policyA.DefinitionVersion != 1 {
		t.Fatalf("run A policy = %+v", policyA)
	}
	if step := promptPolicyStep(t, policyA, "write"); step.PromptOwner != PromptOwnerWorkflowDefinition || step.PromptInput != "prompt" || step.PromptTemplateSHA256 != sha(promptV1) || step.ProfileID != "content-writer" {
		t.Fatalf("run A write step = %+v", step)
	}
	if step := promptPolicyStep(t, policyA, "fetch"); step.PromptOwner != "" || step.PromptTemplateSHA256 != "" {
		t.Fatalf("run A fetch step = %+v", step)
	}

	// A draft save and a preview neither activate nor run anything.
	runsBefore := len(store.ListRuns())
	expect("save v2 draft", promptRequest(t, http.MethodPut, base, "", promptDefinitionBody(t, id, promptV2, 1)), http.StatusOK)
	preview(`{"workflow_id":"` + id + `","step_id":"write","template":"Draft {{topic}}","inputs":{"topic":"x"}}`)
	if runs := len(store.ListRuns()); runs != runsBefore {
		t.Fatalf("draft save or preview created runs: %d -> %d", runsBefore, runs)
	}
	if len(broker.requests) != 1 {
		t.Fatalf("draft save or preview called the model: %d requests", len(broker.requests))
	}
	drafted := findPromptTemplate(t, inventory(), id, "write")
	if drafted.Version != 2 || drafted.ActiveVersion != 1 || !drafted.ChangedSinceActive || drafted.TemplateSHA256 != sha(promptV2) || drafted.ActiveTemplateSHA256 != sha(promptV1) {
		t.Fatalf("draft template = %+v", drafted)
	}
	if _, policy := newRunPolicy(); policy.DefinitionVersion != 1 || promptPolicyStep(t, policy, "write").PromptTemplateSHA256 != sha(promptV1) {
		t.Fatalf("run created while v2 is a draft = %+v", policy)
	}
	// Previewing a stored earlier version uses that version's template.
	if earlier := preview(`{"workflow_id":"` + id + `","step_id":"write","version":1}`); earlier.Version != 1 || earlier.TemplateSHA256 != sha(promptV1) || earlier.ActiveVersion != 1 {
		t.Fatalf("preview of v1 = %+v", earlier)
	}

	// Promotion: activation binds new runs to v2; run A keeps v1.
	expect("activate v2", promptRequest(t, http.MethodPost, base+"/activate", "", `{"version":2}`), http.StatusOK)
	queuedV2, policyV2 := newRunPolicy()
	if policyV2.DefinitionVersion != 2 || promptPolicyStep(t, policyV2, "write").PromptTemplateSHA256 != sha(promptV2) {
		t.Fatalf("run after activation = %+v", policyV2)
	}
	if again := runner.RunModelPolicy(detailA.Run, detailA.Steps); again.DefinitionVersion != 1 || promptPolicyStep(t, again, "write").PromptTemplateSHA256 != sha(promptV1) {
		t.Fatalf("activation changed run A = %+v", again)
	}

	// Rollback appends v3 = v1 and makes it active; the v2 run keeps v2.
	expect("rollback to v1", promptRequest(t, http.MethodPost, base+"/rollback", "", `{"version":1,"expected_version":2}`), http.StatusOK)
	rolled := findPromptTemplate(t, inventory(), id, "write")
	if rolled.Version != 3 || rolled.ActiveVersion != 3 || rolled.ChangedSinceActive || rolled.TemplateSHA256 != sha(promptV1) {
		t.Fatalf("rolled back template = %+v", rolled)
	}
	if _, policy := newRunPolicy(); policy.DefinitionVersion != 3 || promptPolicyStep(t, policy, "write").PromptTemplateSHA256 != sha(promptV1) {
		t.Fatalf("run after rollback = %+v", policy)
	}
	stored, err := store.RunDetail(queuedV2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if policy := runner.RunModelPolicy(stored.Run, stored.Steps); policy.DefinitionVersion != 2 || promptPolicyStep(t, policy, "write").PromptTemplateSHA256 != sha(promptV2) {
		t.Fatalf("rollback changed the queued v2 run = %+v", policy)
	}

	// Run detail exposes the binding.
	detail := decodePromptCall[struct{ Policy RunModelPolicy }](t, expect("run detail", promptRequest(t, http.MethodGet, server.URL+"/api/runs/"+runA.ID, "", ""), http.StatusOK))
	if detail.Policy.DefinitionVersion != 1 || promptPolicyStep(t, &detail.Policy, "write").PromptTemplateSHA256 != sha(promptV1) {
		t.Fatalf("run detail policy = %+v", detail.Policy)
	}
}

func TestPromptPreviewReportsMissingVariablesAndProblems(t *testing.T) {
	store := NewStore(nil)
	const id = "prompt-problems"
	if err := store.UpsertWorkflow(Workflow{ID: id, Kind: WorkflowKind(id), Name: id}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewAPI(store, NewRunner(store)).Handler())
	t.Cleanup(server.Close)
	if call := promptRequest(t, http.MethodPut, server.URL+"/api/workflow-definitions/"+id, "", promptDefinitionBody(t, id, promptV1, 0)); call.status != http.StatusOK {
		t.Fatalf("save = %d %s", call.status, call.body)
	}
	preview := func(body string) PromptPreview {
		t.Helper()
		call := promptRequest(t, http.MethodPost, server.URL+"/api/prompts/preview", "", body)
		if call.status != http.StatusOK {
			t.Fatalf("preview = %d %s", call.status, call.body)
		}
		return decodePromptCall[struct{ Preview PromptPreview }](t, call).Preview
	}

	missing := preview(`{"workflow_id":"` + id + `","step_id":"write"}`)
	if missing.OK || len(missing.MissingVariables) != 2 {
		t.Fatalf("missing = %+v", missing)
	}
	if got := missing.MissingVariables[0]; got.Name != "topic" || got.Kind != PromptVariableInput || !strings.Contains(got.Message, "missing variable {{topic}}") {
		t.Fatalf("missing input = %+v", got)
	}
	if got := missing.MissingVariables[1]; got.Name != "steps.fetch.outputs.body" || got.Kind != PromptVariableStepOutput || !strings.Contains(got.Message, "pauses before the model call") {
		t.Fatalf("missing step output = %+v", got)
	}
	if !strings.Contains(missing.WorkflowRequest, "{{topic}}") {
		t.Fatalf("unfilled tokens must stay visible: %q", missing.WorkflowRequest)
	}

	contract := DefaultAgentProfileRegistry()["content-writer"].PromptContract
	draft, _ := json.Marshal(map[string]any{
		"workflow_id": id, "step_id": "write",
		"template": "Hi {{ topic }} {{steps.write.outputs.content}} {{steps.later.outputs.x}} {{steps.fetch.body}} {{audience}}\n" + contract,
		"inputs":   map[string]string{"topic": "t", "audience": "ops"},
	})
	problems := preview(string(draft))
	if problems.OK || !problems.Draft {
		t.Fatalf("problems preview = %+v", problems)
	}
	joined := ""
	for _, problem := range problems.Problems {
		joined += problem.Severity + ": " + problem.Message + "\n"
	}
	for _, want := range []string{
		"error: {{ topic }} contains spaces",
		"error: {{steps.write.outputs.content}}: a step cannot reference its own outputs",
		`error: {{steps.later.outputs.x}} references unknown step "later"`,
		"error: {{steps.fetch.body}} is not a valid step output reference",
		`warning: run input "audience" is not declared`,
		"error: the template repeats the step profile's prompt contract",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("problems missing %q:\n%s", want, joined)
		}
	}

	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"workflow_id":"` + id + `"}`, http.StatusBadRequest},
		{`{"workflow_id":"` + id + `","step_id":"fetch"}`, http.StatusBadRequest},
		{`{"workflow_id":"` + id + `","step_id":"nope"}`, http.StatusNotFound},
		{`{"workflow_id":"missing","step_id":"write"}`, http.StatusNotFound},
		{`{"workflow_id":"` + id + `","step_id":"write","version":9}`, http.StatusNotFound},
		{`{"workflow_id":"` + id + `","step_id":"write","execute":true}`, http.StatusBadRequest},
	} {
		if call := promptRequest(t, http.MethodPost, server.URL+"/api/prompts/preview", "", tc.body); call.status != tc.status {
			t.Errorf("preview %s = %d %s, want %d", tc.body, call.status, call.body, tc.status)
		}
	}
}

func TestPromptPreviewAndInventoryRedactSecrets(t *testing.T) {
	store := NewStore(nil)
	const id = "prompt-secrets"
	if err := store.UpsertWorkflow(Workflow{ID: id, Kind: WorkflowKind(id), Name: id}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewAPI(store, NewRunner(store)).Handler())
	t.Cleanup(server.Close)
	if call := promptRequest(t, http.MethodPut, server.URL+"/api/workflow-definitions/"+id, "", promptDefinitionBody(t, id, "Use password=hunter2 for {{topic}}", 0)); call.status != http.StatusOK {
		t.Fatalf("save = %d %s", call.status, call.body)
	}
	call := promptRequest(t, http.MethodGet, server.URL+"/api/prompts", "", "")
	if bytes.Contains(call.body, []byte("hunter2")) {
		t.Fatalf("inventory leaked a secret: %s", call.body)
	}
	listed := findPromptTemplate(t, decodePromptCall[PromptInventory](t, call), id, "write")
	if !strings.Contains(listed.Template, "password=[REDACTED]") || len(listed.Problems) != 1 || !strings.Contains(listed.Problems[0].Message, "credential-shaped") {
		t.Fatalf("redacted template = %+v", listed)
	}

	body := `{"workflow_id":"` + id + `","step_id":"write","template":"{{topic}} / {{api_token}} / {{steps.fetch.outputs.body}}","inputs":{"topic":"Bearer sk-abcdefghijklmnop","api_token":"plain-value-123"},"step_outputs":{"fetch":{"body":"token: s3cr3t-output"}}}`
	previewCall := promptRequest(t, http.MethodPost, server.URL+"/api/prompts/preview", "", body)
	if previewCall.status != http.StatusOK {
		t.Fatalf("preview = %d %s", previewCall.status, previewCall.body)
	}
	for _, secret := range []string{"sk-abcdefghijklmnop", "plain-value-123", "s3cr3t-output", "hunter2"} {
		if bytes.Contains(previewCall.body, []byte(secret)) {
			t.Errorf("preview leaked %q: %s", secret, previewCall.body)
		}
	}
	preview := decodePromptCall[struct{ Preview PromptPreview }](t, previewCall).Preview
	if !preview.Redacted || !strings.Contains(preview.Rendered, "[REDACTED]") {
		t.Fatalf("preview = %+v", preview)
	}
}

func TestPromptRoutesAreControlOnlyAndReadOnlyForReadRole(t *testing.T) {
	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "admin-secret")
	t.Setenv("SWITCHYARD_CONTROL_READ_TOKEN", "read-secret")
	store := NewStore(nil)
	const id = "prompt-auth"
	if err := store.UpsertWorkflow(Workflow{ID: id, Kind: WorkflowKind(id), Name: id}); err != nil {
		t.Fatal(err)
	}
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	api := NewAPI(store, NewRunner(store))
	control := httptest.NewServer(api.ControlPlaneHandler())
	t.Cleanup(control.Close)
	model := httptest.NewServer(api.ModelIngressHandler())
	t.Cleanup(model.Close)
	if call := promptRequest(t, http.MethodPut, control.URL+"/api/workflow-definitions/"+id, "admin-secret", promptDefinitionBody(t, id, promptV1, 0)); call.status != http.StatusOK {
		t.Fatalf("admin save = %d %s", call.status, call.body)
	}
	previewBody := `{"workflow_id":"` + id + `","step_id":"write","inputs":{"topic":"x"}}`

	for _, route := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/prompts", ""},
		{http.MethodPost, "/api/prompts/preview", previewBody},
	} {
		if call := promptRequest(t, route.method, model.URL+route.path, "admin-secret", route.body); call.status != http.StatusNotFound {
			t.Errorf("model listener %s %s = %d, want 404", route.method, route.path, call.status)
		}
		if call := promptRequest(t, route.method, control.URL+route.path, "", route.body); call.status != http.StatusUnauthorized {
			t.Errorf("anonymous %s %s = %d, want 401", route.method, route.path, call.status)
		}
	}

	// A read credential sees templates, contracts, and versions.
	readInventory := promptRequest(t, http.MethodGet, control.URL+"/api/prompts", "read-secret", "")
	if readInventory.status != http.StatusOK {
		t.Fatalf("read inventory = %d", readInventory.status)
	}
	inventory := decodePromptCall[PromptInventory](t, readInventory)
	findPromptTemplate(t, inventory, id, "write")
	builtIn := map[string]bool{}
	for _, step := range inventory.BuiltIn {
		builtIn[step.WorkflowID+"/"+step.StepID] = step.Owner == PromptOwnerBuiltIn
	}
	if !builtIn["devlog/generate-markdown"] || !builtIn["open-pilot/agent-work"] {
		t.Fatalf("built-in prompt steps = %+v", inventory.BuiltIn)
	}
	contracts := map[string]PromptContractEntry{}
	for _, contract := range inventory.Contracts {
		contracts[contract.ProfileID] = contract
	}
	writer := contracts["content-writer"]
	if writer.Owner != PromptOwnerAgentProfile || writer.PromptContractSHA256 == "" || len(writer.AllowedExecutors) == 0 {
		t.Fatalf("content-writer contract = %+v", writer)
	}
	referenced := false
	for _, reference := range writer.ReferencedBy {
		referenced = referenced || (reference.WorkflowID == id && reference.StepID == "write")
	}
	if !referenced {
		t.Fatalf("content-writer references = %+v", writer.ReferencedBy)
	}
	if call := promptRequest(t, http.MethodGet, control.URL+"/api/workflow-definitions/"+id+"/versions", "read-secret", ""); call.status != http.StatusOK {
		t.Fatalf("read versions = %d", call.status)
	}

	// It cannot preview, save, activate, or roll back, and nothing changes.
	writes := []struct{ method, path, body string }{
		{http.MethodPost, "/api/prompts/preview", previewBody},
		{http.MethodPut, "/api/workflow-definitions/" + id, promptDefinitionBody(t, id, promptV2, 1)},
		{http.MethodPost, "/api/workflow-definitions/" + id + "/activate", `{"version":1}`},
		{http.MethodPost, "/api/workflow-definitions/" + id + "/rollback", `{"version":1,"expected_version":1}`},
	}
	for _, write := range writes {
		if call := promptRequest(t, write.method, control.URL+write.path, "read-secret", write.body); call.status != http.StatusForbidden {
			t.Errorf("read %s %s = %d, want 403", write.method, write.path, call.status)
		}
	}
	if latest, err := store.WorkflowDefinition(id); err != nil || latest.Version != 1 || latest.ActiveVersion != 0 {
		t.Fatalf("read-role writes changed the definition: %+v %v", latest, err)
	}

	// The admin role may preview; previewing does not dispatch a run.
	if call := promptRequest(t, http.MethodPost, control.URL+"/api/prompts/preview", "admin-secret", previewBody); call.status != http.StatusOK {
		t.Fatalf("admin preview = %d %s", call.status, call.body)
	}
	for _, run := range store.ListRuns() {
		if run.WorkflowID == id {
			t.Fatalf("preview created run %+v", run)
		}
	}
}
