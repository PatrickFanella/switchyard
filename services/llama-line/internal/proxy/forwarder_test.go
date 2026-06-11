package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/PatrickFanella/llama-line/internal/proxy"
	"git.subcult.tv/PatrickFanella/llama-line/internal/queue"
	"log/slog"
	"os"
)

type unavailableRecorder struct {
	count int
}

func (r *unavailableRecorder) RecordOllamaUnavailable() {
	r.count++
}

type unavailableAlerter struct {
	count int
}

func (a *unavailableAlerter) AlertOllamaUnavailable() {
	a.count++
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func makeRequest(id, client, method, path string, body io.Reader) *queue.Request {
	ctx, cancel := context.WithCancel(context.Background())
	httpReq, _ := http.NewRequest(method, "http://broker"+path, body)
	httpReq.Header.Set("Authorization", "Bearer secret")
	httpReq.Header.Set("Content-Type", "application/json")
	return &queue.Request{
		ID:         id,
		ClientName: client,
		EnqueuedAt: time.Now(),
		Ctx:        ctx,
		Cancel:     cancel,
		ResponseCh: make(chan queue.Result, 1),
		StatusCh:   make(chan queue.StatusUpdate, 8),
		HTTPReq:    httpReq,
	}
}

func TestForward_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"response":"hello"}`))
	}))
	defer srv.Close()

	f := proxy.New(srv.URL, 5, testLogger())
	r := makeRequest("1", "client", "POST", "/api/generate", strings.NewReader(`{}`))

	result := f.Forward(r.Ctx, r)
	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	defer result.Resp.Body.Close()
	if result.Resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", result.Resp.StatusCode)
	}
	body, _ := io.ReadAll(result.Resp.Body)
	if string(body) != `{"response":"hello"}` {
		t.Errorf("body = %q", body)
	}
}

func TestForward_AuthHeaderStripped(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	f := proxy.New(srv.URL, 5, testLogger())
	r := makeRequest("1", "client", "POST", "/api/generate", nil)

	result := f.Forward(r.Ctx, r)
	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	result.Resp.Body.Close()
	if gotAuth != "" {
		t.Errorf("Authorization header was forwarded: %q", gotAuth)
	}
}

func TestForward_ConnectionRefused_SendsUnavailableStatus(t *testing.T) {
	f := proxy.New("http://127.0.0.1:1", 2, testLogger()) // port 1 = refused
	r := makeRequest("1", "client", "POST", "/api/generate", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r.Ctx = ctx

	done := make(chan queue.Result, 1)
	go func() {
		done <- f.Forward(ctx, r)
	}()

	// Should receive at least one ollama_unavailable update
	select {
	case update := <-r.StatusCh:
		if update.Status != "ollama_unavailable" {
			t.Errorf("status = %q, want ollama_unavailable", update.Status)
		}
	case <-time.After(3 * time.Second):
		t.Error("timed out waiting for ollama_unavailable status")
	}
	cancel()
	<-done
}

func TestForward_ContextCancelDuringRetry(t *testing.T) {
	f := proxy.New("http://127.0.0.1:1", 60, testLogger())
	r := makeRequest("1", "client", "POST", "/api/generate", nil)

	ctx, cancel := context.WithCancel(context.Background())
	r.Ctx = ctx

	done := make(chan queue.Result, 1)
	go func() {
		done <- f.Forward(ctx, r)
	}()

	// Let one retry attempt happen then cancel
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case result := <-done:
		if result.Err == nil {
			t.Error("expected error on context cancel")
		}
	case <-time.After(3 * time.Second):
		t.Error("timed out waiting for cancelled result")
	}
}

func TestForward_ClientDeadlineBeforeUpstreamHeaders_DoesNotAlertOllamaUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	recorder := &unavailableRecorder{}
	alerter := &unavailableAlerter{}
	f := proxy.New(srv.URL, 2, testLogger())
	f.SetMetrics(recorder)
	f.SetAlerter(alerter)
	r := makeRequest("1", "client", "POST", "/api/generate", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	r.Ctx = ctx

	result := f.Forward(ctx, r)
	if result.Err == nil {
		t.Fatal("expected context deadline error")
	}
	if recorder.count != 0 {
		t.Fatalf("ollama unavailable metric count = %d, want 0", recorder.count)
	}
	if alerter.count != 0 {
		t.Fatalf("ollama unavailable alert count = %d, want 0", alerter.count)
	}
	select {
	case update := <-r.StatusCh:
		t.Fatalf("unexpected status update: %+v", update)
	default:
	}
}

func TestForwarderHealthCheck_HealthyOn2xx(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/version" {
			t.Fatalf("path = %q, want /api/version", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"version":"0.0.0-test"}`))
	}))
	defer upstream.Close()

	fwd := proxy.New(upstream.URL, 1, testLogger())
	result := fwd.HealthCheck(context.Background(), "/api/version")
	if !result.Healthy {
		t.Fatalf("Healthy = false, error = %q", result.Error)
	}
	if result.Duration <= 0 {
		t.Fatalf("Duration = %s, want > 0", result.Duration)
	}
}

