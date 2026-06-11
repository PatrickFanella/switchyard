package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.subcult.tv/PatrickFanella/llama-line/internal/config"
	"git.subcult.tv/PatrickFanella/llama-line/internal/logger"
	"git.subcult.tv/PatrickFanella/llama-line/internal/proxy"
	"git.subcult.tv/PatrickFanella/llama-line/internal/queue"
	"git.subcult.tv/PatrickFanella/llama-line/internal/server"
)

func TestPrimaryQueueMaxDepthUsesUpstreamOverride(t *testing.T) {
	upstreams := []config.UpstreamConfig{{URL: "http://127.0.0.1:1", MaxQueueDepth: 3}}
	cfg := &config.Config{MaxQueueDepth: 10, OllamaRetryMaxSecs: 1, Upstreams: upstreams}

	q := queue.New(primaryQueueMaxDepth(cfg, upstreams))
	if got := q.Snapshot().MaxDepth; got != 3 {
		t.Fatalf("queue max depth = %d, want 3", got)
	}

	srv := server.New(cfg, q, proxy.New(upstreams[0].URL, cfg.OllamaRetryMaxSecs, logger.New("error")), logger.New("error"))
	req := httptest.NewRequest(http.MethodGet, "/broker/status", nil)
	resp := httptest.NewRecorder()
	srv.HandleBrokerStatus(resp, req)

	var body struct {
		MaxDepth int `json:"max_depth"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if body.MaxDepth != 3 {
		t.Fatalf("broker status max_depth = %d, want 3", body.MaxDepth)
	}
}
