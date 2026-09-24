package controlplane_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.subcult.tv/PatrickFanella/switchyard/internal/controlplane"
)

func TestAPIRunModelPolicyPreviewRejectionAndRunEvidence(t *testing.T) {
	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "admin-secret")
	t.Setenv("SWITCHYARD_CONTROL_READ_TOKEN", "read-secret")
	t.Setenv("DEVLOG_MODEL", "model-a")
	t.Setenv("DEVLOG_ALLOWED_MODELS", "model-a,model-b")
	store := controlplane.NewStore(nil)
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	runner := controlplane.NewRunner(store)
	api := controlplane.NewAPI(store, runner)
	control := httptest.NewServer(api.ControlPlaneHandler())
	t.Cleanup(control.Close)
	model := httptest.NewServer(api.ModelIngressHandler())
	t.Cleanup(model.Close)

	if resp, _ := agentProfileRequest(t, http.MethodGet, model.URL+"/api/workflows/devlog/model-policy", "admin-secret", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("model listener status = %d, want 404", resp.StatusCode)
	}
	if resp, _ := agentProfileRequest(t, http.MethodGet, control.URL+"/api/workflows/devlog/model-policy", "", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous preview status = %d", resp.StatusCode)
	}
	if resp, _ := agentProfileRequest(t, http.MethodGet, control.URL+"/api/workflows/no-such-workflow/model-policy", "read-secret", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown workflow status = %d", resp.StatusCode)
	}

	resp, body := agentProfileRequest(t, http.MethodGet, control.URL+"/api/workflows/devlog/model-policy?model=model-c", "read-secret", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("read-token preview status = %d body = %v", resp.StatusCode, body)
	}
	policy := body["policy"].(map[string]any)
	if policy["override_status"] != "rejected" || policy["accepts_override"] != true || policy["override_restricted"] != true {
		t.Fatalf("preview policy = %+v", policy)
	}
	rejections, _ := policy["rejections"].([]any)
	if len(rejections) != 1 || !strings.Contains(rejections[0].(string), `model "model-c" is not allowed for workflow devlog`) || !strings.Contains(rejections[0].(string), "allowed: model-a, model-b") {
		t.Fatalf("rejections = %+v", rejections)
	}

	// Dispatch enforces the same rule, and read tokens cannot dispatch.
	dispatch := `{"workflow_id":"devlog","inputs":{"model":"model-c"}}`
	if resp, _ := agentProfileRequest(t, http.MethodPost, control.URL+"/api/runs", "read-secret", dispatch); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("read-token dispatch status = %d", resp.StatusCode)
	}
	resp, body = agentProfileRequest(t, http.MethodPost, control.URL+"/api/runs", "admin-secret", dispatch)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("disallowed override dispatch status = %d body = %v", resp.StatusCode, body)
	}
	message, _ := body["error"].(map[string]any)["message"].(string)
	if !strings.Contains(message, `model override "model-c" rejected`) {
		t.Fatalf("dispatch rejection = %q", message)
	}
	if runs := store.ListRuns(); len(runs) != 0 {
		t.Fatalf("rejected dispatch created runs: %+v", runs)
	}

	run, err := runner.Enqueue(controlplane.CreateRunInput{WorkflowID: "devlog", Inputs: map[string]string{"model": "model-b"}})
	if err != nil {
		t.Fatal(err)
	}
	resp, body = agentProfileRequest(t, http.MethodGet, control.URL+"/api/runs/"+run.ID, "read-secret", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("run detail status = %d", resp.StatusCode)
	}
	evidence, ok := body["policy"].(map[string]any)
	if !ok || evidence["override"] != "model-b" || evidence["override_status"] != "accepted" {
		t.Fatalf("run policy evidence = %+v", body["policy"])
	}
	found := false
	for _, raw := range evidence["steps"].([]any) {
		step := raw.(map[string]any)
		if step["step_id"] == "generate-markdown" {
			found = true
			if step["profile_id"] != "devlog-summarizer" || step["profile_source"] != "static" || step["model"] != "model-b" || step["model_source"] != "override" || step["inherited_model"] != "model-a" || step["recorded"] != false {
				t.Fatalf("generate step evidence = %+v", step)
			}
		}
	}
	if !found {
		t.Fatalf("run policy steps = %+v", evidence["steps"])
	}
}
