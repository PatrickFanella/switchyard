package controlplane

import (
	"fmt"
	"strings"
	"time"
)

const openPilotDefaultMaxRepairAttempts = 2

type OpenPilotPhase string

const (
	OpenPilotPhaseRequested      OpenPilotPhase = "requested"
	OpenPilotPhaseValidated      OpenPilotPhase = "validated"
	OpenPilotPhaseBranchPrepared OpenPilotPhase = "branch_prepared"
	OpenPilotPhaseAgentCompleted OpenPilotPhase = "agent_completed"
	OpenPilotPhaseVerified       OpenPilotPhase = "verified"
	OpenPilotPhasePushed         OpenPilotPhase = "pushed"
	OpenPilotPhasePRCreated      OpenPilotPhase = "pr_created"
	OpenPilotPhaseAIReviewed     OpenPilotPhase = "ai_reviewed"
	OpenPilotPhaseCIPassed       OpenPilotPhase = "ci_passed"
	OpenPilotPhaseMergeReady     OpenPilotPhase = "merge_ready"
	OpenPilotPhaseMerged         OpenPilotPhase = "merged"
	OpenPilotPhaseIssueClosed    OpenPilotPhase = "issue_closed"
	OpenPilotPhaseFailed         OpenPilotPhase = "failed"
	OpenPilotPhasePaused         OpenPilotPhase = "paused"
)

type OpenPilotGateStatus map[string]string

type OpenPilotIssueState struct {
	ID                    string              `json:"id"`
	Repo                  string              `json:"repo"`
	Issue                 string              `json:"issue"`
	Phase                 OpenPilotPhase      `json:"phase"`
	RunID                 string              `json:"run_id,omitempty"`
	LastStepName          string              `json:"last_step_name,omitempty"`
	Branch                string              `json:"branch,omitempty"`
	Base                  string              `json:"base,omitempty"`
	WorktreePath          string              `json:"worktree_path,omitempty"`
	PullRequestURL        string              `json:"pull_request_url,omitempty"`
	PullRequestID         int                 `json:"pull_request_id,omitempty"`
	PullRequestNum        int                 `json:"pull_request_number,omitempty"`
	MergeCommitSHA        string              `json:"merge_commit_sha,omitempty"`
	IssueClosed           bool                `json:"issue_closed"`
	GateStatus            OpenPilotGateStatus `json:"gate_status,omitempty"`
	ResumeInputs          map[string]string   `json:"resume_inputs,omitempty"`
	RepairAttempts        int                 `json:"repair_attempts"`
	MaxRepairAttempts     int                 `json:"max_repair_attempts"`
	LastError             string              `json:"last_error,omitempty"`
	FailingGate           string              `json:"failing_gate,omitempty"`
	OperatorSummary       string              `json:"operator_summary,omitempty"`
	RecommendedNextAction string              `json:"recommended_next_action,omitempty"`
	UpdatedAt             time.Time           `json:"updated_at"`
	CreatedAt             time.Time           `json:"created_at"`
}

var openPilotPhaseChain = []OpenPilotPhase{
	OpenPilotPhaseRequested,
	OpenPilotPhaseValidated,
	OpenPilotPhaseBranchPrepared,
	OpenPilotPhaseAgentCompleted,
	OpenPilotPhaseVerified,
	OpenPilotPhasePushed,
	OpenPilotPhasePRCreated,
	OpenPilotPhaseAIReviewed,
	OpenPilotPhaseCIPassed,
	OpenPilotPhaseMergeReady,
	OpenPilotPhaseMerged,
	OpenPilotPhaseIssueClosed,
}

var openPilotKnownPhases = map[OpenPilotPhase]struct{}{
	OpenPilotPhaseRequested:      {},
	OpenPilotPhaseValidated:      {},
	OpenPilotPhaseBranchPrepared: {},
	OpenPilotPhaseAgentCompleted: {},
	OpenPilotPhaseVerified:       {},
	OpenPilotPhasePushed:         {},
	OpenPilotPhasePRCreated:      {},
	OpenPilotPhaseAIReviewed:     {},
	OpenPilotPhaseCIPassed:       {},
	OpenPilotPhaseMergeReady:     {},
	OpenPilotPhaseMerged:         {},
	OpenPilotPhaseIssueClosed:    {},
	OpenPilotPhaseFailed:         {},
	OpenPilotPhasePaused:         {},
}

