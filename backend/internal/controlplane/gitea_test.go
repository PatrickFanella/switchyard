package controlplane_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.subcult.tv/PatrickFanella/switchyard/internal/controlplane"
)

func TestEnvGiteaClientFetchesIssue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/OWNER/REPO/issues/123" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "token gitea-token" {
			t.Fatalf("Authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"id":10,"number":123,"title":"Fix switchyard","body":"Acceptance criteria","state":"open","html_url":"https://git/OWNER/REPO/issues/123","labels":[{"name":"ready-for-agent"},{"name":"agent:queued"}]}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("GITEA_BASE_URL", server.URL)
	t.Setenv("GITEA_TOKEN", "gitea-token")

	issue, err := controlplane.NewEnvGiteaClient().FetchIssue(t.Context(), "OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("FetchIssue() error = %v", err)
	}
	if issue.Number != 123 || issue.Title != "Fix switchyard" || len(issue.Labels) != 2 || issue.Labels[0] != "ready-for-agent" {
		t.Fatalf("issue = %+v", issue)
	}
}

func TestEnvGiteaClientCreatesPullRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/OWNER/REPO/pulls" || r.Method != http.MethodPost {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "token gitea-token" {
			t.Fatalf("Authorization = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["base"] != "main" || body["head"] != "open-pilot/issue-123" || body["title"] != "Open Pilot: issue 123" || body["draft"] != true {
			t.Fatalf("body = %+v", body)
		}
		_, _ = w.Write([]byte(`{"id":99,"number":7,"title":"Open Pilot: issue 123","state":"open","html_url":"https://git/OWNER/REPO/pulls/7"}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("GITEA_BASE_URL", server.URL)
	t.Setenv("GITEA_TOKEN", "gitea-token")

	pr, err := controlplane.NewEnvGiteaClient().CreatePullRequest(t.Context(), controlplane.GiteaPullRequestInput{Repo: "OWNER/REPO", Base: "main", Head: "open-pilot/issue-123", Title: "Open Pilot: issue 123", Body: "body", Draft: true})
	if err != nil {
		t.Fatalf("CreatePullRequest() error = %v", err)
	}
	if pr.Number != 7 || pr.HTMLURL == "" {
		t.Fatalf("pr = %+v", pr)
	}
}
