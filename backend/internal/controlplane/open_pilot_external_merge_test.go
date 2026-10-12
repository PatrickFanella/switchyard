package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func externalMergeFixture(t *testing.T, store Store, repo string) (OpenPilotIssueState, GiteaPullRequest, GiteaIssue) {
	t.Helper()
	state, run := sessionRecoveryState(t, store, repo)
	state.PullRequestID, state.PullRequestNum = 123, 7
	state.LastError, state.OperatorSummary = "review rejected", "AI review requested changes"
	state.GateStatus = OpenPilotGateStatus{"ai_review": "changes_requested", "ci_checks": "failed"}
	var err error
	state, err = store.UpsertOpenPilotIssueState(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.FinishRun(run.ID, RunFailed, "review rejected"); err != nil {
		t.Fatal(err)
	}
	pr := GiteaPullRequest{ID: 123, Number: 7, State: "closed", Merged: true, MergeCommitSHA: strings.Repeat("b", 40)}
	pr.Head.Ref, pr.Head.SHA, pr.Base.Ref = state.Branch, strings.Repeat("a", 40), state.Base
	pr.Head.Repo = &struct {
		Name     string `json:"name,omitempty"`
		FullName string `json:"full_name,omitempty"`
	}{FullName: repo}
	pr.Base.Repo = &struct {
		Name     string `json:"name,omitempty"`
		FullName string `json:"full_name,omitempty"`
	}{FullName: repo}
	return state, pr, GiteaIssue{Number: 1, Repository: repo, State: "open"}
}

func assertExternalMergeReconciliation(t *testing.T, store Store, repo string) {
	t.Helper()
	state, pr, issue := externalMergeFixture(t, store, repo)
	for _, mutation := range []func(*OpenPilotIssueState, *GiteaPullRequest, *GiteaIssue){
		func(s *OpenPilotIssueState, _ *GiteaPullRequest, _ *GiteaIssue) { s.RunID = "stale" },
		func(s *OpenPilotIssueState, _ *GiteaPullRequest, _ *GiteaIssue) {
			s.UpdatedAt = s.UpdatedAt.Add(time.Second)
		},
		func(_ *OpenPilotIssueState, p *GiteaPullRequest, _ *GiteaIssue) { p.ID++ },
		func(_ *OpenPilotIssueState, p *GiteaPullRequest, _ *GiteaIssue) { p.Number++ },
		func(_ *OpenPilotIssueState, p *GiteaPullRequest, _ *GiteaIssue) { p.Merged = false },
		func(_ *OpenPilotIssueState, p *GiteaPullRequest, _ *GiteaIssue) { p.MergeCommitSHA = "invalid" },
		func(_ *OpenPilotIssueState, p *GiteaPullRequest, _ *GiteaIssue) { p.Head.Ref = "other" },
		func(_ *OpenPilotIssueState, p *GiteaPullRequest, _ *GiteaIssue) { p.Base.Ref = "other" },
		func(_ *OpenPilotIssueState, p *GiteaPullRequest, _ *GiteaIssue) { p.Base.Repo = nil },
		func(_ *OpenPilotIssueState, p *GiteaPullRequest, _ *GiteaIssue) { p.Head.Repo = nil },
		func(_ *OpenPilotIssueState, _ *GiteaPullRequest, i *GiteaIssue) { i.Number++ },
	} {
		s, p, i := state, pr, issue
		mutation(&s, &p, &i)
		if _, err := store.ReconcileOpenPilotExternalMerge(s, p, i); !errors.Is(err, ErrConflict) {
			t.Fatalf("unsafe observation accepted: %v", err)
		}
	}
	active, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": repo, "issue": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReconcileOpenPilotExternalMerge(state, pr, issue); !errors.Is(err, ErrConflict) {
		t.Fatalf("active run accepted: %v", err)
	}
	if _, err := store.FinishRun(active.ID, RunCanceled, "fixture"); err != nil {
		t.Fatal(err)
	}
	saved, err := store.ReconcileOpenPilotExternalMerge(state, pr, issue)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Phase != OpenPilotPhaseMerged || saved.IssueClosed || saved.MergeCommitSHA != pr.MergeCommitSHA || saved.LastError != "" || saved.GateStatus["ai_review"] != "changes_requested" || saved.GateStatus["ci_checks"] != "failed" {
		t.Fatalf("invalid reconciliation: %+v", saved)
	}
	detail, err := store.RunDetail(state.RunID)
	if err != nil || detail.Run.Status != RunFailed || detail.Run.Summary != "review rejected" {
		t.Fatalf("rewrote history: %+v %v", detail.Run, err)
	}
	if !openPilotRunResolved(store, detail.Run) {
		t.Fatal("resolved failure still active")
	}
	found := false
	for _, e := range detail.Events {
		if e.Type == "open-pilot.external-merge.reconciled" && strings.Contains(e.Message, "review rejected") {
			found = true
		}
	}
	if !found {
		t.Fatal("missing original diagnostic in audit event")
	}
	replayed, err := store.ReconcileOpenPilotExternalMerge(saved, pr, issue)
	if err != nil || !replayed.UpdatedAt.Equal(saved.UpdatedAt) {
		t.Fatalf("repeated observation changed state: %+v %v", replayed, err)
	}
	replayedDetail, _ := store.RunDetail(state.RunID)
	if len(replayedDetail.Events) != len(detail.Events) {
		t.Fatal("repeated observation duplicated audit event")
	}
	if _, err := store.ReconcileOpenPilotExternalMerge(state, pr, issue); !errors.Is(err, ErrConflict) {
		t.Fatal("stale observation accepted after update")
	}
	issue.State = "closed"
	saved, err = store.ReconcileOpenPilotExternalMerge(saved, pr, issue)
	if err != nil || saved.Phase != OpenPilotPhaseIssueClosed || !saved.IssueClosed || saved.GateStatus["issue_close"] != "closed" {
		t.Fatalf("closed observation: %+v %v", saved, err)
	}
}