func ValidateOpenPilotTransition(from, to OpenPilotPhase) error {
	if !isOpenPilotKnownPhase(to) {
		return fmt.Errorf("invalid open pilot transition: %s -> %s", from, to)
	}
	if from == "" {
		if to == OpenPilotPhaseRequested || to == OpenPilotPhaseValidated {
			return nil
		}
		return fmt.Errorf("invalid open pilot transition: %s -> %s", from, to)
	}
	if !isOpenPilotKnownPhase(from) {
		return fmt.Errorf("invalid open pilot transition: %s -> %s", from, to)
	}
	if from == to {
		return nil
	}
	if from == OpenPilotPhaseIssueClosed {
		return fmt.Errorf("invalid open pilot transition: %s -> %s", from, to)
	}
	if from == OpenPilotPhaseFailed || from == OpenPilotPhasePaused {
		if isOpenPilotActivePhase(to) {
			return nil
		}
		return fmt.Errorf("invalid open pilot transition: %s -> %s", from, to)
	}
	if to == OpenPilotPhaseFailed || to == OpenPilotPhasePaused {
		if isOpenPilotActivePhase(from) {
			return nil
		}
		return fmt.Errorf("invalid open pilot transition: %s -> %s", from, to)
	}
	if from == OpenPilotPhaseMerged && to == OpenPilotPhaseIssueClosed {
		return nil
	}
	if from == OpenPilotPhaseAgentCompleted && to == OpenPilotPhasePushed {
		return nil
	}
	if from == OpenPilotPhaseAgentCompleted && to == OpenPilotPhasePRCreated {
		return nil
	}
	if from == OpenPilotPhaseVerified && to == OpenPilotPhasePRCreated {
		return nil
	}
	if !isOpenPilotActivePhase(from) || !isOpenPilotActivePhase(to) {
		return fmt.Errorf("invalid open pilot transition: %s -> %s", from, to)
	}
	for i := 0; i < len(openPilotPhaseChain)-1; i++ {
		if openPilotPhaseChain[i] == from && openPilotPhaseChain[i+1] == to {
			return nil
		}
	}
	return fmt.Errorf("invalid open pilot transition: %s -> %s", from, to)
}

func isOpenPilotKnownPhase(phase OpenPilotPhase) bool {
	_, ok := openPilotKnownPhases[phase]
	return ok
}

func isOpenPilotActivePhase(phase OpenPilotPhase) bool {
	for _, active := range openPilotPhaseChain[:len(openPilotPhaseChain)-1] {
		if active == phase {
			return true
		}
	}
	return false
}

func isOpenPilotResumablePhase(phase OpenPilotPhase) bool {
	switch phase {
	case OpenPilotPhaseAgentCompleted, OpenPilotPhaseFailed, OpenPilotPhasePaused:
		return true
	default:
		return false
	}
}

