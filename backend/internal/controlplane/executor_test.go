package controlplane_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"git.subcult.tv/PatrickFanella/switchyard/internal/controlplane"
)

func TestEnvWorkflowExecutorsDispatchesChangemaker(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/runs" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("Authorization = %q", got)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["kind"] != "manual" || body["since"] != "2026-01-01" || body["until"] != "2026-01-02" {
			t.Fatalf("body = %+v", body)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"state":"running"}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("CHANGEMAKER_BASE_URL", server.URL)
	t.Setenv("CHANGEMAKER_ADMIN_TOKEN", "test-token")

	executor := controlplane.NewEnvWorkflowExecutors().ExecutorFor("changemaker")
	execution, err := executor.Execute(t.Context(), controlplane.Run{
		ID:         "run-1",
		WorkflowID: "changemaker",
		Status:     controlplane.RunRunning,
		Inputs: map[string]string{
			"since": "2026-01-01",
			"until": "2026-01-02",
		},
		CreatedAt: time.Unix(100, 0),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if execution.Summary != "Dispatched Changemaker run" || len(execution.Events) != 2 {
		t.Fatalf("execution = %+v", execution)
	}
}

func TestEnvWorkflowExecutorsStubsChangemakerWhenUnconfigured(t *testing.T) {
	t.Setenv("CHANGEMAKER_BASE_URL", "")
	executor := controlplane.NewEnvWorkflowExecutors().ExecutorFor("changemaker")
	execution, err := executor.Execute(t.Context(), controlplane.Run{WorkflowID: "changemaker"})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if execution.Events[0].Type != "workflow.stub" {
		t.Fatalf("execution = %+v", execution)
	}
}
