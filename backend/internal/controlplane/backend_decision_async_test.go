package controlplane

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAsyncDeprecatedRoutesRetainDecisionAndRequestEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, broker, executor, backend, requestKey string
		status                                      int
		wantStatus                                  string
		wantRequestID                               string
	}{
		{"llama-line success", "llama-line", "", "llama-line", "llama_line_request_id", http.StatusOK, ModelBrokerRequestCompleted, "async-llama-line success"},
		{"llama-line error", "llama-line", "", "llama-line", "llama_line_request_id", http.StatusBadGateway, ModelBrokerRequestFailed, "async-llama-line error"},
		{"nested OCQ success", "", "ocq", "ocq", "ocq_request_id", http.StatusOK, ModelBrokerRequestCompleted, "ocq-upstream"},
		{"nested OCQ error", "", "ocq", "ocq", "ocq_request_id", http.StatusBadGateway, ModelBrokerRequestFailed, "async-nested OCQ error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.backend == "llama-line" {
					w.Header().Set("X-Llama-Line-Request-ID", "llama-upstream")
				} else {
					w.Header().Set("X-OCQ-Request-ID", "ocq-upstream")
				}
				if tc.status != http.StatusOK {
					http.Error(w, "upstream failed", tc.status)
					return
				}
				_, _ = w.Write([]byte(openAIResponse))
			}))
			t.Cleanup(upstream.Close)
			t.Setenv("MODEL_BROKER_BACKEND", tc.broker)
			t.Setenv("OPENCODE_EXECUTOR_BACKEND", tc.executor)
			t.Setenv("LLAMA_LINE_BASE_URL", upstream.URL)
			t.Setenv("OCQ_BASE_URL", upstream.URL)

			store := NewStore(nil)
			id := "async-" + tc.name
			createAsyncDecisionTestRequest(t, store, id)
			claimed, err := store.ClaimNextModelBrokerRequest()
			if err != nil {
				t.Fatal(err)
			}
			service := newDirectAsyncDecisionService(store)
			service.run(claimed)

			finished, err := store.ModelBrokerRequest(id)
			if err != nil {
				t.Fatal(err)
			}
			if finished.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", finished.Status, tc.wantStatus)
			}
			if got := finished.CorrelationIDs[CorrelationBackend]; got != tc.backend {
				t.Fatalf("backend correlation = %q, want %q", got, tc.backend)
			}
			if got := finished.CorrelationIDs[CorrelationDeprecatedBackend]; got != tc.backend {
				t.Fatalf("deprecated backend correlation = %q, want %q", got, tc.backend)
			}
			if got := finished.CorrelationIDs[CorrelationCompatibilityWarning]; got != "deprecated-external-backend" {
				t.Fatalf("compatibility warning = %q", got)
			}
			if got := finished.CorrelationIDs[tc.requestKey]; got != tc.wantRequestID {
				t.Fatalf("%s = %q, want %q; correlations=%v", tc.requestKey, got, tc.wantRequestID, finished.CorrelationIDs)
			}
		})
	}
}

func TestAsyncPersistenceFailurePreventsUpstreamIOAndFailsRequest(t *testing.T) {
	called := false
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	t.Cleanup(upstream.Close)
	t.Setenv("MODEL_BROKER_BACKEND", "llama-line")
	t.Setenv("LLAMA_LINE_BASE_URL", upstream.URL)

	memory := NewStore(nil)
	createAsyncDecisionTestRequest(t, memory, "async-persistence-failure")
	claimed, err := memory.ClaimNextModelBrokerRequest()
	if err != nil {
		t.Fatal(err)
	}
	store := failingAsyncDecisionStore{Store: memory, err: errors.New("decision persistence failed")}
	service := newDirectAsyncDecisionService(store)
	service.run(claimed)

	finished, err := memory.ModelBrokerRequest(claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if called || finished.Status != ModelBrokerRequestFailed || finished.FinishedAt == nil {
		t.Fatalf("upstream_called=%v request=%+v", called, finished)
	}
}

func TestAsyncPersistedDecisionSurvivesRecoveryRetryAndTerminalMerge(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Llama-Line-Request-ID", "retry-upstream")
		_, _ = w.Write([]byte(openAIResponse))
	}))
	t.Cleanup(upstream.Close)
	t.Setenv("MODEL_BROKER_BACKEND", "llama-line")
	t.Setenv("LLAMA_LINE_BASE_URL", upstream.URL)

	store := NewStore(nil)
	const id = "async-recovery-retry"
	createAsyncDecisionTestRequest(t, store, id)
	decision := resolveModelExecutionRoute("test", false).CorrelationIDs()
	decision["evidence_before_restart"] = "retained"
	if _, err := store.MergeModelBrokerRequestCorrelationIDs(id, decision); err != nil {
		t.Fatal(err)
	}

	newDirectAsyncDecisionService(store).recover() // simulated restart recovery
	claimed, err := store.ClaimNextModelBrokerRequest()
	if err != nil {
		t.Fatal(err)
	}
	newDirectAsyncDecisionService(store).run(claimed) // recovered request retry

	finished, err := store.ModelBrokerRequest(id)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != ModelBrokerRequestCompleted || finished.CorrelationIDs["evidence_before_restart"] != "retained" || finished.CorrelationIDs[CorrelationBackend] != "llama-line" || finished.CorrelationIDs["llama_line_request_id"] != id {
		t.Fatalf("recovered request lost decision evidence: %+v", finished)
	}
}

func createAsyncDecisionTestRequest(t *testing.T, store *MemoryStore, id string) {
	t.Helper()
	_, err := store.CreateModelBrokerRequest(CreateModelBrokerRequestInput{ID: id, Model: "test", Path: "/v1/chat/completions", RequestBody: `{"model":"test","messages":[{"role":"user","content":"hi"}]}`})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MergeModelBrokerRequestCorrelationIDs(id, map[string]string{"llama_line_request_id": id}); err != nil {
		t.Fatal(err)
	}
}

func newDirectAsyncDecisionService(store Store) *ModelService {
	return NewModelService(store, NewEnvModelBroker(), ModelServiceOptions{DisableStart: true})
}

type failingAsyncDecisionStore struct {
	Store
	err error
}

func (s failingAsyncDecisionStore) MergeModelBrokerRequestCorrelationIDs(string, map[string]string) (ModelBrokerRequest, error) {
	return ModelBrokerRequest{}, s.err
}
