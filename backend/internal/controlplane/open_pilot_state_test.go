package controlplane

import (
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestClaimAndCreateOpenPilotResumeRunIsAtomic(t *testing.T) {
	store := NewStore(nil)
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	_, prior, err := store.ClaimAndCreateOpenPilotInitialRun("owner/repo", "1", trustedOpenPilotInput("owner/repo", "1", CreateRunInput{WorkflowID: string(WorkflowOpenPilot)}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(prior.ID, RunFailed, "CI failed"); err != nil {
		t.Fatal(err)
	}
	state, err := store.OpenPilotIssueState("owner/repo", "1")
	if err != nil {
		t.Fatal(err)
	}
	state.PullRequestURL, state.PullRequestID, state.PullRequestNum = "https://gitea.test/owner/repo/pulls/1", 1, 1
	state, err = store.UpsertOpenPilotIssueState(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []OpenPilotPhase{OpenPilotPhaseValidated, OpenPilotPhaseBranchPrepared, OpenPilotPhaseAgentCompleted, OpenPilotPhaseVerified, OpenPilotPhasePushed, OpenPilotPhasePRCreated, OpenPilotPhaseAIReviewed} {
		state, err = store.TransitionOpenPilotIssueState(state.Repo, state.Issue, phase)
		if err != nil {
			t.Fatal(err)
		}
	}
	input := CreateRunInput{WorkflowID: string(WorkflowOpenPilot)}
	input = trustedOpenPilotInput(state.Repo, state.Issue, input)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, claimErr := store.ClaimAndCreateOpenPilotResumeRun(state.Repo, state.Issue, input)
			results <- claimErr
		}()
	}
	wg.Wait()
	close(results)
	succeeded := 0
	for claimErr := range results {
		if claimErr == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful claims = %d, want 1", succeeded)
	}
}

func TestOpenPilotLifecycleCanResumeTwice(t *testing.T) {
	store := NewStore(nil)
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	target := OpenPilotTarget{RepositoryID: "legacy", RepositorySlug: "owner/repo", Issue: "1", ExecutorGeneration: "legacy"}
	state, prior, err := store.ClaimAndCreateOpenPilotInitialRun("owner/repo", "1", trustedOpenPilotInput("owner/repo", "1", CreateRunInput{WorkflowID: string(WorkflowOpenPilot)}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(prior.ID, RunFailed, "failed"); err != nil {
		t.Fatal(err)
	}
	state.Branch, state.Base, state.WorktreePath, state.LastStepName = "branch", "main", "/worktree", "open-pilot.agent-work"
	state.ResumeInputs = map[string]string{"resume.open-pilot.branch-plan.repo": "owner/repo", "resume.open-pilot.branch-plan.branch": "branch", "resume.open-pilot.branch-plan.base_branch": "main", "resume.open-pilot.branch-plan.worktree_path": "/worktree", "resume.open-pilot.branch-plan.executed": "true", "resume.open-pilot.prompt": "prompt"}
	if _, err := store.UpsertOpenPilotIssueState(state); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionOpenPilotIssueState(state.Repo, state.Issue, OpenPilotPhaseFailed); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(store, MapExecutorRegistry{})
	runner.SetOpenPilotAdmissionGate(openPilotAdmissionGateForTarget(t, target))
	resumeOnce := func() Run {
		t.Helper()
		current, err := store.OpenPilotIssueState("owner/repo", "1")
		if err != nil {
			t.Fatal(err)
		}
		inputs, err := safeOpenPilotResumeInputs(current, nil)
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := runner.prepareOpenPilotStateRunInput(current, CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: inputs})
		if err != nil {
			t.Fatal(err)
		}
		_, resumed, err := store.ClaimAndCreateOpenPilotResumeRun(current.Repo, current.Issue, prepared)
		if err != nil {
			t.Fatal(err)
		}
		if resumed.Inputs[openPilotLifecycleResumeInputKey] != "true" {
			t.Fatalf("resume marker = %q", resumed.Inputs[openPilotLifecycleResumeInputKey])
		}
		return resumed
	}
	first := resumeOnce()
	if _, err := store.FinishRun(first.ID, RunFailed, "failed again"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionOpenPilotIssueState("owner/repo", "1", OpenPilotPhaseFailed); err != nil {
		t.Fatal(err)
	}
	_ = resumeOnce()
}

func TestSafeOpenPilotEarlyResumeDerivesCursorAndRejectsCallerCursor(t *testing.T) {
	pipeline := Pipeline{Steps: []PipelineStep{{Name: "open-pilot.agent-work", Position: 44}}}
	state := OpenPilotIssueState{RunID: "terminal-run", Phase: OpenPilotPhaseFailed, LastStepName: "open-pilot.agent-work", Branch: "branch", Base: "main", WorktreePath: "/worktree", ResumeInputs: map[string]string{
		"retry_from_step": "open-pilot.merge-pr", "retry_from_step_position": "11", "resume.open-pilot.prompt": "prompt", "resume.open-pilot.branch-plan.executed": "true", "resume.open-pilot.branch-plan.branch": "branch", "resume.open-pilot.branch-plan.worktree_path": "/worktree",
	}}
	inputs, err := safeOpenPilotResumeInputs(state, &pipeline)
	if err != nil {
		t.Fatal(err)
	}
	if inputs["retry_from_step"] != "open-pilot.agent-work" || inputs["retry_from_step_position"] != "44" {
		t.Fatalf("derived cursor = %+v", inputs)
	}
	state.WorktreePath = ""
	if _, err := safeOpenPilotResumeInputs(state, &pipeline); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing evidence error = %v", err)
	}
	state.LastStepName = "open-pilot.merge-pr"
	if _, err := safeOpenPilotResumeInputs(state, &pipeline); !errors.Is(err, ErrInvalid) {
		t.Fatalf("merge rewind error = %v", err)
	}
}

func TestMemoryLifecycleResumeRejectsNullDurableAffinity(t *testing.T) {
	store := NewStore(nil)
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	target := *trustedOpenPilotInput("owner/repo", "2", CreateRunInput{}).OpenPilotTarget
	state, prior, err := store.ClaimAndCreateOpenPilotInitialRun("owner/repo", "2", CreateRunInput{WorkflowID: string(WorkflowOpenPilot), OpenPilotTarget: &target})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(prior.ID, RunFailed, "failed"); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	state.RepositoryID, state.ExecutorGeneration, state.CanonicalRepo, state.CanonicalIssue = "", "", "", ""
	state.Phase, state.LastStepName = OpenPilotPhaseFailed, "open-pilot.ai-review"
	state.PullRequestURL, state.PullRequestID, state.PullRequestNum = "https://git/pulls/2", 2, 2
	store.openPilotIssueStates[openPilotIssueStateKey(state.Repo, state.Issue)] = state
	store.mu.Unlock()
	if _, _, err := store.ClaimAndCreateOpenPilotResumeRun(state.Repo, state.Issue, CreateRunInput{WorkflowID: string(WorkflowOpenPilot), OpenPilotTarget: &target}); !errors.Is(err, ErrConflict) {
		t.Fatalf("null affinity resume error = %v", err)
	}
}

func TestClaimAndCreateOpenPilotInitialRunIsAtomic(t *testing.T) {
	store := NewStore(nil)
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	input := CreateRunInput{WorkflowID: string(WorkflowOpenPilot), TriggerID: "gitea-open-pilot-poll", Inputs: map[string]string{"issue_id": "10", "issue_verified_at": "2026-08-22T00:00:00Z"}}
	input = trustedOpenPilotInput("owner/repo", "10", input)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := store.ClaimAndCreateOpenPilotInitialRun("owner/repo", "10", input)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 || len(store.ListRuns()) != 1 {
		t.Fatalf("successful claims=%d runs=%d", succeeded, len(store.ListRuns()))
	}
}

func TestValidatePolledOpenPilotIssueRequiresClaimedIdentity(t *testing.T) {
	run := Run{TriggerID: "gitea-open-pilot-poll", Inputs: map[string]string{"issue": "7", "issue_id": "10"}}
	if err := validatePolledOpenPilotIssue(run, &GiteaIssue{ID: 10, Number: 7}); err != nil {
		t.Fatal(err)
	}
	for _, issue := range []*GiteaIssue{nil, {ID: 11, Number: 7}, {ID: 10, Number: 8}} {
		if err := validatePolledOpenPilotIssue(run, issue); err == nil {
			t.Fatalf("accepted issue %+v", issue)
		}
	}
}

func TestOpenPilotPhaseAllowsV1Transitions(t *testing.T) {
	allowed := []struct{ from, to OpenPilotPhase }{
		{OpenPilotPhaseRequested, OpenPilotPhaseValidated},
		{OpenPilotPhaseValidated, OpenPilotPhaseBranchPrepared},
		{OpenPilotPhaseBranchPrepared, OpenPilotPhaseAgentCompleted},
		{OpenPilotPhaseAgentCompleted, OpenPilotPhaseVerified},
		{OpenPilotPhaseVerified, OpenPilotPhasePushed},
		{OpenPilotPhasePushed, OpenPilotPhasePRCreated},
		{OpenPilotPhasePRCreated, OpenPilotPhaseAIReviewed},
		{OpenPilotPhaseAIReviewed, OpenPilotPhaseCIPassed},
		{OpenPilotPhaseCIPassed, OpenPilotPhaseMergeReady},
		{OpenPilotPhaseMergeReady, OpenPilotPhaseMerged},
		{OpenPilotPhaseMerged, OpenPilotPhaseIssueClosed},
		{OpenPilotPhaseRequested, OpenPilotPhaseFailed},
		{OpenPilotPhaseRequested, OpenPilotPhasePaused},
		{OpenPilotPhaseMerged, OpenPilotPhaseFailed},
		{OpenPilotPhaseMergeReady, OpenPilotPhasePaused},
	}
	for _, tt := range allowed {
		if err := ValidateOpenPilotTransition(tt.from, tt.to); err != nil {
			t.Fatalf("%s -> %s rejected: %v", tt.from, tt.to, err)
		}
	}
}

func TestOpenPilotPhaseRejectsSkippingMerge(t *testing.T) {
	err := ValidateOpenPilotTransition(OpenPilotPhasePRCreated, OpenPilotPhaseIssueClosed)
	if err == nil || !strings.Contains(err.Error(), "invalid open pilot transition") {
		t.Fatalf("expected invalid transition, got %v", err)
	}
}

func TestOpenPilotPhaseRejectsActivePhaseSkips(t *testing.T) {
	for _, tt := range []struct{ from, to OpenPilotPhase }{
		{OpenPilotPhaseRequested, OpenPilotPhaseCIPassed},
		{OpenPilotPhaseValidated, OpenPilotPhaseMerged},
		{OpenPilotPhasePRCreated, OpenPilotPhaseMerged},
		{OpenPilotPhaseAIReviewed, OpenPilotPhaseMerged},
	} {
		if err := ValidateOpenPilotTransition(tt.from, tt.to); err == nil {
			t.Fatalf("expected active phase skip %s -> %s to be rejected", tt.from, tt.to)
		}
	}
}

func TestOpenPilotPhaseAllowsResumeTransitionsToActivePhases(t *testing.T) {
	for _, tt := range []struct{ from, to OpenPilotPhase }{
		{OpenPilotPhaseFailed, OpenPilotPhaseRequested},
		{OpenPilotPhasePaused, OpenPilotPhaseAgentCompleted},
		{OpenPilotPhaseFailed, OpenPilotPhasePaused},
		{OpenPilotPhasePaused, OpenPilotPhaseFailed},
	} {
		if err := ValidateOpenPilotTransition(tt.from, tt.to); err != nil {
			t.Fatalf("expected resume transition %s -> %s, got %v", tt.from, tt.to, err)
		}
	}
}

func TestOpenPilotPhaseRejectsInvalidTerminalTransitions(t *testing.T) {
	for _, tt := range []struct{ from, to OpenPilotPhase }{
		{"", OpenPilotPhaseFailed},
		{"", OpenPilotPhasePaused},
		{OpenPilotPhaseFailed, OpenPilotPhaseIssueClosed},
		{OpenPilotPhasePaused, OpenPilotPhaseIssueClosed},
		{OpenPilotPhaseIssueClosed, OpenPilotPhaseRequested},
	} {
		if err := ValidateOpenPilotTransition(tt.from, tt.to); err == nil {
			t.Fatalf("expected rejection for %s -> %s", tt.from, tt.to)
		}
	}
}

func TestMemoryStoreOpenPilotIssueStateTransitionValidation(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	saved, err := store.TransitionOpenPilotIssueState("OWNER/REPO", "123", OpenPilotPhaseRequested)
	if err != nil {
		t.Fatalf("TransitionOpenPilotIssueState() error = %v", err)
	}
	if saved.Phase != OpenPilotPhaseRequested {
		t.Fatalf("saved = %+v", saved)
	}
	if _, err := store.TransitionOpenPilotIssueState("OWNER/REPO", "123", OpenPilotPhaseIssueClosed); err == nil {
		t.Fatal("expected invalid skipped transition to be rejected")
	}
	if _, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "456", Phase: OpenPilotPhase("mystery")}); err == nil {
		t.Fatal("expected unknown phase upsert to be rejected")
	}
}

func TestMemoryStoreOpenPilotIssueStateRejectsDirectAdvancedCreation(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	for _, phase := range []OpenPilotPhase{OpenPilotPhasePushed, OpenPilotPhasePRCreated, OpenPilotPhaseMerged, OpenPilotPhaseIssueClosed} {
		if _, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "direct-" + string(phase), Phase: phase}); err == nil {
			t.Fatalf("expected direct creation of %s to be rejected", phase)
		}
	}
}

