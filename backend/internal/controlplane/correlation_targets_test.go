package controlplane_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.subcult.tv/PatrickFanella/switchyard/internal/controlplane"
)

func TestCorrelationTargetsOmittedWhenUnconfigured(t *testing.T) {
	t.Setenv("GITEA_BASE_URL", "")
	t.Setenv("SWITCHYARD_TRACE_UI_URL", "")

	targets, warnings := controlplane.NewEnvCorrelationTargets()
	if targets.GiteaBaseURL != "" || targets.TraceUIBaseURL != "" || len(warnings) != 0 {
		t.Fatalf("targets = %+v warnings = %v", targets, warnings)
	}
}

func TestCorrelationTargetsStripCredentialsQueryAndTrailingSlash(t *testing.T) {
	t.Setenv("GITEA_BASE_URL", "https://oauth2:secret-token@Git.Example.Test/gitea/?x=1#frag")
	t.Setenv("SWITCHYARD_TRACE_UI_URL", "https://phoenix.example.test/projects/abc/traces/{trace_id}?tab=spans")

	targets, warnings := controlplane.NewEnvCorrelationTargets()
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if targets.GiteaBaseURL != "https://git.example.test/gitea" {
		t.Fatalf("gitea base = %q", targets.GiteaBaseURL)
	}
	if targets.TraceUIBaseURL != "https://phoenix.example.test/projects/abc/traces/{trace_id}" {
		t.Fatalf("trace base = %q", targets.TraceUIBaseURL)
	}
	if strings.Contains(targets.GiteaBaseURL, "secret-token") {
		t.Fatalf("credentials leaked: %q", targets.GiteaBaseURL)
	}
}

func TestCorrelationTargetsRejectUnsupportedSchemes(t *testing.T) {
	t.Setenv("GITEA_BASE_URL", "ftp://git.example.test")
	t.Setenv("SWITCHYARD_TRACE_UI_URL", "javascript:alert(1)")

	targets, warnings := controlplane.NewEnvCorrelationTargets()
	if targets.GiteaBaseURL != "" || targets.TraceUIBaseURL != "" {
		t.Fatalf("targets = %+v", targets)
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "GITEA_BASE_URL is not an absolute http(s) URL") || !strings.Contains(joined, "SWITCHYARD_TRACE_UI_URL is not an absolute http(s) URL") {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestOperatorSafetyPublishesCorrelationTargetsOnControlListenerOnly(t *testing.T) {
	t.Setenv("GITEA_BASE_URL", "https://git.example.test/")
	t.Setenv("SWITCHYARD_TRACE_UI_URL", "https://trace.example.test/ui")
	t.Setenv("SWITCHYARD_CONTROL_READ_TOKEN", "read-secret")
	store := controlplane.NewStore(nil)
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))

	control := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/operator-safety", nil)
	request.Header.Set("Authorization", "Bearer read-secret")
	api.ControlPlaneHandler().ServeHTTP(control, request)
	if control.Code != http.StatusOK {
		t.Fatalf("control status = %d body = %s", control.Code, control.Body.String())
	}
	var payload struct {
		CorrelationTargets controlplane.CorrelationTargets `json:"correlation_targets"`
	}
	if err := json.Unmarshal(control.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.CorrelationTargets.GiteaBaseURL != "https://git.example.test" || payload.CorrelationTargets.TraceUIBaseURL != "https://trace.example.test/ui" {
		t.Fatalf("correlation targets = %+v", payload.CorrelationTargets)
	}

	model := httptest.NewRecorder()
	api.ModelIngressHandler().ServeHTTP(model, httptest.NewRequest(http.MethodGet, "/api/operator-safety", nil))
	if model.Code == http.StatusOK {
		t.Fatalf("model ingress exposed operator safety: status = %d", model.Code)
	}
}
