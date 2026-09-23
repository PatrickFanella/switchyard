package controlplane

import (
	"strings"
	"unicode/utf8"
)

const openPilotOperatorFeedbackKey = "resume.open-pilot.operator-feedback"
const openPilotOperatorFeedbackRunKey = "resume.open-pilot.operator-feedback.previous_run_id"

func validateOpenPilotOperatorFeedback(state OpenPilotIssueState, expected, feedback string, inputs map[string]string) error {
	if expected == "" && feedback == "" {
		return nil
	}
	if expected == "" || strings.TrimSpace(feedback) == "" || len(feedback) > 8192 || !utf8.ValidString(feedback) {
		return ErrInvalid
	}
	if expected != state.RunID || inputs["retry_from_step"] != "open-pilot.agent-work" {
		return ErrConflict
	}
	if state.Phase != OpenPilotPhaseFailed && state.Phase != OpenPilotPhasePaused {
		return ErrConflict
	}
	return nil
}

func applyOpenPilotOperatorFeedback(state OpenPilotIssueState, prior Run, input CreateRunInput, inputs map[string]string) error {
	if err := validateOpenPilotOperatorFeedback(state, input.OpenPilotFeedbackRunID, input.OpenPilotFeedback, inputs); err != nil {
		return err
	}
	if input.OpenPilotFeedbackRunID == "" {
		return nil
	}
	if prior.ID != input.OpenPilotFeedbackRunID || (prior.Status != RunPaused && prior.Status != RunFailed && prior.Status != RunCanceled) {
		return ErrConflict
	}
	inputs[openPilotOperatorFeedbackKey] = input.OpenPilotFeedback
	inputs[openPilotOperatorFeedbackRunKey] = prior.ID
	return nil
}
