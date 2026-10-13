package controlplane

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

func (a *API) repairOpenPilotCoding(w http.ResponseWriter, r *http.Request) {
	state, ok := a.openPilotStateFromQuery(w, r)
	if !ok {
		return
	}
	var input struct {
		ExpectedRunID     string    `json:"expected_run_id"`
		ExpectedUpdatedAt time.Time `json:"expected_updated_at"`
		Feedback          string    `json:"feedback"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeJSONDecodeError(w, err)
		return
	}
	if input.ExpectedRunID != state.RunID || !input.ExpectedUpdatedAt.Equal(state.UpdatedAt) {
		writeControlError(w, ErrConflict)
		return
	}
	detail, err := a.store.RunDetailContext(r.Context(), state.RunID)
	if err != nil {
		writeControlError(w, err)
		return
	}
	kind, feedback := openPilotVerificationFailure(detail)
	if kind != "coding_repair" {
		writeControlError(w, ErrConflict)
		return
	}
	if correction := strings.TrimSpace(input.Feedback); correction != "" {
		feedback += "\nOperator correction: " + RedactForLog(correction)
	}
	prepared, err := a.runner.prepareOpenPilotStateRunInput(state, CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": state.Repo, "issue": state.Issue}, PipelineSnapshot: detail.Run.PipelineSnapshot, OpenPilotCodingRepair: &OpenPilotCodingRepairRequest{ExpectedRunID: input.ExpectedRunID, ExpectedUpdatedAt: input.ExpectedUpdatedAt, Feedback: feedback}})
	if err != nil {
		writeControlError(w, err)
		return
	}
	saved, child, err := a.store.WithContext(r.Context()).ClaimAndCreateOpenPilotResumeRun(state.Repo, state.Issue, prepared)
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"state": saved, "run": child})
}
