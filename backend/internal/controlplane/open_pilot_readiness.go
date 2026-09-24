package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Open Pilot readiness preflight composes registration, executor generation,
// OpenCode/verifier health, rollout gate state, and effective lifecycle gates
// into one bounded control-only report. It never executes commands, clones
// repositories, or relaxes policy; every failing prerequisite carries a stable
// reason code and remediation text documented in docs/open-pilot.md.

const (
	OpenPilotReadinessPass = "pass"
	OpenPilotReadinessWarn = "warn"
	OpenPilotReadinessFail = "fail"
	OpenPilotReadinessSkip = "skip"

	maxOpenPilotReadinessCommandBytes = 4096
	openPilotReadinessProbeTimeout    = 3 * time.Second
)

// Stable readiness reason codes. Operators and the UI match on these values.
const (
	OpenPilotReasonGateUnavailable            = "gate_unavailable"
	OpenPilotReasonBundlesUnavailable         = "execution_bundles_unavailable"
	OpenPilotReasonActivationDisabled         = "activation_disabled"
	OpenPilotReasonActivationIsolatedTest     = "activation_isolated_test"
	OpenPilotReasonActivationLegacySingleton  = "activation_legacy_singleton"
	OpenPilotReasonRepositoryUnregistered     = "repository_unregistered"
	OpenPilotReasonIssueInvalid               = "issue_invalid"
	OpenPilotReasonRepositoryNotBound         = "repository_not_bound"
	OpenPilotReasonRolloutStoreUnavailable    = "rollout_store_unavailable"
	OpenPilotReasonRolloutNotConfigured       = "rollout_not_configured"
	OpenPilotReasonRolloutFingerprintStale    = "rollout_fingerprint_stale"
	OpenPilotReasonRolloutClosed              = "rollout_closed"
	OpenPilotReasonRolloutProofExpired        = "rollout_proof_expired"
	OpenPilotReasonRolloutProofExcluded       = "rollout_proof_target_excluded"
	OpenPilotReasonRolloutProofBounded        = "rollout_proof_bounded"
	OpenPilotReasonBundleMissing              = "execution_bundle_missing"
	OpenPilotReasonExecutorStatusUnavailable  = "executor_status_unavailable"
	OpenPilotReasonOpenCodeUnreachable        = "opencode_unreachable"
	OpenPilotReasonVerifierUnreachable        = "verifier_unreachable"
	OpenPilotReasonCommandDenied              = "command_denied"
	OpenPilotReasonCommandDryRun              = "command_dry_run"
	OpenPilotReasonCommandPolicyUnknown       = "command_policy_unknown"
	OpenPilotReasonCommandNotProvided         = "command_not_provided"
	OpenPilotReasonVerifierUnprepared         = "verifier_unprepared"
	OpenPilotReasonVerifierCommandUnsupported = "verifier_command_unsupported"
	OpenPilotReasonIssueAffinityStale         = "issue_affinity_stale"
	OpenPilotReasonIssueRunActive             = "issue_run_active"
	OpenPilotReasonGiteaWriteDisabled         = "gitea_write_disabled"
	OpenPilotReasonGitPushDisabled            = "git_push_disabled"
	OpenPilotReasonAutoCommitDisabled         = "auto_commit_disabled"
	OpenPilotReasonCIChecksDisabled           = "ci_checks_disabled"
	OpenPilotReasonMergeDisabled              = "merge_disabled"
	OpenPilotReasonCloseIssueDisabled         = "close_issue_disabled"

	OpenPilotReasonDocumentDecodeFailed       = "document_decode_failed"
	OpenPilotReasonDocumentContainsSecret     = "document_contains_secret"
	OpenPilotReasonDocumentInvalid            = "document_invalid"
	OpenPilotReasonDocumentKindAmbiguous      = "document_kind_ambiguous"
	OpenPilotReasonRuntimeFingerprintStale    = "runtime_fingerprint_stale"
	OpenPilotReasonRuntimeRegistryUnavailable = "runtime_registry_unavailable"
	OpenPilotReasonRepositoryGenerationDiffer = "repository_generation_differs"
	OpenPilotReasonRepositoryNotInRuntime     = "repository_not_in_runtime"
)

type OpenPilotReadinessCheck struct {
	Name        string `json:"name"`
	Code        string `json:"code"`
	Status      string `json:"status"`
	Message     string `json:"message"`
	Remediation string `json:"remediation,omitempty"`
}

type OpenPilotReadinessTarget struct {
	Repo               string `json:"repo"`
	Issue              string `json:"issue,omitempty"`
	RepositoryID       string `json:"repository_id,omitempty"`
	RepositorySlug     string `json:"repository_slug,omitempty"`
	ExecutorGeneration string `json:"executor_generation,omitempty"`
	CanonicalIssue     string `json:"canonical_issue,omitempty"`
	Registered         bool   `json:"registered"`
}

type OpenPilotReadinessRollout struct {
	Mode                string                 `json:"mode"`
	RegistryFingerprint string                 `json:"registry_fingerprint,omitempty"`
	FingerprintMatches  bool                   `json:"fingerprint_matches"`
	ProofTargets        []OpenPilotProofTarget `json:"proof_targets"`
	ExpiresAt           *time.Time             `json:"expires_at,omitempty"`
	ClosedReason        string                 `json:"closed_reason,omitempty"`
	Generation          int64                  `json:"generation"`
}

type OpenPilotReadinessGates struct {
	AutonomyMode      string   `json:"autonomy_mode"`
	GiteaWriteMode    string   `json:"gitea_write_mode"`
	GitPushMode       string   `json:"git_push_mode"`
	AutoCommitMode    string   `json:"auto_commit_mode"`
	CICheckMode       string   `json:"ci_check_mode"`
	CIRequiredChecks  []string `json:"ci_required_checks"`
	CIAutoRepairMode  string   `json:"ci_auto_repair_mode"`
	MergeMode         string   `json:"merge_mode"`
	CloseIssueMode    string   `json:"close_issue_mode"`
	WorktreeMode      string   `json:"worktree_mode"`
	CommandPolicyMode string   `json:"command_policy_mode"`
	Known             bool     `json:"known"`
}

type OpenPilotReadinessCommand struct {
	Command string `json:"command"`
	Allowed bool   `json:"allowed"`
	DryRun  bool   `json:"dry_run"`
	Reason  string `json:"reason"`
}

