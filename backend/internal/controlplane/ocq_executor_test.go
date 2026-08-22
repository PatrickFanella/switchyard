package controlplane_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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
		if body["model"] != "opencode-test" || body["ocq_session_id"] != "ses-1" || body["directory"] != "/tmp/worktree" {
			t.Fatalf("body = %+v", body)
		}
		w.Header().Set("X-OCQ-Request-ID", "req-1")
		w.Header().Set("X-OCQ-Session", "ses-2")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"done"}}]}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "ocq")
	t.Setenv("OCQ_BASE_URL", server.URL)
	t.Setenv("OCQ_GATEWAY_KEY", "ocq-key")

	response, err := controlplane.NewEnvOpenCodeExecutor().SendPrompt(t.Context(), controlplane.OpenCodeRequest{Model: "opencode-test", Prompt: "do work", SessionID: "ses-1", RequestID: "request-1", WorkDir: "/tmp/worktree"})
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

func TestEnvOpenCodeExecutorSendsPromptDirectlyToOpenCode(t *testing.T) {
	var sessionDirectory string
	var messageBody map[string]any
	var messagePosted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/session" && r.Method == http.MethodPost:
			sessionDirectory = r.URL.Query().Get("directory")
			_, _ = w.Write([]byte(`{"id":"ses-native"}`))
		case r.URL.Path == "/session/ses-native/message" && r.Method == http.MethodPost:
			messagePosted.Store(true)
			if got := r.Header.Get("X-Request-ID"); got != "req-native" {
				t.Fatalf("X-Request-ID = %q", got)
			}
			if err := json.NewDecoder(r.Body).Decode(&messageBody); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			_, _ = w.Write([]byte(`{"parts":[{"type":"text","text":"native done"}]}`))
		case r.URL.Path == "/session/ses-native/message" && r.Method == http.MethodGet:
			if !messagePosted.Load() {
				_, _ = w.Write([]byte(`[{"info":{"id":"before","role":"assistant","finish":"stop"}}]`))
				return
			}
			_, _ = w.Write([]byte(`[{"info":{"id":"after","role":"assistant","finish":"stop"},"parts":[{"type":"text","text":"native done"}]}]`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("OPENCODE_SERVER_URL", server.URL)
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "native")

	response, err := controlplane.NewEnvOpenCodeExecutor().SendPrompt(t.Context(), controlplane.OpenCodeRequest{Model: "openai/gpt-test", Prompt: "do native work", RequestID: "req-native", WorkDir: "/tmp/worktree"})
	if err != nil {
		t.Fatalf("SendPrompt() error = %v", err)
	}
	if sessionDirectory != "/tmp/worktree" || response.Content != "native done" || response.SessionID != "ses-native" || response.RequestID != "req-native" {
		t.Fatalf("response = %+v directory=%q", response, sessionDirectory)
	}
	if response.CorrelationIDs["opencode_executor_backend"] != "native" || response.CorrelationIDs["opencode_session_id"] != "ses-native" {
		t.Fatalf("correlation = %+v", response.CorrelationIDs)
	}
	if messageBody["model"] == nil || len(messageBody["parts"].([]any)) != 1 {
		t.Fatalf("message body = %+v", messageBody)
	}
}

func TestEnvOpenCodeExecutorWaitsForNativeSessionIdle(t *testing.T) {
	var marker atomic.Bool
	var messagePosted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/session":
			_, _ = w.Write([]byte(`{"id":"ses-idle"}`))
		case r.URL.Path == "/session/ses-idle/message" && r.Method == http.MethodPost:
			messagePosted.Store(true)
			go func() {
				time.Sleep(20 * time.Millisecond)
				marker.Store(true)
			}()
			_, _ = w.Write([]byte(`{"parts":[{"type":"text","text":"queued"}]}`))
		case r.URL.Path == "/session/ses-idle/message" && r.Method == http.MethodGet:
			if !messagePosted.Load() {
				_, _ = w.Write([]byte(`[{"info":{"id":"before","role":"assistant","finish":"stop"}}]`))
				return
			}
			if marker.Load() {
				_, _ = w.Write([]byte(`[{"info":{"id":"after","role":"assistant","finish":"stop"},"parts":[{"type":"text","text":"finished"}]}]`))
				return
			}
			_, _ = w.Write([]byte(`[{"info":{"role":"assistant","finish":"tool-calls"}}]`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("OPENCODE_SERVER_URL", server.URL)
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "native")
	t.Setenv("OPENCODE_SESSION_STATUS_POLL_INTERVAL", "1ms")

	response, err := controlplane.NewEnvOpenCodeExecutor().SendPrompt(t.Context(), controlplane.OpenCodeRequest{Model: "openai/gpt-test", Prompt: "work"})
	if err != nil {
		t.Fatalf("SendPrompt() error = %v", err)
	}
	if response.Content != "finished" || !marker.Load() {
		t.Fatalf("response=%+v marker=%v", response, marker.Load())
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
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "ocq")
	t.Setenv("OCQ_BASE_URL", server.URL)

	response, err := controlplane.NewEnvOpenCodeExecutor().SendPrompt(t.Context(), controlplane.OpenCodeRequest{Model: "opencode-test", Prompt: "do work", RequestID: "client-request"})
	if err != nil {
		t.Fatalf("SendPrompt() error = %v", err)
	}
	if response.RequestID != "client-request" || response.CorrelationIDs["ocq_request_id"] != "client-request" {
		t.Fatalf("response = %+v", response)
	}
}

func TestEnvOpenCodeExecutorReturnsOCQCorrelationOnUpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "bad gateway", http.StatusBadGateway) }))
	t.Cleanup(server.Close)
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "ocq")
	t.Setenv("OCQ_BASE_URL", server.URL)
	response, err := controlplane.NewEnvOpenCodeExecutor().SendPrompt(t.Context(), controlplane.OpenCodeRequest{Model: "test", Prompt: "work", RequestID: "failed-request"})
	if err == nil || response.CorrelationIDs["ocq_request_id"] != "failed-request" || response.CorrelationIDs[controlplane.CorrelationDeprecatedBackend] != "ocq" {
		t.Fatalf("response=%+v err=%v", response, err)
	}
}

