package controlplane

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestModelAdapterUsesNativeStructuredOutput(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "success"
		if fail {
			name = "terminal-format-error"
		}
		t.Run(name, func(t *testing.T) {
			var posted atomic.Bool
			var requestBody map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/session":
					_, _ = w.Write([]byte(`{"id":"structured-session"}`))
				case r.Method == http.MethodPost && r.URL.Path == "/session/structured-session/message":
					if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
						t.Error(err)
					}
					posted.Store(true)
					_, _ = w.Write([]byte(`{"parts":[]}`))
				case r.Method == http.MethodGet && r.URL.Path == "/session/structured-session/message":
					if !posted.Load() {
						_, _ = w.Write([]byte(`[]`))
						return
					}
					if fail {
						_, _ = w.Write([]byte(`[{"info":{"id":"result","role":"assistant","finish":"stop","error":{"name":"StructuredOutputError","data":{"message":"private provider text","retries":1}}},"parts":[{"type":"text","text":"not valid output"}]}]`))
						return
					}
					// Native StructuredOutput finishes as tool-calls, without a text result.
					_, _ = w.Write([]byte(`[{"info":{"id":"result","role":"assistant","finish":"tool-calls","structured":{"content":"","tool_calls":[{"id":"call-edit","name":"edit","arguments":{"path":"PROOF.md","text":"corrected"}}]}},"parts":[{"type":"text","text":"misleading prose must not be parsed"}]}]`))
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			t.Setenv("OPENCODE_SERVER_URL", server.URL)
			t.Setenv("OPENCODE_EXECUTOR_BACKEND", "native")
			t.Setenv("MODEL_BROKER_RETRY_ATTEMPTS", "1")
			t.Setenv("OPENCODE_MESSAGE_TIMEOUT", "2s")
			result, err := NewEnvModelBroker().generateViaOpenCode(t.Context(), ModelRequest{Model: "openai/gpt-test", Messages: []ModelMessage{{Role: "user", Content: "repair file"}}, Tools: []any{map[string]any{"type": "function", "function": map[string]any{"name": "edit", "parameters": map[string]any{"type": "object"}}}}})
			if fail {
				if err == nil || strings.Contains(err.Error(), "private provider text") {
					t.Fatalf("error=%v", err)
				}
			} else {
				if err != nil || len(result.ToolCalls) != 1 || result.Content != "" {
					t.Fatalf("result=%+v error=%v", result, err)
				}
				call := result.ToolCalls[0].(map[string]any)["function"].(map[string]any)
				if call["name"] != "edit" || !strings.Contains(call["arguments"].(string), "corrected") {
					t.Fatalf("call=%v", call)
				}
			}
			format, ok := requestBody["format"].(map[string]any)
			if !ok || format["type"] != "json_schema" || format["retryCount"] != float64(1) {
				t.Fatalf("format=%v", requestBody)
			}
			schema := format["schema"].(map[string]any)
			if schema["additionalProperties"] != false {
				t.Fatal("unbounded envelope schema")
			}
			tools := requestBody["tools"].(map[string]any)
			if len(tools) != 1 || tools["StructuredOutput"] != true {
				t.Fatalf("tool permissions=%v", tools)
			}
			if requestBody["agent"] != "switchyard-model-adapter" {
				t.Fatal("adapter agent missing")
			}
		})
	}
}

func TestStructuredAdapterRejectsCompatibilityExecutor(t *testing.T) {
	executor := &EnvOpenCodeExecutor{}
	if _, err := executor.sendPromptViaOCQ(t.Context(), OpenCodeRequest{OutputSchema: modelAdapterOutputSchema()}); err == nil {
		t.Fatal("unsupported structured contract silently ignored")
	}
}