type OpenPilotReadinessIssueState struct {
	Phase      string `json:"phase"`
	RunID      string `json:"run_id,omitempty"`
	NextAction string `json:"next_action"`
	ActiveRun  bool   `json:"active_run"`
}

type OpenPilotOnboardingStep struct {
	Step   string `json:"step"`
	Done   bool   `json:"done"`
	Detail string `json:"detail"`
}

type OpenPilotReadinessReport struct {
	Ready               bool                                `json:"ready"`
	CheckedAt           time.Time                           `json:"checked_at"`
	ActivationMode      string                              `json:"activation_mode"`
	RegistryGeneration  string                              `json:"registry_generation,omitempty"`
	RegistryFingerprint string                              `json:"registry_fingerprint,omitempty"`
	Repositories        []OpenPilotAuditRepository          `json:"repositories"`
	Target              *OpenPilotReadinessTarget           `json:"target,omitempty"`
	Rollout             *OpenPilotReadinessRollout          `json:"rollout,omitempty"`
	Executors           []OpenPilotRepositoryExecutorStatus `json:"executors"`
	Gates               OpenPilotReadinessGates             `json:"gates"`
	Command             *OpenPilotReadinessCommand          `json:"command,omitempty"`
	IssueState          *OpenPilotReadinessIssueState       `json:"issue_state,omitempty"`
	Checks              []OpenPilotReadinessCheck           `json:"checks"`
	Blockers            []string                            `json:"blockers"`
	Onboarding          []OpenPilotOnboardingStep           `json:"onboarding"`
}

type openPilotReadinessInput struct {
	Repo        string
	Issue       string
	TestCommand string
	Gate        *OpenPilotAdmissionGate
	GateError   error
	Executors   *EnvWorkflowExecutors
	Store       Store
	Status      OpenPilotRepositoryStatus
	StatusKnown bool
	Now         time.Time
}

func (r *OpenPilotReadinessReport) add(check OpenPilotReadinessCheck) {
	r.Checks = append(r.Checks, check)
	if check.Status == OpenPilotReadinessFail {
		r.Blockers = append(r.Blockers, check.Code)
	}
}

func openPilotReadinessGatesFromExecutors(env *EnvWorkflowExecutors) OpenPilotReadinessGates {
	gates := OpenPilotReadinessGates{AutonomyMode: firstNonEmpty(openPilotAutonomyMode(), "manual"), CIRequiredChecks: []string{}}
	if env == nil {
		return gates
	}
	gates.Known = true
	gates.GiteaWriteMode = env.openPilotWriteMode
	gates.GitPushMode = env.openPilotGitPushMode
	gates.AutoCommitMode = env.openPilotAutoCommitMode
	gates.CICheckMode = env.openPilotCICheckMode
	gates.CIRequiredChecks = append(gates.CIRequiredChecks, env.openPilotCIRequiredChecks...)
	gates.CIAutoRepairMode = env.openPilotCIAutoRepairMode
	gates.MergeMode = env.openPilotMergeMode
	gates.CloseIssueMode = env.openPilotCloseIssueMode
	gates.WorktreeMode = env.openPilotWorktreeMode
	if runner, ok := env.commandRunner.(*PolicyCommandRunner); ok && runner != nil {
		gates.CommandPolicyMode = string(firstNonEmptyMode(runner.policy.Mode))
	}
	return gates
}

func firstNonEmptyMode(mode CommandPolicyMode) CommandPolicyMode {
	if mode == "" {
		return CommandPolicyDeny
	}
	return mode
}

