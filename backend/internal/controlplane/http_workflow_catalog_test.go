package controlplane_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.subcult.tv/PatrickFanella/switchyard/internal/controlplane"
)

// The workflow draft editor shows the backend's validation message verbatim,
// so every rejected save must say which field or step is wrong.
func TestAPIWorkflowDefinitionSaveReturnsExplicitValidationErrors(t *testing.T) {
	store := controlplane.NewStore(nil)
	srv := httptest.NewServer(controlplane.NewAPI(store, controlplane.NewRunner(store)).Handler())
	t.Cleanup(srv.Close)

	definition := func(steps string, extra string) string {
		return `{"name":"Custom","pipeline":{"id":"custom-pipeline","name":"Custom","steps":[` + steps + `]}` + extra + `}`
	}
	for name, test := range map[string]struct {
		body string
		want string
	}{
		"missing name":        {`{"pipeline":{"id":"p","name":"p","steps":[{"id":"a","position":1,"name":"a","executor":"internal"}]}}`, "name is required"},
		"no steps":            {definition(``, ``), "pipeline needs at least one step"},
		"duplicate step id":   {definition(`{"id":"a","position":1,"name":"a","executor":"internal"},{"id":"a","position":2,"name":"b","executor":"internal"}`, ``), `step "a": id is used by more than one step`},
		"position gap":        {definition(`{"id":"a","position":1,"name":"a","executor":"internal"},{"id":"b","position":3,"name":"b","executor":"internal"}`, ``), "position 2 is missing"},
		"unsupported":         {definition(`{"id":"a","position":1,"name":"a","executor":"opencode"}`, ``), `step "a": executor "opencode" is not supported; use one of internal, model, command, http`},
		"unknown profile":     {definition(`{"id":"a","position":1,"name":"a","executor":"model","profile":"missing"}`, ``), `step "a": profile "missing" is not a known agent profile`},
		"profile executor":    {definition(`{"id":"a","position":1,"name":"a","executor":"command","profile":"content-writer"}`, ``), `step "a": profile "content-writer" does not allow executor "command"`},
		"required input":      {definition(`{"id":"a","position":1,"name":"a","executor":"internal"}`, `,"input_shape":{"type":"object","required":["topic"]}`), `input_shape.required names "topic"`},
		"input type":          {definition(`{"id":"a","position":1,"name":"a","executor":"internal"}`, `,"input_shape":{"type":"object","properties":{"topic":{"type":"text"}}}`), "input_shape.properties.topic.type must be one of"},
		"active status write": {definition(`{"id":"a","position":1,"name":"a","executor":"internal"}`, `,"status":"active"`), "saving writes a draft"},
		"later reference":     {definition(`{"id":"a","position":1,"name":"a","executor":"internal","inputs":{"summary":"{{steps.b.outputs.summary}}"}},{"id":"b","position":2,"name":"b","executor":"internal"}`, ``), `references step "b" which does not run earlier`},
	} {
		t.Run(name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/workflow-definitions/custom", strings.NewReader(test.body))
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var body struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, message = %q", resp.StatusCode, body.Error.Message)
			}
			if !strings.HasPrefix(body.Error.Message, "invalid input: ") || !strings.Contains(body.Error.Message, test.want) {
				t.Fatalf("message = %q, want it to contain %q", body.Error.Message, test.want)
			}
		})
	}
}

func TestControlAccessReportsCallerRole(t *testing.T) {
	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "admin-secret")
	t.Setenv("SWITCHYARD_CONTROL_READ_TOKEN", "read-secret")
	store := controlplane.NewStore(nil)
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	control := httptest.NewServer(api.ControlPlaneHandler())
	t.Cleanup(control.Close)
	model := httptest.NewServer(api.ModelIngressHandler())
	t.Cleanup(model.Close)
	bare := httptest.NewServer(api.Handler())
	t.Cleanup(bare.Close)

	get := func(base, token string) (int, controlplane.ControlAccess) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, base+"/api/control-access", nil)
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
		defer resp.Body.Close()
		var access controlplane.ControlAccess
		if resp.StatusCode == http.StatusOK {
			if err := json.NewDecoder(resp.Body).Decode(&access); err != nil {
				t.Fatal(err)
			}
		}
		return resp.StatusCode, access
	}

	if status, access := get(control.URL, "admin-secret"); status != http.StatusOK || access != (controlplane.ControlAccess{Role: "admin", CanWrite: true}) {
		t.Fatalf("admin access = %d %+v", status, access)
	}
	if status, access := get(control.URL, "read-secret"); status != http.StatusOK || access != (controlplane.ControlAccess{Role: "read", CanWrite: false}) {
		t.Fatalf("read access = %d %+v", status, access)
	}
	if status, _ := get(control.URL, ""); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", status)
	}
	if status, _ := get(model.URL, "admin-secret"); status != http.StatusNotFound {
		t.Fatalf("model ingress status = %d, want 404", status)
	}
	if status, access := get(bare.URL, ""); status != http.StatusOK || access != (controlplane.ControlAccess{Role: "unrestricted", CanWrite: true}) {
		t.Fatalf("bare handler access = %d %+v", status, access)
	}
}
