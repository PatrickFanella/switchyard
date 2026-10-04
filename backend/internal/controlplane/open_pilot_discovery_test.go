package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

type discoveringPilotReader struct {
	fakeOpenPilotIssueReader
	repositories []string
}

func (f *discoveringPilotReader) ListOpenPilotRepositories(context.Context) ([]string, error) {
	return append([]string(nil), f.repositories...), nil
}

func TestOpenPilotWildcardRediscoversAndKeepsExecutionAdmission(t *testing.T) {
	store := NewStore(nil)
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(store)
	reader := &discoveringPilotReader{fakeOpenPilotIssueReader: fakeOpenPilotIssueReader{issue: GiteaIssue{ID: 10, Number: 7, State: "open", Labels: []string{"ready-for-agent", "agent:queued"}}}, repositories: []string{"future/not-prepared"}}
	assigner := &OpenPilotAssigner{store: store, runner: runner, issues: reader, repos: []string{"*"}, labels: []string{"ready-for-agent", "agent:queued"}, now: time.Now}
	if count, err := assigner.Poll(t.Context()); err != nil || count != 0 || reader.lists != 0 {
		t.Fatalf("unprepared: count=%d err=%v lists=%d", count, err, reader.lists)
	}
	reader.repositories = append(reader.repositories, "owner/repo")
	if count, err := assigner.Poll(t.Context()); err != nil || count != 1 {
		t.Fatalf("new repository: count=%d err=%v", count, err)
	}
	if len(store.ListRuns()) != 1 {
		t.Fatal("discovery must dispatch only admitted work")
	}
}

func TestGiteaRepositoryDiscoveryPaginationAndFiltering(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/v1/repos/search" || r.Header.Get("Authorization") != "token private" {
			t.Errorf("unexpected request")
		}
		entries := []map[string]any{}
		if r.URL.Query().Get("page") == "1" {
			for index := range 50 {
				entries = append(entries, map[string]any{"full_name": "owner/repo" + strconv.Itoa(index), "has_issues": true})
			}
		} else {
			entries = append(entries, map[string]any{"full_name": "owner/future", "has_issues": true}, map[string]any{"full_name": "owner/archived", "has_issues": true, "archived": true}, map[string]any{"full_name": "owner/noissues"})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": entries})
	}))
	defer server.Close()
	client := &EnvGiteaClient{baseURL: server.URL, token: "private", client: server.Client()}
	repositories, err := client.ListOpenPilotRepositories(t.Context())
	if err != nil || len(repositories) != 51 || requests != 2 {
		t.Fatalf("repositories=%d requests=%d err=%v", len(repositories), requests, err)
	}
}

func TestOpenPilotRepositoryPolicyDoesNotMutateSharedExecutor(t *testing.T) {
	env := &EnvWorkflowExecutors{openPilotBaseBranch: "main", openPilotCIRequiredChecks: []string{"switchyard"}}
	executor := openPilotExecutor{env: env}
	configured := executor.withRepositoryPolicy(OpenPilotRuntimeRepository{BaseBranch: "master", RequiredChecks: []string{"verify"}})
	if configured.env.openPilotBaseBranch != "master" || configured.env.openPilotCIRequiredChecks[0] != "verify" {
		t.Fatal("repository policy not applied")
	}
	configured.env.openPilotCIRequiredChecks[0] = "changed"
	if env.openPilotBaseBranch != "main" || env.openPilotCIRequiredChecks[0] != "switchyard" {
		t.Fatal("shared executor was mutated")
	}
}

func TestOpenPilotAllChecksRequireConcreteSuccessfulEvidence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		snapshot GiteaCheckSummary
		status   string
		done     bool
	}{
		{"empty", GiteaCheckSummary{State: "success"}, "", false},
		{"passed", GiteaCheckSummary{State: "success", Statuses: []GiteaCommitStatus{{Context: "verify", Status: "success"}}}, "passed", true},
		{"failed", GiteaCheckSummary{State: "failure", Statuses: []GiteaCommitStatus{{Context: "verify", Status: "failure"}}}, "failed", true},
		{"pending", GiteaCheckSummary{State: "pending", Statuses: []GiteaCommitStatus{{Context: "verify", Status: "pending"}}}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, done := evaluateOpenPilotCIChecks([]string{"*"}, tc.snapshot)
			if result.status != tc.status || done != tc.done {
				t.Fatalf("result=%+v done=%v", result, done)
			}
		})
	}
}
