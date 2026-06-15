package controlplane_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"git.subcult.tv/PatrickFanella/switchyard/internal/controlplane"
)

func TestEnvOpenCodeExecutorSendsPromptThroughOCQ(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer ocq-key" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-Request-ID"); got != "request-1" {
			t.Fatalf("X-Request-ID = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["model"] != "opencode-test" || body["ocq_session_id"] != "ses-1" {
			t.Fatalf("body = %+v", body)
		}
		w.Header().Set("X-OCQ-Request-ID", "req-1")
		w.Header().Set("X-OCQ-Session", "ses-2")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"done"}}]}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("OCQ_BASE_URL", server.URL)
	t.Setenv("OCQ_GATEWAY_KEY", "ocq-key")

	response, err := controlplane.NewEnvOpenCodeExecutor().SendPrompt(t.Context(), controlplane.OpenCodeRequest{Model: "opencode-test", Prompt: "do work", SessionID: "ses-1", RequestID: "request-1"})
	if err != nil {
		t.Fatalf("SendPrompt() error = %v", err)
	}
	if response.Content != "done" || response.SessionID != "ses-2" || response.RequestID != "req-1" || response.CorrelationIDs["ocq_request_id"] != "req-1" {
		t.Fatalf("response = %+v", response)
	}
	if response.CorrelationIDs["opencode_executor"] != "switchyard" || response.CorrelationIDs["opencode_executor_backend"] != "ocq" {
		t.Fatalf("correlation = %+v", response.CorrelationIDs)
	}
}

func TestEnvOCQExecutorCompatibilityConstructor(t *testing.T) {
	var executor controlplane.OpenCodeExecutor = controlplane.NewEnvOCQExecutor()
	if executor == nil {
		t.Fatalf("NewEnvOCQExecutor() returned nil")
	}
}

func TestEnvOpenCodeExecutorKeepsClientRequestIDWhenOCQOmitsHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Request-ID"); got != "client-request" {
			t.Fatalf("X-Request-ID = %q", got)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"done"}}]}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("OCQ_BASE_URL", server.URL)

	response, err := controlplane.NewEnvOpenCodeExecutor().SendPrompt(t.Context(), controlplane.OpenCodeRequest{Model: "opencode-test", Prompt: "do work", RequestID: "client-request"})
	if err != nil {
		t.Fatalf("SendPrompt() error = %v", err)
	}
	if response.RequestID != "client-request" || response.CorrelationIDs["ocq_request_id"] != "client-request" {
		t.Fatalf("response = %+v", response)
	}
}

func TestEnvOpenCodeExecutorHonorsConfiguredTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(25 * time.Millisecond)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"done"}}]}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("OCQ_BASE_URL", server.URL)
	t.Setenv("OPENCODE_EXECUTOR_TIMEOUT", "1ms")

	_, err := controlplane.NewEnvOpenCodeExecutor().SendPrompt(t.Context(), controlplane.OpenCodeRequest{Model: "opencode-test", Prompt: "do work"})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SendPrompt() error = %v, want deadline exceeded", err)
	}
}
