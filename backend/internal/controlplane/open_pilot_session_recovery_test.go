package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sessionRecoveryState(t *testing.T, store Store, repo string) (OpenPilotIssueState, Run) {
	t.Helper()
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	state, prior, err := store.ClaimAndCreateOpenPilotInitialRun(repo, "1", trustedOpenPilotInput(repo, "1", CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"opencode_session_id": "lost-session"}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.FinishRun(prior.ID, RunPaused, "missing session"); err != nil {
		t.Fatal(err)
	}
	state.Branch, state.Base, state.WorktreePath, state.LastStepName = "branch", "main", "/worktree", "open-pilot.agent-work"
	state.ResumeInputs = map[string]string{"resume.open-pilot.prompt": "original task", "resume.open-pilot.branch-plan.executed": "true", "resume.open-pilot.branch-plan.branch": "branch", "resume.open-pilot.branch-plan.worktree_path": "/worktree", "resume.open-pilot.commit-changes.sha": strings.Repeat("a", 40), "opencode_session_id": "lost-session", "resume.open-pilot.agent-work.correlation.opencode_session_id": "lost-session"}
	if _, err = store.UpsertOpenPilotIssueState(state); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []OpenPilotPhase{OpenPilotPhaseValidated, OpenPilotPhaseBranchPrepared, OpenPilotPhasePaused} {
		state, err = store.TransitionOpenPilotIssueState(repo, "1", phase)
		if err != nil {
			t.Fatal(err)
		}
	}
	return state, prior
}

func assertSessionRecoveryStore(t *testing.T, store Store, repo string) {
	t.Helper()
	state, prior := sessionRecoveryState(t, store, repo)
	input := trustedOpenPilotInput(repo, "1", CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: cloneMap(state.ResumeInputs), OpenPilotFreshSessionRunID: "wrong-run"})
	if _, _, err := store.ClaimAndCreateOpenPilotResumeRun(repo, "1", input); err == nil {
		t.Fatal("accepted stale operator request")
	}
	input.OpenPilotFreshSessionRunID = prior.ID
	resumedState, resumed, err := store.ClaimAndCreateOpenPilotResumeRun(repo, "1", input)
	if err != nil {
		t.Fatal(err)
	}
	for _, inputs := range []map[string]string{resumed.Inputs, resumedState.ResumeInputs} {
		if openPilotResumeSessionID(inputs) != "" || inputs[openPilotSessionRecoveryRunKey] != prior.ID || inputs[openPilotSessionRecoverySessionKey] != "lost-session" {
			t.Fatalf("recovery inputs=%v", inputs)
		}
		if inputs["resume.open-pilot.prompt"] != "original task" {
			t.Fatal("lost task")
		}
	}
	detail, err := store.RunDetail(prior.ID)
	if err != nil || detail.Run.Status != RunPaused || detail.Run.Inputs["opencode_session_id"] != "lost-session" {
		t.Fatalf("prior audit changed: %v %v", detail.Run, err)
	}
	if _, _, err := store.ClaimAndCreateOpenPilotResumeRun(repo, "1", input); err == nil {
		t.Fatal("replayed recovery request accepted")
	}
}

func TestMemoryFreshSessionResumePreservesAuditAndFencesRequest(t *testing.T) {
	assertSessionRecoveryStore(t, NewStore(nil), "owner/session-recovery")
}
func TestPostgresFreshSessionResumePreservesAuditAndFencesRequest(t *testing.T) {
	databaseURL := os.Getenv("SWITCHYARD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("PostgreSQL integration")
	}
	db, err := OpenPostgres(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	repo := "owner/session-recovery-" + newID("test")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `delete from open_pilot_issue_states where repo=$1`, repo)
		_, _ = db.ExecContext(context.Background(), `delete from runs where inputs->>'repo'=$1`, repo)
	})
	assertSessionRecoveryStore(t, NewPostgresStore(db, nil), repo)
}

