package controlplane

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOperatorFeedbackResumeFencesAndRetainsCorrection(t *testing.T) {
	store := NewStore(nil)
	state, prior := sessionRecoveryState(t, store, "owner/operator-feedback")
	runner := NewRunner(store, MapExecutorRegistry{})
	runner.SetOpenPilotAdmissionGate(openPilotAdmissionGateForTarget(t, *trustedOpenPilotInput(state.Repo, state.Issue, CreateRunInput{}).OpenPilotTarget))
	api := &API{store: store, runner: runner, asyncDispatch: true}
	endpoint := "/api/open-pilot/state/resume?repo=" + state.Repo + "&issue=1"
	for _, body := range []string{
		`{"feedback":"correct placement"}`,
		`{"feedback":"correct placement","expected_run_id":"stale"}`,
		`{"feedback":"correct placement","expected_run_id":"` + prior.ID + `","retry_from_step":"open-pilot.merge-pr"}`,
		`{"feedback":"` + strings.Repeat("x", 8193) + `","expected_run_id":"` + prior.ID + `"}`,
	} {
		rec := httptest.NewRecorder()
		api.resumeOpenPilotState(rec, httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(body)))
		if rec.Code < 400 {
			t.Fatalf("unsafe request accepted: %d", rec.Code)
		}
		current, _ := store.OpenPilotIssueState(state.Repo, state.Issue)
		if current.RunID != prior.ID {
			t.Fatal("rejected request changed run")
		}
	}
	rec := httptest.NewRecorder()
	api.resumeOpenPilotState(rec, httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(`{"expected_run_id":"`+prior.ID+`","feedback":"Move only the new additions after the original final line."}`)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var result openPilotStateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Run.Inputs[openPilotOperatorFeedbackRunKey] != prior.ID || !strings.Contains(result.Run.Inputs[openPilotOperatorFeedbackKey], "original final line") {
		t.Fatalf("missing correction: %+v", result.Run)
	}
	if openPilotResumeSessionID(result.Run.Inputs) != "lost-session" {
		t.Fatal("feedback silently replaced session")
	}
	if result.Run.Inputs["retry_from_step"] != "open-pilot.agent-work" {
		t.Fatal("feedback changed safe cursor")
	}
	old, _ := store.RunDetail(prior.ID)
	if old.Run.Status != RunPaused {
		t.Fatal("prior evidence changed")
	}
}

func TestOperatorFeedbackStoreFenceAndPrompt(t *testing.T) {
	store := NewStore(nil)
	state, prior := sessionRecoveryState(t, store, "owner/feedback-fence")
	input := trustedOpenPilotInput(state.Repo, state.Issue, CreateRunInput{WorkflowID: string(WorkflowOpenPilot), OpenPilotFeedbackRunID: "stale", OpenPilotFeedback: "correction"})
	if _, _, err := store.ClaimAndCreateOpenPilotResumeRun(state.Repo, state.Issue, input); err == nil {
		t.Fatal("store accepted stale feedback")
	}
	input.OpenPilotFeedbackRunID = prior.ID
	inputs := map[string]string{"retry_from_step": "open-pilot.merge-pr"}
	if err := applyOpenPilotOperatorFeedback(state, prior, input, inputs); err == nil {
		t.Fatal("feedback accepted merge cursor")
	}
	inputs = map[string]string{openPilotOperatorFeedbackKey: "correct placement", "ai_review.feedback": "preserve history"}
	first := openPilotRestoredAgentPrompt("original task", inputs)
	second := openPilotRestoredAgentPrompt(first, inputs)
	if first != second || strings.Count(second, "correct placement") != 1 || !strings.Contains(second, "preserve history") {
		t.Fatalf("feedback prompt not stable: %s", second)
	}
	var forged CreateRunInput
	if err := json.Unmarshal([]byte(`{"OpenPilotFeedbackRunID":"forged","OpenPilotFeedback":"forged"}`), &forged); err != nil {
		t.Fatal(err)
	}
	if forged.OpenPilotFeedbackRunID != "" || forged.OpenPilotFeedback != "" {
		t.Fatal("generic run JSON enables operator feedback authority")
	}
}
