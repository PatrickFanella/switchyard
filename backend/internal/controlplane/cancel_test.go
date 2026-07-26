package controlplane_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"git.subcult.tv/PatrickFanella/switchyard/internal/controlplane"
)

func TestEnvCancelDispatcherCallsOCQAndLlamaLine(t *testing.T) {
	ocq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ocq/requests/ocq-1/abort" || r.Method != http.MethodPost {
			t.Fatalf("ocq request = %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(ocq.Close)
	llama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin/queue/llama-1" || r.Method != http.MethodDelete {
			t.Fatalf("llama request = %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(llama.Close)
	t.Setenv("OCQ_BASE_URL", ocq.URL)
	t.Setenv("LLAMA_LINE_BASE_URL", llama.URL)

	result := controlplane.NewEnvCancelDispatcher().Cancel(t.Context(), controlplane.Run{WorkflowID: "devlog", Inputs: map[string]string{"ocq_request_id": "ocq-1", "llama_line_request_id": "llama-1"}})
	if len(result.Events) != 2 || result.Events[0].Type != "ocq.abort.accepted" || result.Events[1].Type != "llama-line.drop.accepted" {
		t.Fatalf("result = %+v", result)
	}
}

func TestEnvCancelDispatcherUsesRunningStepCorrelationIDs(t *testing.T) {
	ocq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ocq/sessions/ses-1/messages/ocq-step/abort" || r.Method != http.MethodPost {
			t.Fatalf("ocq request = %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(ocq.Close)
	t.Setenv("OCQ_BASE_URL", ocq.URL)

	detail := controlplane.RunDetail{
		Run: controlplane.Run{WorkflowID: "devlog", Inputs: map[string]string{"ocq_request_id": "stale"}},
		Steps: []controlplane.RunStep{{
			Status:         controlplane.StepRunning,
			CorrelationIDs: map[string]string{"ocq_request_id": "ocq-step", "ocq_session_id": "ses-1"},
		}},
	}
	result := controlplane.NewEnvCancelDispatcher().CancelRunDetail(t.Context(), detail)
	if len(result.Events) != 1 || result.Events[0].Type != "ocq.abort.accepted" {
		t.Fatalf("result = %+v", result)
	}
}

func TestEnvCancelDispatcherAbortsNativeOpenCodeSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/session/ses-native/abort" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		user, password, ok := r.BasicAuth()
		if !ok || user != "switchyard" || password != "secret" {
			t.Fatalf("basic auth = %q %q %t", user, password, ok)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)
	t.Setenv("OPENCODE_SERVER_URL", server.URL)
	t.Setenv("OPENCODE_SERVER_USERNAME", "switchyard")
	t.Setenv("OPENCODE_SERVER_PASSWORD", "secret")

	result := controlplane.NewEnvCancelDispatcher().CancelRunDetail(t.Context(), controlplane.RunDetail{Run: controlplane.Run{WorkflowID: "devlog"}, Steps: []controlplane.RunStep{{Status: controlplane.StepRunning, CorrelationIDs: map[string]string{"opencode_session_id": "ses-native"}}}})
	if len(result.Events) != 1 || result.Events[0].Type != "opencode.abort.accepted" {
		t.Fatalf("result = %+v", result)
	}
}