func TestExternalMergeReconciliation(t *testing.T) {
	assertExternalMergeReconciliation(t, NewStore(nil), "owner/external-merge")
}

func TestPostgresExternalMergeReconciliation(t *testing.T) {
	dsn := os.Getenv("SWITCHYARD_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("PostgreSQL integration")
	}
	db, err := OpenPostgres(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	repo := "owner/external-merge-" + newID("test")
	t.Cleanup(func() {
		db.ExecContext(context.Background(), `delete from open_pilot_issue_states where repo=$1`, repo)
		db.ExecContext(context.Background(), `delete from runs where inputs->>'repo'=$1`, repo)
	})
	assertExternalMergeReconciliation(t, NewPostgresStore(db, nil), repo)
}

func TestExternalMergeReconciliationHTTP(t *testing.T) {
	store := NewStore(nil)
	state, pr, issue := externalMergeFixture(t, store, "owner/external-api")
	env := newOpenPilotAIReviewTestEnv(store)
	env.giteaPullRequestGetter = &scriptedGiteaMerger{getPR: pr}
	env.issueFetcher = &stubIssueFetcher{issue: issue}
	runner := NewRunner(store, env)
	runner.SetOpenPilotAdmissionGate(openPilotAdmissionGateForTarget(t, *trustedOpenPilotInput(state.Repo, state.Issue, CreateRunInput{}).OpenPilotTarget))
	api := &API{store: store, runner: runner}
	body, _ := json.Marshal(map[string]any{"expected_run_id": state.RunID, "expected_updated_at": state.UpdatedAt})
	rec := httptest.NewRecorder()
	api.reconcileOpenPilotState(rec, httptest.NewRequest(http.MethodPost, "/api/open-pilot/state/reconcile?repo="+state.Repo+"&issue=1", strings.NewReader(string(body))))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.ListRuns()) != 1 {
		t.Fatal("reconciliation dispatched work")
	}
}
