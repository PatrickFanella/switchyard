package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func codingRepairFixture(t *testing.T, store Store, repo string) (OpenPilotIssueState, Run) {
	t.Helper()
	state, prior := sessionRecoveryState(t, store, repo)
	state.Phase = OpenPilotPhaseFailed
	state.LastStepName = "open-pilot.verify-command"
	state.ResumeInputs["test_command"] = "make verify"
	state.ResumeInputs["resume.open-pilot.agent-work.response"] = "original implementation"
	state.ResumeInputs["resume.open-pilot.verify-command.manifest"] = "old-manifest"
	state.GateStatus = OpenPilotGateStatus{"local_verify": "failed"}
	var err error
	state, err = store.UpsertOpenPilotIssueState(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(prior.ID, RunFailed, "verification failed"); err != nil {
		t.Fatal(err)
	}
	// Build the source through the supported resume path with a pinned snapshot.
	pipeline := &Pipeline{ID: "original", WorkflowID: string(WorkflowOpenPilot), Steps: []PipelineStep{{Name: "open-pilot.agent-work", Position: 3}, {Name: "open-pilot.verify-command", Position: 4}, {Name: "open-pilot.commit-changes", Position: 5}}}
	input := trustedOpenPilotInput(repo, "1", CreateRunInput{WorkflowID: string(WorkflowOpenPilot), MaxAttempts: 1, PipelineSnapshot: pipeline})
	state, prior, err = store.ClaimAndCreateOpenPilotResumeRun(repo, "1", input)
	if err != nil {
		t.Fatal("fixture resume", err)
	}
	if _, err := store.FinishRun(prior.ID, RunFailed, "verification failed"); err != nil {
		t.Fatal(err)
	}
	state.Phase = OpenPilotPhaseFailed
	state.LastStepName = "open-pilot.verify-command"
	state, err = store.UpsertOpenPilotIssueState(state)
	if err != nil {
		t.Fatal(err)
	}
	return state, prior
}

func assertCodingRepairStore(t *testing.T, store Store, repo string) {
	t.Helper()
	state, prior := codingRepairFixture(t, store, repo)
	request := &OpenPilotCodingRepairRequest{ExpectedRunID: prior.ID, ExpectedUpdatedAt: state.UpdatedAt, Feedback: "TypeScript error TS2322: fix the mismatched type"}
	input := trustedOpenPilotInput(repo, "1", CreateRunInput{WorkflowID: string(WorkflowOpenPilot), MaxAttempts: 9, PipelineSnapshot: prior.PipelineSnapshot, OpenPilotCodingRepair: request})
	input.OpenPilotCodingRepair.ExpectedUpdatedAt = request.ExpectedUpdatedAt.Add(time.Second)
	if _, _, err := store.ClaimAndCreateOpenPilotResumeRun(repo, "1", input); !errors.Is(err, ErrConflict) {
		t.Fatal("stale revision accepted", err)
	}
	input.OpenPilotCodingRepair.ExpectedUpdatedAt = state.UpdatedAt
	saved, child, err := store.ClaimAndCreateOpenPilotResumeRun(repo, "1", input)
	if err != nil {
		t.Fatal("coding repair", err)
	}
	if child.MaxAttempts != 1 || child.PipelineVersion != prior.PipelineVersion || child.Inputs["retry_from_step"] != "open-pilot.agent-work" || child.Inputs[openPilotCodingRepairCountKey] != "1" || saved.Branch != state.Branch || child.Inputs["resume.open-pilot.agent-work.response"] != "" || child.Inputs["resume.open-pilot.verify-command.manifest"] != "" {
		t.Fatalf("unsafe correction: %+v", child)
	}
	if !strings.Contains(child.Inputs[openPilotOperatorFeedbackKey], "TS2322") {
		t.Fatal("missing feedback")
	}
	detail, _ := store.RunDetail(prior.ID)
	if detail.Run.Status != RunFailed || detail.Run.MaxAttempts != prior.MaxAttempts {
		t.Fatal("source history changed")
	}
	if _, _, err := store.ClaimAndCreateOpenPilotResumeRun(repo, "1", input); err == nil {
		t.Fatal("duplicate correction accepted")
	}
	if _, err := store.FinishRun(child.ID, RunFailed, "correction failed"); err != nil {
		t.Fatal(err)
	}
	saved.Phase = OpenPilotPhaseFailed
	saved.LastStepName = "open-pilot.verify-command"
	saved.ResumeInputs["resume.open-pilot.agent-work.response"] = "correction output"
	saved.ResumeInputs[openPilotCodingRepairCountKey] = "2"
	saved, err = store.UpsertOpenPilotIssueState(saved)
	if err != nil {
		t.Fatal(err)
	}
	input.OpenPilotCodingRepair = &OpenPilotCodingRepairRequest{ExpectedRunID: child.ID, ExpectedUpdatedAt: saved.UpdatedAt, Feedback: "correction"}
	input.PipelineSnapshot = child.PipelineSnapshot
	if _, _, err := store.ClaimAndCreateOpenPilotResumeRun(repo, "1", input); !errors.Is(err, ErrConflict) {
		t.Fatal("exhausted coding budget accepted", err)
	}
	input.OpenPilotCodingRepair = nil
	input.Inputs[openPilotCodingRepairCountKey] = "0"
	_, manual, err := store.ClaimAndCreateOpenPilotResumeRun(repo, "1", input)
	if err != nil {
		t.Fatal("manual metadata protection", err)
	}
	if manual.Inputs[openPilotCodingRepairCountKey] != "2" {
		t.Fatal("manual resume reset protected repair count")
	}
	if _, err := store.FinishRun(manual.ID, RunCanceled, "fixture complete"); err != nil {
		t.Fatal(err)
	}
}

func TestCodingRepairStore(t *testing.T) {
	assertCodingRepairStore(t, NewStore(nil), "owner/coding-repair")
}

func TestVerificationFailureClassifiesInfrastructureBeforeTestMarkers(t *testing.T) {
	for _, fixture := range []struct{ output, kind string }{{"error TS2322: wrong type", "coding_repair"}, {"FAIL test\nEAI_AGAIN registry.npmjs.org", "verifier_preparation_required"}, {"command failed", "verification_failure_unclassified"}} {
		kind, feedback := openPilotVerificationFailure(RunDetail{Steps: []RunStep{{Name: "open-pilot.verify-command", Status: StepFailed, OutputSummary: fixture.output}}})
		if kind != fixture.kind || feedback == "" {
			t.Fatalf("%s => %s", fixture.output, kind)
		}
	}
}

func TestOverseerCodingHandbackUsesBoundedFeedbackAndStopsRepeat(t *testing.T) {
	o, store, _, now := overseerFixture(t, queueFixture("owner/repo", 1))
	state, prior := codingRepairFixture(t, store, "owner/repo")
	store.steps["failed-verify"] = RunStep{ID: "failed-verify", RunID: prior.ID, Name: "open-pilot.verify-command", Status: StepFailed, OutputSummary: "error TS2322: wrong type"}
	o.runner.SetOpenPilotAdmissionGate(openPilotAdmissionGateForTarget(t, *trustedOpenPilotInput(state.Repo, state.Issue, CreateRunInput{}).OpenPilotTarget))
	if err := o.Tick(context.Background(), true, false); err != nil {
		t.Fatal(err)
	}
	first, _ := o.State(context.Background())
	if len(first.History) != 0 {
		t.Fatal("first observation sent work back")
	}
	*now = now.Add(15 * time.Minute)
	if err := o.Tick(context.Background(), true, false); err != nil {
		t.Fatal(err)
	}
	receipt, _ := o.State(context.Background())
	if len(receipt.History) != 1 || receipt.History[0].Kind != "coding_repair" || receipt.History[0].ResultingRunID == "" {
		t.Fatalf("no handback receipt: %+v", receipt)
	}
	child, _ := store.RunDetail(receipt.History[0].ResultingRunID)
	if child.Run.Status != RunQueued || child.Run.MaxAttempts != 1 || child.Run.Inputs["retry_from_step"] != "open-pilot.agent-work" {
		t.Fatal("coding was not queued")
	}
	progress, err := o.codingRepairProgress(context.Background(), &receipt, receipt.History[0], *now)
	if err != nil || progress {
		t.Fatal("unverified handback counted as progress", err)
	}
}

func TestOverseerVerifierEnvironmentFailureNeverConsumesCodingBudget(t *testing.T) {
	o, store, _, now := overseerFixture(t, queueFixture("owner/repo", 1))
	state, prior := codingRepairFixture(t, store, "owner/repo")
	store.steps["failed-verify"] = RunStep{ID: "failed-verify", RunID: prior.ID, Name: "open-pilot.verify-command", Status: StepFailed, OutputSummary: "EAI_AGAIN registry.npmjs.org"}
	o.runner.SetOpenPilotAdmissionGate(openPilotAdmissionGateForTarget(t, *trustedOpenPilotInput(state.Repo, state.Issue, CreateRunInput{}).OpenPilotTarget))
	for i := 0; i < 2; i++ {
		if err := o.Tick(context.Background(), true, false); err != nil {
			t.Fatal(err)
		}
		*now = now.Add(15 * time.Minute)
	}
	receipt, _ := o.State(context.Background())
	if len(receipt.History) != 0 || receipt.Report.Incidents[0].Kind != "verifier_preparation_required" {
		t.Fatal("environment failure was returned to coding")
	}
}

func TestCodingRepairHTTPBoundaryAndFences(t *testing.T) {
	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "admin")
	t.Setenv("SWITCHYARD_CONTROL_READ_TOKEN", "reader")
	store := NewStore(nil)
	state, prior := codingRepairFixture(t, store, "owner/http-repair")
	store.steps["failed-verify"] = RunStep{ID: "failed-verify", RunID: prior.ID, Name: "open-pilot.verify-command", Status: StepFailed, OutputSummary: "error TS2322: wrong type"}
	runner := NewRunner(store)
	runner.SetOpenPilotAdmissionGate(openPilotAdmissionGateForTarget(t, *trustedOpenPilotInput(state.Repo, state.Issue, CreateRunInput{}).OpenPilotTarget))
	api := NewAPI(store, runner)
	defer api.Shutdown(context.Background())
	body, _ := json.Marshal(map[string]any{"expected_run_id": state.RunID, "expected_updated_at": state.UpdatedAt})
	path := "/api/open-pilot/state/repair?repo=" + state.Repo + "&issue=1"
	request := func(handler http.Handler, token string) int {
		r := httptest.NewRequest("POST", path, strings.NewReader(string(body)))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	if code := request(api.ControlPlaneHandler(), "reader"); code != 403 {
		t.Fatal("read credential wrote repair", code)
	}
	if code := request(api.ModelIngressHandler(), "admin"); code != 404 {
		t.Fatal("repair exposed on model listener", code)
	}
	if code := request(api.ControlPlaneHandler(), "admin"); code != 201 {
		t.Fatal("admin repair failed", code)
	}
	if code := request(api.ControlPlaneHandler(), "admin"); code != 409 {
		t.Fatal("replayed source accepted", code)
	}
}

func TestCodingRepairPreservesPauseCancelAndSnapshot(t *testing.T) {
	store := NewStore(nil)
	state, prior := codingRepairFixture(t, store, "owner/repair-fences")
	inputs := mergeOpenPilotResumeStateInputs(state)
	input := trustedOpenPilotInput(state.Repo, state.Issue, CreateRunInput{WorkflowID: string(WorkflowOpenPilot), PipelineSnapshot: prior.PipelineSnapshot, OpenPilotCodingRepair: &OpenPilotCodingRepairRequest{ExpectedRunID: prior.ID, ExpectedUpdatedAt: state.UpdatedAt, Feedback: "error TS2322: mismatch"}})
	paused := state
	paused.Phase = OpenPilotPhasePaused
	if err := applyOpenPilotCodingRepair(&paused, prior, &input, cloneMap(inputs)); !errors.Is(err, ErrConflict) {
		t.Fatal("operator pause undone", err)
	}
	canceled := prior
	canceled.Status = RunCanceled
	if err := applyOpenPilotCodingRepair(&state, canceled, &input, cloneMap(inputs)); !errors.Is(err, ErrConflict) {
		t.Fatal("cancellation undone", err)
	}
	altered := clonePipeline(*prior.PipelineSnapshot)
	altered.Steps[0].Model = "unapproved-model"
	input.PipelineSnapshot = &altered
	if err := applyOpenPilotCodingRepair(&state, prior, &input, cloneMap(inputs)); !errors.Is(err, ErrConflict) {
		t.Fatal("snapshot changed", err)
	}
	if err := validateCreateRunInput(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": state.Repo, "issue": state.Issue, openPilotCodingRepairCountKey: "0"}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("public run input forged repair count", err)
	}
}
