package controlplane

import (
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"
)

const openPilotProofMaximumDuration = 24 * time.Hour

type OpenPilotProofTarget struct {
	RepositoryID       string `json:"repository_id"`
	ExecutorGeneration string `json:"executor_generation"`
	RepositorySlug     string `json:"repository_slug"`
	Issue              string `json:"issue"`
}

type OpenPilotRolloutStatus struct {
	Mode                string                 `json:"mode"`
	RegistryFingerprint string                 `json:"registry_fingerprint,omitempty"`
	ProofTargets        []OpenPilotProofTarget `json:"proof_targets"`
	ExpiresAt           *time.Time             `json:"expires_at,omitempty"`
	ClosedReason        string                 `json:"closed_reason,omitempty"`
	Generation          int64                  `json:"generation"`
	UpdatedAt           time.Time              `json:"updated_at"`
}

type ConfigureOpenPilotRolloutInput struct {
	Mode                string                 `json:"mode"`
	RegistryFingerprint string                 `json:"registry_fingerprint,omitempty"`
	ProofTargets        []OpenPilotProofTarget `json:"proof_targets,omitempty"`
	ExpiresAt           *time.Time             `json:"expires_at,omitempty"`
	Reason              string                 `json:"reason,omitempty"`
}

func validateOpenPilotRolloutInput(input ConfigureOpenPilotRolloutInput, now time.Time) error {
	if input.Mode != "disabled" && input.Mode != "proof" && input.Mode != "allow" {
		return ErrInvalid
	}
	if len(input.RegistryFingerprint) != 64 {
		return ErrInvalid
	}
	if _, err := hex.DecodeString(input.RegistryFingerprint); err != nil || input.RegistryFingerprint != strings.ToLower(input.RegistryFingerprint) {
		return ErrInvalid
	}
	if input.Mode != "proof" {
		return nil
	}
	if len(input.ProofTargets) != 2 || input.ExpiresAt == nil || !input.ExpiresAt.After(now) || input.ExpiresAt.After(now.Add(openPilotProofMaximumDuration)) {
		return ErrInvalid
	}
	repositories := map[string]struct{}{}
	identities := map[string]struct{}{}
	for _, target := range input.ProofTargets {
		if !openPilotRepositoryIDPattern.MatchString(target.RepositoryID) || !validOpenPilotGeneration(target.ExecutorGeneration) || !validOpenPilotRepositorySlug(target.RepositorySlug) {
			return ErrInvalid
		}
		issue, err := ParseOpenPilotIssue(target.Issue)
		if err != nil || issue != target.Issue {
			return ErrInvalid
		}
		if _, exists := repositories[target.RepositoryID]; exists {
			return errors.New("proof targets must use two distinct repositories")
		}
		repositories[target.RepositoryID] = struct{}{}
		key := openPilotExecutionBundleKey(target.RepositoryID, target.ExecutorGeneration) + "\x00" + target.Issue
		if _, exists := identities[key]; exists {
			return ErrInvalid
		}
		identities[key] = struct{}{}
	}
	return nil
}

func cloneOpenPilotProofTargets(targets []OpenPilotProofTarget) []OpenPilotProofTarget {
	out := append([]OpenPilotProofTarget(nil), targets...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].RepositoryID != out[j].RepositoryID {
			return out[i].RepositoryID < out[j].RepositoryID
		}
		return out[i].Issue < out[j].Issue
	})
	return out
}

func openPilotRolloutAllows(status OpenPilotRolloutStatus, run Run, now time.Time) bool {
	if run.WorkflowID != string(WorkflowOpenPilot) || status.Mode == "legacy" || status.Mode == "allow" {
		return true
	}
	if status.Mode != "proof" || status.ExpiresAt == nil || !status.ExpiresAt.After(now) {
		return false
	}
	for _, target := range status.ProofTargets {
		if run.Inputs[openPilotRepositoryIDInputKey] == target.RepositoryID && run.Inputs[openPilotExecutorGenerationInputKey] == target.ExecutorGeneration && run.Inputs["issue"] == target.Issue {
			return true
		}
	}
	return false
}