func TestFreshSessionWorktreeRejectsDirtyOrMovedHead(t *testing.T) {
	plan, pr := reviewDiffFixture(t, "committed correction\n")
	inputs := map[string]string{"resume.open-pilot.commit-changes.sha": pr.Head.SHA}
	if err := validateOpenPilotFreshSessionWorktree(t.Context(), plan, inputs); err != nil {
		t.Fatal(err)
	}
	inputs["resume.open-pilot.commit-changes.sha"] = pr.Base.SHA
	if err := validateOpenPilotFreshSessionWorktree(t.Context(), plan, inputs); err == nil {
		t.Fatal("accepted moved head")
	}
	inputs["resume.open-pilot.commit-changes.sha"] = pr.Head.SHA
	if err := os.WriteFile(filepath.Join(plan.WorktreePath, "untracked.txt"), []byte("unfinished"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateOpenPilotFreshSessionWorktree(t.Context(), plan, inputs); err == nil {
		t.Fatal("accepted uncommitted work")
	}
}

func TestFreshSessionOptionRequiresExplicitLifecycleRequest(t *testing.T) {
	var input CreateRunInput
	if err := json.Unmarshal([]byte(`{"workflow_id":"open-pilot","OpenPilotFreshSessionRunID":"forged"}`), &input); err != nil {
		t.Fatal(err)
	}
	if input.OpenPilotFreshSessionRunID != "" {
		t.Fatal("generic run JSON can request recovery")
	}
	store := NewStore(nil)
	state, prior := sessionRecoveryState(t, store, "owner/session-http")
	runner := NewRunner(store, MapExecutorRegistry{})
	runner.SetOpenPilotAdmissionGate(openPilotAdmissionGateForTarget(t, *trustedOpenPilotInput(state.Repo, state.Issue, CreateRunInput{}).OpenPilotTarget))
	api := &API{store: store, runner: runner, asyncDispatch: true}
	req := httptest.NewRequest(http.MethodPost, "/api/open-pilot/state/resume?repo="+state.Repo+"&issue=1&fresh_session_for_run="+prior.ID, nil)
	rec := httptest.NewRecorder()
	api.resumeOpenPilotState(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("unsafe worktree status=%d body=%s", rec.Code, rec.Body.String())
	}
	current, _ := store.OpenPilotIssueState(state.Repo, state.Issue)
	if current.RunID != prior.ID {
		t.Fatal("failed preflight queued work")
	}
}

func TestOrdinaryResumeKeepsMissingSession(t *testing.T) {
	store := NewStore(nil)
	state, _ := sessionRecoveryState(t, store, "owner/ordinary-session")
	_, run, err := store.ClaimAndCreateOpenPilotResumeRun(state.Repo, state.Issue, trustedOpenPilotInput(state.Repo, state.Issue, CreateRunInput{WorkflowID: string(WorkflowOpenPilot)}))
	if err != nil {
		t.Fatal(err)
	}
	if openPilotResumeSessionID(run.Inputs) != "lost-session" || run.Inputs[openPilotSessionRecoveryRunKey] != "" {
		t.Fatalf("ordinary resume replaced session: %v", run.Inputs)
	}
}

func TestFreshSessionHTTPResumesCleanCommittedWorktree(t *testing.T) {
	plan, pr := reviewDiffFixture(t, "committed work\n")
	store := NewStore(nil)
	state, prior := sessionRecoveryState(t, store, plan.Repo)
	state.Branch, state.Base, state.WorktreePath = plan.Branch, plan.BaseBranch, plan.WorktreePath
	state.ResumeInputs["resume.open-pilot.branch-plan.repo"] = plan.Repo
	state.ResumeInputs["resume.open-pilot.branch-plan.repo_path"] = plan.RepoPath
	state.ResumeInputs["resume.open-pilot.branch-plan.branch"] = plan.Branch
	state.ResumeInputs["resume.open-pilot.branch-plan.base_branch"] = plan.BaseBranch
	state.ResumeInputs["resume.open-pilot.branch-plan.worktree_path"] = plan.WorktreePath
	state.ResumeInputs["resume.open-pilot.commit-changes.sha"] = pr.Head.SHA
	var err error
	state, err = store.UpsertOpenPilotIssueState(state)
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(store, MapExecutorRegistry{})
	runner.SetOpenPilotAdmissionGate(openPilotAdmissionGateForTarget(t, *trustedOpenPilotInput(state.Repo, state.Issue, CreateRunInput{}).OpenPilotTarget))
	api := &API{store: store, runner: runner, asyncDispatch: true}
	req := httptest.NewRequest(http.MethodPost, "/api/open-pilot/state/resume?repo="+state.Repo+"&issue=1&fresh_session_for_run="+prior.ID, nil)
	rec := httptest.NewRecorder()
	api.resumeOpenPilotState(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var result openPilotStateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Run == nil || openPilotResumeSessionID(result.Run.Inputs) != "" || result.Run.Inputs[openPilotSessionRecoveryRunKey] != prior.ID {
		t.Fatalf("result=%+v", result)
	}
}