func cloneOpenPilotGateStatus(in OpenPilotGateStatus) OpenPilotGateStatus {
	if len(in) == 0 {
		return nil
	}
	out := make(OpenPilotGateStatus, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneOpenPilotIssueState(in OpenPilotIssueState) OpenPilotIssueState {
	out := in
	out.GateStatus = cloneOpenPilotGateStatus(in.GateStatus)
	out.ResumeInputs = cloneMap(in.ResumeInputs)
	return out
}

type openPilotFailureDiagnostics struct {
	failingGate           string
	operatorSummary       string
	recommendedNextAction string
}

func openPilotFailureDiagnosticsForState(saved OpenPilotIssueState, phase OpenPilotPhase, stepName, lastErr string, state map[string]any) openPilotFailureDiagnostics {
	if phase != OpenPilotPhaseFailed && phase != OpenPilotPhasePaused {
		return openPilotFailureDiagnostics{}
	}
	gateStatus := openPilotGateStatusFromState(state)
	status := gateStatus[strings.TrimPrefix(stepName, "open-pilot.")]
	if status == "" {
		status = gateStatusForOpenPilotStep(stepName, gateStatus)
	}
	repairAttempts := saved.RepairAttempts
	if attempts, ok := state["open-pilot.repair-attempts"].(int); ok && attempts > 0 {
		repairAttempts = attempts
	}
	maxAttempts := saved.MaxRepairAttempts
	if maxAttempts <= 0 {
		maxAttempts = openPilotDefaultMaxRepairAttempts
	}
	feedback, _ := state["ai_review.feedback"].(string)
	changesRequested, _ := state["open-pilot.ai-review.changes-requested"].(bool)
	if feedback == "" && changesRequested && saved.ResumeInputs != nil {
		feedback = saved.ResumeInputs["ai_review.feedback"]
	}
	if feedback == "" {
		feedback = strings.TrimSpace(lastErr)
	}
	switch stepName {
	case "open-pilot.validate-issue":
		return openPilotFailureDiagnostics{failingGate: "issue_validation", operatorSummary: summarizeOpenPilotFailure("Issue validation failed", lastErr), recommendedNextAction: "manual_review"}
	case "open-pilot.branch-plan":
		return openPilotFailureDiagnostics{failingGate: "branch_preparation", operatorSummary: summarizeOpenPilotFailure("Branch preparation failed", lastErr), recommendedNextAction: "manual_review"}
	case "open-pilot.prepare-prompt":
		return openPilotFailureDiagnostics{failingGate: "prompt_preparation", operatorSummary: summarizeOpenPilotFailure("Prompt preparation failed", lastErr), recommendedNextAction: "resume"}
	case "open-pilot.agent-work":
		return openPilotFailureDiagnostics{failingGate: "agent_work", operatorSummary: summarizeOpenPilotFailure("Agent execution failed", lastErr), recommendedNextAction: "resume"}
	case "open-pilot.verify-command":
		return openPilotFailureDiagnostics{failingGate: "local_verify", operatorSummary: summarizeOpenPilotFailure("Local verification failed", lastErr), recommendedNextAction: "rerun_verify"}
	case "open-pilot.push-branch":
		return openPilotFailureDiagnostics{failingGate: "git_push", operatorSummary: summarizeOpenPilotFailure("Push or branch publication failed", lastErr), recommendedNextAction: "fix_agent_commit"}
	case "open-pilot.pr-plan":
		return openPilotFailureDiagnostics{failingGate: "pull_request", operatorSummary: summarizeOpenPilotFailure("PR creation failed", lastErr), recommendedNextAction: giteaFailureAction(lastErr)}
	case "open-pilot.ai-review":
		if phase == OpenPilotPhasePaused || changesRequested {
			summary := fmt.Sprintf("AI review requested changes (repair attempt %d/%d)", repairAttempts, maxAttempts)
			if feedback != "" {
				summary += ": " + feedback
			}
			return openPilotFailureDiagnostics{failingGate: "ai_review", operatorSummary: summary, recommendedNextAction: "fix_agent_commit"}
		}
		return openPilotFailureDiagnostics{failingGate: "ai_review", operatorSummary: summarizeOpenPilotFailure("AI review failed", lastErr), recommendedNextAction: "fix_agent_commit"}
	case "open-pilot.ci-checks":
		if status == "timeout" || status == "pending" {
			return openPilotFailureDiagnostics{failingGate: "ci_checks", operatorSummary: summarizeOpenPilotFailure("CI checks timed out", lastErr), recommendedNextAction: "wait_ci"}
		}
		return openPilotFailureDiagnostics{failingGate: "ci_checks", operatorSummary: summarizeOpenPilotFailure("CI checks failed", lastErr), recommendedNextAction: "fix_agent_commit"}
	case "open-pilot.merge-pr":
		return openPilotFailureDiagnostics{failingGate: "merge", operatorSummary: summarizeOpenPilotFailure("Merge failed", lastErr), recommendedNextAction: giteaFailureAction(lastErr)}
	case "open-pilot.close-issue":
		return openPilotFailureDiagnostics{failingGate: "issue_close", operatorSummary: summarizeOpenPilotFailure("Issue close failed", lastErr), recommendedNextAction: giteaFailureAction(lastErr)}
	default:
		label := openPilotFailureLabel(stepName)
		if status == "timeout" || status == "pending" {
			return openPilotFailureDiagnostics{failingGate: status, operatorSummary: summarizeOpenPilotFailure(label+" failed", lastErr), recommendedNextAction: "wait_ci"}
		}
		return openPilotFailureDiagnostics{failingGate: strings.TrimPrefix(stepName, "open-pilot."), operatorSummary: summarizeOpenPilotFailure(label+" failed", lastErr), recommendedNextAction: "manual_review"}
	}
}

func openPilotFailureLabel(stepName string) string {
	label := strings.TrimPrefix(stepName, "open-pilot.")
	label = strings.ReplaceAll(label, "-", " ")
	label = strings.ReplaceAll(label, "_", " ")
	if label == "" {
		return "Open Pilot step"
	}
	return strings.ToUpper(label[:1]) + label[1:]
}

func openPilotResumeClaimPhase(current OpenPilotPhase, inputs map[string]string) OpenPilotPhase {
	switch inputs["retry_from_step"] {
	case "open-pilot.validate-issue":
		return OpenPilotPhaseRequested
	case "open-pilot.branch-plan":
		return OpenPilotPhaseValidated
	case "open-pilot.prepare-prompt", "open-pilot.agent-work":
		return OpenPilotPhaseBranchPrepared
	case "open-pilot.verify-command":
		return OpenPilotPhaseAgentCompleted
	case "open-pilot.push-branch":
		if current == OpenPilotPhaseAgentCompleted {
			return OpenPilotPhaseAgentCompleted
		}
		return OpenPilotPhaseVerified
	case "open-pilot.pr-plan":
		if current == OpenPilotPhaseAgentCompleted || current == OpenPilotPhaseVerified || current == OpenPilotPhasePushed {
			return current
		}
		return OpenPilotPhasePushed
	case "open-pilot.ai-review":
		return OpenPilotPhasePRCreated
	case "open-pilot.ci-checks":
		return OpenPilotPhaseAIReviewed
	case "open-pilot.merge-pr":
		return OpenPilotPhaseCIPassed
	case "open-pilot.close-issue":
		return OpenPilotPhaseMerged
	default:
		return OpenPilotPhaseRequested
	}
}

func gateStatusForOpenPilotStep(stepName string, gateStatus map[string]string) string {
	switch stepName {
	case "open-pilot.validate-issue":
		return gateStatus["issue_validation"]
	case "open-pilot.branch-plan":
		return gateStatus["branch_preparation"]
	case "open-pilot.prepare-prompt":
		return gateStatus["prompt_preparation"]
	case "open-pilot.agent-work":
		return gateStatus["agent_work"]
	case "open-pilot.verify-command":
		return gateStatus["local_verify"]
	case "open-pilot.push-branch":
		return gateStatus["git_push"]
	case "open-pilot.pr-plan":
		return gateStatus["pull_request"]
	case "open-pilot.ai-review":
		return gateStatus["ai_review"]
	case "open-pilot.ci-checks":
		return gateStatus["ci_checks"]
	case "open-pilot.merge-pr":
		return gateStatus["merge"]
	case "open-pilot.close-issue":
		return gateStatus["issue_close"]
	default:
		return ""
	}
}

func summarizeOpenPilotFailure(prefix, lastErr string) string {
	lastErr = strings.TrimSpace(lastErr)
	if lastErr == "" {
		return prefix
	}
	return prefix + ": " + lastErr
}

func giteaFailureAction(lastErr string) string {
	lower := strings.ToLower(lastErr)
	if strings.Contains(lower, "401") || strings.Contains(lower, "403") || strings.Contains(lower, "unauthor") || strings.Contains(lower, "forbidden") || strings.Contains(lower, "token") || strings.Contains(lower, "auth") {
		return "check_gitea_token"
	}
	return "manual_review"
}

func normalizeOpenPilotRepairCounts(state *OpenPilotIssueState) error {
	if state.MaxRepairAttempts == 0 {
		state.MaxRepairAttempts = openPilotDefaultMaxRepairAttempts
	}
	if state.RepairAttempts < 0 || state.MaxRepairAttempts < 0 || state.RepairAttempts > state.MaxRepairAttempts {
		return ErrInvalid
	}
	return nil
}

func validateOpenPilotIssueStateForUpsert(input OpenPilotIssueState, existing *OpenPilotIssueState) (OpenPilotIssueState, error) {
	if input.Repo == "" || input.Issue == "" || input.Phase == "" {
		return OpenPilotIssueState{}, ErrInvalid
	}
	if !isOpenPilotKnownPhase(input.Phase) {
		return OpenPilotIssueState{}, ErrInvalid
	}
	state := cloneOpenPilotIssueState(input)
	if err := normalizeOpenPilotRepairCounts(&state); err != nil {
		return OpenPilotIssueState{}, err
	}
	if existing == nil {
		if state.Phase != OpenPilotPhaseRequested && state.Phase != OpenPilotPhaseValidated {
			return OpenPilotIssueState{}, ErrInvalid
		}
		return state, nil
	}
	if existing.Phase != "" && !isOpenPilotKnownPhase(existing.Phase) {
		return OpenPilotIssueState{}, ErrInvalid
	}
	if existing.Phase != state.Phase {
		if err := ValidateOpenPilotTransition(existing.Phase, state.Phase); err != nil {
			return OpenPilotIssueState{}, err
		}
	}
	state.ID = existing.ID
	state.CreatedAt = existing.CreatedAt
	return state, nil
}
