package controlplane_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.subcult.tv/PatrickFanella/switchyard/internal/controlplane"
)

func TestEnvModelExecutorUsesLlamaLine(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer llama-key" {
			t.Fatalf("Authorization = %q", got)
		}
		w.Header().Set("X-Llama-Line-Request-ID", "llama-request-1")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"model output"}}]}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("LLAMA_LINE_BASE_URL", server.URL)
	t.Setenv("LLAMA_LINE_API_KEY", "llama-key")
	t.Setenv("OCQ_BASE_URL", "")

	response, err := controlplane.NewEnvModelExecutor().Generate(t.Context(), controlplane.ModelRequest{Model: "model-a", Messages: []controlplane.ModelMessage{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if response.Content != "model output" || response.CorrelationIDs["model_source"] != "llama-line" || response.CorrelationIDs["llama_line_request_id"] != "llama-request-1" {
		t.Fatalf("response = %+v", response)
	}
}

func TestEnvModelExecutorFallsBackToOCQ(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer ocq-key" {
			t.Fatalf("Authorization = %q", got)
		}
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.Model != "ocq-model" {
			t.Fatalf("model = %s", body.Model)
		}
		w.Header().Set("X-OCQ-Request-ID", "ocq-request-1")
		w.Header().Set("X-OCQ-Session", "ocq-session-1")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ocq output"}}]}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("LLAMA_LINE_BASE_URL", "")
	t.Setenv("OCQ_BASE_URL", server.URL)
	t.Setenv("OCQ_GATEWAY_KEY", "ocq-key")

	response, err := controlplane.NewEnvModelExecutor().Generate(t.Context(), controlplane.ModelRequest{Model: "ocq-model", Messages: []controlplane.ModelMessage{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if response.Content != "ocq output" || response.CorrelationIDs["model_source"] != "ocq" || response.CorrelationIDs["ocq_request_id"] != "ocq-request-1" || response.CorrelationIDs["ocq_session_id"] != "ocq-session-1" {
		t.Fatalf("response = %+v", response)
	}
}