func buildOpenPilotReadiness(input openPilotReadinessInput) OpenPilotReadinessReport {
	now := input.Now
	if now.IsZero() {
		now = time.Now()
	}
	report := OpenPilotReadinessReport{
		CheckedAt: now.UTC(), Repositories: []OpenPilotAuditRepository{}, Executors: []OpenPilotRepositoryExecutorStatus{},
		Checks: []OpenPilotReadinessCheck{}, Blockers: []string{}, Onboarding: []OpenPilotOnboardingStep{},
		Gates: openPilotReadinessGatesFromExecutors(input.Executors),
	}
	gate := input.Gate
	if input.GateError != nil || gate == nil || gate.registry == nil {
		message := "the Open Pilot admission gate could not be constructed"
		if input.GateError != nil {
			message = input.GateError.Error()
		}
		report.add(OpenPilotReadinessCheck{Name: "admission_gate", Code: OpenPilotReasonGateUnavailable, Status: OpenPilotReadinessFail, Message: message,
			Remediation: "Fix the runtime configuration named in the message (activation mode, runtime manifest, source origin, or singleton binding) and restart the API."})
		report.Onboarding = openPilotOnboardingChecklist(report, nil, false, false)
		return report
	}
	report.ActivationMode = gate.mode
	report.RegistryGeneration = gate.registry.manifest.RegistryGeneration
	report.RegistryFingerprint = gate.registry.manifest.RegistryFingerprint
	for _, repository := range gate.registry.Repositories() {
		report.Repositories = append(report.Repositories, OpenPilotAuditRepository{ID: repository.ID, Slug: repository.Slug, Aliases: append([]string(nil), repository.Aliases...), ExecutorGeneration: repository.ExecutorGeneration})
	}

	var bundles map[string]openPilotExecutionBundle
	bundlesKnown := false
	if input.Executors != nil {
		if input.Executors.openPilotGateError != nil {
			report.add(OpenPilotReadinessCheck{Name: "execution_bundles", Code: OpenPilotReasonBundlesUnavailable, Status: OpenPilotReadinessFail, Message: input.Executors.openPilotGateError.Error(),
				Remediation: "Repair the repository entry named in the message (private password file, endpoints, or duplicate identity) in the operator registry, re-render the topology, and restart the API."})
		} else {
			bundles, bundlesKnown = input.Executors.openPilotBundles, true
		}
	}

	switch gate.mode {
	case "disabled":
		report.add(OpenPilotReadinessCheck{Name: "activation_mode", Code: OpenPilotReasonActivationDisabled, Status: OpenPilotReadinessFail, Message: "SWITCHYARD_OPEN_PILOT_ACTIVATION_MODE=disabled loads the topology but admits no work",
			Remediation: "After the topology health checks pass, set SWITCHYARD_OPEN_PILOT_ACTIVATION_MODE=durable and open the rollout gate."})
	case "isolated-test":
		report.add(OpenPilotReadinessCheck{Name: "activation_mode", Code: OpenPilotReasonActivationIsolatedTest, Status: OpenPilotReadinessWarn, Message: "isolated-test admits registered repositories for bounded non-production proof only",
			Remediation: "Production validation rejects isolated-test; use durable activation with the rollout gate for production."})
	case "durable":
		report.add(OpenPilotReadinessCheck{Name: "activation_mode", Code: "activation_durable", Status: OpenPilotReadinessPass, Message: "durable activation admits work through the PostgreSQL rollout gate"})
	default:
		report.add(OpenPilotReadinessCheck{Name: "activation_mode", Code: OpenPilotReasonActivationLegacySingleton, Status: OpenPilotReadinessWarn, Message: "legacy-singleton admits only the execution-bound source origin repository",
			Remediation: "Render a runtime manifest with scripts/render_open_pilot_topology.py and activate durable mode to admit registered repositories."})
	}

	var target OpenPilotTarget
	targetResolved := false
	if input.Repo != "" {
		report.Target = &OpenPilotReadinessTarget{Repo: input.Repo, Issue: input.Issue}
		resolved, err := gate.registry.ResolveTarget(input.Repo, firstNonEmpty(input.Issue, "1"))
		switch {
		case errors.Is(err, ErrOpenPilotRepositoryUnknown):
			report.add(OpenPilotReadinessCheck{Name: "repository_registered", Code: OpenPilotReasonRepositoryUnregistered, Status: OpenPilotReadinessFail, Message: "repository " + input.Repo + " matches no registered canonical slug or alias",
				Remediation: "Add the repository to the operator registry with its exact Gitea origin slug, run scripts/prepare_open_pilot_registry.py, re-render the topology, and restart the API."})
		case err != nil:
			report.add(OpenPilotReadinessCheck{Name: "repository_registered", Code: OpenPilotReasonIssueInvalid, Status: OpenPilotReadinessFail, Message: err.Error(),
				Remediation: "Provide the issue as a positive decimal number without a leading '#'."})
		default:
			target, targetResolved = resolved, true
			report.Target.Registered = true
			report.Target.RepositoryID = target.RepositoryID
			report.Target.RepositorySlug = target.RepositorySlug
			report.Target.ExecutorGeneration = target.ExecutorGeneration
			if input.Issue != "" {
				report.Target.CanonicalIssue = target.Issue
			}
			report.add(OpenPilotReadinessCheck{Name: "repository_registered", Code: "repository_registered", Status: OpenPilotReadinessPass, Message: "repository resolves to " + target.RepositorySlug + " (" + target.RepositoryID + "/" + target.ExecutorGeneration + ")"})
			if (gate.mode == "" || gate.mode == "legacy-singleton") && (target.RepositoryID != gate.bound.ID || target.ExecutorGeneration != gate.bound.ExecutorGeneration) {
				report.add(OpenPilotReadinessCheck{Name: "singleton_binding", Code: OpenPilotReasonRepositoryNotBound, Status: OpenPilotReadinessFail, Message: "legacy-singleton runtime is bound to " + gate.bound.Slug + " and rejects other registered repositories",
					Remediation: "Dispatch against the bound repository, or activate durable mode with a rendered runtime manifest."})
			}
		}
	}

	if gate.mode == "durable" {
		report.Rollout = openPilotReadinessRollout(report.RegistryFingerprint, gate.rollout, now)
		report.add(openPilotRolloutCheck(report.Rollout, gate.rollout != nil, target, targetResolved, input.Issue != "", now))
	} else {
		report.add(OpenPilotReadinessCheck{Name: "rollout_gate", Code: "rollout_not_applicable", Status: OpenPilotReadinessSkip, Message: "the durable rollout gate applies only to durable activation"})
	}

	bundleFound := false
	if gate.mode != "legacy-singleton" && gate.mode != "" {
		switch {
		case !bundlesKnown:
			report.add(OpenPilotReadinessCheck{Name: "execution_bundle", Code: OpenPilotReasonBundlesUnavailable, Status: OpenPilotReadinessSkip, Message: "execution bundles are not available to this readiness probe"})
		case targetResolved:
			_, bundleFound = bundles[openPilotExecutionBundleKey(target.RepositoryID, target.ExecutorGeneration)]
			if bundleFound {
				report.add(OpenPilotReadinessCheck{Name: "execution_bundle", Code: "execution_bundle_bound", Status: OpenPilotReadinessPass, Message: "an immutable execution bundle exists for " + target.RepositoryID + "/" + target.ExecutorGeneration})
			} else {
				report.add(OpenPilotReadinessCheck{Name: "execution_bundle", Code: OpenPilotReasonBundleMissing, Status: OpenPilotReadinessFail, Message: "no execution bundle is loaded for " + target.RepositoryID + "/" + target.ExecutorGeneration + "; the running API predates this registry generation",
					Remediation: "Re-render the topology for the current registry generation and restart the API so bundles match the manifest."})
			}
		default:
			bundleFound = len(bundles) > 0
			report.add(OpenPilotReadinessCheck{Name: "execution_bundle", Code: "execution_bundles_loaded", Status: openPilotPassOrFail(bundleFound), Message: fmt.Sprintf("%d execution bundle(s) loaded", len(bundles)),
				Remediation: openPilotRemediationIf(!bundleFound, "Render a runtime manifest with at least one repository and restart the API.")})
		}
	} else {
		report.add(OpenPilotReadinessCheck{Name: "execution_bundle", Code: "execution_bundle_legacy", Status: OpenPilotReadinessSkip, Message: "legacy-singleton uses the ambient OpenCode executor and verifier configuration"})
	}

	openCodeHealthy, verifierHealthy := openPilotReadinessExecutorChecks(&report, input, target, targetResolved)

	if report.IssueState == nil && targetResolved && input.Issue != "" && input.Store != nil {
		openPilotReadinessIssueChecks(&report, gate, input.Store, target)
	}

	commandAllowed := openPilotReadinessCommandCheck(&report, input, bundles, target, targetResolved)
	openPilotReadinessPreparationCheck(&report, input.TestCommand)
	openPilotReadinessGateChecks(&report)

	report.Ready = len(report.Blockers) == 0
	report.Onboarding = openPilotOnboardingChecklist(report, gate, bundleFound, openCodeHealthy && verifierHealthy)
	_ = commandAllowed
	return report
}

func openPilotPassOrFail(ok bool) string {
	if ok {
		return OpenPilotReadinessPass
	}
	return OpenPilotReadinessFail
}

func openPilotRemediationIf(condition bool, text string) string {
	if condition {
		return text
	}
	return ""
}

