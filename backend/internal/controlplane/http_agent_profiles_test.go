package controlplane_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.subcult.tv/PatrickFanella/switchyard/internal/controlplane"
)

func agentProfileRequest(t *testing.T, method, url, token, body string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	decoded := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("%s %s: invalid JSON %q", method, url, raw)
		}
	}
	return resp, decoded
}

func TestAPIAgentProfileVersionRoutesAreAuthorizedAuditedAndControlOnly(t *testing.T) {
	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "admin-secret")
	t.Setenv("SWITCHYARD_CONTROL_READ_TOKEN", "read-secret")
	store := controlplane.NewStore(nil)
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	control := httptest.NewServer(api.ControlPlaneHandler())
	t.Cleanup(control.Close)
	model := httptest.NewServer(api.ModelIngressHandler())
	t.Cleanup(model.Close)

	draft := `{"expected_version":0,"name":"Content Writer","role":"writer","default_model":"gpt-5.4","allowed_models":["gpt-5.4"],"allowed_executors":["model"],"prompt_contract":"Persisted contract.","note":"first draft"}`

	// Model listener never exposes profile routes.
	for _, route := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/agent-profiles", ""},
		{http.MethodGet, "/api/agent-profiles/content-writer", ""},
		{http.MethodPut, "/api/agent-profiles/content-writer", draft},
		{http.MethodGet, "/api/agent-profiles/content-writer/versions", ""},
		{http.MethodGet, "/api/agent-profiles/content-writer/audit", ""},
		{http.MethodPost, "/api/agent-profiles/content-writer/activate", `{"version":1}`},
		{http.MethodPost, "/api/agent-profiles/content-writer/rollback", `{"version":1}`},
	} {
		resp, _ := agentProfileRequest(t, route.method, model.URL+route.path, "admin-secret", route.body)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("model listener %s %s status = %d, want 404", route.method, route.path, resp.StatusCode)
		}
	}

	// Unauthenticated and read-only callers cannot mutate.
	if resp, _ := agentProfileRequest(t, http.MethodPut, control.URL+"/api/agent-profiles/content-writer", "", draft); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous PUT status = %d", resp.StatusCode)
	}
	if resp, _ := agentProfileRequest(t, http.MethodPut, control.URL+"/api/agent-profiles/content-writer", "read-secret", draft); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("read-token PUT status = %d", resp.StatusCode)
	}
	if resp, _ := agentProfileRequest(t, http.MethodPost, control.URL+"/api/agent-profiles/content-writer/activate", "read-secret", `{"version":1}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("read-token activate status = %d", resp.StatusCode)
	}
	if _, err := store.AgentProfileIdentity("content-writer"); err == nil {
		t.Fatal("rejected requests must not persist anything")
	}

	// Static default is readable before anything is persisted.
	resp, body := agentProfileRequest(t, http.MethodGet, control.URL+"/api/agent-profiles/content-writer", "read-secret", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET static profile status = %d", resp.StatusCode)
	}
	if profile := body["profile"].(map[string]any); profile["source"] != "static" || profile["version"].(float64) != 0 {
		t.Fatalf("static profile = %+v", profile)
	}
	if identity := body["identity"].(map[string]any); identity["static_default"] != true || identity["active_version"].(float64) != 0 {
		t.Fatalf("static identity = %+v", identity)
	}
	resp, body = agentProfileRequest(t, http.MethodGet, control.URL+"/api/agent-profiles/content-writer/versions", "read-secret", "")
	if resp.StatusCode != http.StatusOK || len(body["profile_versions"].([]any)) != 0 {
		t.Fatalf("static versions status=%d body=%+v", resp.StatusCode, body)
	}
	if resp, _ := agentProfileRequest(t, http.MethodGet, control.URL+"/api/agent-profiles/unknown-profile", "read-secret", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown profile status = %d", resp.StatusCode)
	}

	// Invalid bodies are rejected with 400 and unknown fields are refused.
	if resp, _ := agentProfileRequest(t, http.MethodPut, control.URL+"/api/agent-profiles/content-writer", "admin-secret", `{"expected_version":0,"name":"x","role":"r","allowed_executors":["shell"],"prompt_contract":"c"}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid executor status = %d", resp.StatusCode)
	}
	if resp, _ := agentProfileRequest(t, http.MethodPut, control.URL+"/api/agent-profiles/content-writer", "admin-secret", `{"expected_version":0,"name":"x","role":"r","allowed_executors":["model"],"prompt_contract":"c","status":"active"}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d", resp.StatusCode)
	}

	// Admin saves a draft; a stale concurrent save conflicts.
	resp, body = agentProfileRequest(t, http.MethodPut, control.URL+"/api/agent-profiles/content-writer", "admin-secret", draft)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT draft status = %d body=%+v", resp.StatusCode, body)
	}
	version := body["profile_version"].(map[string]any)
	if version["version"].(float64) != 1 || version["status"] != "draft" || version["provenance"].(map[string]any)["actor"] != "token:admin" {
		t.Fatalf("draft version = %+v", version)
	}
	if resp, _ := agentProfileRequest(t, http.MethodPut, control.URL+"/api/agent-profiles/content-writer", "admin-secret", draft); resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale PUT status = %d", resp.StatusCode)
	}
	// Draft does not serve yet.
	_, body = agentProfileRequest(t, http.MethodGet, control.URL+"/api/agent-profiles", "read-secret", "")
	profiles := body["profiles"].([]any)
	if len(profiles) != 4 || profiles[0].(map[string]any)["source"] != "static" || len(body["identities"].([]any)) != 1 {
		t.Fatalf("list before activation = %+v", body)
	}

	// Activation validates, records the actor, and switches the effective profile.
	if resp, _ := agentProfileRequest(t, http.MethodPost, control.URL+"/api/agent-profiles/content-writer/activate", "admin-secret", `{"version":0}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("activate version 0 status = %d", resp.StatusCode)
	}
	if resp, _ := agentProfileRequest(t, http.MethodPost, control.URL+"/api/agent-profiles/content-writer/activate", "admin-secret", `{"version":7}`); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("activate missing version status = %d", resp.StatusCode)
	}
	resp, body = agentProfileRequest(t, http.MethodPost, control.URL+"/api/agent-profiles/content-writer/activate", "admin-secret", `{"version":1,"reason":"ship it"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("activate status = %d body=%+v", resp.StatusCode, body)
	}
	if profile := body["profile"].(map[string]any); profile["source"] != "persisted" || profile["version"].(float64) != 1 || profile["prompt_contract"] != "Persisted contract." {
		t.Fatalf("activated profile = %+v", profile)
	}
	if resp, _ := agentProfileRequest(t, http.MethodPost, control.URL+"/api/agent-profiles/content-writer/activate", "admin-secret", `{"version":1}`); resp.StatusCode != http.StatusConflict {
		t.Fatalf("re-activate status = %d", resp.StatusCode)
	}

	// Second version, then rollback to the first through a new version.
	second := strings.Replace(strings.Replace(draft, `"expected_version":0`, `"expected_version":1`, 1), "Persisted contract.", "Second contract.", 1)
	if resp, _ = agentProfileRequest(t, http.MethodPut, control.URL+"/api/agent-profiles/content-writer", "admin-secret", second); resp.StatusCode != http.StatusOK {
		t.Fatalf("second PUT status = %d", resp.StatusCode)
	}
	if resp, _ = agentProfileRequest(t, http.MethodPost, control.URL+"/api/agent-profiles/content-writer/activate", "admin-secret", `{"version":2}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("second activate status = %d", resp.StatusCode)
	}
	if resp, _ = agentProfileRequest(t, http.MethodPost, control.URL+"/api/agent-profiles/content-writer/rollback", "admin-secret", `{"version":2}`); resp.StatusCode != http.StatusConflict {
		t.Fatalf("rollback to active status = %d", resp.StatusCode)
	}
	resp, body = agentProfileRequest(t, http.MethodPost, control.URL+"/api/agent-profiles/content-writer/rollback", "admin-secret", `{"version":1,"reason":"regression"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rollback status = %d body=%+v", resp.StatusCode, body)
	}
	if body["rolled_back_from_version"].(float64) != 1 || body["profile_version"].(map[string]any)["version"].(float64) != 3 || body["profile"].(map[string]any)["prompt_contract"] != "Persisted contract." {
		t.Fatalf("rollback body = %+v", body)
	}

	// History and audit are readable with the read token and remain complete.
	_, body = agentProfileRequest(t, http.MethodGet, control.URL+"/api/agent-profiles/content-writer/versions", "read-secret", "")
	versions := body["profile_versions"].([]any)
	if len(versions) != 3 || versions[0].(map[string]any)["status"] != "active" || versions[1].(map[string]any)["status"] != "superseded" || versions[2].(map[string]any)["prompt_contract"] != "Persisted contract." {
		t.Fatalf("versions = %+v", versions)
	}
	_, body = agentProfileRequest(t, http.MethodGet, control.URL+"/api/agent-profiles/content-writer/audit?limit=2", "read-secret", "")
	audit := body["audit"].([]any)
	if len(audit) != 2 || audit[0].(map[string]any)["action"] != "rolled-back" || audit[0].(map[string]any)["actor"] != "token:admin" || audit[0].(map[string]any)["reason"] != "regression" {
		t.Fatalf("audit = %+v", audit)
	}
	_, body = agentProfileRequest(t, http.MethodGet, control.URL+"/api/agent-profiles/content-writer/audit", "read-secret", "")
	if len(body["audit"].([]any)) != 5 {
		t.Fatalf("full audit = %+v", body["audit"])
	}

	// Activation that would break a shipped pipeline is rejected before any write.
	narrow := `{"expected_version":0,"name":"Devlog","role":"summarizer","allowed_executors":["opencode"],"prompt_contract":"narrow"}`
	if resp, _ = agentProfileRequest(t, http.MethodPut, control.URL+"/api/agent-profiles/devlog-summarizer", "admin-secret", narrow); resp.StatusCode != http.StatusOK {
		t.Fatalf("narrow draft status = %d", resp.StatusCode)
	}
	resp, body = agentProfileRequest(t, http.MethodPost, control.URL+"/api/agent-profiles/devlog-summarizer/activate", "admin-secret", `{"version":1}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("breaking activation status = %d body=%+v", resp.StatusCode, body)
	}
	if identity, _ := store.AgentProfileIdentity("devlog-summarizer"); identity.ActiveVersion != 0 {
		t.Fatalf("breaking activation must not persist: %+v", identity)
	}
	if entries, _ := store.ListAgentProfileAudit("devlog-summarizer", 0); len(entries) != 1 {
		t.Fatalf("rejected activation must not audit as activated: %+v", entries)
	}
}
