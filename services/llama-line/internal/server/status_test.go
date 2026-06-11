package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"git.subcult.tv/PatrickFanella/llama-line/internal/config"
	"git.subcult.tv/PatrickFanella/llama-line/internal/logger"
	"git.subcult.tv/PatrickFanella/llama-line/internal/proxy"
	"git.subcult.tv/PatrickFanella/llama-line/internal/queue"
	"git.subcult.tv/PatrickFanella/llama-line/internal/server"
)

type healthStatusUpstream struct {
	Name                string `json:"name"`
	Healthy             *bool  `json:"healthy,omitempty"`
	LastProbeAt         string `json:"last_probe_at,omitempty"`
	LastProbeMS         int64  `json:"last_probe_ms,omitempty"`
	LastProbeError      string `json:"last_probe_error,omitempty"`
	ConsecutiveFailures int    `json:"consecutive_failures,omitempty"`
}

type healthStatusResponse struct {
	Upstreams []healthStatusUpstream `json:"upstreams"`
}

type brokerStatusRawResponse struct {
	Upstreams []json.RawMessage `json:"upstreams"`
}

func newTestServer() *server.Server {
	cfg := &config.Config{
		ListenAddr:            ":0",
		OllamaURL:             "http://127.0.0.1:1",
		MaxQueueDepth:         10,
		HeartbeatIntervalSecs: 5,
	}
	q := queue.New(cfg.MaxQueueDepth)
	log := logger.New("error")
	fwd := proxy.New("http://127.0.0.1:1", 1, log) // unreachable — not used in status tests
	return server.New(cfg, q, fwd, log)
}

func decodeHealthStatus(t *testing.T, srv *server.Server) healthStatusResponse {
	t.Helper()
	r := httptest.NewRequest("GET", "/broker/status", nil)
	w := httptest.NewRecorder()
	srv.HandleBrokerStatus(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp healthStatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode broker status: %v", err)
	}
	return resp
}

func decodeBrokerStatusRaw(t *testing.T, srv *server.Server) brokerStatusRawResponse {
	t.Helper()
	r := httptest.NewRequest("GET", "/broker/status", nil)
	w := httptest.NewRecorder()
	srv.HandleBrokerStatus(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp brokerStatusRawResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode broker status: %v", err)
	}
	return resp
}

func rawUpstreamFields(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("decode upstream fields: %v", err)
	}
	return fields
}