func openPilotReadinessRollout(fingerprint string, store Store, now time.Time) *OpenPilotReadinessRollout {
	rollout := &OpenPilotReadinessRollout{ProofTargets: []OpenPilotProofTarget{}}
	if store == nil {
		return rollout
	}
	status, err := store.OpenPilotRolloutStatus()
	if err != nil {
		return rollout
	}
	rollout.Mode = status.Mode
	rollout.RegistryFingerprint = status.RegistryFingerprint
	rollout.FingerprintMatches = status.RegistryFingerprint != "" && status.RegistryFingerprint == fingerprint
	rollout.ProofTargets = cloneOpenPilotProofTargets(status.ProofTargets)
	rollout.ExpiresAt = status.ExpiresAt
	rollout.ClosedReason = status.ClosedReason
	rollout.Generation = status.Generation
	return rollout
}

func openPilotRolloutCheck(rollout *OpenPilotReadinessRollout, storeKnown bool, target OpenPilotTarget, targetResolved, issueGiven bool, now time.Time) OpenPilotReadinessCheck {
	check := OpenPilotReadinessCheck{Name: "rollout_gate"}
	switch {
	case !storeKnown || rollout == nil || rollout.Mode == "":
		check.Code, check.Status, check.Message = OpenPilotReasonRolloutStoreUnavailable, OpenPilotReadinessFail, "the durable rollout status could not be read from the store"
		check.Remediation = "Check PostgreSQL connectivity and the Open Pilot rollout migration, then retry."
	case rollout.Mode == "legacy":
		check.Code, check.Status, check.Message = OpenPilotReasonRolloutNotConfigured, OpenPilotReadinessFail, "the durable rollout gate has never been configured"
		check.Remediation = "POST /api/open-pilot/rollout with the current registry fingerprint to start a bounded proof or promote to allow."
	case !rollout.FingerprintMatches:
		check.Code, check.Status, check.Message = OpenPilotReasonRolloutFingerprintStale, OpenPilotReadinessFail, "the rollout gate was configured for a different registry fingerprint than the loaded runtime manifest"
		check.Remediation = "Re-run POST /api/open-pilot/rollout with the registry_fingerprint reported by this endpoint; admission rejects every target until the fingerprints match."
	case rollout.Mode == "disabled":
		check.Code, check.Status = OpenPilotReasonRolloutClosed, OpenPilotReadinessFail
		check.Message = "the rollout gate is closed" + openPilotSuffixIf(rollout.ClosedReason != "", ": "+rollout.ClosedReason)
		check.Remediation = "Inspect the close reason, then reopen the gate with a bounded proof or promote to allow."
	case rollout.Mode == "proof" && (rollout.ExpiresAt == nil || !rollout.ExpiresAt.After(now)):
		check.Code, check.Status, check.Message = OpenPilotReasonRolloutProofExpired, OpenPilotReadinessFail, "the bounded proof has expired"
		check.Remediation = "Start a new bounded proof (at most 24 hours) or promote to allow if both proof targets closed successfully."
	case rollout.Mode == "proof" && !targetResolved:
		check.Code, check.Status, check.Message = OpenPilotReasonRolloutProofBounded, OpenPilotReadinessWarn, "proof mode admits only the two configured proof targets"
		check.Remediation = "Preflight with repo and issue to confirm a specific target is admitted."
	case rollout.Mode == "proof":
		for _, proof := range rollout.ProofTargets {
			if proof.RepositoryID == target.RepositoryID && proof.ExecutorGeneration == target.ExecutorGeneration && (!issueGiven || proof.Issue == target.Issue) {
				check.Code, check.Status, check.Message = "rollout_proof_admits_target", OpenPilotReadinessPass, "the bounded proof admits this target until "+rollout.ExpiresAt.UTC().Format(time.RFC3339)
				return check
			}
		}
		check.Code, check.Status, check.Message = OpenPilotReasonRolloutProofExcluded, OpenPilotReadinessFail, "this target is not one of the configured proof targets"
		check.Remediation = "Dispatch one of the listed proof targets, or promote the gate to allow after both proof targets close successfully."
	case rollout.Mode == "allow":
		check.Code, check.Status, check.Message = "rollout_allow", OpenPilotReadinessPass, "the rollout gate allows every registered repository"
	default:
		check.Code, check.Status, check.Message = OpenPilotReasonRolloutClosed, OpenPilotReadinessFail, "unknown rollout mode "+rollout.Mode
		check.Remediation = "Reconfigure the rollout gate with a supported mode."
	}
	return check
}

func openPilotSuffixIf(condition bool, text string) string {
	if condition {
		return text
	}
	return ""
}

