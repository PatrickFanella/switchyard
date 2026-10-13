package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGiteaQueueAdmissionPagesAndPreservesLabels(t *testing.T) {
	pages := 0
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token fixture-runtime" {
			t.Error("runtime credential not used")
		}
		switch r.URL.Path {
		case "/api/v1/repos/issues/search":
			pages++
			if r.URL.Query().Get("labels") != "ready-for-agent" || r.URL.Query().Get("limit") != "50" {
				t.Error("queue filter changed")
			}
			rows := []map[string]any{}
			if r.URL.Query().Get("page") == "1" {
				for i := 1; i <= 50; i++ {
					rows = append(rows, map[string]any{"id": i, "number": i, "state": "open", "repository": map[string]string{"full_name": "owner/repo"}, "labels": []map[string]string{{"name": "ready-for-agent"}}})
				}
			}
			_ = json.NewEncoder(w).Encode(rows)
		case "/api/v1/repos/owner/repo":
			_ = json.NewEncoder(w).Encode(map[string]any{"full_name": "owner/repo", "has_issues": true, "permissions": map[string]bool{"push": true}})
		case "/api/v1/repos/owner/repo/labels":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 8, "name": "agent:queued"}, {"id": 9, "name": "ready-for-agent"}})
		case "/api/v1/repos/owner/repo/issues/1/labels":
			if r.Method != http.MethodPost {
				t.Fatal("labels were replaced instead of added")
			}
			var body struct {
				Labels []int `json:"labels"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Labels) != 1 || body.Labels[0] != 8 {
				t.Error("unexpected additive label body")
			}
			posts++
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 8}, {"id": 9}})
		default:
			t.Error("unexpected route", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := &EnvGiteaClient{baseURL: server.URL, token: "fixture-runtime", client: server.Client()}
	rows, err := client.ListQueueIssues(context.Background(), "ready-for-agent", false)
	if err != nil || len(rows) != 50 || pages != 2 {
		t.Fatal("incomplete pages", len(rows), pages, err)
	}
	blockers, err := client.QueueAdmissionBlockers(context.Background(), "owner/repo")
	if err != nil || len(blockers) != 0 {
		t.Fatal("qualification", blockers, err)
	}
	if err := client.AddQueueLabel(context.Background(), "owner/repo", 1); err != nil || posts != 1 {
		t.Fatal("additive write", err)
	}
}

func TestGiteaQueueAdmissionRejectsDuplicatedPagesAndUnavailableDependencies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/repos/issues/search" {
			rows := []map[string]any{}
			for i := 1; i <= 50; i++ {
				rows = append(rows, map[string]any{"id": i, "number": i, "html_url": fmt.Sprintf("https://git.example/owner/repo/issues/%d", i)})
			}
			_ = json.NewEncoder(w).Encode(rows)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client := &EnvGiteaClient{baseURL: server.URL, client: server.Client()}
	if _, err := client.ListQueueIssues(context.Background(), "ready-for-agent", false); err == nil {
		t.Fatal("duplicate cursor pages accepted")
	}
	if _, err := client.QueueDependencies(context.Background(), "owner/repo", 1); err == nil {
		t.Fatal("unavailable dependencies treated as none")
	}
}
