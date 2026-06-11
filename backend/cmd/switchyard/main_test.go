package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenPilotRunCLI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workflows/open-pilot/run" || r.Method != http.MethodPost {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"run":{"id":"run_1","status":"succeeded"}}`))
	}))
	t.Cleanup(server.Close)

	var out bytes.Buffer
	err := run([]string{"open-pilot", "run", "--repo", "OWNER/REPO", "--issue", "123", "--api-url", server.URL}, &out)
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if out.String() != "run_1 succeeded\n" {
		t.Fatalf("out = %q", out.String())
	}
}

func TestOpenPilotCancelCLI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/runs/run_1/cancel" || r.Method != http.MethodPost {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"run":{"id":"run_1","status":"canceled"}}`))
	}))
	t.Cleanup(server.Close)

	var out bytes.Buffer
	err := run([]string{"open-pilot", "cancel", "--run", "run_1", "--api-url", server.URL}, &out)
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if out.String() != "run_1 canceled\n" {
		t.Fatalf("out = %q", out.String())
	}
}