func TestMemoryStoreOpenPilotIssueStateUpsertValidation(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	t.Run("default max repair attempts", func(t *testing.T) {
		saved, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "123", Phase: OpenPilotPhaseRequested, MaxRepairAttempts: 0})
		if err != nil {
			t.Fatalf("UpsertOpenPilotIssueState() error = %v", err)
		}
		if saved.MaxRepairAttempts != openPilotDefaultMaxRepairAttempts {
			t.Fatalf("saved.MaxRepairAttempts = %d, want %d", saved.MaxRepairAttempts, openPilotDefaultMaxRepairAttempts)
		}
	})
	t.Run("rejects repair counter overflow", func(t *testing.T) {
		if _, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "124", Phase: OpenPilotPhaseRequested, RepairAttempts: 3, MaxRepairAttempts: 2}); err == nil {
			t.Fatal("expected repair counter overflow to be rejected")
		}
	})
	t.Run("rejects issue closed jump", func(t *testing.T) {
		if _, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "125", Phase: OpenPilotPhaseRequested}); err != nil {
			t.Fatalf("seed upsert error = %v", err)
		}
		if _, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: "OWNER/REPO", Issue: "125", Phase: OpenPilotPhaseIssueClosed}); err == nil {
			t.Fatal("expected requested -> issue_closed upsert to be rejected")
		}
	})
}

