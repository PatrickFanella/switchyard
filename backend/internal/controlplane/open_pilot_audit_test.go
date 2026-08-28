package controlplane

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type failingOpenPilotAuditStore struct{ Store }

func (failingOpenPilotAuditStore) ListOpenPilotIssueStatesForAudit(int) ([]OpenPilotIssueState, error) {
	return nil, errors.New("audit query failed")
}

func TestMemoryOpenPilotAuditIsDeterministicBoundedAndRedacted(t *testing.T) {
	store := NewStore(nil)
	store.mu.Lock()
	for index := 120; index >= 0; index-- {
		issue := decimalTestValue(index + 1)
		store.openPilotIssueStates[openPilotIssueStateKey("Subcult/Switchyard", issue)] = OpenPilotIssueState{ID: "state-" + issue, Repo: "Subcult/Switchyard", Issue: issue, Phase: OpenPilotPhaseRequested}
	}
	store.mu.Unlock()
	states, err := store.ListOpenPilotIssueStatesForAudit(maxOpenPilotAuditStates + 1)
	if err != nil || len(states) != maxOpenPilotAuditStates+1 || states[0].Issue != "1" {
		t.Fatalf("states=%d first=%+v err=%v", len(states), states[0], err)
	}
	report := buildOpenPilotRepositoryAudit(testOpenPilotAdmissionGate(t), states)
	if !report.Truncated || len(report.ActiveOrResumable) != maxOpenPilotAuditStates || report.SingletonAdmissionOK {
		t.Fatalf("report = %+v", report)
	}
	if len(report.Registry) != 1 || strings.Contains(report.Registry[0].Slug, "/workspace/") {
		t.Fatalf("registry leaked execution locations: %+v", report.Registry)
	}
}

func TestOpenPilotAuditGroupsCanonicalAndAliasByRegistryIdentity(t *testing.T) {
	report := buildOpenPilotRepositoryAudit(testOpenPilotAdmissionGate(t), []OpenPilotIssueState{
		{Repo: "Subcult/Switchyard", Issue: "01", Phase: OpenPilotPhaseFailed},
		{Repo: "LEGACY/SWITCHYARD", Issue: "1", Phase: OpenPilotPhaseFailed},
	})
	if len(report.Collisions) != 1 || report.Collisions[0].Repo != "Subcult/Switchyard" || report.Collisions[0].Issue != "1" || report.Collisions[0].States != 2 {
		t.Fatalf("collisions = %+v", report.Collisions)
	}
}

func TestOpenPilotAuditEndpointFailsClosedOnGateAndStoreErrors(t *testing.T) {
	store := NewStore(nil)
	runner := NewRunner(store)
	runner.SetOpenPilotAdmissionGate(testOpenPilotAdmissionGate(t))
	request := httptest.NewRequest(http.MethodGet, "/api/open-pilot/repository-audit", nil)
	response := httptest.NewRecorder()
	NewAPI(failingOpenPilotAuditStore{Store: store}, runner).routes().ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), `"singleton_admission_safe":true`) {
		t.Fatalf("store error response = %d %s", response.Code, response.Body.String())
	}

	t.Setenv("OPEN_PILOT_REPO_PATH", "")
	t.Setenv("OPEN_PILOT_WORKDIR", "")
	runner = NewRunner(store)
	response = httptest.NewRecorder()
	NewAPI(store, runner).routes().ServeHTTP(response, request)
	if response.Code == http.StatusOK || strings.Contains(response.Body.String(), `"singleton_admission_safe":true`) {
		t.Fatalf("gate error response = %d %s", response.Code, response.Body.String())
	}
}

func TestOpenPilotAuditListenerAndAuthenticationBoundary(t *testing.T) {
	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "admin-token")
	t.Setenv("SWITCHYARD_CONTROL_READ_TOKEN", "read-token")
	store := NewStore(nil)
	api := NewAPI(store, NewRunner(store))
	for _, test := range []struct {
		handler http.Handler
		token   string
		want    int
	}{
		{handler: api.ControlPlaneHandler(), want: http.StatusUnauthorized},
		{handler: api.ControlPlaneHandler(), token: "read-token", want: http.StatusOK},
		{handler: api.ModelIngressHandler(), token: "read-token", want: http.StatusNotFound},
	} {
		request := httptest.NewRequest(http.MethodGet, "/api/open-pilot/repository-audit", nil)
		if test.token != "" {
			request.Header.Set("Authorization", "Bearer "+test.token)
		}
		response := httptest.NewRecorder()
		test.handler.ServeHTTP(response, request)
		if response.Code != test.want {
			t.Fatalf("status = %d, want %d", response.Code, test.want)
		}
	}
}
