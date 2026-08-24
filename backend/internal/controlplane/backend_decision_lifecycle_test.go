package controlplane

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const openAIResponse = `{"id":"chatcmpl-upstream","object":"chat.completion","created":1,"model":"test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`
const openAISSE = "data: {\"id\":\"chatcmpl-upstream\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n"

// TestBackendDecisionLifecycleMatrix uses protocol-valid upstream payloads so
// it tests route decisions, not decoder error handling.
func TestBackendDecisionLifecycleMatrix(t *testing.T) {
	for _, tc := range []struct {
		name, broker, executor     string
		upstreamStatus, wantStatus int
		stream, warning            bool
	}{
		{"native JSON", "", "", http.StatusOK, http.StatusOK, false, false},
		{"ollama JSON", "ollama", "", http.StatusOK, http.StatusOK, false, false},
		{"llama-line JSON success", "llama-line", "", http.StatusOK, http.StatusOK, false, true},
		{"llama-line JSON error", "llama-line", "", http.StatusBadGateway, http.StatusBadGateway, false, true},
		{"nested OCQ JSON success", "", "ocq", http.StatusOK, http.StatusOK, false, true},
		{"nested OCQ JSON error", "", "ocq", http.StatusBadGateway, http.StatusBadGateway, false, true},
		{"native SSE", "", "", http.StatusOK, http.StatusOK, true, false},
		{"ollama SSE", "ollama", "", http.StatusOK, http.StatusOK, true, false},
		{"llama-line SSE success", "llama-line", "", http.StatusOK, http.StatusOK, true, true},
		{"llama-line SSE error", "llama-line", "", http.StatusBadGateway, http.StatusOK, true, true},
		{"nested OCQ SSE success", "", "ocq", http.StatusOK, http.StatusOK, true, true},
		{"nested OCQ SSE error", "", "ocq", http.StatusBadGateway, http.StatusOK, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messagePosted := false
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.upstreamStatus != http.StatusOK {
					http.Error(w, "upstream failed", tc.upstreamStatus)
					return
				}
				if tc.executor == "ocq" {
					if r.URL.Path == "/healthz" || r.URL.Path == "/ocq/sessions" {
						_, _ = w.Write([]byte(`{"sessions":[]}`))
						return
					}
					_, _ = w.Write([]byte(openAIResponse))
					return
				}
				if tc.broker == "" && r.URL.Path == "/session" {
					_, _ = w.Write([]byte(`{"id":"s1"}`))
					return
				}
				if tc.broker == "" && r.URL.Path == "/session/s1/message" && r.Method == http.MethodGet {
					if !messagePosted {
						_, _ = w.Write([]byte(`[{"info":{"id":"before","role":"assistant","finish":"stop"}}]`))
						return
					}
					_, _ = w.Write([]byte(`[{"info":{"id":"after","role":"assistant","finish":"stop"},"parts":[{"type":"text","text":"ok"}]}]`))
					return
				}
				if tc.broker == "" && r.URL.Path == "/session/s1/message" && r.Method == http.MethodPost {
					messagePosted = true
				}
				if tc.broker == "ollama" {
					_, _ = w.Write([]byte("{\"message\":{\"role\":\"assistant\",\"content\":\"ok\"},\"done\":true}\n"))
					return
				}
				if tc.stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte(openAISSE))
					return
				}
				_, _ = w.Write([]byte(openAIResponse))
			}))
			t.Cleanup(upstream.Close)
			t.Setenv("MODEL_BROKER_BACKEND", tc.broker)
			t.Setenv("OPENCODE_EXECUTOR_BACKEND", tc.executor)
			t.Setenv("LLAMA_LINE_BASE_URL", upstream.URL)
			t.Setenv("OLLAMA_BASE_URL", upstream.URL)
			t.Setenv("OPENCODE_SERVER_URL", upstream.URL)
			t.Setenv("OCQ_BASE_URL", upstream.URL)
			store := NewStore(nil)
			api := NewAPI(store, NewRunner(store))
			payload := `{"model":"test","messages":[{"role":"user","content":"hi"}]}`
			if tc.stream {
				payload = `{"model":"test","stream":true,"messages":[{"role":"user","content":"hi"}]}`
			}
			recorder := httptest.NewRecorder()
			api.routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(payload)))
			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
			}
			if tc.warning {
				wantBackend := tc.broker
				if wantBackend == "" {
					wantBackend = "ocq"
				}
				if got := recorder.Header().Get("X-Switchyard-Backend"); got != wantBackend {
					t.Fatalf("backend header = %q, want %q", got, wantBackend)
				}
				if got := recorder.Header().Get("X-Switchyard-Compatibility-Warning"); got != "deprecated-external-backend" {
					t.Fatalf("warning header = %q", got)
				}
			} else if got := recorder.Header().Get("X-Switchyard-Backend"); got != "" {
				t.Fatalf("native route warning header = %q", got)
			}
			if tc.stream && !strings.Contains(recorder.Body.String(), "data: [DONE]\n\n") {
				t.Fatalf("SSE body missing DONE: %q", recorder.Body.String())
			}
			if tc.stream && tc.upstreamStatus != http.StatusOK && !strings.Contains(recorder.Body.String(), "event: error") {
				t.Fatalf("SSE error body missing error event: %q", recorder.Body.String())
			}
			if !tc.stream && recorder.Code == http.StatusOK && !json.Valid(recorder.Body.Bytes()) {
				t.Fatalf("response is not JSON: %q", recorder.Body.String())
			}
		})
	}
}

