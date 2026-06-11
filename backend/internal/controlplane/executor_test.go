package controlplane_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestEnvWorkflowExecutorsDispatchesOpenPilotCommand(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "args.txt")
	scriptPath := filepath.Join(dir, "open-pilot-test")
	script := "#!/bin/sh\nprintf '%s\n' \"$*\" > \"" + logPath + "\"\nprintf '{\"ok\":true}'\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	t.Setenv("OPEN_PILOT_COMMAND", scriptPath)

	executor := controlplane.NewEnvWorkflowExecutors().ExecutorFor("open-pilot")
	execution, err := executor.Execute(t.Context(), controlplane.Run{WorkflowID: "open-pilot", Inputs: map[string]string{"repo": "PatrickFanella/switchyard", "issue": "42"}})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	args, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	if string(args) != "run --repo PatrickFanella/switchyard --issue 42 --json\n" {
		t.Fatalf("args = %q", string(args))
	}
	if execution.Summary != "Dispatched Open Pilot issue 42" || execution.Events[1].Type != "open-pilot.accepted" {
		t.Fatalf("execution = %+v", execution)
	}
}

func TestRunnerGeneratesDevlogArtifact(t *testing.T) {
	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	seed, err := store.CreateRun(controlplane.CreateRunInput{WorkflowID: "open-pilot"})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	if _, err := store.FinishRun(seed.ID, controlplane.RunSucceeded, "seed run complete"); err != nil {
		t.Fatalf("FinishRun() error = %v", err)
	}
	runner := controlplane.NewRunner(store)
	run, err := runner.RunTrigger(t.Context(), "manual-devlog")
	if err != nil {
		t.Fatalf("RunTrigger() error = %v", err)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if run.Status != controlplane.RunSucceeded || run.Summary != "Generated Devlog markdown artifact" {
		t.Fatalf("run = %+v", run)
	}
	if len(detail.Artifacts) != 1 || detail.Artifacts[0].Kind != "markdown" || detail.Artifacts[0].Name != "devlog.md" {
		t.Fatalf("artifacts = %+v", detail.Artifacts)
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
