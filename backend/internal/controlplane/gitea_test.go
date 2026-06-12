package controlplane_test

import (
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