func TestBypassGenerationEndpointsDoNotEmitDeprecatedHeaders(t *testing.T) {
	t.Setenv("MODEL_BROKER_BACKEND", "llama-line")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"embeddings":[[0.1]],"models":[]}`))
	}))
	t.Cleanup(upstream.Close)
	t.Setenv("OLLAMA_BASE_URL", upstream.URL)
	store := NewStore(nil)
	api := NewAPI(store, NewRunner(store))
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/models", ""},
		{http.MethodGet, "/api/tags", ""},
		{http.MethodPost, "/v1/embeddings", `{"model":"test","input":"hi"}`},
		{http.MethodPost, "/api/embed", `{"model":"test","input":"hi"}`},
		{http.MethodPost, "/api/embeddings", `{"model":"test","input":"hi"}`},
		{http.MethodGet, "/broker/status", ""},
		{http.MethodGet, "/health", ""},
		{http.MethodGet, "/healthz", ""},
		{http.MethodGet, "/readyz", ""},
		{http.MethodGet, "/metrics", ""},
		{http.MethodGet, "/api/config", ""},
	} {
		t.Run(tc.path, func(t *testing.T) {
			r := httptest.NewRecorder()
			api.routes().ServeHTTP(r, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
			if r.Header().Get("X-Switchyard-Backend") != "" || r.Header().Get("X-Switchyard-Compatibility-Warning") != "" {
				t.Fatalf("bypass endpoint emitted deprecated headers")
			}
		})
	}
}

func TestBrokerStatusAndConfigExposeEffectiveSelectors(t *testing.T) {
	for _, tc := range []struct {
		name, broker, executor, wantBroker, wantExecutor, wantStatus, warning string
	}{
		{"defaults", "", "", "native", "native", "native", ""},
		{"explicit native", "native", "native", "native", "native", "native", ""},
		{"unknown selectors", "SuRpRiSe", "OtHeR", "native", "native", "native", ""},
		{"ollama", "ollama", "", "ollama", "native", "ollama", ""},
		{"llama-line", "llama-line", "", "llama-line", "native", "llama-line", "deprecated-external-backend"},
		{"nested OCQ", "", "ocq", "native", "ocq", "ocq", "deprecated-external-backend"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/config", "/api/tags", "/healthz":
					_, _ = w.Write([]byte(`[]`))
				case "/ocq/sessions":
					_, _ = w.Write([]byte(`{"sessions":[]}`))
				case "/broker/status":
					_, _ = w.Write([]byte(`{"upstreams":[]}`))
				}
			}))
			t.Cleanup(upstream.Close)
			t.Setenv("MODEL_BROKER_BACKEND", tc.broker)
			t.Setenv("OPENCODE_EXECUTOR_BACKEND", tc.executor)
			t.Setenv("OPENCODE_SERVER_URL", upstream.URL)
			t.Setenv("OLLAMA_BASE_URL", upstream.URL)
			t.Setenv("LLAMA_LINE_BASE_URL", upstream.URL)
			t.Setenv("OCQ_BASE_URL", upstream.URL)
			store := NewStore(nil)
			api := NewAPI(store, NewRunner(store))
			for _, path := range []string{"/broker/status", "/api/config"} {
				r := httptest.NewRecorder()
				api.routes().ServeHTTP(r, httptest.NewRequest(http.MethodGet, path, nil))
				if r.Code != http.StatusOK {
					t.Fatalf("%s status=%d body=%s", path, r.Code, r.Body.String())
				}
				var body map[string]any
				if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if path == "/broker/status" {
					if got := body["effective_selector"]; got != tc.wantStatus {
						t.Fatalf("status effective selector=%v want %q", got, tc.wantStatus)
					}
					got, present := body["configured_selector"].(string)
					if tc.broker == "" && present {
						t.Fatalf("status configured selector should be omitted: %v", body)
					}
					if tc.broker != "" && got != tc.broker {
						t.Fatalf("status configured selector=%q want %q", got, tc.broker)
					}
				} else {
					if body["model_broker_selector"] != tc.wantBroker || body["opencode_executor_selector"] != tc.wantExecutor {
						t.Fatalf("config selectors=%v", body)
					}
					if tc.broker == "" && body["model_broker_configured_selector"] != "" {
						t.Fatalf("blank broker selector should remain optional: %v", body)
					}
					if tc.broker != "" && body["model_broker_configured_selector"] != tc.broker {
						t.Fatalf("configured broker selector is not raw: %v", body)
					}
					if body["opencode_executor_configured_selector"] != tc.executor {
						t.Fatalf("configured executor selector is not raw: %v", body)
					}
					if got, _ := body["compatibility_warning"].(string); got != tc.warning {
						t.Fatalf("warning=%q want %q", got, tc.warning)
					}
				}
			}
		})
	}
}

func TestPreAttemptPersistenceFailurePreventsUpstreamCall(t *testing.T) {
	called := false
	broker := NewEnvModelBroker()
	broker.llamaLineBaseURL = "http://example.invalid"
	broker.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { called = true; return nil, nil })}
	t.Setenv("MODEL_BROKER_BACKEND", "llama-line")
	_, err := broker.Generate(t.Context(), ModelRequest{Model: "test", Messages: []ModelMessage{{Role: "user", Content: "hi"}}, PreAttempt: func(BackendDecision) error { return errPersistence }})
	if err == nil || called {
		t.Fatalf("callback error=%v upstream_called=%v; expected callback to prevent outbound call", err, called)
	}
}

func TestAsyncTrackerRetainsDeprecatedDecisionEvidenceOnSuccessAndError(t *testing.T) {
	for _, tc := range []struct {
		name           string
		upstreamStatus int
		wantStatus     string
	}{
		{"success", http.StatusOK, ModelBrokerRequestCompleted},
		{"error", http.StatusBadGateway, ModelBrokerRequestFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.upstreamStatus != http.StatusOK {
					http.Error(w, "failed", tc.upstreamStatus)
					return
				}
				w.Header().Set("X-Llama-Line-Request-ID", "upstream-request")
				_, _ = w.Write([]byte(openAIResponse))
			}))
			t.Cleanup(upstream.Close)
			t.Setenv("MODEL_BROKER_BACKEND", "llama-line")
			t.Setenv("LLAMA_LINE_BASE_URL", upstream.URL)
			store := NewStore(nil)
			request, err := store.CreateModelBrokerRequest(CreateModelBrokerRequestInput{ID: "async-" + tc.name, Model: "test", Path: "/v1/chat/completions", RequestBody: `{"model":"test","messages":[{"role":"user","content":"hi"}]}`})
			if err != nil {
				t.Fatal(err)
			}
			_, _ = store.MergeModelBrokerRequestCorrelationIDs(request.ID, map[string]string{"llama_line_request_id": request.ID})
			claimed, err := store.ClaimNextModelBrokerRequest()
			if err != nil {
				t.Fatal(err)
			}
			service := NewModelService(store, NewEnvModelBroker(), ModelServiceOptions{DisableStart: true})
			service.run(claimed)
			finished, err := store.ModelBrokerRequest(request.ID)
			if err != nil || finished.Status != tc.wantStatus {
				t.Fatalf("request=%+v err=%v", finished, err)
			}
			if finished.CorrelationIDs[CorrelationDeprecatedBackend] != "llama-line" || finished.CorrelationIDs[CorrelationCompatibilityWarning] != "deprecated-external-backend" || finished.CorrelationIDs["llama_line_request_id"] != request.ID {
				t.Fatalf("correlations=%v", finished.CorrelationIDs)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

var errPersistence = &backendDecisionTestError{}

type backendDecisionTestError struct{}

func (*backendDecisionTestError) Error() string { return "durable persistence failed" }
