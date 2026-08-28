package controlplane_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
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
		_, _ = w.Write([]byte(`{"id":10,"number":123,"repository":{"full_name":"OWNER/REPO"},"title":"Fix switchyard","body":"Acceptance criteria","state":"open","html_url":"https://git/OWNER/REPO/issues/123","labels":[{"name":"ready-for-agent"},{"name":"agent:queued"}]}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("GITEA_BASE_URL", server.URL)
	t.Setenv("GITEA_TOKEN", "gitea-token")

	issue, err := controlplane.NewEnvGiteaClient().FetchIssue(t.Context(), "OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("FetchIssue() error = %v", err)
	}
	if issue.Number != 123 || issue.Repository != "OWNER/REPO" || issue.Title != "Fix switchyard" || len(issue.Labels) != 2 || issue.Labels[0] != "ready-for-agent" {
		t.Fatalf("issue = %+v", issue)
	}
}

func TestEnvGiteaClientListsOpenIssuesAndExcludesPullRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/OWNER/REPO/issues" || r.URL.Query().Get("state") != "open" || r.URL.Query().Get("type") != "issues" || r.URL.Query().Get("labels") != "ready-for-agent,agent:queued" {
			t.Fatalf("request = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`[{"id":10,"number":7,"title":"Task","body":"## Test Command\nmake verify","state":"open","labels":[{"name":"ready-for-agent"},{"name":"agent:queued"}]},{"id":11,"number":8,"title":"PR","state":"open","pull_request":{},"labels":[]}]`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("GITEA_BASE_URL", server.URL)
	t.Setenv("GITEA_TOKEN", "token")
	issues, err := controlplane.NewEnvGiteaClient().ListIssues(t.Context(), "OWNER/REPO", []string{"ready-for-agent", "agent:queued"})
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Number != 7 {
		t.Fatalf("issues = %+v", issues)
	}
}

func TestEnvGiteaClientRejectsInvalidRepo(t *testing.T) {
	t.Setenv("GITEA_BASE_URL", "http://example.invalid")
	_, err := controlplane.NewEnvGiteaClient().FetchIssue(t.Context(), "OWNER/REPO/extra", "123")
	if err == nil {
		t.Fatal("FetchIssue() error = nil")
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

func TestEnvGiteaClientFindPullRequestByHead(t *testing.T) {
	tests := []struct {
		name       string
		head       string
		wantNumber int
		wantRef    string
		wantLabel  string
	}{
		{name: "branch", head: "feature-123", wantNumber: 8, wantRef: "feature-123", wantLabel: "OWNER:feature-123"},
		{name: "prefixed", head: "OWNER:feature-123", wantNumber: 8, wantRef: "feature-123", wantLabel: "OWNER:feature-123"},
		{name: "same repo bare label", head: "same-repo-branch", wantNumber: 9, wantRef: "same-repo-branch", wantLabel: "same-repo-branch"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/repos/OWNER/REPO/pulls" || r.Method != http.MethodGet {
					t.Fatalf("request = %s %s", r.Method, r.URL.String())
				}
				if got := r.Header.Get("Authorization"); got != "token gitea-token" {
					t.Fatalf("Authorization = %q", got)
				}
				if got := r.URL.Query().Get("state"); got != "open" {
					t.Fatalf("state = %q", got)
				}
				if got := r.URL.Query().Get("limit"); got != "50" {
					t.Fatalf("limit = %q", got)
				}
				if got := r.URL.Query().Get("page"); got != "1" {
					t.Fatalf("page = %q", got)
				}
				_, _ = w.Write([]byte(`[{"id":1,"number":7,"title":"Unrelated","state":"open","html_url":"https://git/OWNER/REPO/pulls/7","head":{"ref":"unrelated","label":"OWNER:unrelated","sha":"abc123"}},{"id":2,"number":8,"title":"Open Pilot: issue 123","state":"open","html_url":"https://git/OWNER/REPO/pulls/8","head":{"ref":"feature-123","label":"OWNER:feature-123","sha":"def456"}},{"id":3,"number":9,"title":"Same repo","state":"open","html_url":"https://git/OWNER/REPO/pulls/9","head":{"ref":"same-repo-branch","label":"same-repo-branch","sha":"fff999"}}]`))
			}))
			t.Cleanup(server.Close)
			t.Setenv("GITEA_BASE_URL", server.URL)
			t.Setenv("GITEA_TOKEN", "gitea-token")

			pr, found, err := controlplane.NewEnvGiteaClient().FindPullRequestByHead(t.Context(), "OWNER/REPO", tt.head)
			if err != nil {
				t.Fatalf("FindPullRequestByHead() error = %v", err)
			}
			if !found {
				t.Fatalf("found = false")
			}
			if pr.Number != tt.wantNumber || pr.Head.Ref != tt.wantRef || pr.Head.Label != tt.wantLabel {
				t.Fatalf("pr = %+v", pr)
			}
		})
	}
}

func TestEnvGiteaClientFindPullRequestByHeadNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/OWNER/REPO/pulls" || r.Method != http.MethodGet {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		if got := r.URL.Query().Get("state"); got != "open" {
			t.Fatalf("state = %q", got)
		}
		if got := r.URL.Query().Get("limit"); got != "50" {
			t.Fatalf("limit = %q", got)
		}
		_, _ = w.Write([]byte(`[{"id":1,"number":7,"title":"Wrong branch","state":"open","html_url":"https://git/OWNER/REPO/pulls/7","head":{"ref":"other-branch","label":"OWNER:other-branch","sha":"abc123","repo":{"name":"missing-branch","full_name":"OWNER/missing-branch"}}},{"id":2,"number":8,"title":"Fork branch","state":"open","html_url":"https://git/OWNER/REPO/pulls/8","head":{"ref":"missing-branch","label":"FORK:missing-branch","sha":"def456"}}]`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("GITEA_BASE_URL", server.URL)
	t.Setenv("GITEA_TOKEN", "gitea-token")

	_, found, err := controlplane.NewEnvGiteaClient().FindPullRequestByHead(t.Context(), "OWNER/REPO", "missing-branch")
	if err != nil {
		t.Fatalf("FindPullRequestByHead() error = %v", err)
	}
	if found {
		t.Fatalf("found = true")
	}
}

func TestEnvGiteaClientPullRequestChecks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/OWNER/REPO/commits/abc123/status" || r.Method != http.MethodGet {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		if got := r.Header.Get("Authorization"); got != "token gitea-token" {
			t.Fatalf("Authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"state":"success","sha":"abc123","total_count":2,"statuses":[{"context":"verify","status":"success","description":"passed"}],"check_runs":[{"name":"lint","status":"completed","conclusion":"success"}]}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("GITEA_BASE_URL", server.URL)
	t.Setenv("GITEA_TOKEN", "gitea-token")

	summary, err := controlplane.NewEnvGiteaClient().PullRequestChecks(t.Context(), "OWNER/REPO", controlplane.GiteaPullRequest{Head: controlplane.GiteaPullRequestRef{SHA: "abc123"}})
	if err != nil {
		t.Fatalf("PullRequestChecks() error = %v", err)
	}
	if summary.State != "success" || summary.SHA != "abc123" || len(summary.Statuses) != 1 || summary.Statuses[0].Status != "success" || len(summary.Checks) != 1 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestEnvGiteaClientMergePullRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "token gitea-token" {
			t.Fatalf("Authorization = %q", got)
		}
		if r.URL.Path == "/api/v1/repos/OWNER/REPO/pulls/7" && r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(expectedMergePullRequest())
			return
		}
		if r.URL.Path != "/api/v1/repos/OWNER/REPO/pulls/7/merge" || r.Method != http.MethodPost {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		want := map[string]any{"do": "merge", "head_commit_id": "abc123", "force_merge": false, "merge_when_checks_succeed": false, "delete_branch_after_merge": false}
		if !reflect.DeepEqual(body, want) {
			t.Fatalf("body = %+v", body)
		}
		_, _ = w.Write([]byte(`{"merged":true,"sha":"deadbeef","message":"Merged PR #7"}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("GITEA_BASE_URL", server.URL)
	t.Setenv("GITEA_TOKEN", "gitea-token")

	result, err := controlplane.NewEnvGiteaClient().MergePullRequest(t.Context(), "OWNER/REPO", expectedMergePullRequest())
	if err != nil {
		t.Fatalf("MergePullRequest() error = %v", err)
	}
	if !result.Merged || result.SHA != "deadbeef" {
		t.Fatalf("result = %+v", result)
	}
}

func TestEnvGiteaClientMergePullRequestEmptyBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/repos/OWNER/REPO/pulls/7" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(expectedMergePullRequest())
		case r.URL.Path == "/api/v1/repos/OWNER/REPO/pulls/7/merge" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("GITEA_BASE_URL", server.URL)
	t.Setenv("GITEA_TOKEN", "gitea-token")

	result, err := controlplane.NewEnvGiteaClient().MergePullRequest(t.Context(), "OWNER/REPO", expectedMergePullRequest())
	if err != nil {
		t.Fatalf("MergePullRequest() error = %v", err)
	}
	if !result.Merged {
		t.Fatalf("result = %+v", result)
	}
}

