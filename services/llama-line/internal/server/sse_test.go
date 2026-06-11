package server_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"git.subcult.tv/PatrickFanella/llama-line/internal/queue"
	"git.subcult.tv/PatrickFanella/llama-line/internal/server"
)

func TestWriteStatusEvent_Format(t *testing.T) {
	w := httptest.NewRecorder()
	update := queue.StatusUpdate{RequestID: "abc123", Position: 2, WaitSeconds: 10, Status: "queued"}
	if err := server.WriteStatusEvent(w, update); err != nil {
		t.Fatal(err)
	}
	body := w.Body.String()
	if !strings.HasPrefix(body, "data: ") {
		t.Errorf("body does not start with 'data: ': %q", body)
	}
	if !strings.HasSuffix(body, "\n\n") {
		t.Errorf("body does not end with double newline: %q", body)
	}
	if !strings.Contains(body, `"position":2`) {
		t.Errorf("body missing position: %q", body)
	}
	if !strings.Contains(body, `"request_id":"abc123"`) {
		t.Errorf("body missing request_id: %q", body)
	}
}

func TestSetSSEHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	server.SetSSEHeaders(w)
	if w.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("Content-Type = %q", w.Header().Get("Content-Type"))
	}
	if w.Header().Get("X-Ollama-Broker") != "true" {
		t.Error("X-Ollama-Broker header missing")
	}
	if w.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("Cache-Control = %q", w.Header().Get("Cache-Control"))
	}
}