func openPilotReadinessExecutorChecks(report *OpenPilotReadinessReport, input openPilotReadinessInput, target OpenPilotTarget, targetResolved bool) (bool, bool) {
	if input.Gate.mode == "legacy-singleton" || input.Gate.mode == "" {
		report.add(OpenPilotReadinessCheck{Name: "opencode_executor", Code: "opencode_legacy", Status: OpenPilotReadinessSkip, Message: "legacy-singleton health is reported by GET /api/opencode-executor/status"})
		report.add(OpenPilotReadinessCheck{Name: "verifier", Code: "verifier_legacy", Status: OpenPilotReadinessSkip, Message: "legacy-singleton verifier health is reported by GET /api/opencode-executor/status"})
		return true, true
	}
	if !input.StatusKnown || input.Status.Error != "" {
		message := firstNonEmpty(input.Status.Error, "the repository status provider is not available")
		report.add(OpenPilotReadinessCheck{Name: "opencode_executor", Code: OpenPilotReasonExecutorStatusUnavailable, Status: OpenPilotReadinessFail, Message: message,
			Remediation: "Resolve the admission gate and bundle errors reported above; executor health cannot be probed until the topology loads."})
		report.add(OpenPilotReadinessCheck{Name: "verifier", Code: OpenPilotReasonExecutorStatusUnavailable, Status: OpenPilotReadinessFail, Message: message})
		return false, false
	}
	openCodeHealthy, verifierHealthy := true, true
	matched := false
	for _, repository := range input.Status.Repositories {
		if targetResolved && (repository.RepositoryID != target.RepositoryID || repository.ExecutorGeneration != target.ExecutorGeneration) {
			continue
		}
		matched = true
		report.Executors = append(report.Executors, repository)
		openCodeHealthy = openCodeHealthy && repository.OpenCode.Healthy
		verifierHealthy = verifierHealthy && repository.VerifierHealthy
	}
	if !matched {
		if targetResolved {
			report.add(OpenPilotReadinessCheck{Name: "opencode_executor", Code: OpenPilotReasonExecutorStatusUnavailable, Status: OpenPilotReadinessFail, Message: "no executor status is projected for " + target.RepositoryID + "/" + target.ExecutorGeneration,
				Remediation: "Re-render the topology for the current registry generation and restart the API."})
			report.add(OpenPilotReadinessCheck{Name: "verifier", Code: OpenPilotReasonExecutorStatusUnavailable, Status: OpenPilotReadinessFail, Message: "no verifier status is projected for " + target.RepositoryID + "/" + target.ExecutorGeneration})
			return false, false
		}
		report.add(OpenPilotReadinessCheck{Name: "opencode_executor", Code: OpenPilotReasonExecutorStatusUnavailable, Status: OpenPilotReadinessFail, Message: "no repository executors are configured",
			Remediation: "Render a runtime manifest with at least one repository and restart the API."})
		report.add(OpenPilotReadinessCheck{Name: "verifier", Code: OpenPilotReasonExecutorStatusUnavailable, Status: OpenPilotReadinessFail, Message: "no repository verifiers are configured"})
		return false, false
	}
	if openCodeHealthy {
		report.add(OpenPilotReadinessCheck{Name: "opencode_executor", Code: "opencode_healthy", Status: OpenPilotReadinessPass, Message: "the repository OpenCode sidecar answered its session probe"})
	} else {
		report.add(OpenPilotReadinessCheck{Name: "opencode_executor", Code: OpenPilotReasonOpenCodeUnreachable, Status: OpenPilotReadinessFail, Message: "the repository OpenCode sidecar is unreachable or unhealthy: " + openPilotExecutorStatusSummary(report.Executors, func(status OpenPilotRepositoryExecutorStatus) string {
			return firstNonEmpty(status.OpenCode.Status, "unknown")
		}),
			Remediation: "Start or repair the repository's OpenCode service in the rendered Compose override, confirm its private password file, then retry."})
	}
	if verifierHealthy {
		report.add(OpenPilotReadinessCheck{Name: "verifier", Code: "verifier_healthy", Status: OpenPilotReadinessPass, Message: "the repository verifier answered /healthz"})
	} else {
		report.add(OpenPilotReadinessCheck{Name: "verifier", Code: OpenPilotReasonVerifierUnreachable, Status: OpenPilotReadinessFail, Message: "the repository verifier is unreachable or unhealthy: " + openPilotExecutorStatusSummary(report.Executors, func(status OpenPilotRepositoryExecutorStatus) string {
			return firstNonEmpty(status.VerifierStatus, "unknown")
		}),
			Remediation: "Start or repair the repository's verifier service and prepared verifier image (scripts/prepare_verifier.py), then retry."})
	}
	return openCodeHealthy, verifierHealthy
}

func openPilotExecutorStatusSummary(executors []OpenPilotRepositoryExecutorStatus, describe func(OpenPilotRepositoryExecutorStatus) string) string {
	parts := make([]string, 0, len(executors))
	for _, executor := range executors {
		parts = append(parts, executor.RepositoryID+"/"+executor.ExecutorGeneration+"="+describe(executor))
	}
	return strings.Join(parts, ", ")
}

func openPilotReadinessIssueChecks(report *OpenPilotReadinessReport, gate *OpenPilotAdmissionGate, store Store, target OpenPilotTarget) {
	state, err := store.OpenPilotIssueStateForTarget(target)
	if errors.Is(err, ErrNotFound) {
		report.add(OpenPilotReadinessCheck{Name: "issue_state", Code: "issue_state_new", Status: OpenPilotReadinessPass, Message: "no durable lifecycle state exists; dispatch starts a new lifecycle at issue validation"})
		return
	}
	if err != nil {
		report.add(OpenPilotReadinessCheck{Name: "issue_state", Code: OpenPilotReasonRolloutStoreUnavailable, Status: OpenPilotReadinessFail, Message: "the durable issue state could not be read: " + err.Error(),
			Remediation: "Check store connectivity and retry."})
		return
	}
	active := openPilotHasActiveRun(store, state.Repo, state.Issue)
	report.IssueState = &OpenPilotReadinessIssueState{Phase: string(state.Phase), RunID: state.RunID, NextAction: openPilotNextAction(state), ActiveRun: active}
	if _, err := gate.ValidateState(state); err != nil {
		bound := state.RepositoryID + "/" + state.ExecutorGeneration
		if state.RepositoryID == "" {
			bound = "no affinity"
		}
		report.add(OpenPilotReadinessCheck{Name: "issue_state", Code: OpenPilotReasonIssueAffinityStale, Status: OpenPilotReadinessFail, Message: "the durable issue state is bound to " + bound + " but the registry now resolves " + target.RepositoryID + "/" + target.ExecutorGeneration,
			Remediation: "Do not edit affinity by hand. Finish or pause the issue on its original executor generation, or review it through GET /api/open-pilot/repository-audit before re-registering."})
		return
	}
	if active {
		report.add(OpenPilotReadinessCheck{Name: "issue_state", Code: OpenPilotReasonIssueRunActive, Status: OpenPilotReadinessFail, Message: "a queued or running Open Pilot run already owns this issue (" + state.RunID + ")",
			Remediation: "Wait for the run to finish, or cancel it, before dispatching again."})
		return
	}
	report.add(OpenPilotReadinessCheck{Name: "issue_state", Code: "issue_state_" + string(state.Phase), Status: OpenPilotReadinessPass, Message: "durable phase " + string(state.Phase) + "; next action: " + openPilotNextAction(state)})
}

