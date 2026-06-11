package metrics

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAlerterSendsPayload(t *testing.T) {
	done := make(chan AlertPayload, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q", got)
		}
		var payload AlertPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		done <- payload
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	a := NewAlerter(server.URL, slog.Default())
	a.Send("route_test", "test message", map[string]int{"depth": 180})

	select {
	case payload := <-done:
		if payload.Event != "route_test" || payload.Message != "test message" {
			t.Fatalf("payload = %#v", payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for alert")
	}
}

func TestQueueFullThreshold(t *testing.T) {
	requests := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	a := NewAlerter(server.URL, slog.Default())

	a.AlertQueueFull(179, 200, 180)
	select {
	case <-requests:
		t.Fatal("unexpected alert below threshold")
	case <-time.After(100 * time.Millisecond):
	}

	a.AlertQueueFull(180, 200, 180)
	select {
	case <-requests:
	case <-time.After(2 * time.Second):
		t.Fatal("expected alert at threshold")
	}
}
