package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenPilotRunCLI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workflows/open-pilot/run" || r.Method != http.MethodPost {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"run":{"id":"run_1","status":"succeeded"}}`))
	}))
	t.Cleanup(server.Close)

	var out bytes.Buffer
	err := run([]string{"open-pilot", "run", "--repo", "OWNER/REPO", "--issue", "123", "--api-url", server.URL}, &out)
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if out.String() != "run_1 succeeded\n" {
		t.Fatalf("out = %q", out.String())
	}
}

func TestOpenPilotRunCLIWaitsForQueuedRun(t *testing.T) {
	t.Setenv("SWITCHYARD_CLI_POLL_INTERVAL", "1ms")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/workflows/open-pilot/run":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"run":{"id":"run_async","status":"queued"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/runs/run_async":
			_, _ = w.Write([]byte(`{"run":{"id":"run_async","status":"succeeded"},"steps":[],"events":[],"artifacts":[]}`))
		default:
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	var out bytes.Buffer
	if err := run([]string{"open-pilot", "run", "--repo", "OWNER/REPO", "--issue", "123", "--wait-timeout", "1s", "--api-url", server.URL}, &out); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if out.String() != "run_async succeeded\n" || requests != 2 {
		t.Fatalf("out/requests = %q/%d", out.String(), requests)
	}
}

func TestOpenPilotCancelCLI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/runs/run_1/cancel" || r.Method != http.MethodPost {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"run":{"id":"run_1","status":"canceled"}}`))
	}))
	t.Cleanup(server.Close)

	var out bytes.Buffer
	err := run([]string{"open-pilot", "cancel", "--run", "run_1", "--api-url", server.URL}, &out)
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if out.String() != "run_1 canceled\n" {
		t.Fatalf("out = %q", out.String())
	}
}

func TestModelsListCLI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/models" || r.Method != http.MethodGet {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"ollama":{"status":"healthy","models":[{"id":"qwen3.5:latest"}]},"opencode":{"status":"healthy","models":[{"id":"gpt-5.5","provider":"openai"}]}}`))
	}))
	t.Cleanup(server.Close)

	var out bytes.Buffer
	err := run([]string{"models", "list", "--api-url", server.URL}, &out)
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !strings.Contains(out.String(), "ollama/qwen3.5:latest") || !strings.Contains(out.String(), "openai/gpt-5.5") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestModelsPullCLI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/models/pull" || r.Method != http.MethodPost {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"provider":"ollama","results":[{"model":"qwen3.5:latest","status":"success"}],"count":1}`))
	}))
	t.Cleanup(server.Close)

	var out bytes.Buffer
	err := run([]string{"models", "pull", "--models", "qwen3.5:latest", "--api-url", server.URL}, &out)
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if out.String() != "qwen3.5:latest success\n" {
		t.Fatalf("out = %q", out.String())
	}
}
