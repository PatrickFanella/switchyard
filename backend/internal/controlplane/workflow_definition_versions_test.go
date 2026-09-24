package controlplane

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWorkflowDefinitionVersionContractMemory(t *testing.T) {
	exerciseWorkflowDefinitionVersionContract(t, NewStore(nil), "versions-contract")
}

func TestWorkflowDefinitionVersionAuthorization(t *testing.T) {
	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "admin-secret")
	t.Setenv("SWITCHYARD_CONTROL_READ_TOKEN", "read-secret")
	store := NewStore(nil)
	const id = "versions-auth"
	if err := store.UpsertWorkflow(Workflow{ID: id, Kind: WorkflowKind(id), Name: id}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveWorkflowDefinition(versionContractDefinition(id, "v1", "internal"), nil); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewAPI(store, NewRunner(store)).ControlPlaneHandler())
	t.Cleanup(server.Close)
	call := func(token, method, path, body string) int {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	base := "/api/workflow-definitions/" + id
	for _, path := range []string{base, base + "/versions"} {
		if status := call("read-secret", http.MethodGet, path, ""); status != http.StatusOK {
			t.Fatalf("read GET %s = %d, want 200", path, status)
		}
	}
	writes := []struct{ method, path, body string }{
		{http.MethodPut, base, versionContractBody(id, "v2", "internal", intPtr(1))},
		{http.MethodPost, base + "/activate", `{"version":1}`},
		{http.MethodPost, base + "/rollback", `{"version":1}`},
	}
	for _, write := range writes {
		if status := call("read-secret", write.method, write.path, write.body); status != http.StatusForbidden {
			t.Fatalf("read %s %s = %d, want 403", write.method, write.path, status)
		}
		if status := call("", write.method, write.path, write.body); status != http.StatusUnauthorized {
			t.Fatalf("anonymous %s %s = %d, want 401", write.method, write.path, status)
		}
	}
	if latest, err := store.WorkflowDefinition(id); err != nil || latest.Version != 1 || latest.ActiveVersion != 0 {
		t.Fatalf("rejected writes changed the definition: %+v %v", latest, err)
	}
	if status := call("admin-secret", http.MethodPost, base+"/activate", `{"version":1}`); status != http.StatusOK {
		t.Fatalf("admin activate = %d, want 200", status)
	}
	if status := call("admin-secret", http.MethodPut, base, versionContractBody(id, "v2", "internal", intPtr(1))); status != http.StatusOK {
		t.Fatalf("admin save = %d, want 200", status)
	}
	if status := call("admin-secret", http.MethodPost, base+"/rollback", `{"version":1,"expected_version":2}`); status != http.StatusOK {
		t.Fatalf("admin rollback = %d, want 200", status)
	}
}


// versionContractDefinition is a one-step internal pipeline whose step name
// and published output identify the version that produced it.
func versionContractDefinition(id, marker, executor string) WorkflowDefinition {
	inputs := map[string]string{"output.marker": marker}
	return WorkflowDefinition{ID: id, Name: "Versions " + marker, Pipeline: PipelineDefinition{ID: id + "-pipeline", WorkflowID: id, Name: "Versions", Steps: []PipelineStepDefinition{{ID: "mark", Position: 1, Name: "versions." + marker, Executor: executor, Inputs: inputs}}}}
}

func versionContractBody(id, marker, executor string, expectedVersion *int) string {
	definition := versionContractDefinition(id, marker, executor)
	payload := map[string]any{"name": definition.Name, "pipeline": definition.Pipeline}
	if expectedVersion != nil {
		payload["expected_version"] = *expectedVersion
	}
	encoded, _ := json.Marshal(payload)
	return string(encoded)
}

type versionContractResponse struct {
	Status                int
	Message               string
	Definition            WorkflowDefinition
	PreviousActiveVersion *int
	RolledBackFromVersion int
}