func openPilotReadinessCommandCheck(report *OpenPilotReadinessReport, input openPilotReadinessInput, bundles map[string]openPilotExecutionBundle, target OpenPilotTarget, targetResolved bool) bool {
	command := strings.TrimSpace(input.TestCommand)
	if command == "" {
		report.add(OpenPilotReadinessCheck{Name: "test_command", Code: OpenPilotReasonCommandNotProvided, Status: OpenPilotReadinessSkip, Message: "pass test_command to preflight it against the repository command allowlist"})
		return false
	}
	var runner CommandRunner
	if targetResolved {
		if bundle, ok := bundles[openPilotExecutionBundleKey(target.RepositoryID, target.ExecutorGeneration)]; ok {
			runner = bundle.commandRunner
		}
	}
	if runner == nil && input.Executors != nil {
		runner = input.Executors.commandRunner
	}
	policy, ok := runner.(*PolicyCommandRunner)
	if !ok || policy == nil {
		report.add(OpenPilotReadinessCheck{Name: "test_command", Code: OpenPilotReasonCommandPolicyUnknown, Status: OpenPilotReadinessSkip, Message: "the selected command runner does not expose an argv policy; the command was not evaluated"})
		return false
	}
	decision := policy.Decide(command)
	report.Command = &OpenPilotReadinessCommand{Command: command, Allowed: decision.Allowed, DryRun: decision.DryRun, Reason: decision.Reason}
	switch {
	case !decision.Allowed:
		report.add(OpenPilotReadinessCheck{Name: "test_command", Code: OpenPilotReasonCommandDenied, Status: OpenPilotReadinessFail, Message: "the verify step would reject this command: " + decision.Reason,
			Remediation: "Use a plain argv command whose executable is in OPEN_PILOT_ALLOWED_COMMANDS (and git verb in OPEN_PILOT_ALLOWED_GIT_VERBS), with OPEN_PILOT_COMMAND_POLICY_MODE=allow. Readiness does not relax the allowlist."})
		return false
	case decision.DryRun:
		report.add(OpenPilotReadinessCheck{Name: "test_command", Code: OpenPilotReasonCommandDryRun, Status: OpenPilotReadinessWarn, Message: "the command is allowlisted but OPEN_PILOT_COMMAND_POLICY_MODE=dry-run records it without executing",
			Remediation: "Set OPEN_PILOT_COMMAND_POLICY_MODE=allow when real verification is required."})
		return true
	default:
		report.add(OpenPilotReadinessCheck{Name: "test_command", Code: "command_allowed", Status: OpenPilotReadinessPass, Message: "the command passes the argv allowlist"})
		return true
	}
}

// openPilotReadinessPreparationCheck compares the proposed test command with the
// exact argv commands each projected verifier's prepared bundle supports. The
// verifier enforces the same contract; this reports the outcome before admission.
func openPilotReadinessPreparationCheck(report *OpenPilotReadinessReport, testCommand string) {
	command := strings.TrimSpace(testCommand)
	if command == "" {
		return
	}
	var reported []OpenPilotRepositoryExecutorStatus
	for _, executor := range report.Executors {
		if executor.VerifierPreparation != nil {
			reported = append(reported, executor)
		}
	}
	if len(reported) == 0 {
		return
	}
	var unprepared, unsupported []string
	for _, executor := range reported {
		preparation := executor.VerifierPreparation
		name := executor.RepositoryID + "/" + executor.ExecutorGeneration
		switch {
		case !preparation.Prepared:
			unprepared = append(unprepared, name+" ("+firstNonEmpty(preparation.Error, "no prepared bundle")+")")
		case !preparation.SupportsCommand(command):
			unsupported = append(unsupported, name+" supports "+openPilotPreparedCommands(preparation.Commands))
		}
	}
	switch {
	case len(unsupported) > 0:
		report.add(OpenPilotReadinessCheck{Name: "verifier_preparation", Code: OpenPilotReasonVerifierCommandUnsupported, Status: OpenPilotReadinessFail,
			Message:     "the prepared verifier would reject this command: " + strings.Join(unsupported, "; "),
			Remediation: "Use a command listed by the verifier's prepared profile, or extend backend/cmd/verifier/profiles.json and rebuild the image with scripts/prepare_verifier.py."})
	case len(unprepared) > 0:
		report.add(OpenPilotReadinessCheck{Name: "verifier_preparation", Code: OpenPilotReasonVerifierUnprepared, Status: OpenPilotReadinessWarn,
			Message:     "the verifier has no usable prepared dependency bundle, so only dependency-free commands can pass on its isolated network: " + strings.Join(unprepared, "; "),
			Remediation: "Build a prepared verifier image with scripts/prepare_verifier.py and set the repository's verifier_image registry field."})
	default:
		report.add(OpenPilotReadinessCheck{Name: "verifier_preparation", Code: "verifier_command_supported", Status: OpenPilotReadinessPass, Message: "the prepared verifier supports this exact command"})
	}
}

func openPilotPreparedCommands(commands [][]string) string {
	if len(commands) == 0 {
		return "no commands"
	}
	parts := make([]string, 0, len(commands))
	for _, command := range commands {
		parts = append(parts, strconv.Quote(strings.Join(command, " ")))
	}
	return strings.Join(parts, ", ")
}

func openPilotReadinessGateChecks(report *OpenPilotReadinessReport) {
	gates := report.Gates
	if !gates.Known {
		report.add(OpenPilotReadinessCheck{Name: "lifecycle_gates", Code: "lifecycle_gates_unknown", Status: OpenPilotReadinessSkip, Message: "effective lifecycle gates are not available from this executor registry"})
		return
	}
	type gateCheck struct {
		name, code, mode, want, consequence, env string
	}
	for _, check := range []gateCheck{
		{"gitea_write_gate", OpenPilotReasonGiteaWriteDisabled, gates.GiteaWriteMode, "allow", "Gitea writes (assignment, PR creation, comments) are skipped or dry-run", "OPEN_PILOT_GITEA_WRITE_MODE"},
		{"git_push_gate", OpenPilotReasonGitPushDisabled, gates.GitPushMode, "allow", "the branch is never pushed, so no PR can be created", "OPEN_PILOT_GIT_PUSH_MODE"},
		{"auto_commit_gate", OpenPilotReasonAutoCommitDisabled, gates.AutoCommitMode, "allow", "agent changes are not committed automatically", "OPEN_PILOT_AUTO_COMMIT_MODE"},
		{"ci_check_gate", OpenPilotReasonCIChecksDisabled, gates.CICheckMode, "require-pass", "hosted CI status is not awaited before merge readiness", "OPEN_PILOT_CI_CHECK_MODE"},
		{"merge_gate", OpenPilotReasonMergeDisabled, gates.MergeMode, "allow", "the PR is left open for a human merge", "OPEN_PILOT_MERGE_MODE"},
		{"close_issue_gate", OpenPilotReasonCloseIssueDisabled, gates.CloseIssueMode, "allow", "the issue stays open after merge", "OPEN_PILOT_CLOSE_ISSUE_MODE"},
	} {
		if check.mode == check.want {
			report.add(OpenPilotReadinessCheck{Name: check.name, Code: check.name + "_" + check.want, Status: OpenPilotReadinessPass, Message: check.env + "=" + check.mode})
			continue
		}
		report.add(OpenPilotReadinessCheck{Name: check.name, Code: check.code, Status: OpenPilotReadinessWarn, Message: check.env + "=" + firstNonEmpty(check.mode, "disabled") + ": " + check.consequence,
			Remediation: "Set " + check.env + "=" + check.want + " in the private runtime configuration when that lifecycle stage should run autonomously. Readiness reports the effective value and does not change it."})
	}
}