func TestMemoryStoreOpenPilotIssueStateCRUDDeepCopy(t *testing.T) {
	now := time.Unix(100, 0)
	store := NewStore(func() time.Time { return now })
	input := OpenPilotIssueState{
		Repo:                  "OWNER/REPO",
		Issue:                 "123",
		Phase:                 OpenPilotPhaseRequested,
		GateStatus:            OpenPilotGateStatus{"ai_review": "pending"},
		ResumeInputs:          map[string]string{"branch": "open-pilot/issue-123"},
		RepairAttempts:        1,
		MaxRepairAttempts:     0,
		FailingGate:           "local_verify",
		OperatorSummary:       "Local verification failed",
		RecommendedNextAction: "rerun_verify",
	}
	saved, err := store.UpsertOpenPilotIssueState(input)
	if err != nil {
		t.Fatalf("UpsertOpenPilotIssueState() error = %v", err)
	}
	if saved.MaxRepairAttempts != openPilotDefaultMaxRepairAttempts {
		t.Fatalf("saved.MaxRepairAttempts = %d, want %d", saved.MaxRepairAttempts, openPilotDefaultMaxRepairAttempts)
	}
	if saved.ID == "" || saved.CreatedAt.IsZero() || saved.UpdatedAt.IsZero() {
		t.Fatalf("saved = %+v", saved)
	}
	input.GateStatus["ai_review"] = "approved"
	input.ResumeInputs["branch"] = "mutated"
	fetched, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if fetched.ID != saved.ID || fetched.GateStatus["ai_review"] != "pending" || fetched.ResumeInputs["branch"] != "open-pilot/issue-123" || fetched.FailingGate != "local_verify" || fetched.OperatorSummary != "Local verification failed" || fetched.RecommendedNextAction != "rerun_verify" {
		t.Fatalf("fetched = %+v", fetched)
	}
	fetched.GateStatus["ai_review"] = "changed"
	fetched.ResumeInputs["branch"] = "changed"
	listed := store.ListOpenPilotIssueStates()
	if len(listed) != 1 {
		t.Fatalf("len(listed) = %d", len(listed))
	}
	if listed[0].GateStatus["ai_review"] != "pending" || listed[0].ResumeInputs["branch"] != "open-pilot/issue-123" || listed[0].FailingGate != "local_verify" || listed[0].OperatorSummary != "Local verification failed" || listed[0].RecommendedNextAction != "rerun_verify" {
		t.Fatalf("listed = %+v", listed[0])
	}
	listed[0].CreatedAt = time.Unix(200, 0)
	listed[0].Phase = OpenPilotPhaseValidated
	updated, err := store.UpsertOpenPilotIssueState(listed[0])
	if err != nil {
		t.Fatalf("UpsertOpenPilotIssueState() update error = %v", err)
	}
	if updated.ID != saved.ID || updated.Phase != OpenPilotPhaseValidated || !updated.CreatedAt.Equal(saved.CreatedAt) || updated.FailingGate != "local_verify" || updated.OperatorSummary != "Local verification failed" || updated.RecommendedNextAction != "rerun_verify" {
		t.Fatalf("updated = %+v", updated)
	}
}

