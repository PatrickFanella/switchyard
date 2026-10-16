package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBoundedExtractionProfileReachesNativeOpenCode(t *testing.T) {
	for _, tc := range []struct {
		name           string
		ready, failure bool
	}{{"missing-plugin", false, false}, {"ready", true, false}, {"title-failure", true, false}, {"provider-failure-no-retry", true, true}, {"truncated", true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			ready := tc.ready
			posts := 0
			posted := false
			selected := false
			agent := false
			titled := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/plugin":
					if ready {
						w.Write([]byte(`{"data":[{"id":"switchyard.extraction","state":{"status":"active"}}]}`))
					} else {
						w.Write([]byte(`{"data":[]}`))
					}
				case "/api/session":
					w.Write([]byte(`{"data":{"id":"ses_bounded"}}`))
				case "/api/session/ses_bounded":
					var b map[string]string
					json.NewDecoder(r.Body).Decode(&b)
					if r.Method != http.MethodPatch || b["title"] != "Switchyard news extraction" {
						t.Error("extraction session must have a fixed title before prompting")
					}
					if tc.name == "title-failure" {
						w.WriteHeader(503)
						return
					}
					titled = true
					w.WriteHeader(204)
				case "/api/session/ses_bounded/agent":
					var b map[string]string
					json.NewDecoder(r.Body).Decode(&b)
					agent = b["agent"] == "switchyard-news-extractor"
					w.WriteHeader(204)
				case "/api/session/ses_bounded/model":
					var b struct {
						Model map[string]string `json:"model"`
					}
					json.NewDecoder(r.Body).Decode(&b)
					selected = b.Model["providerID"] == "openai" && b.Model["id"] == "gpt-6.1-sol" && b.Model["variant"] == "low"
					w.WriteHeader(204)
				case "/api/session/ses_bounded/prompt":
					if !titled {
						t.Error("source dispatched before title was set")
					}
					posted = true
					posts++
					w.Write([]byte(`{"data":{"id":"input"}}`))
				case "/api/session/ses_bounded/message":
					if posted && tc.failure {
						w.Write([]byte(`{"data":[{"id":"idle","type":"idle","outcome":"failed"},{"id":"answer","type":"assistant","error":{"type":"provider","status":503}}]}`))
					} else if posted && tc.name == "truncated" {
						w.Write([]byte(`{"data":[{"id":"idle","type":"idle","outcome":"succeeded"},{"id":"answer","type":"assistant","finish":"length","content":[{"type":"text","text":"{}"}]}]}`))
					} else if posted {
						w.Write([]byte(`{"data":[{"id":"idle","type":"idle","outcome":"succeeded"},{"id":"answer","type":"assistant","finish":"stop","content":[{"type":"text","text":"{}"}]}]}`))
					} else {
						w.Write([]byte(`{"data":[]}`))
					}
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			t.Setenv("OPENCODE_SERVER_URL", server.URL+"/api")
			t.Setenv("OPENCODE_SECURITY_MODE", "development")
			t.Setenv("OPENCODE_EXECUTOR_BACKEND", "native")
			request := llamaLineCompatChatRequest{Model: "openai/gpt-6.1-sol", MaxTokens: 1024, ReasoningEffort: "low"}
			request.ResponseFormat = &struct {
				Type string `json:"type"`
			}{Type: "json_object"}
			t.Setenv("MODEL_BROKER_BACKEND", "native")
			t.Setenv("MODEL_BROKER_RETRY_ATTEMPTS", "3")
			response, err := NewEnvModelBroker().Generate(context.Background(), request.modelRequest(request.Model, "bounded", nil))
			if tc.name == "title-failure" {
				if err == nil || posts != 0 || selected || agent {
					t.Fatal("failed title update admitted extraction", err, posts)
				}
			} else if tc.failure || tc.name == "truncated" {
				if err == nil || posts != 1 {
					t.Fatal("provider failure retried", err, posts)
				}
			} else if ready {
				if err != nil || response.Content != "{}" || !posted || !selected || !agent {
					t.Fatal(response, err, posted, selected, agent)
				}
			} else if err == nil || posted || selected || agent {
				t.Fatal("unready extraction dispatched", err)
			}
		})
	}
}

func TestBoundedExtractionRejectsIgnoredSettings(t *testing.T) {
	for _, options := range []map[string]any{
		{"reasoning_effort": "high", "num_predict": 1024},
		{"reasoning_effort": "low", "num_predict": 4096},
		{"reasoning_effort": "low", "num_predict": 1024, "temperature": 0},
	} {
		_, err := NewEnvModelBroker().generateViaOpenCode(context.Background(), ModelRequest{Model: "openai/gpt-6.1-sol", Format: "json", Options: options})
		if err == nil {
			t.Fatal("unsupported extraction settings ignored")
		}
	}
	e := &EnvOpenCodeExecutor{baseURL: "http://127.0.0.1:1"}
	if _, err := e.SendPrompt(context.Background(), OpenCodeRequest{Extraction: true, Model: "openai/gpt-6.1-sol", Agent: "switchyard-news-extractor"}); err == nil {
		t.Fatal("legacy endpoint accepted bounded profile")
	}
}