func TestEnvOpenCodeExecutorHonorsConfiguredTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(25 * time.Millisecond)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"done"}}]}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "ocq")
	t.Setenv("OCQ_BASE_URL", server.URL)
	t.Setenv("OPENCODE_EXECUTOR_TIMEOUT", "1ms")

	_, err := controlplane.NewEnvOpenCodeExecutor().SendPrompt(t.Context(), controlplane.OpenCodeRequest{Model: "opencode-test", Prompt: "do work"})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SendPrompt() error = %v, want deadline exceeded", err)
	}
}

func TestEnvOpenCodeExecutorAbortsNativeSessionOnMessageTimeout(t *testing.T) {
	aborted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/session" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"id":"ses-timeout"}`))
		case r.URL.Path == "/session/ses-timeout/message" && r.Method == http.MethodPost:
			time.Sleep(50 * time.Millisecond)
			_, _ = w.Write([]byte(`{"parts":[{"type":"text","text":"late"}]}`))
		case r.URL.Path == "/session/ses-timeout/message" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`[]`))
		case r.URL.Path == "/session/ses-timeout/abort" && r.Method == http.MethodPost:
			aborted = true
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("OPENCODE_SERVER_URL", server.URL)
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "native")
	t.Setenv("OPENCODE_SESSION_CREATE_TIMEOUT", "1s")
	t.Setenv("OPENCODE_MESSAGE_TIMEOUT", "1ms")
	t.Setenv("OPENCODE_ABORT_TIMEOUT", "1s")

	_, err := controlplane.NewEnvOpenCodeExecutor().SendPrompt(t.Context(), controlplane.OpenCodeRequest{Model: "openai/gpt-test", Prompt: "timeout", WorkDir: "/tmp/worktree"})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SendPrompt() error = %v, want deadline exceeded", err)
	}
	if !aborted {
		t.Fatalf("expected native session abort after timeout")
	}
}

func TestEnvOpenCodeExecutorPersistsNativeSessionCorrelationBeforeMessageCompletes(t *testing.T) {
	messageStarted := make(chan struct{})
	releaseMessage := make(chan struct{})
	var messagePosted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/session":
			_, _ = w.Write([]byte(`{"id":"ses-early"}`))
		case r.URL.Path == "/session/ses-early/message" && r.Method == http.MethodPost:
			messagePosted.Store(true)
			close(messageStarted)
			<-releaseMessage
			_, _ = w.Write([]byte(`{"parts":[{"type":"text","text":"done"}]}`))
		case r.URL.Path == "/session/ses-early/message" && r.Method == http.MethodGet:
			if !messagePosted.Load() {
				_, _ = w.Write([]byte(`[{"info":{"id":"before","role":"assistant","finish":"stop"}}]`))
				return
			}
			_, _ = w.Write([]byte(`[{"info":{"id":"after","role":"assistant","finish":"stop"},"parts":[{"type":"text","text":"done"}]}]`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("OPENCODE_SERVER_URL", server.URL)
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "native")

	correlations := make(chan map[string]string, 1)
	done := make(chan error, 1)
	go func() {
		_, err := controlplane.NewEnvOpenCodeExecutor().SendPrompt(context.Background(), controlplane.OpenCodeRequest{
			Model:     "openai/gpt-test",
			Prompt:    "work",
			RequestID: "request-early",
			OnCorrelation: func(correlation map[string]string) error {
				correlations <- correlation
				return nil
			},
		})
		done <- err
	}()
	select {
	case correlation := <-correlations:
		if correlation["opencode_session_id"] != "ses-early" || correlation["opencode_request_id"] != "request-early" {
			t.Fatalf("correlation = %+v", correlation)
		}
	case <-time.After(time.Second):
		t.Fatal("session correlation was not published")
	}
	select {
	case <-messageStarted:
	case <-time.After(time.Second):
		t.Fatal("message did not start")
	}
	close(releaseMessage)
	if err := <-done; err != nil {
		t.Fatalf("SendPrompt() error = %v", err)
	}
}
