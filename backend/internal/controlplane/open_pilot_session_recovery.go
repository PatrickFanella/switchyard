package controlplane

import (
	"context"
	"errors"
	"strings"
)

const openPilotSessionRecoveryRunKey = "resume.open-pilot.session-recovery.previous_run_id"
const openPilotSessionRecoverySessionKey = "resume.open-pilot.session-recovery.previous_session_id"

// Session replacement is an explicit operator action, fenced to a terminal run.
// Ordinary resume and executor retries retain fail-closed stale-session behavior.
func validateOpenPilotFreshSessionState(state OpenPilotIssueState, expectedRunID string) error {
	if expectedRunID == "" || state.RunID != expectedRunID || state.Phase != OpenPilotPhasePaused || state.LastStepName != "open-pilot.agent-work" {
		return ErrConflict
	}
	if !openPilotFullGitSHA(state.ResumeInputs["resume.open-pilot.commit-changes.sha"]) || openPilotResumeSessionID(state.ResumeInputs) == "" {
		return ErrInvalid
	}
	return nil
}

func openPilotResumeSessionID(inputs map[string]string) string {
	return firstNonEmpty(openCodeSessionID(inputs), inputs["resume.open-pilot.agent-work.correlation.opencode_session_id"], inputs["resume.open-pilot.agent-work.correlation.ocq_session_id"])
}

func applyOpenPilotFreshSessionResume(state OpenPilotIssueState, prior Run, expectedRunID string, inputs map[string]string) error {
	if expectedRunID == "" {
		return nil
	}
	if err := validateOpenPilotFreshSessionState(state, expectedRunID); err != nil {
		return err
	}
	if prior.ID != expectedRunID || prior.Status != RunPaused || inputs["retry_from_step"] != "open-pilot.agent-work" {
		return ErrConflict
	}
	oldSession := openPilotResumeSessionID(state.ResumeInputs)
	for _, key := range []string{"opencode_session_id", "ocq_session_id", "resume.open-pilot.agent-work.correlation.opencode_session_id", "resume.open-pilot.agent-work.correlation.ocq_session_id"} {
		delete(inputs, key)
	}
	inputs[openPilotSessionRecoveryRunKey] = prior.ID
	inputs[openPilotSessionRecoverySessionKey] = oldSession
	return nil
}

func validateOpenPilotFreshSessionWorktree(ctx context.Context, plan OpenPilotWorktreePlan, inputs map[string]string) error {
	expected := inputs["resume.open-pilot.commit-changes.sha"]
	if !openPilotFullGitSHA(expected) {
		return errors.New("fresh OpenCode session requires a recorded full commit SHA")
	}
	identity, err := validateOpenPilotWorktreeIdentity(ctx, plan.Repo, plan.RepoPath, plan.WorktreePath, plan.Branch, false)
	if err != nil {
		return err
	}
	defer identity.Close()
	head, err := identityGitOutput(ctx, identity, nil, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(head) != expected {
		return errors.New("fresh OpenCode session requires the recorded worktree HEAD")
	}
	status, err := identityGitOutput(ctx, identity, nil, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) != "" {
		return errors.New("fresh OpenCode session requires a clean committed worktree; inspect outstanding changes first")
	}
	return nil
}