func waitForHealthCondition(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

func expectProbePath(t *testing.T, paths <-chan string, want string) {
	t.Helper()
	select {
	case got := <-paths:
		if got != want {
			t.Fatalf("probe path = %q, want %q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("did not observe probe path %q", want)
	}
}

func TestHandleBrokerStatus_ReturnsJSON(t *testing.T) {
	srv := newTestServer()
	r := httptest.NewRequest("GET", "/broker/status", nil)
	w := httptest.NewRecorder()
	srv.HandleBrokerStatus(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	for _, field := range []string{"queue_depth", "max_depth", "in_flight_client", "connected_clients"} {
		if _, ok := resp[field]; !ok {
			t.Errorf("missing field %q in response", field)
		}
	}
}

func TestHandleBrokerStatus_NoAuthRequired(t *testing.T) {
	srv := newTestServer()
	// No Authorization header — should still succeed
	r := httptest.NewRequest("GET", "/broker/status", nil)
	w := httptest.NewRecorder()
	srv.HandleBrokerStatus(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (no auth required)", w.Code)
	}
}

func TestHandleBrokerStatus_BrokerHeader(t *testing.T) {
	srv := newTestServer()
	r := httptest.NewRequest("GET", "/broker/status", nil)
	w := httptest.NewRecorder()
	srv.HandleBrokerStatus(w, r)
	if w.Header().Get("X-Ollama-Broker") != "true" {
		t.Error("X-Ollama-Broker header missing")
	}
}

func TestHandleBrokerStatus_HealthUnknownBeforeProbe(t *testing.T) {
	srv := newTestServer()
	resp := decodeBrokerStatusRaw(t, srv)
	if len(resp.Upstreams) != 1 {
		t.Fatalf("upstreams = %d, want 1", len(resp.Upstreams))
	}
	fields := rawUpstreamFields(t, resp.Upstreams[0])
	for _, field := range []string{"healthy", "last_probe_at", "last_probe_ms", "last_probe_error", "consecutive_failures"} {
		if _, ok := fields[field]; ok {
			t.Fatalf("field %q present before first probe", field)
		}
	}
}

func TestStartHealthProbes_ReportsHealthyUpstream(t *testing.T) {
	paths := make(chan string, 10)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := &config.Config{
		ListenAddr:                      ":0",
		OllamaURL:                       upstream.URL,
		MaxQueueDepth:                   10,
		HeartbeatIntervalSecs:           5,
		UpstreamHealthProbeIntervalSecs: 1,
		UpstreamHealthProbeTimeoutSecs:  1,
	}
	q := queue.New(cfg.MaxQueueDepth)
	fwd := proxy.New(upstream.URL, 1, logger.New("error"))
	srv := server.New(cfg, q, fwd, logger.New("error"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.StartHealthProbes(ctx, 10*time.Millisecond, 500*time.Millisecond)

	waitForHealthCondition(t, time.Second, func() bool {
		resp := decodeHealthStatus(t, srv)
		return len(resp.Upstreams) == 1 && resp.Upstreams[0].Healthy != nil && *resp.Upstreams[0].Healthy
	})
	expectProbePath(t, paths, "/api/version")

	resp := decodeHealthStatus(t, srv)
	up := resp.Upstreams[0]
	if up.LastProbeAt == "" {
		t.Fatal("last_probe_at is empty")
	}
	raw := decodeBrokerStatusRaw(t, srv)
	fields := rawUpstreamFields(t, raw.Upstreams[0])
	for _, field := range []string{"healthy", "last_probe_at", "last_probe_ms", "last_probe_error", "consecutive_failures"} {
		if _, ok := fields[field]; !ok {
			t.Fatalf("field %q missing after successful probe", field)
		}
	}
	if got := string(fields["healthy"]); got != "true" {
		t.Fatalf("healthy JSON = %s, want true", got)
	}
	if got := string(fields["last_probe_error"]); got != "\"\"" {
		t.Fatalf("last_probe_error JSON = %s, want empty string", got)
	}
	if got := string(fields["consecutive_failures"]); got != "0" {
		t.Fatalf("consecutive_failures JSON = %s, want 0", got)
	}
}

func TestStartHealthProbes_ReportsUnhealthyUpstream(t *testing.T) {
	paths := make(chan string, 10)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer upstream.Close()

	cfg := &config.Config{
		ListenAddr: ":0",
		Upstreams: []config.UpstreamConfig{{
			Name:       "broken",
			URL:        upstream.URL,
			HealthPath: "/healthz",
		}},
		MaxQueueDepth:                   10,
		HeartbeatIntervalSecs:           5,
		UpstreamHealthProbeIntervalSecs: 1,
		UpstreamHealthProbeTimeoutSecs:  1,
	}
	q := queue.New(cfg.MaxQueueDepth)
	fwd := proxy.New(upstream.URL, 1, logger.New("error"))
	srv := server.New(cfg, q, fwd, logger.New("error"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.StartHealthProbes(ctx, 10*time.Millisecond, 500*time.Millisecond)

	waitForHealthCondition(t, time.Second, func() bool {
		resp := decodeHealthStatus(t, srv)
		return len(resp.Upstreams) == 1 && resp.Upstreams[0].Healthy != nil && !*resp.Upstreams[0].Healthy
	})
	expectProbePath(t, paths, "/healthz")

	resp := decodeHealthStatus(t, srv)
	up := resp.Upstreams[0]
	if up.LastProbeAt == "" {
		t.Fatal("last_probe_at is empty")
	}
	raw := decodeBrokerStatusRaw(t, srv)
	fields := rawUpstreamFields(t, raw.Upstreams[0])
	for _, field := range []string{"healthy", "last_probe_at", "last_probe_ms", "last_probe_error", "consecutive_failures"} {
		if _, ok := fields[field]; !ok {
			t.Fatalf("field %q missing after failed probe", field)
		}
	}
	if got := string(fields["healthy"]); got != "false" {
		t.Fatalf("healthy JSON = %s, want false", got)
	}
	if got := string(fields["last_probe_error"]); got == "\"\"" || got == "null" {
		t.Fatalf("last_probe_error JSON = %s, want non-empty error", got)
	}
	if got := string(fields["consecutive_failures"]); got == "0" || got == "null" {
		t.Fatalf("consecutive_failures JSON = %s, want > 0", got)
	}
}
