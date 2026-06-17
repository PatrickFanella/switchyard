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
	t.Setenv("MODEL_BROKER_BACKEND", "llama-line")
	t.Setenv("LLAMA_LINE_BASE_URL", server.URL)
	t.Setenv("LLAMA_LINE_API_KEY", "llama-key")
	t.Setenv("OCQ_BASE_URL", "")

	response, err := controlplane.NewEnvModelBroker().Generate(t.Context(), controlplane.ModelRequest{Model: "model-a", Messages: []controlplane.ModelMessage{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if response.Content != "model output" || response.CorrelationIDs["model_broker"] != "switchyard" || response.CorrelationIDs["model_source"] != "llama-line" || response.CorrelationIDs["llama_line_request_id"] != "llama-request-1" {
		t.Fatalf("response = %+v", response)
	}
}

func TestEnvModelBrokerUsesOllamaBackend(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		var body struct {
			Model    string `json:"model"`
			Stream   bool   `json:"stream"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.Model != "llama3.2" || body.Stream || len(body.Messages) != 1 || body.Messages[0].Content != "hello" {
			t.Fatalf("body = %+v", body)
		}
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"ollama output"},"done":true}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("MODEL_BROKER_BACKEND", "ollama")
	t.Setenv("OLLAMA_BASE_URL", server.URL)

	response, err := controlplane.NewEnvModelBroker().Generate(t.Context(), controlplane.ModelRequest{Model: "llama3.2", Messages: []controlplane.ModelMessage{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if response.Content != "ollama output" || response.CorrelationIDs["model_source"] != "ollama" || response.CorrelationIDs["ollama_model"] != "llama3.2" {
		t.Fatalf("response = %+v", response)
	}
}

func TestEnvModelBrokerRoutesOllamaPrefixToOllama(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.Model != "qwen2.5-coder" {
			t.Fatalf("model = %q", body.Model)
		}
		_, _ = w.Write([]byte(`{"response":"ollama generate output","done":true}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("OLLAMA_BASE_URL", server.URL)

	response, err := controlplane.NewEnvModelBroker().Generate(t.Context(), controlplane.ModelRequest{Model: "ollama/qwen2.5-coder", Messages: []controlplane.ModelMessage{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if response.Content != "ollama generate output" || response.CorrelationIDs["ollama_model"] != "qwen2.5-coder" {
		t.Fatalf("response = %+v", response)
	}
}

func TestNewEnvModelExecutorUsesNativeBrokerCompatibility(t *testing.T) {
	var _ controlplane.ModelBroker = controlplane.NewEnvModelExecutor()
}

func TestEnvModelBrokerDoesNotCallOCQDirectly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("OCQ should not be called directly by the native model broker: %s", r.URL.Path)
	}))
	t.Cleanup(server.Close)
	t.Setenv("LLAMA_LINE_BASE_URL", "")
	t.Setenv("MODEL_BROKER_BACKEND", "llama-line")
	t.Setenv("OCQ_BASE_URL", server.URL)
	t.Setenv("OCQ_GATEWAY_KEY", "ocq-key")

	_, err := controlplane.NewEnvModelBroker().Generate(t.Context(), controlplane.ModelRequest{Model: "ocq-model", Messages: []controlplane.ModelMessage{{Role: "user", Content: "hello"}}})
	if err == nil || err.Error() != "LLAMA_LINE_BASE_URL is not configured" {
		t.Fatalf("Generate() error = %v", err)
	}
}
