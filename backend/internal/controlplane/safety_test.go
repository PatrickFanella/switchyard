package controlplane_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/PatrickFanella/switchyard/internal/controlplane"
)

func TestOperatorSafetyWarnsWhenAutonomyFullWithoutProxyAuth(t *testing.T) {
	t.Setenv("OPEN_PILOT_AUTONOMY_MODE", "full")
	t.Setenv("SWITCHYARD_PUBLIC_URL", "https://switchyard.example.test")

	status := controlplane.NewEnvOperatorSafetyStatus()
	if status.OpenPilotMode != "full" || status.ReverseProxyAuth || len(status.Warnings) == 0 {
		t.Fatalf("status = %+v", status)
	}
}

func TestOperatorSafetyWarnsWhenAutonomyFullRequiresCIWithoutGitea(t *testing.T) {
	t.Setenv("OPEN_PILOT_AUTONOMY_MODE", "full")
	t.Setenv("SWITCHYARD_REVERSE_PROXY_AUTH", "true")

	status := controlplane.NewEnvOperatorSafetyStatus()
	joined := strings.Join(status.Warnings, "\n")
	if !strings.Contains(joined, "OPEN_PILOT_CI_CHECK_MODE=require-pass") {
		t.Fatalf("warnings = %+v", status.Warnings)
	}
}

func TestOperatorSafetyAllowsExplicitCIDisabledWithoutGitea(t *testing.T) {
	t.Setenv("OPEN_PILOT_AUTONOMY_MODE", "full")
	t.Setenv("SWITCHYARD_REVERSE_PROXY_AUTH", "true")
	t.Setenv("OPEN_PILOT_CI_CHECK_MODE", "disabled")

	status := controlplane.NewEnvOperatorSafetyStatus()
	joined := strings.Join(status.Warnings, "\n")
	if strings.Contains(joined, "OPEN_PILOT_CI_CHECK_MODE=require-pass") {
		t.Fatalf("warnings = %+v", status.Warnings)
	}
}

func TestOperatorSafetyEndpoint(t *testing.T) {
	t.Setenv("OPEN_PILOT_AUTONOMY_MODE", "full")
	t.Setenv("SWITCHYARD_REVERSE_PROXY_AUTH", "true")
	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	runner := controlplane.NewRunner(store)
	req := httptest.NewRequest(http.MethodGet, "/api/operator-safety", nil)
	rec := httptest.NewRecorder()

	controlplane.NewAPI(store, runner).Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"open_pilot_autonomy_mode":"full"`) || !strings.Contains(rec.Body.String(), `"reverse_proxy_auth":true`) {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}