// exerciseWorkflowDefinitionVersionContract drives the version API against a
// store: create-only and expected-version saves, drafts that leave the active
// version serving, activation of only the validated latest version, failed
// activation, rollback with history, and a queued run keeping its snapshot.
func exerciseWorkflowDefinitionVersionContract(t *testing.T, store Store, id string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.UpsertWorkflow(Workflow{ID: id, Kind: WorkflowKind(id), Name: id}); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(store, MapExecutorRegistry{id: genericWorkflowExecutor{env: &EnvWorkflowExecutors{store: store}}})
	server := httptest.NewServer(NewAPI(store, runner).Handler())
	t.Cleanup(server.Close)
	base := server.URL + "/api/workflow-definitions/" + id
	call := func(method, url, body string) versionContractResponse {
		t.Helper()
		req, err := http.NewRequest(method, url, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var envelope struct {
			Definition            WorkflowDefinition `json:"workflow_definition"`
			PreviousActiveVersion *int               `json:"previous_active_version"`
			RolledBackFromVersion int                `json:"rolled_back_from_version"`
			Error                 struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatalf("%s %s: decode %q: %v", method, url, raw, err)
		}
		return versionContractResponse{Status: resp.StatusCode, Message: envelope.Error.Message, Definition: envelope.Definition, PreviousActiveVersion: envelope.PreviousActiveVersion, RolledBackFromVersion: envelope.RolledBackFromVersion}
	}
	expect := func(label string, got versionContractResponse, status int) versionContractResponse {
		t.Helper()
		if got.Status != status {
			t.Fatalf("%s status = %d (%s), want %d", label, got.Status, got.Message, status)
		}
		return got
	}
	activeMarker := func() string {
		t.Helper()
		pipeline, ok := runner.pipelines.PipelineForWorkflow(id)
		if !ok {
			return ""
		}
		return pipeline.Steps[0].Inputs["output.marker"]
	}

	// Create-only saves fail once the ID exists.
	v1 := expect("create", call(http.MethodPut, base, versionContractBody(id, "v1", "internal", intPtr(0))), http.StatusOK).Definition
	if v1.Version != 1 || v1.Status != "draft" || v1.ActiveVersion != 0 {
		t.Fatalf("v1 = %+v", v1)
	}
	conflict := expect("create over existing", call(http.MethodPut, base, versionContractBody(id, "dup", "internal", intPtr(0))), http.StatusConflict)
	if !strings.Contains(conflict.Message, "already exists at version 1") {
		t.Fatalf("create conflict message = %q", conflict.Message)
	}
	if marker := activeMarker(); marker != "" {
		t.Fatalf("draft-only definition serves runs: %q", marker)
	}

	activated := expect("activate v1", call(http.MethodPost, base+"/activate", `{"version":1}`), http.StatusOK)
	if activated.Definition.Version != 1 || activated.Definition.Status != "active" || activated.Definition.ActiveVersion != 1 || activated.PreviousActiveVersion == nil || *activated.PreviousActiveVersion != 0 {
		t.Fatalf("activate v1 = %+v", activated)
	}

	// A run created while v1 is active stays queued across the later changes.
	inFlight, err := runner.EnqueueContext(ctx, CreateRunInput{WorkflowID: id})
	if err != nil || inFlight.PipelineSnapshot == nil || inFlight.PipelineSnapshot.Steps[0].Name != "versions.v1" {
		t.Fatalf("queued run = %+v, %v", inFlight, err)
	}

	// Saving a draft over the active definition keeps v1 serving.
	v2 := expect("save v2", call(http.MethodPut, base, versionContractBody(id, "v2", "internal", intPtr(1))), http.StatusOK).Definition
	if v2.Version != 2 || v2.Status != "draft" || v2.ActiveVersion != 1 {
		t.Fatalf("v2 = %+v", v2)
	}
	if marker := activeMarker(); marker != "v1" {
		t.Fatalf("active pipeline after draft save = %q, want v1", marker)
	}
	if active, err := store.ActiveWorkflowDefinition(id); err != nil || active.Version != 1 || active.Status != "active" {
		t.Fatalf("ActiveWorkflowDefinition() = %+v, %v", active, err)
	}
	latest := expect("get latest", call(http.MethodGet, base, ""), http.StatusOK).Definition
	if latest.Version != 2 || latest.Status != "draft" || latest.ActiveVersion != 1 {
		t.Fatalf("latest = %+v", latest)
	}
	listed, err := store.ListWorkflowDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range listed {
		if definition.ID == id && (definition.Version != 2 || definition.ActiveVersion != 1) {
			t.Fatalf("listed = %+v", definition)
		}
	}

	// Stale writes are rejected without adding a version.
	stale := expect("stale save", call(http.MethodPut, base, versionContractBody(id, "stale", "internal", intPtr(1))), http.StatusConflict)
	if !strings.Contains(stale.Message, "latest version is 2, expected_version was 1") {
		t.Fatalf("stale message = %q", stale.Message)
	}
	expect("stale activate", call(http.MethodPost, base+"/activate", `{"version":1}`), http.StatusConflict)
	expect("stale rollback", call(http.MethodPost, base+"/rollback", `{"version":1,"expected_version":1}`), http.StatusConflict)
	if versions, err := store.ListWorkflowDefinitionVersions(id); err != nil || len(versions) != 2 {
		t.Fatalf("versions after stale writes = %d, %v", len(versions), err)
	}

	// A draft that fails activation validation leaves v1 active.
	expect("save v3 invalid for activation", call(http.MethodPut, base, `{"name":"Versions v3","expected_version":2,"pipeline":{"id":"`+id+`-pipeline","name":"Versions","steps":[{"id":"mark","position":1,"name":"versions.v3","executor":"http","inputs":{"url":"http://example.invalid"}}]}}`), http.StatusOK)
	failed := expect("activate invalid v3", call(http.MethodPost, base+"/activate", `{"version":3}`), http.StatusBadRequest)
	if !strings.Contains(failed.Message, "allow_http") {
		t.Fatalf("failed activation message = %q", failed.Message)
	}
	if marker := activeMarker(); marker != "v1" {
		t.Fatalf("active pipeline after failed activation = %q, want v1", marker)
	}
	expect("rollback to invalid v3", call(http.MethodPost, base+"/rollback", `{"version":3}`), http.StatusBadRequest)
	expect("activate missing definition", call(http.MethodPost, server.URL+"/api/workflow-definitions/"+id+"-missing/activate", ``), http.StatusNotFound)

	v4 := expect("save v4", call(http.MethodPut, base, versionContractBody(id, "v4", "internal", intPtr(3))), http.StatusOK).Definition
	activatedV4 := expect("activate v4", call(http.MethodPost, base+"/activate", `{"version":4}`), http.StatusOK)
	if v4.Version != 4 || activatedV4.Definition.ActiveVersion != 4 || *activatedV4.PreviousActiveVersion != 1 {
		t.Fatalf("activate v4 = %+v", activatedV4)
	}
	if marker := activeMarker(); marker != "v4" {
		t.Fatalf("active pipeline after activation = %q, want v4", marker)
	}
	// Activation is idempotent for the version already active.
	expect("reactivate v4", call(http.MethodPost, base+"/activate", ``), http.StatusOK)

	// The run queued under v1 still executes the v1 snapshot.
	started, err := store.StartRun(inFlight.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.ExecuteRun(ctx, started); err != nil {
		t.Fatalf("ExecuteRun() error = %v", err)
	}
	detail, err := store.RunDetail(inFlight.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Run.Status != RunSucceeded || len(detail.Steps) != 1 || detail.Steps[0].Name != "versions.v1" || detail.Steps[0].Outputs["marker"] != "v1" {
		t.Fatalf("in-flight run detail = %+v steps=%+v", detail.Run, detail.Steps)
	}
	fresh, err := runner.EnqueueContext(ctx, CreateRunInput{WorkflowID: id})
	if err != nil || fresh.PipelineSnapshot.Steps[0].Name != "versions.v4" {
		t.Fatalf("new run snapshot = %+v, %v", fresh.PipelineSnapshot, err)
	}

	// Rollback appends a copy of v1 as the new active version.
	rolledBack := expect("rollback to v1", call(http.MethodPost, base+"/rollback", `{"version":1,"expected_version":4}`), http.StatusOK)
	if rolledBack.Definition.Version != 5 || rolledBack.Definition.Status != "active" || rolledBack.Definition.ActiveVersion != 5 || rolledBack.RolledBackFromVersion != 1 || *rolledBack.PreviousActiveVersion != 4 || rolledBack.Definition.Pipeline.Steps[0].Name != "versions.v1" {
		t.Fatalf("rollback = %+v", rolledBack)
	}
	if marker := activeMarker(); marker != "v1" {
		t.Fatalf("active pipeline after rollback = %q, want v1", marker)
	}
	versions, err := store.ListWorkflowDefinitionVersions(id)
	if err != nil || len(versions) != 5 {
		t.Fatalf("versions = %d, %v", len(versions), err)
	}
	for index, version := range versions {
		wantStatus := "draft"
		if version.Version == 5 {
			wantStatus = "active"
		}
		if version.Version != 5-index || version.Status != wantStatus || version.ActiveVersion != 5 {
			t.Fatalf("version %d = status %q active %d", version.Version, version.Status, version.ActiveVersion)
		}
	}
	if versions[4].Pipeline.Steps[0].Name != "versions.v1" || versions[1].Pipeline.Steps[0].Name != "versions.v4" {
		t.Fatalf("history changed: %+v", versions)
	}
	freshDetail, err := store.RunDetail(fresh.ID)
	if err != nil || freshDetail.Run.PipelineSnapshot.Steps[0].Name != "versions.v4" {
		t.Fatalf("queued v4 run snapshot changed after rollback: %+v %v", freshDetail.Run.PipelineSnapshot, err)
	}
}