func openPilotOnboardingChecklist(report OpenPilotReadinessReport, gate *OpenPilotAdmissionGate, bundleFound, executorsHealthy bool) []OpenPilotOnboardingStep {
	steps := []OpenPilotOnboardingStep{}
	manifestLoaded := gate != nil && gate.registry != nil && gate.mode != "" && gate.mode != "legacy-singleton"
	steps = append(steps, OpenPilotOnboardingStep{Step: "render_runtime_manifest", Done: manifestLoaded, Detail: "Validate the operator registry with scripts/prepare_open_pilot_registry.py, render the topology with scripts/render_open_pilot_topology.py, and point SWITCHYARD_OPEN_PILOT_RUNTIME_MANIFEST at the result."})
	registered := report.Target != nil && report.Target.Registered
	if report.Target == nil {
		registered = len(report.Repositories) > 0
	}
	steps = append(steps, OpenPilotOnboardingStep{Step: "register_repository", Done: registered, Detail: "The repository's exact Gitea origin slug (or an alias) must appear in the loaded registry."})
	steps = append(steps, OpenPilotOnboardingStep{Step: "load_execution_bundle", Done: bundleFound, Detail: "The running API must hold an immutable bundle for the repository's executor generation; restart after re-rendering."})
	steps = append(steps, OpenPilotOnboardingStep{Step: "executors_healthy", Done: executorsHealthy && (manifestLoaded || (gate != nil && gate.mode == "legacy-singleton")), Detail: "The repository OpenCode sidecar and prepared verifier must answer their health probes."})
	rolloutOpen := report.Rollout != nil && report.Rollout.FingerprintMatches && (report.Rollout.Mode == "allow" || report.Rollout.Mode == "proof")
	steps = append(steps, OpenPilotOnboardingStep{Step: "open_rollout_gate", Done: rolloutOpen, Detail: "Production uses durable activation; configure the rollout gate for the current registry fingerprint (bounded proof, then allow)."})
	gates := report.Gates
	steps = append(steps, OpenPilotOnboardingStep{Step: "enable_write_gates", Done: gates.Known && gates.GiteaWriteMode == "allow" && gates.GitPushMode == "allow" && gates.AutoCommitMode == "allow", Detail: "Gitea write, Git push, and auto-commit modes control whether the agent's work reaches a pull request."})
	steps = append(steps, OpenPilotOnboardingStep{Step: "enable_ci_and_merge_gates", Done: gates.Known && gates.CICheckMode == "require-pass" && gates.MergeMode == "allow", Detail: "CI require-pass and merge allow complete the autonomous lifecycle; leave them disabled for human merge."})
	commandDone := report.Command != nil && report.Command.Allowed && !report.Command.DryRun
	steps = append(steps, OpenPilotOnboardingStep{Step: "allow_test_command", Done: commandDone, Detail: "The issue's Test Command must pass the argv allowlist with OPEN_PILOT_COMMAND_POLICY_MODE=allow."})
	return steps
}

// Dry-run validation of repository configuration documents.

type OpenPilotConfigurationDryRunInput struct {
	Registry json.RawMessage `json:"registry,omitempty"`
	Manifest json.RawMessage `json:"manifest,omitempty"`
}

type OpenPilotConfigurationRepositoryResult struct {
	ID                 string `json:"id"`
	Slug               string `json:"slug"`
	ExecutorGeneration string `json:"executor_generation"`
	RuntimeMatch       string `json:"runtime_match"`
}

type OpenPilotConfigurationDryRunReport struct {
	Valid                     bool                                     `json:"valid"`
	Kind                      string                                   `json:"kind"`
	CheckedAt                 time.Time                                `json:"checked_at"`
	Generation                string                                   `json:"generation,omitempty"`
	Fingerprint               string                                   `json:"fingerprint,omitempty"`
	RuntimeFingerprint        string                                   `json:"runtime_fingerprint,omitempty"`
	RuntimeFingerprintMatches bool                                     `json:"runtime_fingerprint_matches"`
	Repositories              []OpenPilotConfigurationRepositoryResult `json:"repositories"`
	Checks                    []OpenPilotReadinessCheck                `json:"checks"`
	Blockers                  []string                                 `json:"blockers"`
}

func (r *OpenPilotConfigurationDryRunReport) add(check OpenPilotReadinessCheck) {
	r.Checks = append(r.Checks, check)
	if check.Status == OpenPilotReadinessFail {
		r.Blockers = append(r.Blockers, check.Code)
	}
}