func TestScanOpenPilotIssueStateRowReturnsJSONErrors(t *testing.T) {
	_, err := scanOpenPilotIssueStateRow(fakeOpenPilotStateRow{})
	if err == nil {
		t.Fatal("expected JSON unmarshal error")
	}
}

type fakeOpenPilotStateRow struct{}

func (fakeOpenPilotStateRow) Scan(dest ...any) error {
	*dest[0].(*string) = "opi-1"
	*dest[1].(*string) = "OWNER/REPO"
	*dest[2].(*string) = "123"
	for index := 3; index < 9; index++ {
		*dest[index].(*sql.NullString) = sql.NullString{}
	}
	*dest[9].(*OpenPilotPhase) = OpenPilotPhaseRequested
	for index := 10; index < 16; index++ {
		*dest[index].(*sql.NullString) = sql.NullString{}
	}
	*dest[16].(*sql.NullInt64) = sql.NullInt64{}
	*dest[17].(*sql.NullInt64) = sql.NullInt64{}
	*dest[18].(*sql.NullString) = sql.NullString{}
	*dest[19].(*bool) = false
	*dest[20].(*[]byte) = []byte(`{"broken":`)
	*dest[21].(*[]byte) = []byte(`{"branch":"open-pilot/issue-123"}`)
	*dest[22].(*int) = 0
	*dest[23].(*int) = 2
	for index := 24; index < 28; index++ {
		*dest[index].(*sql.NullString) = sql.NullString{}
	}
	*dest[28].(*time.Time) = time.Unix(100, 0).UTC()
	*dest[29].(*time.Time) = time.Unix(101, 0).UTC()
	return nil
}
