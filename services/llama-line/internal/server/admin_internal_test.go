package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"git.subcult.tv/PatrickFanella/llama-line/internal/config"
	"git.subcult.tv/PatrickFanella/llama-line/internal/proxy"
	"git.subcult.tv/PatrickFanella/llama-line/internal/queue"
)

func TestHandleAdminDropRequest_SendsTerminalStatus(t *testing.T) {
	cfg := &config.Config{
		MaxQueueDepth:         5,
		RequestTimeoutSecs:    30,
		QueueWaitTimeoutSecs:  60,
		HeartbeatIntervalSecs: 60,
		OllamaRetryMaxSecs:    1,
		AdminKey:              "admin-secret",
		Upstreams: []config.UpstreamConfig{
			{Name: "primary", URL: "http://primary.invalid"},
			{Name: "secondary", URL: "http://secondary.invalid", Models: []string{"secondary*"}},
		},
	}
	q := queue.New(cfg.MaxQueueDepth)
	fwd := proxy.New("http://primary.invalid", cfg.OllamaRetryMaxSecs, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := New(cfg, q, fwd, slog.New(slog.NewTextHandler(io.Discard, nil)))

	secondary := &srv.upstreamEntries[1]
	queued := &queue.Request{
		ID:         "queued-secondary",
		ClientName: "client",
		Priority:   0,
		EnqueuedAt: time.Now(),
		ResponseCh: make(chan queue.Result, 1),
		StatusCh:   make(chan queue.StatusUpdate, 1),
	}
	if err := secondary.queue.Enqueue(queued); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/admin/queue/queued-secondary", nil)
	rec := httptest.NewRecorder()
	srv.HandleAdminDropRequest(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["status"] != "dropped" {
		t.Fatalf("status = %q, want dropped", resp["status"])
	}

	select {
	case update := <-queued.StatusCh:
		if update.Status != "dropped_by_admin" {
			t.Fatalf("status = %q, want dropped_by_admin", update.Status)
		}
		if update.Message != "queue item dropped by admin" {
			t.Fatalf("message = %q, want queue item dropped by admin", update.Message)
		}
		if update.RequestID != queued.ID {
			t.Fatalf("request_id = %q, want %q", update.RequestID, queued.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for drop status")
	}
}
