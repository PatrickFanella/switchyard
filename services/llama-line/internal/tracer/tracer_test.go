package tracer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/PatrickFanella/llama-line/internal/config"
)

func TestNewDisabled(t *testing.T) {
	tr, shutdown, err := New(context.Background(), config.PhoenixConfig{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if tr != nil {
		t.Fatalf("New() tracer = %v, want nil", tr)
	}
	if shutdown == nil {
		t.Fatal("New() shutdown nil")
	}
	shutdown()
}

func TestNewEnabledLazyExport(t *testing.T) {
	var requests atomic.Int32
	var done = make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusAccepted)
		done <- struct{}{}
	}))
	defer server.Close()

	tr, shutdown, err := New(context.Background(), config.PhoenixConfig{Enabled: true, OTLPEndpoint: server.URL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if tr == nil {
		t.Fatal("New() tracer nil")
	}

	select {
	case <-done:
		t.Fatal("unexpected export before span")
	case <-time.After(100 * time.Millisecond):
	}

	_, span := tr.Start(context.Background(), "test")
	span.End()

	select {
	case <-done:
		t.Fatal("unexpected export before shutdown")
	case <-time.After(100 * time.Millisecond):
	}
	shutdown()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for export")
	}
	if requests.Load() == 0 {
		t.Fatal("expected export request")
	}
}

func TestNewProjectNameHeader(t *testing.T) {
	var gotProject string
	var done = make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotProject = r.Header.Get("x-project-name")
		w.WriteHeader(http.StatusAccepted)
		done <- struct{}{}
	}))
	defer server.Close()

	tr, shutdown, err := New(context.Background(), config.PhoenixConfig{Enabled: true, OTLPEndpoint: server.URL, ProjectName: "llama-line"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, span := tr.Start(context.Background(), "test")
	span.End()
	shutdown()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for export")
	}
	if gotProject != "llama-line" {
		t.Fatalf("x-project-name = %q, want llama-line", gotProject)
	}
}
