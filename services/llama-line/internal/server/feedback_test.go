package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.subcult.tv/PatrickFanella/llama-line/internal/config"
	"git.subcult.tv/PatrickFanella/llama-line/internal/proxy"
	"git.subcult.tv/PatrickFanella/llama-line/internal/queue"
	"git.subcult.tv/PatrickFanella/llama-line/internal/server"
)

func newFeedbackHarness(t *testing.T, phoenixURL string, enabled bool) string {
	t.Helper()
	cfg := &config.Config{
		ListenAddr:            "127.0.0.1:0",
		OllamaURL:             phoenixURL,
		MaxQueueDepth:         5,
		RequestTimeoutSecs:    30,
		QueueWaitTimeoutSecs:  60,
		HeartbeatIntervalSecs: 60,
		OllamaRetryMaxSecs:    1,
		AdminKey:              "admin-secret",
		Keys:                  []config.APIKey{{Name: "test", Key: "secret"}},
		Phoenix:               config.PhoenixConfig{Enabled: enabled, OTLPEndpoint: phoenixURL + "/v1/traces"},
	}
	q := queue.New(cfg.MaxQueueDepth)
	fwd := proxy.New(phoenixURL, cfg.OllamaRetryMaxSecs, testLogger())
	srv := server.New(cfg, q, fwd, testLogger())
	mux := http.NewServeMux()
	mux.Handle("/admin/", srv.AdminHandler())
	httpSrv := httptest.NewServer(mux)
	t.Cleanup(httpSrv.Close)
	return httpSrv.URL
}

func TestAdminFeedback_RequiresAdminKey(t *testing.T) {
	phoenix := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer phoenix.Close()
	url := newFeedbackHarness(t, phoenix.URL, true)
	req, _ := http.NewRequest(http.MethodPost, url+"/admin/feedback", strings.NewReader(`{"span_id":"s","name":"quality"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", resp.StatusCode)
	}
}

func TestAdminFeedback_PhoenixDisabled(t *testing.T) {
	phoenix := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer phoenix.Close()
	url := newFeedbackHarness(t, phoenix.URL, false)
	req := adminRequest(t, http.MethodPost, url+"/admin/feedback", strings.NewReader(`{"span_id":"s","name":"quality"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", resp.StatusCode)
	}
}

func TestAdminFeedback_Validation(t *testing.T) {
	phoenix := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer phoenix.Close()
	url := newFeedbackHarness(t, phoenix.URL, true)
	req := adminRequest(t, http.MethodPost, url+"/admin/feedback", strings.NewReader(`{"name":"quality"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", resp.StatusCode)
	}
}

func TestAdminFeedback_HappyPath(t *testing.T) {
	var got map[string]any
	phoenix := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method=%s", r.Method)
		}
		if r.URL.Path != "/v1/span_annotations" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if r.URL.RawQuery != "sync=false" {
			t.Fatalf("query=%s", r.URL.RawQuery)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer phoenix.Close()
	url := newFeedbackHarness(t, phoenix.URL, true)
	req := adminRequest(t, http.MethodPost, url+"/admin/feedback", strings.NewReader(`{"span_id":"abc123","name":"quality","label":"positive","score":1,"explanation":"Good answer"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	if got["name"] != "quality" || got["annotator_kind"] != "HUMAN" {
		t.Fatalf("payload=%v", got)
	}
	if got["span_id"] != "abc123" || got["identifier"] != "abc123" {
		t.Fatalf("payload=%v", got)
	}
	result := got["result"].(map[string]any)
	if result["label"] != "positive" || result["explanation"] != "Good answer" {
		t.Fatalf("payload=%v", got)
	}
}