func TestForwarderHealthCheck_UnhealthyOn5xx(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`broken`))
	}))
	defer upstream.Close()

	fwd := proxy.New(upstream.URL, 1, testLogger())
	result := fwd.HealthCheck(context.Background(), "/api/version")
	if result.Healthy {
		t.Fatal("Healthy = true, want false")
	}
	if !strings.Contains(result.Error, "status 500") {
		t.Fatalf("Error = %q, want status 500", result.Error)
	}
}

func TestForwarderHealthCheck_ContextDeadlineExceeded(t *testing.T) {
	started := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-r.Context().Done()
	}))
	defer upstream.Close()

	fwd := proxy.New(upstream.URL, 1, testLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	done := make(chan proxy.HealthCheckResult, 1)
	go func() {
		done <- fwd.HealthCheck(ctx, "/api/version")
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for health check to start")
	}

	result := <-done
	if result.Healthy {
		t.Fatal("Healthy = true, want false")
	}
	if !strings.Contains(result.Error, "context deadline exceeded") {
		t.Fatalf("Error = %q, want context deadline exceeded", result.Error)
	}
}

func TestForwarderHealthCheck_ContextCanceled(t *testing.T) {
	started := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-r.Context().Done()
	}))
	defer upstream.Close()

	fwd := proxy.New(upstream.URL, 1, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan proxy.HealthCheckResult, 1)
	go func() {
		done <- fwd.HealthCheck(ctx, "/api/version")
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for health check to start")
	}
	cancel()

	result := <-done
	if result.Healthy {
		t.Fatal("Healthy = true, want false")
	}
	if !strings.Contains(result.Error, "context canceled") {
		t.Fatalf("Error = %q, want context canceled", result.Error)
	}
}

func TestForwarderHealthCheck_UsesDefaultPath(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/version" {
			t.Fatalf("path = %q, want /api/version", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	fwd := proxy.New(upstream.URL, 1, testLogger())
	result := fwd.HealthCheck(context.Background(), "")
	if !result.Healthy {
		t.Fatalf("Healthy = false, error = %q", result.Error)
	}
}

func TestForwarderHealthCheck_TrimsWhitespace(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Fatalf("path = %q, want /healthz", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	fwd := proxy.New(upstream.URL, 1, testLogger())
	result := fwd.HealthCheck(context.Background(), "  /healthz  ")
	if !result.Healthy {
		t.Fatalf("Healthy = false, error = %q", result.Error)
	}
}

func TestForwarderHealthCheck_PreservesQuery(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Fatalf("path = %q, want /healthz", r.URL.Path)
		}
		if r.URL.RawQuery != "ready=1" {
			t.Fatalf("query = %q, want ready=1", r.URL.RawQuery)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	fwd := proxy.New(upstream.URL, 1, testLogger())
	result := fwd.HealthCheck(context.Background(), "/healthz?ready=1")
	if !result.Healthy {
		t.Fatalf("Healthy = false, error = %q", result.Error)
	}
}

func TestForwarderHealthCheck_InvalidPathReturnsError(t *testing.T) {
	fwd := proxy.New("http://127.0.0.1:1", 1, testLogger())
	result := fwd.HealthCheck(context.Background(), " /bad%zz ")
	if result.Healthy {
		t.Fatal("Healthy = true, want false")
	}
	if !strings.Contains(result.Error, "parse health path") {
		t.Fatalf("Error = %q, want parse health path", result.Error)
	}
}