func TestEnvGiteaClientMergePullRequestPreservesFalseResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/repos/OWNER/REPO/pulls/7" && r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(expectedMergePullRequest())
			return
		}
		if r.URL.Path != "/api/v1/repos/OWNER/REPO/pulls/7/merge" || r.Method != http.MethodPost {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		_, _ = w.Write([]byte(`{"merged":false,"message":"not merged"}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("GITEA_BASE_URL", server.URL)

	result, err := controlplane.NewEnvGiteaClient().MergePullRequest(t.Context(), "OWNER/REPO", expectedMergePullRequest())
	if err != nil {
		t.Fatalf("MergePullRequest() error = %v", err)
	}
	if result.Merged || result.Message != "not merged" {
		t.Fatalf("result = %+v", result)
	}
}

func TestEnvGiteaClientMergePullRequestRejectsRetarget(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*controlplane.GiteaPullRequest)
	}{
		{name: "id", mutate: func(pr *controlplane.GiteaPullRequest) { pr.ID++ }},
		{name: "number", mutate: func(pr *controlplane.GiteaPullRequest) { pr.Number++ }},
		{name: "head repo", mutate: func(pr *controlplane.GiteaPullRequest) { pr.Head.Repo.FullName = "OTHER/REPO" }},
		{name: "head ref", mutate: func(pr *controlplane.GiteaPullRequest) { pr.Head.Ref = "changed" }},
		{name: "head sha", mutate: func(pr *controlplane.GiteaPullRequest) { pr.Head.SHA = "changed" }},
		{name: "base repo", mutate: func(pr *controlplane.GiteaPullRequest) { pr.Base.Repo.FullName = "OTHER/REPO" }},
		{name: "base ref", mutate: func(pr *controlplane.GiteaPullRequest) { pr.Base.Ref = "release" }},
		{name: "base sha", mutate: func(pr *controlplane.GiteaPullRequest) { pr.Base.SHA = "changed" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			posts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					posts++
				}
				current := expectedMergePullRequest()
				test.mutate(&current)
				_ = json.NewEncoder(w).Encode(current)
			}))
			t.Cleanup(server.Close)
			t.Setenv("GITEA_BASE_URL", server.URL)
			if _, err := controlplane.NewEnvGiteaClient().MergePullRequest(t.Context(), "OWNER/REPO", expectedMergePullRequest()); err == nil {
				t.Fatal("expected retarget rejection")
			}
			if posts != 0 {
				t.Fatalf("POST requests = %d", posts)
			}
		})
	}
}

func TestEnvGiteaClientMergePullRequestRequiresCompleteIdentity(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*controlplane.GiteaPullRequest)
	}{
		{name: "id", mutate: func(pr *controlplane.GiteaPullRequest) { pr.ID = 0 }},
		{name: "number", mutate: func(pr *controlplane.GiteaPullRequest) { pr.Number = 0 }},
		{name: "head repo", mutate: func(pr *controlplane.GiteaPullRequest) { pr.Head.Repo.FullName = "" }},
		{name: "head ref", mutate: func(pr *controlplane.GiteaPullRequest) { pr.Head.Ref = "" }},
		{name: "head sha", mutate: func(pr *controlplane.GiteaPullRequest) { pr.Head.SHA = "" }},
		{name: "base repo", mutate: func(pr *controlplane.GiteaPullRequest) { pr.Base.Repo.FullName = "" }},
		{name: "base ref", mutate: func(pr *controlplane.GiteaPullRequest) { pr.Base.Ref = "" }},
		{name: "base sha", mutate: func(pr *controlplane.GiteaPullRequest) { pr.Base.SHA = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
			t.Cleanup(server.Close)
			t.Setenv("GITEA_BASE_URL", server.URL)
			expected := expectedMergePullRequest()
			test.mutate(&expected)
			if _, err := controlplane.NewEnvGiteaClient().MergePullRequest(t.Context(), "OWNER/REPO", expected); err == nil {
				t.Fatal("expected incomplete identity rejection")
			}
			if requests != 0 {
				t.Fatalf("requests = %d", requests)
			}
		})
	}
}

func TestEnvGiteaClientMergePullRequestDoesNotRetryConflict(t *testing.T) {
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(expectedMergePullRequest())
			return
		}
		posts++
		http.Error(w, "conflict", http.StatusConflict)
	}))
	t.Cleanup(server.Close)
	t.Setenv("GITEA_BASE_URL", server.URL)
	if _, err := controlplane.NewEnvGiteaClient().MergePullRequest(t.Context(), "OWNER/REPO", expectedMergePullRequest()); err == nil {
		t.Fatal("expected merge conflict")
	}
	if posts != 1 {
		t.Fatalf("POST requests = %d", posts)
	}
}

func expectedMergePullRequest() controlplane.GiteaPullRequest {
	var pr controlplane.GiteaPullRequest
	if err := json.Unmarshal([]byte(`{"id":99,"number":7,"state":"open","head":{"repo":{"full_name":"OWNER/REPO"},"ref":"open-pilot/issue-123","sha":"abc123"},"base":{"repo":{"full_name":"OWNER/REPO"},"ref":"main","sha":"base123"}}`), &pr); err != nil {
		panic(err)
	}
	return pr
}

func TestEnvGiteaClientClosesIssue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/OWNER/REPO/issues/123" || r.Method != http.MethodPatch {
			t.Fatalf("request = %s %s", r.Method, r.URL.String())
		}
		if got := r.Header.Get("Authorization"); got != "token gitea-token" {
			t.Fatalf("Authorization = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["state"] != "closed" {
			t.Fatalf("body = %+v", body)
		}
		_, _ = w.Write([]byte(`{"id":10,"number":123,"title":"Fix switchyard","state":"closed","html_url":"https://git/OWNER/REPO/issues/123"}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("GITEA_BASE_URL", server.URL)
	t.Setenv("GITEA_TOKEN", "gitea-token")

	issue, err := controlplane.NewEnvGiteaClient().CloseIssue(t.Context(), "OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("CloseIssue() error = %v", err)
	}
	if issue.State != "closed" || issue.Number != 123 {
		t.Fatalf("issue = %+v", issue)
	}
}
