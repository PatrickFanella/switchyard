package proxy_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.subcult.tv/PatrickFanella/llama-line/internal/config"
	"git.subcult.tv/PatrickFanella/llama-line/internal/proxy"
	"git.subcult.tv/PatrickFanella/llama-line/internal/queue"
)

func TestForwarder_ReplacesIncomingAuthorizationWithConfiguredBearer(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(upstream.Close)

	fwd := proxy.New(upstream.URL, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	fwd.SetOutboundAuth(config.UpstreamAuthConfig{Type: "bearer", Token: "ocq-secret"})

	req, err := http.NewRequest(http.MethodPost, "http://broker.test/v1/chat/completions", strings.NewReader(`{"model":"opencode"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer llama-line-client-key")

	result := fwd.Forward(context.Background(), &queue.Request{HTTPReq: req, StatusCh: make(chan queue.StatusUpdate, 1)})
	if result.Err != nil {
		t.Fatalf("Forward() error = %v", result.Err)
	}
	defer result.Resp.Body.Close()

	if gotAuth != "Bearer ocq-secret" {
		t.Fatalf("Authorization = %q, want Bearer ocq-secret", gotAuth)
	}
}

func TestForwarder_DropsIncomingAuthorizationWhenNoOutboundAuth(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(upstream.Close)

	fwd := proxy.New(upstream.URL, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))

	req, err := http.NewRequest(http.MethodPost, "http://broker.test/v1/chat/completions", strings.NewReader(`{"model":"qwen"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer llama-line-client-key")

	result := fwd.Forward(context.Background(), &queue.Request{HTTPReq: req, StatusCh: make(chan queue.StatusUpdate, 1)})
	if result.Err != nil {
		t.Fatalf("Forward() error = %v", result.Err)
	}
	defer result.Resp.Body.Close()

	if gotAuth != "" {
		t.Fatalf("Authorization = %q, want empty", gotAuth)
	}
}

func TestForwarder_DropsIncomingAuthorizationWithEmptyBearerToken(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(upstream.Close)

	fwd := proxy.New(upstream.URL, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	fwd.SetOutboundAuth(config.UpstreamAuthConfig{Type: "bearer", Token: ""})

	req, err := http.NewRequest(http.MethodPost, "http://broker.test/v1/chat/completions", strings.NewReader(`{"model":"qwen"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("aUtHoRiZaTiOn", "Bearer llama-line-client-key")

	result := fwd.Forward(context.Background(), &queue.Request{HTTPReq: req, StatusCh: make(chan queue.StatusUpdate, 1)})
	if result.Err != nil {
		t.Fatalf("Forward() error = %v", result.Err)
	}
	defer result.Resp.Body.Close()

	if gotAuth != "" {
		t.Fatalf("Authorization = %q, want empty", gotAuth)
	}
}

func TestForwarderHealthCheck_AppliesOutboundBearerAuth(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer upstream-secret" {
			t.Fatalf("Authorization = %q, want Bearer upstream-secret", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	fwd := proxy.New(upstream.URL, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	fwd.SetOutboundAuth(config.UpstreamAuthConfig{Type: "bearer", Token: "upstream-secret"})
	result := fwd.HealthCheck(context.Background(), "/healthz")
	if !result.Healthy {
		t.Fatalf("Healthy = false, error = %q", result.Error)
	}
}