// dryRunOpenPilotConfiguration validates one operator registry or runtime
// manifest document structurally and against the loaded runtime registry. It
// performs no filesystem, Git, or network access and never echoes the document.
func dryRunOpenPilotConfiguration(input OpenPilotConfigurationDryRunInput, gate *OpenPilotAdmissionGate, now time.Time) OpenPilotConfigurationDryRunReport {
	report := OpenPilotConfigurationDryRunReport{CheckedAt: now.UTC(), Repositories: []OpenPilotConfigurationRepositoryResult{}, Checks: []OpenPilotReadinessCheck{}, Blockers: []string{}}
	hasRegistry, hasManifest := len(input.Registry) > 0, len(input.Manifest) > 0
	if hasRegistry == hasManifest {
		report.add(OpenPilotReadinessCheck{Name: "document", Code: OpenPilotReasonDocumentKindAmbiguous, Status: OpenPilotReadinessFail, Message: "provide exactly one of registry or manifest",
			Remediation: "Send {\"registry\": <operator registry>} or {\"manifest\": <runtime manifest>}."})
		return report
	}
	var entries []openPilotRepositoryIdentity
	if hasRegistry {
		report.Kind = "operator-registry"
		registry, err := DecodeOpenPilotOperatorRegistry(input.Registry)
		if err != nil {
			report.add(openPilotDocumentCheck(err))
			return report
		}
		report.Generation = registry.Generation
		for _, repository := range registry.Repositories {
			entries = append(entries, openPilotRepositoryIdentity{id: repository.ID, slug: repository.Slug, aliases: repository.Aliases, generation: repository.ExecutorGeneration})
		}
	} else {
		report.Kind = "runtime-manifest"
		manifest, err := DecodeOpenPilotRuntimeManifest(input.Manifest)
		if err != nil {
			report.add(openPilotDocumentCheck(err))
			return report
		}
		report.Generation = manifest.RegistryGeneration
		report.Fingerprint = manifest.RegistryFingerprint
		for _, repository := range manifest.Repositories {
			entries = append(entries, openPilotRepositoryIdentity{id: repository.ID, slug: repository.Slug, aliases: repository.Aliases, generation: repository.ExecutorGeneration})
		}
	}
	report.add(OpenPilotReadinessCheck{Name: "document", Code: "document_valid", Status: OpenPilotReadinessPass, Message: fmt.Sprintf("%s decodes strictly with %d repository entr%s and no secret-looking material", report.Kind, len(entries), openPilotPlural(len(entries), "y", "ies"))})
	if gate == nil || gate.registry == nil {
		report.add(OpenPilotReadinessCheck{Name: "runtime_registry", Code: OpenPilotReasonRuntimeRegistryUnavailable, Status: OpenPilotReadinessWarn, Message: "no runtime registry is loaded, so the document was not compared with the running API",
			Remediation: "Structural validation passed; compare again after the API loads a runtime manifest."})
		for _, entry := range entries {
			report.Repositories = append(report.Repositories, OpenPilotConfigurationRepositoryResult{ID: entry.id, Slug: entry.slug, ExecutorGeneration: entry.generation, RuntimeMatch: "unknown"})
		}
		report.Valid = len(report.Blockers) == 0
		return report
	}
	report.RuntimeFingerprint = gate.registry.manifest.RegistryFingerprint
	if report.Fingerprint != "" {
		report.RuntimeFingerprintMatches = report.Fingerprint == report.RuntimeFingerprint
		if report.RuntimeFingerprintMatches {
			report.add(OpenPilotReadinessCheck{Name: "runtime_fingerprint", Code: "runtime_fingerprint_current", Status: OpenPilotReadinessPass, Message: "the manifest fingerprint matches the loaded runtime registry"})
		} else {
			report.add(OpenPilotReadinessCheck{Name: "runtime_fingerprint", Code: OpenPilotReasonRuntimeFingerprintStale, Status: OpenPilotReadinessWarn, Message: "the manifest fingerprint differs from the loaded runtime registry; the running API will not admit this generation until restarted",
				Remediation: "Install the rendered topology through deploy/switchyard-compose, restart the API, then reconfigure the rollout gate with the new fingerprint."})
		}
	}
	for _, entry := range entries {
		result := OpenPilotConfigurationRepositoryResult{ID: entry.id, Slug: entry.slug, ExecutorGeneration: entry.generation}
		loaded, ok := gate.registry.RepositoryByID(entry.id)
		switch {
		case !ok:
			result.RuntimeMatch = "unregistered"
			report.add(OpenPilotReadinessCheck{Name: "repository:" + entry.id, Code: OpenPilotReasonRepositoryNotInRuntime, Status: OpenPilotReadinessWarn, Message: "repository " + entry.id + " is not in the loaded runtime registry",
				Remediation: "Re-render the topology and restart the API before dispatching work to this repository."})
		case loaded.Slug != entry.slug:
			result.RuntimeMatch = "slug-differs"
			report.add(OpenPilotReadinessCheck{Name: "repository:" + entry.id, Code: OpenPilotReasonDocumentInvalid, Status: OpenPilotReadinessFail, Message: "repository " + entry.id + " changes its canonical slug from the loaded runtime registry",
				Remediation: "Repository IDs are immutable identities; register a new ID for a different origin instead of re-pointing an existing one."})
		case loaded.ExecutorGeneration != entry.generation:
			result.RuntimeMatch = "generation-differs"
			report.add(OpenPilotReadinessCheck{Name: "repository:" + entry.id, Code: OpenPilotReasonRepositoryGenerationDiffer, Status: OpenPilotReadinessWarn, Message: "repository " + entry.id + " advances its executor generation; durable states bound to " + loaded.ExecutorGeneration + " will fail affinity validation",
				Remediation: "Finish or pause in-flight issues on the current generation, then re-render and restart."})
		default:
			result.RuntimeMatch = "exact"
		}
		report.Repositories = append(report.Repositories, result)
	}
	report.Valid = len(report.Blockers) == 0
	return report
}

func openPilotPlural(count int, singular, plural string) string {
	if count == 1 {
		return singular
	}
	return plural
}

func openPilotDocumentCheck(err error) OpenPilotReadinessCheck {
	message := err.Error()
	switch {
	case strings.Contains(message, "secret-looking material"):
		return OpenPilotReadinessCheck{Name: "document", Code: OpenPilotReasonDocumentContainsSecret, Status: OpenPilotReadinessFail, Message: "the document contains secret-looking material and was rejected without being echoed",
			Remediation: "Keep credentials in private password files referenced by path; registry and manifest documents must stay non-secret."}
	case strings.Contains(message, "decode open pilot"):
		return OpenPilotReadinessCheck{Name: "document", Code: OpenPilotReasonDocumentDecodeFailed, Status: OpenPilotReadinessFail, Message: message,
			Remediation: "Match the strict v1 schema in docs/schemas/; unknown fields, oversized documents, and non-UTF-8 input are rejected."}
	default:
		return OpenPilotReadinessCheck{Name: "document", Code: OpenPilotReasonDocumentInvalid, Status: OpenPilotReadinessFail, Message: message,
			Remediation: "Correct the entry named in the message; identities must be unique, paths absolute and clean, endpoints http(s) URLs."}
	}
}

func (a *API) openPilotReadinessInput(ctx context.Context, repo, issue, testCommand string) openPilotReadinessInput {
	input := openPilotReadinessInput{Repo: repo, Issue: issue, TestCommand: testCommand, Store: a.store, Now: time.Now()}
	if a.runner != nil {
		input.Gate, input.GateError = a.runner.openPilotGate, a.runner.openPilotGateError
		if env, ok := a.runner.executors.(*EnvWorkflowExecutors); ok {
			input.Executors = env
		}
	}
	if input.GateError == nil && input.Gate != nil && input.Gate.mode != "legacy-singleton" && a.openPilotRepositories != nil {
		probeCtx, cancel := context.WithTimeout(ctx, openPilotReadinessProbeTimeout)
		defer cancel()
		input.Status, input.StatusKnown = a.openPilotRepositories.Status(probeCtx), true
	}
	return input
}
