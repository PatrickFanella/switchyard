package controlplane

import (
	"os"
	"strconv"
	"strings"
)

func terminalStepStatusForRun(status RunStatus) (RunStepStatus, bool) {
	switch status {
	case RunFailed:
		return StepFailed, true
	case RunTimedOut:
		return StepTimedOut, true
	case RunCanceled:
		return StepCanceled, true
	default:
		return "", false
	}
}

func validateArtifactInput(input CreateArtifactInput) error {
	if input.RunID == "" || input.Name == "" || input.Kind == "" || input.URI == "" || len(input.Name) > 256 || len(input.Kind) > 64 {
		return ErrInvalid
	}
	if len(input.URI) > artifactMaxBytes() {
		return ErrInvalid
	}
	return nil
}

func artifactMaxBytes() int {
	maxBytes := 1 << 20
	if parsed, err := strconv.Atoi(strings.TrimSpace(os.Getenv("SWITCHYARD_MAX_ARTIFACT_BYTES"))); err == nil && parsed >= 1024 && parsed <= 16<<20 {
		maxBytes = parsed
	}
	return maxBytes
}

func isTerminalStepStatus(status RunStepStatus) bool {
	return status == StepSucceeded || status == StepFailed || status == StepCanceled || status == StepTimedOut || status == StepPaused
}
