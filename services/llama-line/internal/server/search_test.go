package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"git.subcult.tv/PatrickFanella/llama-line/internal/config"
)

func TestParseLimit(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"", 100}, {"0", 1}, {"-1", 1}, {"9", 9}, {"9999", 500}, {"bad", 100},
	}
	for _, tc := range tests {
		if got := parseLimit(tc.in, 100); got != tc.want {
			t.Fatalf("parseLimit(%q)=%d want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseRFC3339Ptr(t *testing.T) {
	tm := parseRFC3339Ptr("2026-05-24T00:00:00Z")
	if tm == nil || tm.UTC() != (time.Date(2026, 5, 24, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected time: %#v", tm)
	}
	if parseRFC3339Ptr("bad") != nil {
		t.Fatal("expected invalid time to return nil")
	}
}

func TestAdminSearchEndpointsNilStore(t *testing.T) {
	srv := &Server{cfg: &config.Config{AdminKey: "k"}}
	for _, tc := range []struct{ name, path string }{{"logs", "/admin/search/logs"}, {"requests", "/admin/search/requests"}} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.Header.Set("X-Admin-Key", "k")
		rr := httptest.NewRecorder()
		srv.AdminHandler().ServeHTTP(rr, req)
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status=%d want 503", tc.name, rr.Code)
		}
	}
}

func TestAdminSemanticSearchRequiresStoreAndEmbedder(t *testing.T) {
	srv := &Server{cfg: &config.Config{AdminKey: "k", Embeddings: config.EmbeddingConfig{Enabled: true}}}
	req := httptest.NewRequest(http.MethodPost, "/admin/search/semantic", bytes.NewBufferString(`{"q":"docker api"}`))
	req.Header.Set("X-Admin-Key", "k")
	rr := httptest.NewRecorder()
	srv.AdminHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", rr.Code)
	}
}

func TestAdminStatsSummaryRequiresStore(t *testing.T) {
	srv := &Server{cfg: &config.Config{AdminKey: "k"}}
	req := httptest.NewRequest(http.MethodGet, "/admin/stats/summary", nil)
	req.Header.Set("X-Admin-Key", "k")
	rr := httptest.NewRecorder()
	srv.AdminHandler().ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", rr.Code)
	}
}
