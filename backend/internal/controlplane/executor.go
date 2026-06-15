package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

type WorkflowExecution struct {
	Summary   string
	Events    []WorkflowExecutionEvent
	Artifacts []CreateArtifactInput
}

type WorkflowExecutionEvent struct {
	Type    string
	Message string
}

type WorkflowExecutor interface {
	Execute(ctx context.Context, run Run) (WorkflowExecution, error)
}

type StepwiseWorkflowExecutor interface {
	WorkflowExecutor
	ExecuteSteps(ctx context.Context, run Run, store Store) (WorkflowExecution, error)
}

type WorkflowExecutorFunc func(context.Context, Run) (WorkflowExecution, error)

func (f WorkflowExecutorFunc) Execute(ctx context.Context, run Run) (WorkflowExecution, error) {
	return f(ctx, run)
}

type ExecutorRegistry interface {
	ExecutorFor(workflowID string) WorkflowExecutor
}

type MapExecutorRegistry map[string]WorkflowExecutor

func (r MapExecutorRegistry) ExecutorFor(workflowID string) WorkflowExecutor {
	return r[workflowID]
}

type EnvWorkflowExecutors struct {
	store                     Store
	changemakerBaseURL        string
	changemakerToken          string
	openPilotCommand          string
	ocqBaseURL                string
	ocqGatewayKey             string
	llamaLineBaseURL          string
	llamaLineAPIKey           string
	devlogModel               string
	openPilotModel            string
	devlogAllowedModels       map[string]bool
	openPilotAllowedModels    map[string]bool
	openPilotLabels           []string
	openPilotCICheckMode      string
	openPilotMergeMode        string
	openPilotCloseIssueMode   string
	openPilotCIRequiredChecks []string
	openPilotCICheckTimeout   time.Duration
	openPilotCICheckInterval  time.Duration
	requireIssueSpec          bool
	openPilotWriteMode        string
	openPilotGitPushMode      string
	openPilotBaseBranch       string
	openPilotBranchPrefix     string
	openPilotPRDraft          bool
	modelExecutor             ModelBroker
	openCodeExecutor          OpenCodeExecutor
	issueFetcher              IssueFetcher
	giteaReader               GiteaPullRequestReader
	giteaMerger               GiteaPullRequestMerger
	giteaWriter               GiteaWriter
	commandRunner             CommandRunner
	worktreeManager           OpenPilotWorktreeManager
	client                    *http.Client
}

func NewEnvWorkflowExecutors(store ...Store) *EnvWorkflowExecutors {
	var configuredStore Store
	if len(store) > 0 {
		configuredStore = store[0]
	}
	autonomyFull := openPilotAutonomyFullEnabled()
	openPilotWriteMode := firstNonEmpty(os.Getenv("OPEN_PILOT_GITEA_WRITE_MODE"), "disabled")
	openPilotGitPushMode := firstNonEmpty(os.Getenv("OPEN_PILOT_GIT_PUSH_MODE"), "disabled")
	if autonomyFull {
		openPilotWriteMode = firstNonEmpty(os.Getenv("OPEN_PILOT_GITEA_WRITE_MODE"), "allow")
		openPilotGitPushMode = firstNonEmpty(os.Getenv("OPEN_PILOT_GIT_PUSH_MODE"), "allow")
	}
	ciCheckMode := firstNonEmpty(os.Getenv("OPEN_PILOT_CI_CHECK_MODE"), "disabled")
	if autonomyFull && strings.TrimSpace(os.Getenv("OPEN_PILOT_CI_CHECK_MODE")) == "" {
		ciCheckMode = "require-pass"
	}
	mergeMode := firstNonEmpty(os.Getenv("OPEN_PILOT_MERGE_MODE"), "disabled")
	if autonomyFull && strings.TrimSpace(os.Getenv("OPEN_PILOT_MERGE_MODE")) == "" {
		mergeMode = "allow"
	}
	closeIssueMode := firstNonEmpty(os.Getenv("OPEN_PILOT_CLOSE_ISSUE_MODE"), "disabled")
	if autonomyFull && strings.TrimSpace(os.Getenv("OPEN_PILOT_CLOSE_ISSUE_MODE")) == "" {
		closeIssueMode = "allow"
	}
	requiredChecksRaw := strings.TrimSpace(os.Getenv("OPEN_PILOT_REQUIRED_CHECKS"))
	requiredChecks := splitCSV(requiredChecksRaw)
	if len(requiredChecks) == 0 && ciCheckMode != "disabled" {
		requiredChecks = splitCSV("verify,lint,test")
	}
	ciCheckTimeout := 10 * time.Minute
	if raw := strings.TrimSpace(os.Getenv("OPEN_PILOT_CI_CHECK_TIMEOUT")); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil {
			ciCheckTimeout = parsed
		}
	}
	ciCheckInterval := 15 * time.Second
	if raw := strings.TrimSpace(os.Getenv("OPEN_PILOT_CI_CHECK_INTERVAL")); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil {
			ciCheckInterval = parsed
		}
	}
	giteaClient := NewEnvGiteaClient()
	return &EnvWorkflowExecutors{
		store:                     configuredStore,
		changemakerBaseURL:        strings.TrimRight(os.Getenv("CHANGEMAKER_BASE_URL"), "/"),
		changemakerToken:          os.Getenv("CHANGEMAKER_ADMIN_TOKEN"),
		openPilotCommand:          strings.TrimSpace(os.Getenv("OPEN_PILOT_COMMAND")),
		ocqBaseURL:                strings.TrimRight(os.Getenv("OCQ_BASE_URL"), "/"),
		ocqGatewayKey:             os.Getenv("OCQ_GATEWAY_KEY"),
		llamaLineBaseURL:          strings.TrimRight(os.Getenv("LLAMA_LINE_BASE_URL"), "/"),
		llamaLineAPIKey:           os.Getenv("LLAMA_LINE_API_KEY"),
		devlogModel:               firstNonEmpty(os.Getenv("DEVLOG_MODEL"), "gpt-5.4"),
		openPilotModel:            firstNonEmpty(os.Getenv("OPEN_PILOT_MODEL"), "gpt-5.4"),
		devlogAllowedModels:       setFromCSV(firstNonEmpty(os.Getenv("DEVLOG_ALLOWED_MODELS"), firstNonEmpty(os.Getenv("DEVLOG_MODEL"), "gpt-5.4"))),
		openPilotAllowedModels:    setFromCSV(firstNonEmpty(os.Getenv("OPEN_PILOT_ALLOWED_MODELS"), firstNonEmpty(os.Getenv("OPEN_PILOT_MODEL"), "gpt-5.4"))),
		openPilotLabels:           splitCSV(firstNonEmpty(os.Getenv("OPEN_PILOT_REQUIRED_LABELS"), "ready-for-agent,agent:queued")),
		openPilotCICheckMode:      ciCheckMode,
		openPilotMergeMode:        mergeMode,
		openPilotCloseIssueMode:   closeIssueMode,
		openPilotCIRequiredChecks: requiredChecks,
		openPilotCICheckTimeout:   ciCheckTimeout,
		openPilotCICheckInterval:  ciCheckInterval,
		requireIssueSpec:          strings.EqualFold(os.Getenv("OPEN_PILOT_REQUIRE_ISSUE_SPEC"), "true"),
		openPilotWriteMode:        openPilotWriteMode,
		openPilotGitPushMode:      openPilotGitPushMode,
		openPilotBaseBranch:       firstNonEmpty(os.Getenv("OPEN_PILOT_BASE_BRANCH"), "main"),
		openPilotBranchPrefix:     firstNonEmpty(os.Getenv("OPEN_PILOT_BRANCH_PREFIX"), "open-pilot/"),
		openPilotPRDraft:          !strings.EqualFold(os.Getenv("OPEN_PILOT_PR_DRAFT"), "false"),
		modelExecutor:             NewEnvModelBroker(),
		openCodeExecutor:          NewEnvOpenCodeExecutor(),
		issueFetcher:              giteaClient,
		giteaReader:               giteaClient,
		giteaMerger:               giteaClient,
		giteaWriter:               giteaClient,
		commandRunner:             NewEnvCommandRunner(),
		worktreeManager:           NewEnvOpenPilotWorktreeManager(),
		client:                    &http.Client{Timeout: 10 * time.Second},
	}
}

func (e *EnvWorkflowExecutors) ExecutorFor(workflowID string) WorkflowExecutor {
	switch workflowID {
	case string(WorkflowChangemaker):
		return changemakerExecutor{env: e}
	case string(WorkflowOpenPilot):
		return openPilotExecutor{env: e}
	case string(WorkflowDevlog):
		return devlogExecutor{env: e}
	default:
		return WorkflowExecutorFunc(stubWorkflowExecution(workflowID, "Workflow execution adapter is not wired yet"))
	}
}

type changemakerExecutor struct {
	env *EnvWorkflowExecutors
}

func (c changemakerExecutor) Execute(ctx context.Context, run Run) (WorkflowExecution, error) {
	return c.env.executeChangemaker(ctx, run)
}

func (c changemakerExecutor) ExecutePipelineStep(ctx context.Context, run Run, step PipelineStep, state map[string]any) (PipelineStepExecution, error) {
	if step.ID != "dispatch" {
		return PipelineStepExecution{}, fmt.Errorf("unsupported changemaker pipeline step %q", step.ID)
	}
	execution, err := c.env.executeChangemaker(ctx, run)
	if execution.Summary != "" {
		state["summary"] = execution.Summary
	}
	return PipelineStepExecution{Summary: execution.Summary, Events: execution.Events, Artifacts: execution.Artifacts}, err
}

type openPilotExecutor struct {
	env *EnvWorkflowExecutors
}

func (o openPilotExecutor) Execute(ctx context.Context, run Run) (WorkflowExecution, error) {
	return o.env.executeOpenPilot(ctx, run)
}

func (o openPilotExecutor) ExecuteSteps(ctx context.Context, run Run, store Store) (WorkflowExecution, error) {
	repo := run.Inputs["repo"]
	issue := run.Inputs["issue"]
	validate, err := createAndStartStep(store, CreateRunStepInput{RunID: run.ID, Position: 1, Name: "open-pilot.validate-issue", Executor: "internal", Inputs: run.Inputs})
	if err != nil {
		return WorkflowExecution{}, err
	}
	if repo == "" || issue == "" {
		message := "open-pilot requires run inputs repo and issue"
		_, _ = store.FinishRunStep(validate.ID, StepFailed, "", message, nil)
		return WorkflowExecution{}, errors.New(message)
	}
	issueData, issueSummary, fetchedIssue, fetchErr := o.env.fetchOpenPilotIssue(ctx, repo, issue)
	if fetchErr != nil {
		_, _ = store.AddEvent(run.ID, "open-pilot.issue.fetch.skipped", fetchErr.Error())
	}
	if fetchedIssue != nil {
		if err := o.env.validateOpenPilotIssue(*fetchedIssue); err != nil {
			_, _ = store.FinishRunStep(validate.ID, StepFailed, "", err.Error(), nil)
			return WorkflowExecution{}, err
		}
	}
	if issueData != nil {
		if _, err := store.AddArtifact(CreateArtifactInput{RunID: run.ID, Name: "open-pilot-issue.json", Kind: "json", URI: "data:application/json;charset=utf-8," + url.QueryEscape(string(issueData))}); err != nil {
			_, _ = store.FinishRunStep(validate.ID, StepFailed, "", err.Error(), nil)
			return WorkflowExecution{}, err
		}
	}
	validateSummary := "Validated Open Pilot run inputs"
	if issueSummary != "" {
		validateSummary = issueSummary
	}
	if _, err := store.FinishRunStep(validate.ID, StepSucceeded, validateSummary, "", nil); err != nil {
		return WorkflowExecution{}, err
	}
	branchName := o.env.openPilotBranchName(repo, issue, fetchedIssue)
	baseBranch := firstNonEmpty(run.Inputs["base_branch"], o.env.openPilotBaseBranch)
	branchPlan, prepareWorktreeErr := o.env.worktreeManager.Prepare(ctx, run, repo, baseBranch, branchName)
	branch, err := createAndStartStep(store, CreateRunStepInput{RunID: run.ID, Position: 2, Name: "open-pilot.branch-plan", Executor: "internal", Inputs: branchPlan.StepInputs()})
	if err != nil {
		return WorkflowExecution{}, err
	}
	branchBytes, _ := json.MarshalIndent(branchPlan, "", "  ")
	if _, err := store.AddArtifact(CreateArtifactInput{RunID: run.ID, Name: "open-pilot-branch-plan.json", Kind: "json", URI: "data:application/json;charset=utf-8," + url.QueryEscape(string(branchBytes))}); err != nil {
		_, _ = store.FinishRunStep(branch.ID, StepFailed, "", err.Error(), nil)
		return WorkflowExecution{}, err
	}
	if prepareWorktreeErr != nil {
		_, _ = store.FinishRunStep(branch.ID, StepFailed, "", prepareWorktreeErr.Error(), nil)
		return WorkflowExecution{}, prepareWorktreeErr
	}
	branchSummary := "Planned branch " + branchName
	if branchPlan.Executed {
		branchSummary = "Created worktree " + branchPlan.WorktreePath
	} else if branchPlan.DryRun {
		branchSummary = "Worktree dry-run; planned branch " + branchName
	}
	branchCorrelation := map[string]string{"branch": branchName}
	if branchPlan.Executed && branchPlan.WorktreePath != "" {
		branchCorrelation["worktree_path"] = branchPlan.WorktreePath
	} else if branchPlan.PlannedWorktreePath != "" {
		branchCorrelation["planned_worktree_path"] = branchPlan.PlannedWorktreePath
	}
	if _, err := store.FinishRunStep(branch.ID, StepSucceeded, branchSummary, "", branchCorrelation); err != nil {
		return WorkflowExecution{}, err
	}

	prepare, err := createAndStartStep(store, CreateRunStepInput{RunID: run.ID, Position: 3, Name: "open-pilot.prepare-prompt", Executor: "internal"})
	if err != nil {
		return WorkflowExecution{}, err
	}
	prompt := buildOpenPilotPrompt(repo, issue, issueSummary, fetchedIssue, run.Inputs, branchPlan)
	if _, err := store.AddArtifact(CreateArtifactInput{RunID: run.ID, Name: "open-pilot-prompt.md", Kind: "markdown", URI: "data:text/markdown;charset=utf-8," + url.QueryEscape(prompt)}); err != nil {
		_, _ = store.FinishRunStep(prepare.ID, StepFailed, "", err.Error(), nil)
		return WorkflowExecution{}, err
	}
	if _, err := store.FinishRunStep(prepare.ID, StepSucceeded, "Prepared Open Pilot implementation prompt", "", nil); err != nil {
		return WorkflowExecution{}, err
	}

	model, err := o.env.resolveWorkflowModel(string(WorkflowOpenPilot), run.Inputs)
	if err != nil {
		_, _ = store.FinishRunStep(prepare.ID, StepFailed, "", err.Error(), nil)
		return WorkflowExecution{}, err
	}
	agent, err := createAndStartStep(store, CreateRunStepInput{RunID: run.ID, Position: 4, Name: "open-pilot.agent-work", Executor: "opencode", Model: model})
	if err != nil {
		return WorkflowExecution{}, err
	}
	response, err := o.env.openCodeExecutor.SendPrompt(ctx, OpenCodeRequest{Model: model, Prompt: prompt, SessionID: openCodeSessionID(run.Inputs), RequestID: openPilotOpenCodeRequestID(run, "open-pilot.agent-work")})
	if err != nil {
		fallbackExecution := WorkflowExecution{Summary: "Prepared Open Pilot prompt for issue " + issue, Events: []WorkflowExecutionEvent{{Type: "open-pilot.prepared", Message: "Prompt artifact ready for " + repo + "#" + issue}}}
		_, _ = store.FinishRunStep(agent.ID, StepSucceeded, "OpenCode executor unavailable; prompt prepared for operator handoff", "", nil)
		_, _ = store.AddEvent(run.ID, "open-pilot.agent.fallback", err.Error())
		if persistErr := o.persistOpenPilotIssueState(run, OpenPilotPhasePaused, "open-pilot.agent-work", err.Error(), map[string]any{"open-pilot.prompt": prompt, "open-pilot.branch-plan": branchPlan, "open-pilot.issue-summary": issueSummary}); persistErr != nil {
			return fallbackExecution, persistErr
		}
		return fallbackExecution, nil
	}
	if _, err := store.AddArtifact(CreateArtifactInput{RunID: run.ID, Name: "open-pilot-response.md", Kind: "markdown", URI: "data:text/markdown;charset=utf-8," + url.QueryEscape(response.Content)}); err != nil {
		_, _ = store.FinishRunStep(agent.ID, StepFailed, "", err.Error(), response.CorrelationIDs)
		return WorkflowExecution{}, err
	}
	if _, err := store.FinishRunStep(agent.ID, StepSucceeded, "OCQ completed Open Pilot prompt", "", response.CorrelationIDs); err != nil {
		return WorkflowExecution{}, err
	}
	if command := strings.TrimSpace(run.Inputs["test_command"]); command != "" {
		verify, err := createAndStartStep(store, CreateRunStepInput{RunID: run.ID, Position: 5, Name: "open-pilot.verify-command", Executor: "command", Inputs: map[string]string{"command": command}})
		if err != nil {
			return WorkflowExecution{}, err
		}
		workdir := ""
		if branchPlan.Executed {
			workdir = branchPlan.WorktreePath
		}
		result, err := o.env.commandRunner.Run(ctx, command, workdir)
		correlation := map[string]string{"policy_reason": result.Decision.Reason}
		if workdir != "" {
			correlation["worktree_path"] = branchPlan.WorktreePath
		}
		if result.Decision.DryRun {
			correlation["policy_mode"] = string(CommandPolicyDryRun)
		}
		if err != nil {
			_, _ = store.FinishRunStep(verify.ID, StepFailed, result.Output, err.Error(), correlation)
			return WorkflowExecution{}, err
		}
		summary := "Command allowed"
		if result.Decision.DryRun {
			summary = "Command allowed by policy in dry-run mode"
		} else if result.Executed {
			summary = "Command executed by policy runner"
		}
		if _, err := store.FinishRunStep(verify.ID, StepSucceeded, summary, "", correlation); err != nil {
			return WorkflowExecution{}, err
		}
	}
	pushPlan := o.env.openPilotPushPlan(branchPlan)
	pushStep, err := createAndStartStep(store, CreateRunStepInput{RunID: run.ID, Position: 6, Name: "open-pilot.push-branch", Executor: "git", Inputs: pushPlan.StepInputs()})
	if err != nil {
		return WorkflowExecution{}, err
	}
	pushPlanBytes, _ := json.MarshalIndent(pushPlan, "", "  ")
	if _, err := store.AddArtifact(CreateArtifactInput{RunID: run.ID, Name: "open-pilot-push-plan.json", Kind: "json", URI: "data:application/json;charset=utf-8," + url.QueryEscape(string(pushPlanBytes))}); err != nil {
		_, _ = store.FinishRunStep(pushStep.ID, StepFailed, "", err.Error(), nil)
		return WorkflowExecution{}, err
	}
	pushCorrelation := map[string]string{"mode": pushPlan.Mode}
	if pushPlan.Executed {
		pushCorrelation["branch"] = pushPlan.Branch
		pushCorrelation["remote"] = pushPlan.Remote
	}
	if err := o.env.executeOpenPilotPush(ctx, pushPlan); err != nil {
		_, _ = store.FinishRunStep(pushStep.ID, StepFailed, "", err.Error(), pushCorrelation)
		return WorkflowExecution{}, err
	}
	pushSummary := "Git push disabled; plan artifact attached"
	if pushPlan.DryRun {
		pushSummary = "Git push dry-run; plan artifact attached"
	} else if pushPlan.Executed {
		pushSummary = "Pushed branch " + pushPlan.Branch
	}
	if _, err := store.FinishRunStep(pushStep.ID, StepSucceeded, pushSummary, "", pushCorrelation); err != nil {
		return WorkflowExecution{}, err
	}

	prInput := o.env.openPilotPullRequestInput(repo, issue, branchName, issueSummary, response.Content, run.Inputs)
	prStep, err := createAndStartStep(store, CreateRunStepInput{RunID: run.ID, Position: 7, Name: "open-pilot.pr-plan", Executor: "gitea", Inputs: map[string]string{"mode": o.env.openPilotWriteMode, "head": prInput.Head, "base": prInput.Base}})
	if err != nil {
		return WorkflowExecution{}, err
	}
	prPlanBytes, _ := json.MarshalIndent(prInput, "", "  ")
	if _, err := store.AddArtifact(CreateArtifactInput{RunID: run.ID, Name: "open-pilot-pr-plan.json", Kind: "json", URI: "data:application/json;charset=utf-8," + url.QueryEscape(string(prPlanBytes))}); err != nil {
		_, _ = store.FinishRunStep(prStep.ID, StepFailed, "", err.Error(), nil)
		return WorkflowExecution{}, err
	}
	switch o.env.openPilotWriteMode {
	case "allow":
		created, err := o.env.giteaWriter.CreatePullRequest(ctx, prInput)
		if err != nil {
			_, _ = store.FinishRunStep(prStep.ID, StepFailed, "", err.Error(), nil)
			return WorkflowExecution{}, err
		}
		createdBytes, _ := json.MarshalIndent(created, "", "  ")
		_, _ = store.AddArtifact(CreateArtifactInput{RunID: run.ID, Name: "open-pilot-pr.json", Kind: "json", URI: "data:application/json;charset=utf-8," + url.QueryEscape(string(createdBytes))})
		if _, err := store.FinishRunStep(prStep.ID, StepSucceeded, "Created pull request", "", map[string]string{"pull_request": created.HTMLURL}); err != nil {
			return WorkflowExecution{}, err
		}
	case "dry-run":
		if _, err := store.FinishRunStep(prStep.ID, StepSucceeded, "PR creation dry-run; plan artifact attached", "", nil); err != nil {
			return WorkflowExecution{}, err
		}
	default:
		if _, err := store.FinishRunStep(prStep.ID, StepSucceeded, "PR creation disabled; plan artifact attached", "", nil); err != nil {
			return WorkflowExecution{}, err
		}
	}
	return WorkflowExecution{Summary: "Ran Open Pilot issue " + issue + " through OCQ", Events: []WorkflowExecutionEvent{{Type: "open-pilot.agent.completed", Message: repo + "#" + issue}}}, nil
}

func (o openPilotExecutor) ResolvePipelineStepModel(workflowID string, step PipelineStep, inputs map[string]string) (string, error) {
	if step.Model == "" {
		return "", nil
	}
	if step.Model == "${model}" {
		return o.env.resolveWorkflowModel(workflowID, inputs)
	}
	return step.Model, nil
}

func (o openPilotExecutor) ShouldExecutePipelineStep(run Run, step PipelineStep, state map[string]any) bool {
	switch step.ID {
	case "verify-command":
		return strings.TrimSpace(run.Inputs["test_command"]) != ""
	case "push-branch", "pr-plan":
		completed, _ := state["open-pilot.agent-completed"].(bool)
		return completed
	case "ai-review":
		return o.env != nil && o.env.modelBrokerConfigured() && openPilotHasKnownPullRequest(state, run.Inputs)
	case "ci-checks":
		gateStatus := openPilotGateStatusFromState(state)
		if gateStatus["ai_review"] != "approved" {
			return false
		}
		if pr, ok := state["open-pilot.pull-request"].(GiteaPullRequest); ok && pr.Head.SHA != "" {
			return true
		}
		if openPilotPullRequestHeadCandidate(state, run.Inputs) != "" {
			return true
		}
		return false
	case "merge-pr":
		return openPilotMergeStepReady(run, state)
	case "close-issue":
		return openPilotCloseIssueStepReady(run, state)
	default:
		return true
	}
}

func (e *EnvWorkflowExecutors) modelBrokerConfigured() bool {
	if e == nil || e.modelExecutor == nil {
		return false
	}
	if configured, ok := e.modelExecutor.(ModelBrokerConfiguration); ok {
		return configured.Configured()
	}
	return strings.TrimSpace(e.llamaLineBaseURL) != ""
}

func openPilotGateStatusFromState(state map[string]any) map[string]string {
	switch gateStatus := state["open-pilot.gate-status"].(type) {
	case map[string]string:
		return gateStatus
	case OpenPilotGateStatus:
		return map[string]string(gateStatus)
	default:
		return nil
	}
}

func openPilotHasKnownPullRequest(state map[string]any, inputs map[string]string) bool {
	if pr, ok := state["open-pilot.pull-request"].(GiteaPullRequest); ok && openPilotPullRequestKnown(pr) {
		return true
	}
	return firstNonEmpty(inputs["resume.open-pilot.pr-plan.pull_request_url"], inputs["pull_request_url"], inputs["resume.open-pilot.pr-plan.pull_request_id"], inputs["resume.open-pilot.pr-plan.pull_request_number"]) != ""
}

func openPilotPullRequestHeadCandidate(state map[string]any, inputs map[string]string) string {
	if pr, ok := state["open-pilot.pull-request"].(GiteaPullRequest); ok {
		if head := firstNonEmpty(pr.Head.Ref, pr.Head.Label); head != "" {
			return head
		}
	}
	if branchName, ok := state["open-pilot.branch-name"].(string); ok && branchName != "" {
		return branchName
	}
	if branchPlan, ok := state["open-pilot.branch-plan"].(OpenPilotWorktreePlan); ok && branchPlan.Branch != "" {
		return branchPlan.Branch
	}
	return firstNonEmpty(inputs["branch"], inputs["resume.open-pilot.branch-plan.branch"], inputs["open-pilot.branch-name"])
}

func (o openPilotExecutor) HydratePipelineResumeState(run Run, _ Pipeline, state map[string]any) {
	if o.env == nil || o.env.store == nil {
		return
	}
	repo := run.Inputs["repo"]
	issue := run.Inputs["issue"]
	if repo == "" || issue == "" {
		return
	}
	saved, err := o.env.store.OpenPilotIssueState(repo, issue)
	if err != nil {
		o.hydrateOpenPilotPipelineStateFromResumeInputs(run, state)
		return
	}
	if saved.Phase != OpenPilotPhaseFailed && saved.Phase != OpenPilotPhasePaused && saved.Phase != OpenPilotPhaseMerged && saved.Phase != OpenPilotPhaseIssueClosed {
		o.hydrateOpenPilotPipelineStateFromResumeInputs(run, state)
		return
	}
	o.hydrateOpenPilotPipelineStateFromIssueState(run, saved, state)
}

func (o openPilotExecutor) hydrateOpenPilotPipelineStateFromIssueState(run Run, issueState OpenPilotIssueState, state map[string]any) {
	mergedInputs := cloneMap(run.Inputs)
	for k, v := range issueState.ResumeInputs {
		mergedInputs[k] = v
	}
	if issueState.Repo != "" {
		mergedInputs["repo"] = issueState.Repo
	}
	if issueState.Issue != "" {
		mergedInputs["issue"] = issueState.Issue
	}
	if issueState.Base != "" {
		mergedInputs["resume.open-pilot.branch-plan.base_branch"] = issueState.Base
	}
	if issueState.Branch != "" {
		mergedInputs["resume.open-pilot.branch-plan.branch"] = issueState.Branch
		mergedInputs["resume.open-pilot.branch-plan.correlation.branch"] = issueState.Branch
	}
	if issueState.WorktreePath != "" {
		mergedInputs["resume.open-pilot.branch-plan.worktree_path"] = issueState.WorktreePath
		mergedInputs["resume.open-pilot.branch-plan.correlation.worktree_path"] = issueState.WorktreePath
		mergedInputs["resume.open-pilot.branch-plan.executed"] = "true"
	}
	if issueState.PullRequestURL != "" {
		mergedInputs["resume.open-pilot.pr-plan.pull_request_url"] = issueState.PullRequestURL
	}
	if issueState.PullRequestID != 0 {
		mergedInputs["resume.open-pilot.pr-plan.pull_request_id"] = fmt.Sprintf("%d", issueState.PullRequestID)
	}
	if issueState.PullRequestNum != 0 {
		mergedInputs["resume.open-pilot.pr-plan.pull_request_number"] = fmt.Sprintf("%d", issueState.PullRequestNum)
	}
	if issueState.ResumeInputs != nil {
		if prompt := issueState.ResumeInputs["resume.open-pilot.prompt"]; prompt != "" {
			state["open-pilot.prompt"] = prompt
		}
		if summary := issueState.ResumeInputs["resume.open-pilot.validate-issue.summary"]; summary != "" {
			state["open-pilot.issue-summary"] = summary
		}
		if response := issueState.ResumeInputs["resume.open-pilot.agent-work.response"]; response != "" {
			state["open-pilot.response"] = response
			state["open-pilot.agent-completed"] = true
		}
		if corr := mergeResumeCorrelation(issueState.ResumeInputs, "resume.open-pilot.agent-work.correlation."); len(corr) > 0 {
			state["open-pilot.agent-correlation"] = corr
		}
		if corr := mergeResumeCorrelation(issueState.ResumeInputs, "resume.open-pilot.push-branch.correlation."); len(corr) > 0 {
			state["open-pilot.push-correlation"] = corr
		}
	}
	branchPlan := openPilotWorktreePlanFromResume(mergedInputs)
	if branchPlan.Branch != "" {
		state["open-pilot.branch-name"] = branchPlan.Branch
		state["open-pilot.branch-plan"] = branchPlan
		if _, ok := state["open-pilot.issue-summary"].(string); !ok {
			state["open-pilot.issue-summary"] = "Retried Open Pilot issue " + issueState.Issue
		}
		if _, ok := state["open-pilot.prompt"].(string); !ok {
			issueSummary, _ := state["open-pilot.issue-summary"].(string)
			state["open-pilot.prompt"] = buildOpenPilotPrompt(issueState.Repo, issueState.Issue, issueSummary, nil, mergedInputs, branchPlan)
		}
	}
	pushPlan := openPilotPushPlanFromResume(mergedInputs)
	if pushPlan.Branch != "" {
		state["open-pilot.push-plan"] = pushPlan
	}
	if issueState.PullRequestURL != "" || issueState.PullRequestID != 0 || issueState.PullRequestNum != 0 {
		state["open-pilot.pull-request"] = GiteaPullRequest{ID: issueState.PullRequestID, Number: issueState.PullRequestNum, HTMLURL: issueState.PullRequestURL}
	}
	if issueState.MergeCommitSHA != "" {
		state["open-pilot.merge-commit-sha"] = issueState.MergeCommitSHA
	}
	if issueState.IssueClosed {
		state["open-pilot.issue-closed"] = true
	}
	if len(issueState.GateStatus) > 0 {
		state["open-pilot.gate-status"] = cloneOpenPilotGateStatus(issueState.GateStatus)
	}
}

func (o openPilotExecutor) hydrateOpenPilotPipelineStateFromResumeInputs(run Run, state map[string]any) {
	issueSummary := run.Inputs["resume.open-pilot.validate-issue.summary"]
	if issueSummary != "" {
		state["open-pilot.issue-summary"] = issueSummary
	}
	if prompt := run.Inputs["resume.open-pilot.prompt"]; prompt != "" {
		state["open-pilot.prompt"] = prompt
	}
	branchPlan := openPilotWorktreePlanFromResume(run.Inputs)
	if branchPlan.Branch != "" {
		state["open-pilot.branch-name"] = branchPlan.Branch
		state["open-pilot.branch-plan"] = branchPlan
		if issueSummary == "" {
			issueSummary = "Retried Open Pilot issue " + run.Inputs["issue"]
		}
		if _, ok := state["open-pilot.prompt"].(string); !ok {
			state["open-pilot.prompt"] = buildOpenPilotPrompt(run.Inputs["repo"], run.Inputs["issue"], issueSummary, nil, run.Inputs, branchPlan)
		}
	}
	if summary := run.Inputs["resume.open-pilot.agent-work.summary"]; summary != "" {
		state["open-pilot.agent-completed"] = true
		state["open-pilot.response"] = summary
	}
	if response := run.Inputs["resume.open-pilot.agent-work.response"]; response != "" {
		state["open-pilot.agent-completed"] = true
		state["open-pilot.response"] = response
	}
	if pushPlan := openPilotPushPlanFromResume(run.Inputs); pushPlan.Branch != "" {
		state["open-pilot.push-plan"] = pushPlan
	}
	if prURL := run.Inputs["resume.open-pilot.pr-plan.pull_request_url"]; prURL != "" || run.Inputs["resume.open-pilot.pr-plan.pull_request_id"] != "" || run.Inputs["resume.open-pilot.pr-plan.pull_request_number"] != "" {
		pr := GiteaPullRequest{HTMLURL: prURL}
		if id, err := strconv.Atoi(run.Inputs["resume.open-pilot.pr-plan.pull_request_id"]); err == nil {
			pr.ID = id
		}
		if num, err := strconv.Atoi(run.Inputs["resume.open-pilot.pr-plan.pull_request_number"]); err == nil {
			pr.Number = num
		}
		state["open-pilot.pull-request"] = pr
	}
	if sha := run.Inputs["resume.open-pilot.merge-pr.merge_commit_sha"]; sha != "" {
		state["open-pilot.merge-commit-sha"] = sha
	}
	if strings.EqualFold(run.Inputs["resume.open-pilot.close-issue.issue_closed"], "true") {
		state["open-pilot.issue-closed"] = true
	}
}

func mergeResumeCorrelation(inputs map[string]string, prefix string) map[string]string {
	out := map[string]string{}
	for k, v := range inputs {
		if strings.HasPrefix(k, prefix) {
			out[strings.TrimPrefix(k, prefix)] = v
		}
	}
	return out
}

func mergeOpenPilotResumeInputs(base map[string]string, overrides map[string]string) map[string]string {
	out := cloneMap(base)
	if out == nil {
		out = map[string]string{}
	}
	for k, v := range overrides {
		if v != "" {
			out[k] = v
		}
	}
	return out
}

func setOpenPilotResumeInput(inputs map[string]string, key, value string) {
	if value == "" {
		return
	}
	if inputs == nil {
		return
	}
	inputs[key] = value
}

func setOpenPilotResumeBool(inputs map[string]string, key string, value bool) {
	if !value || inputs == nil {
		return
	}
	inputs[key] = "true"
}

func openPilotPullRequestKnown(pr GiteaPullRequest) bool {
	return pr.HTMLURL != "" || pr.ID != 0 || pr.Number != 0
}

func openPilotMergeCommitSHAFromState(state map[string]any) string {
	if sha, ok := state["open-pilot.merge-commit-sha"].(string); ok {
		return sha
	}
	if mergeResult, ok := state["open-pilot.merge-result"].(GiteaMergeResult); ok {
		return mergeResult.SHA
	}
	return ""
}

func openPilotIssueClosedFromState(state map[string]any) bool {
	if closed, ok := state["open-pilot.issue-closed"].(bool); ok {
		return closed
	}
	return false
}

func openPilotMergeStepReady(run Run, state map[string]any) bool {
	gateStatus := openPilotGateStatusFromState(state)
	if gateStatus["merge"] == "merged" || openPilotMergeCommitSHAFromState(state) != "" {
		return false
	}
	if !openPilotHasKnownPullRequest(state, run.Inputs) {
		return false
	}
	if gateStatus["ai_review"] != "approved" || gateStatus["ci_checks"] != "passed" {
		return false
	}
	if strings.TrimSpace(run.Inputs["test_command"]) != "" && gateStatus["local_verify"] != "passed" {
		return false
	}
	return true
}

func openPilotCloseIssueStepReady(run Run, state map[string]any) bool {
	if openPilotIssueClosedFromState(state) {
		return false
	}
	if openPilotMergeCommitSHAFromState(state) == "" {
		gateStatus := openPilotGateStatusFromState(state)
		if gateStatus["merge"] != "merged" {
			return false
		}
	}
	return run.Inputs["repo"] != "" && run.Inputs["issue"] != ""
}

func (o openPilotExecutor) persistOpenPilotIssueState(run Run, phase OpenPilotPhase, stepName, lastErr string, state map[string]any) error {
	if o.env == nil || o.env.store == nil {
		return nil
	}
	repo := run.Inputs["repo"]
	issue := run.Inputs["issue"]
	if repo == "" || issue == "" {
		return nil
	}
	missing := false
	saved, err := o.env.store.OpenPilotIssueState(repo, issue)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		missing = true
		saved = OpenPilotIssueState{Repo: repo, Issue: issue}
	}
	if !missing && saved.Phase == OpenPilotPhasePaused && run.Inputs["retry_from_step"] == "" && phase != OpenPilotPhasePaused && phase != OpenPilotPhaseFailed {
		return errors.New("open-pilot issue is paused by operator")
	}
	saved.Phase = phase
	saved.RunID = run.ID
	saved.LastStepName = stepName
	saved.LastError = lastErr
	if saved.GateStatus == nil {
		saved.GateStatus = OpenPilotGateStatus{}
	}
	if saved.ResumeInputs == nil {
		saved.ResumeInputs = map[string]string{}
	}
	for k, v := range run.Inputs {
		switch k {
		case "retry_from_step_position", "retry_from_step":
			continue
		}
		if v != "" {
			saved.ResumeInputs[k] = v
		}
	}
	if retryStep, ok := state["retry_from_step"].(string); ok {
		setOpenPilotResumeInput(saved.ResumeInputs, "retry_from_step", retryStep)
	}
	if retryPos, ok := state["retry_from_step_position"].(string); ok {
		setOpenPilotResumeInput(saved.ResumeInputs, "retry_from_step_position", retryPos)
	}
	if issueSummary, ok := state["open-pilot.issue-summary"].(string); ok {
		setOpenPilotResumeInput(saved.ResumeInputs, "resume.open-pilot.validate-issue.summary", issueSummary)
	}
	if prompt, ok := state["open-pilot.prompt"].(string); ok {
		setOpenPilotResumeInput(saved.ResumeInputs, "resume.open-pilot.prompt", prompt)
	}
	if verifySummary, ok := state["open-pilot.verify-summary"].(string); ok {
		setOpenPilotResumeInput(saved.ResumeInputs, "resume.open-pilot.verify-command.summary", verifySummary)
	}
	if response, ok := state["open-pilot.response"].(string); ok {
		setOpenPilotResumeInput(saved.ResumeInputs, "resume.open-pilot.agent-work.response", response)
		setOpenPilotResumeInput(saved.ResumeInputs, "resume.open-pilot.agent-work.summary", response)
	}
	if agentSummary, ok := state["open-pilot.agent-summary"].(string); ok {
		setOpenPilotResumeInput(saved.ResumeInputs, "resume.open-pilot.agent-work.summary", agentSummary)
	}
	if branchPlan, ok := state["open-pilot.branch-plan"].(OpenPilotWorktreePlan); ok {
		saved.Branch = branchPlan.Branch
		saved.Base = branchPlan.BaseBranch
		saved.WorktreePath = branchPlan.WorktreePath
		setOpenPilotResumeInput(saved.ResumeInputs, "resume.open-pilot.branch-plan.repo", branchPlan.Repo)
		setOpenPilotResumeInput(saved.ResumeInputs, "resume.open-pilot.branch-plan.repo_path", branchPlan.RepoPath)
		setOpenPilotResumeInput(saved.ResumeInputs, "resume.open-pilot.branch-plan.base_branch", branchPlan.BaseBranch)
		setOpenPilotResumeInput(saved.ResumeInputs, "resume.open-pilot.branch-plan.branch", branchPlan.Branch)
		setOpenPilotResumeInput(saved.ResumeInputs, "resume.open-pilot.branch-plan.mode", branchPlan.Mode)
		setOpenPilotResumeInput(saved.ResumeInputs, "resume.open-pilot.branch-plan.planned_worktree_path", branchPlan.PlannedWorktreePath)
		setOpenPilotResumeInput(saved.ResumeInputs, "resume.open-pilot.branch-plan.worktree_path", branchPlan.WorktreePath)
		setOpenPilotResumeBool(saved.ResumeInputs, "resume.open-pilot.branch-plan.executed", branchPlan.Executed)
		setOpenPilotResumeBool(saved.ResumeInputs, "resume.open-pilot.branch-plan.dry_run", branchPlan.DryRun)
	}
	if pushPlan, ok := state["open-pilot.push-plan"].(OpenPilotPushPlan); ok {
		saved.Branch = firstNonEmpty(saved.Branch, pushPlan.Branch)
		saved.WorktreePath = firstNonEmpty(saved.WorktreePath, pushPlan.WorktreePath)
		setOpenPilotResumeInput(saved.ResumeInputs, "resume.open-pilot.push-branch.mode", pushPlan.Mode)
		setOpenPilotResumeInput(saved.ResumeInputs, "resume.open-pilot.push-branch.remote", pushPlan.Remote)
		setOpenPilotResumeInput(saved.ResumeInputs, "resume.open-pilot.push-branch.branch", pushPlan.Branch)
		setOpenPilotResumeInput(saved.ResumeInputs, "resume.open-pilot.push-branch.worktree_path", pushPlan.WorktreePath)
		setOpenPilotResumeBool(saved.ResumeInputs, "resume.open-pilot.push-branch.executed", pushPlan.Executed)
		setOpenPilotResumeBool(saved.ResumeInputs, "resume.open-pilot.push-branch.dry_run", pushPlan.DryRun)
		setOpenPilotResumeBool(saved.ResumeInputs, "resume.open-pilot.push-branch.remote_head_exists", pushPlan.RemoteHeadExists)
		if corr, ok := state["open-pilot.push-correlation"].(map[string]string); ok {
			for k, v := range corr {
				setOpenPilotResumeInput(saved.ResumeInputs, "resume.open-pilot.push-branch.correlation."+k, v)
			}
		}
	}
	if corr, ok := state["open-pilot.agent-correlation"].(map[string]string); ok {
		for k, v := range corr {
			setOpenPilotResumeInput(saved.ResumeInputs, "resume.open-pilot.agent-work.correlation."+k, v)
		}
	}
	if pr, ok := state["open-pilot.pull-request"].(GiteaPullRequest); ok {
		saved.PullRequestURL = pr.HTMLURL
		saved.PullRequestID = pr.ID
		saved.PullRequestNum = pr.Number
		setOpenPilotResumeInput(saved.ResumeInputs, "resume.open-pilot.pr-plan.pull_request_url", pr.HTMLURL)
		setOpenPilotResumeInput(saved.ResumeInputs, "resume.open-pilot.pr-plan.pull_request_id", fmt.Sprintf("%d", pr.ID))
		setOpenPilotResumeInput(saved.ResumeInputs, "resume.open-pilot.pr-plan.pull_request_number", fmt.Sprintf("%d", pr.Number))
	}
	if mergeResult, ok := state["open-pilot.merge-result"].(GiteaMergeResult); ok && mergeResult.Merged {
		saved.MergeCommitSHA = mergeResult.SHA
		setOpenPilotResumeInput(saved.ResumeInputs, "resume.open-pilot.merge-pr.merge_commit_sha", mergeResult.SHA)
		setOpenPilotResumeInput(saved.ResumeInputs, "resume.open-pilot.merge-pr.merged", fmt.Sprintf("%t", mergeResult.Merged))
		setOpenPilotResumeInput(saved.ResumeInputs, "resume.open-pilot.merge-pr.message", mergeResult.Message)
	} else if sha, ok := state["open-pilot.merge-commit-sha"].(string); ok {
		saved.MergeCommitSHA = sha
		setOpenPilotResumeInput(saved.ResumeInputs, "resume.open-pilot.merge-pr.merge_commit_sha", sha)
	}
	if issueClosed, ok := state["open-pilot.issue-closed"].(bool); ok && issueClosed {
		saved.IssueClosed = true
		setOpenPilotResumeInput(saved.ResumeInputs, "resume.open-pilot.close-issue.issue_closed", "true")
	}
	if issueCloseResult, ok := state["open-pilot.issue-close-result"].(GiteaIssue); ok {
		saved.IssueClosed = issueCloseResult.State == "closed"
		setOpenPilotResumeInput(saved.ResumeInputs, "resume.open-pilot.close-issue.issue_closed", fmt.Sprintf("%t", saved.IssueClosed))
		setOpenPilotResumeInput(saved.ResumeInputs, "resume.open-pilot.close-issue.state", issueCloseResult.State)
	}
	if feedback, ok := state["ai_review.feedback"].(string); ok {
		setOpenPilotResumeInput(saved.ResumeInputs, "ai_review.feedback", feedback)
	}
	if clearRepair, ok := state["open-pilot.ai-review.clear-repair"].(bool); ok && clearRepair {
		delete(saved.ResumeInputs, "ai_review.feedback")
		delete(saved.ResumeInputs, "retry_from_step")
		delete(saved.ResumeInputs, "retry_from_step_position")
		saved.RepairAttempts = 0
	}
	if retryStep, ok := state["retry_from_step"].(string); ok {
		setOpenPilotResumeInput(saved.ResumeInputs, "retry_from_step", retryStep)
	}
	if retryPosition, ok := state["retry_from_step_position"].(string); ok {
		setOpenPilotResumeInput(saved.ResumeInputs, "retry_from_step_position", retryPosition)
	}
	if gateStatus, ok := state["open-pilot.gate-status"].(map[string]string); ok {
		if saved.GateStatus == nil {
			saved.GateStatus = OpenPilotGateStatus{}
		}
		for k, v := range gateStatus {
			saved.GateStatus[k] = v
		}
	}
	if attempts, ok := state["open-pilot.repair-attempts"].(int); ok {
		saved.RepairAttempts = attempts
	}
	if phase == OpenPilotPhaseVerified {
		saved.GateStatus["local_verify"] = "passed"
	} else if phase == OpenPilotPhaseFailed && stepName == "open-pilot.verify-command" {
		saved.GateStatus["local_verify"] = "failed"
	}
	if lastErr != "" {
		saved.LastError = lastErr
	}
	if phase == OpenPilotPhaseFailed || phase == OpenPilotPhasePaused {
		diagnostics := openPilotFailureDiagnosticsForState(saved, phase, stepName, lastErr, state)
		if diagnostics.failingGate != "" {
			saved.FailingGate = diagnostics.failingGate
		}
		if diagnostics.operatorSummary != "" {
			saved.OperatorSummary = diagnostics.operatorSummary
		}
		if diagnostics.recommendedNextAction != "" {
			saved.RecommendedNextAction = diagnostics.recommendedNextAction
		}
		if state != nil {
			state["open-pilot.failure-diagnostics.persisted"] = true
		}
	}
	if saved.MaxRepairAttempts == 0 {
		saved.MaxRepairAttempts = openPilotDefaultMaxRepairAttempts
	}
	if missing {
		if err := o.bootstrapOpenPilotIssueState(repo, issue, stepName, saved, phase); err != nil {
			return err
		}
	}
	_, err = o.env.store.UpsertOpenPilotIssueState(saved)
	return err
}

func (o openPilotExecutor) bootstrapOpenPilotIssueState(repo, issue, stepName string, saved OpenPilotIssueState, target OpenPilotPhase) error {
	if o.env == nil || o.env.store == nil {
		return nil
	}
	seed := saved
	seed.Phase = OpenPilotPhaseRequested
	seed.LastStepName = stepName
	seed.LastError = ""
	if _, err := o.env.store.UpsertOpenPilotIssueState(seed); err != nil {
		return err
	}
	if target == OpenPilotPhaseRequested {
		return nil
	}
	if target == OpenPilotPhaseFailed || target == OpenPilotPhasePaused {
		_, err := o.env.store.TransitionOpenPilotIssueState(repo, issue, target)
		return err
	}
	if target == OpenPilotPhaseValidated {
		_, err := o.env.store.TransitionOpenPilotIssueState(repo, issue, target)
		return err
	}
	phaseIndex := -1
	for i, phase := range openPilotPhaseChain {
		if phase == target {
			phaseIndex = i
			break
		}
	}
	if phaseIndex == -1 {
		return ErrInvalid
	}
	for _, next := range openPilotPhaseChain[1 : phaseIndex+1] {
		if _, err := o.env.store.TransitionOpenPilotIssueState(repo, issue, next); err != nil {
			return err
		}
	}
	return nil
}

func (o openPilotExecutor) HandlePipelineStepFailure(ctx context.Context, run Run, step PipelineStep, state map[string]any, err error) {
	_ = ctx
	if err == nil {
		return
	}
	if persisted, _ := state["open-pilot.failure-diagnostics.persisted"].(bool); persisted {
		return
	}
	phase := OpenPilotPhaseFailed
	switch step.ID {
	case "validate-issue":
		phase = OpenPilotPhaseFailed
	case "branch-plan":
		phase = OpenPilotPhaseFailed
	case "prepare-prompt":
		phase = OpenPilotPhaseFailed
	case "agent-work":
		phase = OpenPilotPhaseFailed
	case "verify-command":
		phase = OpenPilotPhaseFailed
	case "push-branch":
		phase = OpenPilotPhaseFailed
	case "pr-plan":
		phase = OpenPilotPhaseFailed
	case "ai-review":
		return
	case "merge-pr":
		phase = OpenPilotPhaseFailed
	case "close-issue":
		phase = OpenPilotPhaseFailed
	}
	if persistErr := o.persistOpenPilotIssueState(run, phase, step.Name, err.Error(), state); persistErr != nil {
		if o.env != nil && o.env.store != nil {
			_, _ = o.env.store.AddEvent(run.ID, "open-pilot.lifecycle.failure.persistence.failed", step.Name+": "+persistErr.Error())
		}
	}
}

func (o openPilotExecutor) PipelineStepCorrelationIDs(run Run, step PipelineStep, state map[string]any) map[string]string {
	_ = o
	_ = state
	if step.ID != "agent-work" {
		return nil
	}
	requestID := openPilotOpenCodeRequestID(run, step.Name)
	correlation := map[string]string{
		"opencode_executor":         "switchyard",
		"opencode_executor_backend": "ocq",
		"ocq_request_id":            requestID,
	}
	if sessionID := strings.TrimSpace(run.Inputs["opencode_session_id"]); sessionID != "" {
		correlation["ocq_session_id"] = sessionID
	} else if sessionID := strings.TrimSpace(run.Inputs["ocq_session_id"]); sessionID != "" {
		correlation["ocq_session_id"] = sessionID
	}
	return correlation
}

func (o openPilotExecutor) ExecutePipelineStep(ctx context.Context, run Run, step PipelineStep, state map[string]any) (PipelineStepExecution, error) {
	repo := run.Inputs["repo"]
	issue := run.Inputs["issue"]
	switch step.ID {
	case "validate-issue":
		if repo == "" || issue == "" {
			return PipelineStepExecution{}, errors.New("open-pilot requires run inputs repo and issue")
		}
		issueData, issueSummary, fetchedIssue, fetchErr := o.env.fetchOpenPilotIssue(ctx, repo, issue)
		out := PipelineStepExecution{}
		state["open-pilot.issue-summary"] = issueSummary
		state["open-pilot.fetched-issue"] = fetchedIssue
		if fetchErr != nil {
			out.Events = append(out.Events, WorkflowExecutionEvent{Type: "open-pilot.issue.fetch.skipped", Message: fetchErr.Error()})
		}
		if fetchedIssue != nil {
			if err := o.env.validateOpenPilotIssue(*fetchedIssue); err != nil {
				if persistErr := o.persistOpenPilotIssueState(run, OpenPilotPhaseFailed, step.Name, err.Error(), state); persistErr != nil {
					return out, errors.Join(err, persistErr)
				}
				return out, err
			}
		}
		if issueData != nil {
			out.Artifacts = append(out.Artifacts, CreateArtifactInput{Name: "open-pilot-issue.json", Kind: "json", URI: "data:application/json;charset=utf-8," + url.QueryEscape(string(issueData))})
		}
		out.Summary = "Validated Open Pilot run inputs"
		if issueSummary != "" {
			out.Summary = issueSummary
		}
		if err := o.persistOpenPilotIssueState(run, OpenPilotPhaseValidated, step.Name, "", state); err != nil {
			return out, err
		}
		return out, nil
	case "branch-plan":
		fetchedIssue, _ := state["open-pilot.fetched-issue"].(*GiteaIssue)
		branchName := o.env.openPilotBranchName(repo, issue, fetchedIssue)
		baseBranch := firstNonEmpty(run.Inputs["base_branch"], o.env.openPilotBaseBranch)
		branchPlan, prepareWorktreeErr := o.env.worktreeManager.Prepare(ctx, run, repo, baseBranch, branchName)
		branchBytes, _ := json.MarshalIndent(branchPlan, "", "  ")
		state["open-pilot.branch-name"] = branchName
		state["open-pilot.branch-plan"] = branchPlan
		out := PipelineStepExecution{Inputs: branchPlan.StepInputs(), Artifacts: []CreateArtifactInput{{Name: "open-pilot-branch-plan.json", Kind: "json", URI: "data:application/json;charset=utf-8," + url.QueryEscape(string(branchBytes))}}}
		if prepareWorktreeErr != nil {
			if persistErr := o.persistOpenPilotIssueState(run, OpenPilotPhaseFailed, step.Name, prepareWorktreeErr.Error(), state); persistErr != nil {
				return out, errors.Join(prepareWorktreeErr, persistErr)
			}
			return out, prepareWorktreeErr
		}
		out.Summary = "Planned branch " + branchName
		if branchPlan.Executed {
			out.Summary = "Created worktree " + branchPlan.WorktreePath
		} else if branchPlan.DryRun {
			out.Summary = "Worktree dry-run; planned branch " + branchName
		}
		out.CorrelationIDs = map[string]string{"branch": branchName}
		if branchPlan.Executed && branchPlan.WorktreePath != "" {
			out.CorrelationIDs["worktree_path"] = branchPlan.WorktreePath
		} else if branchPlan.PlannedWorktreePath != "" {
			out.CorrelationIDs["planned_worktree_path"] = branchPlan.PlannedWorktreePath
		}
		issueSummary, _ := state["open-pilot.issue-summary"].(string)
		state["open-pilot.prompt"] = buildOpenPilotPrompt(repo, issue, issueSummary, fetchedIssue, run.Inputs, branchPlan)
		if err := o.persistOpenPilotIssueState(run, OpenPilotPhaseBranchPrepared, step.Name, "", state); err != nil {
			return out, err
		}
		return out, nil
	case "prepare-prompt":
		issueSummary, _ := state["open-pilot.issue-summary"].(string)
		fetchedIssue, _ := state["open-pilot.fetched-issue"].(*GiteaIssue)
		branchPlan, _ := state["open-pilot.branch-plan"].(OpenPilotWorktreePlan)
		prompt := buildOpenPilotPrompt(repo, issue, issueSummary, fetchedIssue, run.Inputs, branchPlan)
		state["open-pilot.prompt"] = prompt
		return PipelineStepExecution{Summary: "Prepared Open Pilot implementation prompt", Artifacts: []CreateArtifactInput{{Name: "open-pilot-prompt.md", Kind: "markdown", URI: "data:text/markdown;charset=utf-8," + url.QueryEscape(prompt)}}}, nil
	case "agent-work":
		prompt, _ := state["open-pilot.prompt"].(string)
		issueSummary, _ := state["open-pilot.issue-summary"].(string)
		branchPlan, _ := state["open-pilot.branch-plan"].(OpenPilotWorktreePlan)
		model := firstNonEmpty(step.Model, o.env.openPilotModel)
		response, err := o.env.openCodeExecutor.SendPrompt(ctx, OpenCodeRequest{Model: model, Prompt: prompt, SessionID: openCodeSessionID(run.Inputs), RequestID: openPilotOpenCodeRequestID(run, step.Name)})
		if err != nil {
			fallbackState := map[string]any{"open-pilot.prompt": prompt, "open-pilot.branch-plan": branchPlan, "open-pilot.issue-summary": issueSummary}
			state["summary"] = "Prepared Open Pilot prompt for issue " + issue
			if persistErr := o.persistOpenPilotIssueState(run, OpenPilotPhasePaused, step.Name, err.Error(), fallbackState); persistErr != nil {
				return PipelineStepExecution{Summary: "OpenCode executor unavailable; prompt prepared for operator handoff", Events: []WorkflowExecutionEvent{{Type: "open-pilot.agent.fallback", Message: err.Error()}, {Type: "open-pilot.prepared", Message: "Prompt artifact ready for " + repo + "#" + issue}}, Halt: true}, persistErr
			}
			return PipelineStepExecution{Summary: "OpenCode executor unavailable; prompt prepared for operator handoff", Events: []WorkflowExecutionEvent{{Type: "open-pilot.agent.fallback", Message: err.Error()}, {Type: "open-pilot.prepared", Message: "Prompt artifact ready for " + repo + "#" + issue}}, Halt: true}, nil
		}
		state["open-pilot.agent-completed"] = true
		state["open-pilot.agent-summary"] = response.Content
		state["open-pilot.response"] = response.Content
		state["open-pilot.agent-correlation"] = response.CorrelationIDs
		stepResult := PipelineStepExecution{Summary: "OCQ completed Open Pilot prompt", Artifacts: []CreateArtifactInput{{Name: "open-pilot-response.md", Kind: "markdown", URI: "data:text/markdown;charset=utf-8," + url.QueryEscape(response.Content)}}, CorrelationIDs: response.CorrelationIDs}
		if err := o.persistOpenPilotIssueState(run, OpenPilotPhaseAgentCompleted, step.Name, "", state); err != nil {
			return stepResult, err
		}
		return stepResult, nil
	case "verify-command":
		command := strings.TrimSpace(run.Inputs["test_command"])
		branchPlan, _ := state["open-pilot.branch-plan"].(OpenPilotWorktreePlan)
		workdir := ""
		if branchPlan.Executed {
			workdir = branchPlan.WorktreePath
		}
		result, err := o.env.commandRunner.Run(ctx, command, workdir)
		correlation := map[string]string{"policy_reason": result.Decision.Reason}
		if workdir != "" {
			correlation["worktree_path"] = branchPlan.WorktreePath
		}
		if result.Decision.DryRun {
			correlation["policy_mode"] = string(CommandPolicyDryRun)
		}
		if err != nil {
			state["open-pilot.verify-command.command"] = command
			if persistErr := o.persistOpenPilotIssueState(run, OpenPilotPhaseFailed, step.Name, err.Error(), state); persistErr != nil {
				return PipelineStepExecution{Summary: result.Output, Inputs: map[string]string{"command": command}, CorrelationIDs: correlation}, errors.Join(err, persistErr)
			}
			return PipelineStepExecution{Summary: result.Output, Inputs: map[string]string{"command": command}, CorrelationIDs: correlation}, err
		}
		summary := "Command allowed"
		if result.Decision.DryRun {
			summary = "Command allowed by policy in dry-run mode"
		} else if result.Executed {
			summary = "Command executed by policy runner"
		}
		state["open-pilot.verify-command.command"] = command
		state["open-pilot.verify-summary"] = summary
		state["open-pilot.gate-status"] = map[string]string{"local_verify": "passed"}
		if err := o.persistOpenPilotIssueState(run, OpenPilotPhaseVerified, step.Name, "", state); err != nil {
			return PipelineStepExecution{Summary: summary, Inputs: map[string]string{"command": command}, CorrelationIDs: correlation}, err
		}
		return PipelineStepExecution{Summary: summary, Inputs: map[string]string{"command": command}, CorrelationIDs: correlation}, nil
	case "push-branch":
		branchPlan, _ := state["open-pilot.branch-plan"].(OpenPilotWorktreePlan)
		pushPlan := o.env.openPilotPushPlan(branchPlan)
		pushCorrelation := map[string]string{"mode": pushPlan.Mode}
		if branchPlan.Executed && branchPlan.WorktreePath != "" {
			remoteHeadExists, err := o.env.openPilotRemoteBranchExists(ctx, branchPlan.WorktreePath, pushPlan.Remote, pushPlan.Branch)
			if err != nil {
				return PipelineStepExecution{Inputs: pushPlan.StepInputs(), CorrelationIDs: pushCorrelation}, err
			}
			pushPlan.RemoteHeadExists = remoteHeadExists
		}
		state["open-pilot.push-plan"] = pushPlan
		state["open-pilot.push-correlation"] = pushCorrelation
		pushPlanBytes, _ := json.MarshalIndent(pushPlan, "", "  ")
		if pushPlan.Executed {
			pushCorrelation["branch"] = pushPlan.Branch
			pushCorrelation["remote"] = pushPlan.Remote
		}
		if pushPlan.RemoteHeadExists {
			pushCorrelation["remote_head_exists"] = "true"
		}
		out := PipelineStepExecution{Inputs: pushPlan.StepInputs(), Artifacts: []CreateArtifactInput{{Name: "open-pilot-push-plan.json", Kind: "json", URI: "data:application/json;charset=utf-8," + url.QueryEscape(string(pushPlanBytes))}}, CorrelationIDs: pushCorrelation}
		if branchPlan.Executed && branchPlan.WorktreePath != "" {
			status, err := o.env.openPilotWorktreeStatus(ctx, branchPlan.WorktreePath)
			if err != nil {
				if persistErr := o.persistOpenPilotIssueState(run, OpenPilotPhaseFailed, step.Name, err.Error(), state); persistErr != nil {
					return out, errors.Join(err, persistErr)
				}
				return out, err
			}
			out.Artifacts = append(out.Artifacts, CreateArtifactInput{Name: "open-pilot-git-status.txt", Kind: "text", URI: "data:text/plain;charset=utf-8," + url.QueryEscape(status)})
			if pushPlan.Executed && strings.TrimSpace(status) != "clean" {
				return out, errors.New("OPEN_PILOT_GIT_PUSH_MODE=allow requires a clean worktree; commit or discard uncommitted changes")
			}
			ahead, err := o.env.openPilotWorktreeAheadCount(ctx, branchPlan)
			if err != nil {
				return out, err
			}
			out.Artifacts = append(out.Artifacts, CreateArtifactInput{Name: "open-pilot-git-ahead.txt", Kind: "text", URI: "data:text/plain;charset=utf-8," + url.QueryEscape(fmt.Sprintf("%d\n", ahead))})
			out.CorrelationIDs["commits_ahead"] = fmt.Sprintf("%d", ahead)
			if pushPlan.Executed && ahead == 0 {
				return out, errors.New("OPEN_PILOT_GIT_PUSH_MODE=allow requires committed changes ahead of base branch")
			}
		}
		if err := o.env.executeOpenPilotPush(ctx, pushPlan); err != nil {
			if persistErr := o.persistOpenPilotIssueState(run, OpenPilotPhaseFailed, step.Name, err.Error(), state); persistErr != nil {
				return out, errors.Join(err, persistErr)
			}
			return out, err
		}
		out.Summary = "Git push disabled; plan artifact attached"
		if pushPlan.DryRun {
			out.Summary = "Git push dry-run; plan artifact attached"
		} else if pushPlan.Executed {
			out.Summary = "Pushed branch " + pushPlan.Branch
		}
		if pushPlan.Executed || pushPlan.RemoteHeadExists {
			if err := o.persistOpenPilotIssueState(run, OpenPilotPhasePushed, step.Name, "", state); err != nil {
				return out, err
			}
		}
		return out, nil
	case "pr-plan":
		issueSummary, _ := state["open-pilot.issue-summary"].(string)
		branchName, _ := state["open-pilot.branch-name"].(string)
		response, _ := state["open-pilot.response"].(string)
		branchPlan, _ := state["open-pilot.branch-plan"].(OpenPilotWorktreePlan)
		pushPlan, _ := state["open-pilot.push-plan"].(OpenPilotPushPlan)
		existingPR, hasExistingPR := state["open-pilot.pull-request"].(GiteaPullRequest)
		prKnown := hasExistingPR && openPilotPullRequestKnown(existingPR)
		prInput := o.env.openPilotPullRequestInput(repo, issue, branchName, issueSummary, response, run.Inputs)
		prPlanBytes, _ := json.MarshalIndent(prInput, "", "  ")
		out := PipelineStepExecution{Inputs: map[string]string{"mode": o.env.openPilotWriteMode, "head": prInput.Head, "base": prInput.Base}, Artifacts: []CreateArtifactInput{{Name: "open-pilot-pr-plan.json", Kind: "json", URI: "data:application/json;charset=utf-8," + url.QueryEscape(string(prPlanBytes))}}}
		switch o.env.openPilotWriteMode {
		case "allow":
			if branchPlan.Executed && !pushPlan.Executed && !pushPlan.RemoteHeadExists && !prKnown {
				if persistErr := o.persistOpenPilotIssueState(run, OpenPilotPhaseFailed, step.Name, "OPEN_PILOT_GITEA_WRITE_MODE=allow requires pushed worktree branch or externally published head", state); persistErr != nil {
					return out, persistErr
				}
				return out, errors.New("OPEN_PILOT_GITEA_WRITE_MODE=allow requires pushed worktree branch or externally published head")
			}
			if prKnown {
				out.Summary = "Using existing pull request"
				out.CorrelationIDs = map[string]string{"pull_request": existingPR.HTMLURL}
				break
			}
			created, err := o.env.giteaWriter.CreatePullRequest(ctx, prInput)
			if err != nil {
				if persistErr := o.persistOpenPilotIssueState(run, OpenPilotPhaseFailed, step.Name, err.Error(), state); persistErr != nil {
					return out, errors.Join(err, persistErr)
				}
				return out, err
			}
			state["open-pilot.pull-request"] = created
			prKnown = true
			createdBytes, _ := json.MarshalIndent(created, "", "  ")
			out.Artifacts = append(out.Artifacts, CreateArtifactInput{Name: "open-pilot-pr.json", Kind: "json", URI: "data:application/json;charset=utf-8," + url.QueryEscape(string(createdBytes))})
			out.Summary = "Created pull request"
			out.CorrelationIDs = map[string]string{"pull_request": created.HTMLURL}
		case "dry-run":
			out.Summary = "PR creation dry-run; plan artifact attached"
		default:
			out.Summary = "PR creation disabled; plan artifact attached"
		}
		if branchPlan.Executed {
			cleanupPlan, err := o.env.worktreeManager.Cleanup(ctx, branchPlan, pushPlan.Executed || pushPlan.RemoteHeadExists || prKnown)
			cleanupBytes, _ := json.MarshalIndent(cleanupPlan, "", "  ")
			out.Artifacts = append(out.Artifacts, CreateArtifactInput{Name: "open-pilot-worktree-cleanup.json", Kind: "json", URI: "data:application/json;charset=utf-8," + url.QueryEscape(string(cleanupBytes))})
			if err != nil {
				if persistErr := o.persistOpenPilotIssueState(run, OpenPilotPhaseFailed, step.Name, err.Error(), state); persistErr != nil {
					return out, errors.Join(err, persistErr)
				}
				return out, err
			}
			if out.CorrelationIDs == nil {
				out.CorrelationIDs = map[string]string{}
			}
			if cleanupPlan.Executed {
				out.CorrelationIDs["worktree_cleanup"] = "executed"
			} else if cleanupPlan.Skipped {
				out.CorrelationIDs["worktree_cleanup"] = "skipped"
				out.CorrelationIDs["worktree_cleanup_reason"] = cleanupPlan.Reason
			}
		}
		state["summary"] = "Ran Open Pilot issue " + issue + " through OCQ"
		out.Events = append(out.Events, WorkflowExecutionEvent{Type: "open-pilot.agent.completed", Message: repo + "#" + issue})
		if prKnown {
			if err := o.persistOpenPilotIssueState(run, OpenPilotPhasePRCreated, step.Name, "", state); err != nil {
				return out, err
			}
		}
		return out, nil
	case "ai-review":
		model := firstNonEmpty(step.Model, o.env.openPilotModel)
		reviewPrompt := buildOpenPilotAIReviewPrompt(repo, issue, run.Inputs, state)
		if o.env == nil || o.env.modelExecutor == nil {
			return PipelineStepExecution{}, errors.New("model executor not configured")
		}
		response, err := o.env.modelExecutor.Generate(ctx, ModelRequest{Model: model, Messages: []ModelMessage{{Role: "system", Content: "You are the Open Pilot operator reviewer. Review the completed work against the issue, branch, local verification, and agent output. Reply with exactly one paragraph using the required verdict contract."}, {Role: "user", Content: reviewPrompt}}})
		if err != nil {
			if persistErr := o.persistOpenPilotIssueState(run, OpenPilotPhaseFailed, step.Name, err.Error(), state); persistErr != nil {
				return PipelineStepExecution{}, errors.Join(err, persistErr)
			}
			return PipelineStepExecution{}, err
		}
		verdict, feedback, parseErr := parseOpenPilotAIReviewResponse(response.Content)
		if parseErr != nil {
			if persistErr := o.persistOpenPilotIssueState(run, OpenPilotPhaseFailed, step.Name, parseErr.Error(), state); persistErr != nil {
				return PipelineStepExecution{CorrelationIDs: response.CorrelationIDs}, errors.Join(parseErr, persistErr)
			}
			return PipelineStepExecution{CorrelationIDs: response.CorrelationIDs}, parseErr
		}
		gateStatus := map[string]string{}
		var existing OpenPilotIssueState
		var existingErr error
		if o.env != nil && o.env.store != nil {
			existing, existingErr = o.env.store.OpenPilotIssueState(repo, issue)
			if existingErr == nil {
				gateStatus = cloneOpenPilotGateStatus(existing.GateStatus)
			}
		}
		if gateStatus == nil {
			gateStatus = map[string]string{}
		}
		artifactBody := buildOpenPilotAIReviewArtifact(repo, issue, run.Inputs, state, verdict, feedback, response.Content)
		artifact := CreateArtifactInput{RunID: run.ID, Name: "open-pilot-ai-review.md", Kind: "markdown", URI: "data:text/markdown;charset=utf-8," + url.QueryEscape(artifactBody)}
		state["open-pilot.gate-status"] = gateStatus
		state["open-pilot.ai-review.body"] = response.Content
		summary := "AI review approved"
		if verdict == "changes_requested" {
			state["ai_review.feedback"] = feedback
			state["open-pilot.ai-review.changes-requested"] = true
			state["retry_from_step"] = "open-pilot.agent-work"
			state["retry_from_step_position"] = strconv.Itoa(4)
			attempts := 1
			maxAttempts := openPilotDefaultMaxRepairAttempts
			if existingErr == nil {
				if existing.RepairAttempts > 0 {
					attempts = existing.RepairAttempts + 1
				}
				if existing.MaxRepairAttempts > 0 {
					maxAttempts = existing.MaxRepairAttempts
				}
			}
			state["open-pilot.repair-attempts"] = attempts
			gateStatus["ai_review"] = "changes_requested"
			state["open-pilot.gate-status"] = gateStatus
			phase := OpenPilotPhaseFailed
			if attempts >= maxAttempts {
				phase = OpenPilotPhasePaused
			}
			if err := o.persistOpenPilotIssueState(run, phase, step.Name, feedback, state); err != nil {
				return PipelineStepExecution{Summary: "AI review requested changes", Artifacts: []CreateArtifactInput{artifact}, CorrelationIDs: response.CorrelationIDs}, err
			}
			return PipelineStepExecution{Summary: "AI review requested changes", Artifacts: []CreateArtifactInput{artifact}, CorrelationIDs: response.CorrelationIDs}, errors.New("ai review requested changes")
		}
		gateStatus["ai_review"] = "approved"
		state["open-pilot.gate-status"] = gateStatus
		state["open-pilot.ai-review.clear-repair"] = true
		if err := o.persistOpenPilotIssueState(run, OpenPilotPhaseAIReviewed, step.Name, "", state); err != nil {
			return PipelineStepExecution{Summary: summary, Artifacts: []CreateArtifactInput{artifact}, CorrelationIDs: response.CorrelationIDs}, err
		}
		return PipelineStepExecution{Summary: summary, Artifacts: []CreateArtifactInput{artifact}, CorrelationIDs: response.CorrelationIDs}, nil
	case "ci-checks":
		return o.executeOpenPilotCIChecks(ctx, run, state)
	case "merge-pr":
		return o.executeOpenPilotMerge(ctx, run, state)
	case "close-issue":
		return o.executeOpenPilotCloseIssue(ctx, run, state)
	default:
		return PipelineStepExecution{}, fmt.Errorf("unsupported open-pilot pipeline step %q", step.ID)
	}
}

type openPilotCICheckResult struct {
	status  string
	summary string
}

func (o openPilotExecutor) executeOpenPilotCIChecks(ctx context.Context, run Run, state map[string]any) (PipelineStepExecution, error) {
	if o.env == nil {
		return PipelineStepExecution{}, errors.New("open-pilot CI checks not configured")
	}
	mode := strings.TrimSpace(o.env.openPilotCICheckMode)
	if mode == "" {
		mode = "disabled"
	}
	if mode != "disabled" && mode != "poll" && mode != "require-pass" {
		return PipelineStepExecution{}, fmt.Errorf("unsupported OPEN_PILOT_CI_CHECK_MODE %q", mode)
	}
	gateStatus := map[string]string{}
	if existing := openPilotGateStatusFromState(state); len(existing) > 0 {
		gateStatus = cloneMap(existing)
	}
	if gateStatus == nil {
		gateStatus = map[string]string{}
	}
	if mode == "disabled" {
		gateStatus["ci_checks"] = "skipped"
		state["open-pilot.gate-status"] = gateStatus
		if err := o.persistOpenPilotIssueState(run, OpenPilotPhaseAIReviewed, "open-pilot.ci-checks", "", state); err != nil {
			return PipelineStepExecution{Summary: "CI checks skipped"}, err
		}
		return PipelineStepExecution{Summary: "CI checks skipped"}, nil
	}
	pr, prFound, err := o.resolveOpenPilotCIPullRequest(ctx, run, state, false)
	if err != nil {
		gateStatus["ci_checks"] = "failed"
		state["open-pilot.gate-status"] = gateStatus
		if persistErr := o.persistOpenPilotIssueState(run, OpenPilotPhaseFailed, "open-pilot.ci-checks", err.Error(), state); persistErr != nil {
			return PipelineStepExecution{Summary: "CI checks failed"}, errors.Join(err, persistErr)
		}
		return PipelineStepExecution{Summary: "CI checks failed"}, err
	}
	if prFound {
		state["open-pilot.pull-request"] = pr
	}
	if o.env.giteaReader == nil {
		return PipelineStepExecution{}, errors.New("gitea pull request reader not configured")
	}
	requiredChecks := o.env.openPilotCIRequiredChecks
	if len(requiredChecks) == 0 {
		requiredChecks = splitCSV("verify,lint,test")
	}
	deadline := time.Now().Add(o.env.openPilotCICheckTimeout)
	if o.env.openPilotCICheckTimeout <= 0 {
		deadline = time.Now()
	}
	interval := o.env.openPilotCICheckInterval
	if interval <= 0 {
		interval = 15 * time.Second
	}
	for {
		if pr.Head.SHA == "" {
			return o.persistOpenPilotCICheckFailure(run, state, gateStatus, "failed", pr, nil, errors.New("open-pilot pull request head sha is required"))
		}
		snapshot, err := o.env.giteaReader.PullRequestChecks(ctx, run.Inputs["repo"], pr)
		if err != nil {
			return o.persistOpenPilotCICheckFailure(run, state, gateStatus, "failed", pr, nil, err)
		}
		outcome, done := evaluateOpenPilotCIChecks(requiredChecks, snapshot)
		if done {
			if outcome.status == "failed" {
				return o.persistOpenPilotCICheckFailure(run, state, gateStatus, "failed", pr, &snapshot, errors.New(outcome.summary))
			}
			gateStatus["ci_checks"] = outcome.status
			state["open-pilot.gate-status"] = gateStatus
			artifactBody, artifact := buildOpenPilotCICheckArtifact(run.Inputs["repo"], pr, mode, requiredChecks, snapshot, outcome.status, outcome.summary)
			_ = artifactBody
			if err := o.persistOpenPilotIssueState(run, OpenPilotPhaseCIPassed, "open-pilot.ci-checks", "", state); err != nil {
				return PipelineStepExecution{Summary: outcome.summary, Artifacts: []CreateArtifactInput{artifact}, CorrelationIDs: prCorrelationIDs(pr)}, err
			}
			return PipelineStepExecution{Summary: outcome.summary, Artifacts: []CreateArtifactInput{artifact}, CorrelationIDs: prCorrelationIDs(pr)}, nil
		}
		if time.Now().After(deadline) {
			return o.persistOpenPilotCICheckFailure(run, state, gateStatus, "timeout", pr, &snapshot, errors.New("open pilot ci checks timed out waiting for required checks"))
		}
		wait := interval
		if remaining := time.Until(deadline); remaining < wait {
			wait = remaining
		}
		if wait <= 0 {
			wait = time.Millisecond
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return PipelineStepExecution{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func openPilotPullRequestNumberFromState(state map[string]any, inputs map[string]string) int {
	if pr, ok := state["open-pilot.pull-request"].(GiteaPullRequest); ok && pr.Number > 0 {
		return pr.Number
	}
	for _, raw := range []string{inputs["resume.open-pilot.pr-plan.pull_request_number"], inputs["pull_request_number"]} {
		if num, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && num > 0 {
			return num
		}
	}
	for _, raw := range []string{inputs["resume.open-pilot.pr-plan.pull_request_url"], inputs["pull_request_url"]} {
		if num := openPilotPullRequestNumberFromURL(raw); num > 0 {
			return num
		}
	}
	return 0
}

func openPilotPullRequestNumberFromURL(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	parts := strings.Split(strings.TrimRight(raw, "/"), "/")
	if len(parts) == 0 {
		return 0
	}
	num, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil || num < 1 {
		return 0
	}
	return num
}

func mergeOpenPilotGateStatus(state map[string]any) map[string]string {
	gateStatus := map[string]string{}
	if existing := openPilotGateStatusFromState(state); len(existing) > 0 {
		gateStatus = cloneMap(existing)
	}
	if gateStatus == nil {
		gateStatus = map[string]string{}
	}
	return gateStatus
}

func (o openPilotExecutor) executeOpenPilotMerge(ctx context.Context, run Run, state map[string]any) (PipelineStepExecution, error) {
	gateStatus := mergeOpenPilotGateStatus(state)
	if !openPilotMergeStepReady(run, state) {
		gateStatus["merge"] = "failed"
		state["open-pilot.gate-status"] = gateStatus
		if err := o.persistOpenPilotIssueState(run, OpenPilotPhaseFailed, "open-pilot.merge-pr", "merge prerequisites not met", state); err != nil {
			return PipelineStepExecution{}, err
		}
		return PipelineStepExecution{}, errors.New("merge prerequisites not met")
	}
	pr, _ := state["open-pilot.pull-request"].(GiteaPullRequest)
	if pr.Number == 0 {
		pr.Number = openPilotPullRequestNumberFromState(state, run.Inputs)
	}
	if pr.Number == 0 {
		gateStatus["merge"] = "failed"
		state["open-pilot.gate-status"] = gateStatus
		if err := o.persistOpenPilotIssueState(run, OpenPilotPhaseFailed, "open-pilot.merge-pr", "pull request number is required", state); err != nil {
			return PipelineStepExecution{}, err
		}
		return PipelineStepExecution{}, errors.New("pull request number is required")
	}
	mode := strings.TrimSpace(o.env.openPilotMergeMode)
	if mode == "" {
		mode = "disabled"
	}
	switch mode {
	case "disabled":
		gateStatus["merge"] = "skipped"
		state["open-pilot.gate-status"] = gateStatus
		if err := o.persistOpenPilotIssueState(run, OpenPilotPhaseCIPassed, "open-pilot.merge-pr", "", state); err != nil {
			return PipelineStepExecution{Summary: "Merge skipped"}, err
		}
		return PipelineStepExecution{Summary: "Merge skipped"}, nil
	case "dry-run":
		gateStatus["merge"] = "dry-run"
		state["open-pilot.gate-status"] = gateStatus
		if err := o.persistOpenPilotIssueState(run, OpenPilotPhaseCIPassed, "open-pilot.merge-pr", "", state); err != nil {
			return PipelineStepExecution{Summary: "Merge dry-run"}, err
		}
		return PipelineStepExecution{Summary: "Merge dry-run"}, nil
	case "allow":
		if o.env == nil || o.env.giteaMerger == nil {
			return PipelineStepExecution{}, errors.New("gitea pull request merger not configured")
		}
		gateStatus["merge"] = "merge_ready"
		state["open-pilot.gate-status"] = gateStatus
		if err := o.persistOpenPilotIssueState(run, OpenPilotPhaseMergeReady, "open-pilot.merge-pr", "", state); err != nil {
			return PipelineStepExecution{}, err
		}
		result, err := o.env.giteaMerger.MergePullRequest(ctx, run.Inputs["repo"], pr.Number)
		if err != nil {
			gateStatus["merge"] = "failed"
			state["open-pilot.gate-status"] = gateStatus
			if persistErr := o.persistOpenPilotIssueState(run, OpenPilotPhaseFailed, "open-pilot.merge-pr", err.Error(), state); persistErr != nil {
				return PipelineStepExecution{}, errors.Join(err, persistErr)
			}
			return PipelineStepExecution{}, err
		}
		if !result.Merged {
			err := errors.New("gitea merge response did not confirm merged=true")
			gateStatus["merge"] = "failed"
			state["open-pilot.gate-status"] = gateStatus
			state["open-pilot.merge-result"] = result
			if persistErr := o.persistOpenPilotIssueState(run, OpenPilotPhaseFailed, "open-pilot.merge-pr", err.Error(), state); persistErr != nil {
				return PipelineStepExecution{}, errors.Join(err, persistErr)
			}
			return PipelineStepExecution{}, err
		}
		gateStatus["merge"] = "merged"
		state["open-pilot.gate-status"] = gateStatus
		state["open-pilot.merge-result"] = result
		state["open-pilot.merge-commit-sha"] = result.SHA
		artifactBody, artifact := buildOpenPilotMergeArtifact(run.Inputs["repo"], pr, result, gateStatus)
		_ = artifactBody
		if err := o.persistOpenPilotIssueState(run, OpenPilotPhaseMerged, "open-pilot.merge-pr", "", state); err != nil {
			return PipelineStepExecution{Summary: "Merged pull request", Artifacts: []CreateArtifactInput{artifact}, CorrelationIDs: mergeCorrelationIDs(pr, result)}, err
		}
		return PipelineStepExecution{Summary: "Merged pull request", Artifacts: []CreateArtifactInput{artifact}, CorrelationIDs: mergeCorrelationIDs(pr, result)}, nil
	default:
		return PipelineStepExecution{}, fmt.Errorf("unsupported OPEN_PILOT_MERGE_MODE %q", mode)
	}
}

func (o openPilotExecutor) executeOpenPilotCloseIssue(ctx context.Context, run Run, state map[string]any) (PipelineStepExecution, error) {
	gateStatus := mergeOpenPilotGateStatus(state)
	if !openPilotCloseIssueStepReady(run, state) {
		gateStatus["issue_close"] = "failed"
		state["open-pilot.gate-status"] = gateStatus
		if err := o.persistOpenPilotIssueState(run, OpenPilotPhaseFailed, "open-pilot.close-issue", "merge prerequisites not met", state); err != nil {
			return PipelineStepExecution{}, err
		}
		return PipelineStepExecution{}, errors.New("merge prerequisites not met")
	}
	mode := strings.TrimSpace(o.env.openPilotCloseIssueMode)
	if mode == "" {
		mode = "disabled"
	}
	switch mode {
	case "disabled":
		gateStatus["issue_close"] = "skipped"
		state["open-pilot.gate-status"] = gateStatus
		if err := o.persistOpenPilotIssueState(run, OpenPilotPhaseMerged, "open-pilot.close-issue", "", state); err != nil {
			return PipelineStepExecution{Summary: "Issue close skipped"}, err
		}
		return PipelineStepExecution{Summary: "Issue close skipped"}, nil
	case "dry-run":
		gateStatus["issue_close"] = "dry-run"
		state["open-pilot.gate-status"] = gateStatus
		if err := o.persistOpenPilotIssueState(run, OpenPilotPhaseMerged, "open-pilot.close-issue", "", state); err != nil {
			return PipelineStepExecution{Summary: "Issue close dry-run"}, err
		}
		return PipelineStepExecution{Summary: "Issue close dry-run"}, nil
	case "allow":
		if o.env == nil || o.env.giteaMerger == nil {
			return PipelineStepExecution{}, errors.New("gitea issue closer not configured")
		}
		closed, err := o.env.giteaMerger.CloseIssue(ctx, run.Inputs["repo"], run.Inputs["issue"])
		if err != nil {
			gateStatus["issue_close"] = "failed"
			state["open-pilot.gate-status"] = gateStatus
			if persistErr := o.persistOpenPilotIssueState(run, OpenPilotPhaseFailed, "open-pilot.close-issue", err.Error(), state); persistErr != nil {
				return PipelineStepExecution{}, errors.Join(err, persistErr)
			}
			return PipelineStepExecution{}, err
		}
		if closed.State != "closed" {
			err := fmt.Errorf("gitea issue close response state is %q", closed.State)
			gateStatus["issue_close"] = "failed"
			state["open-pilot.gate-status"] = gateStatus
			state["open-pilot.issue-close-result"] = closed
			if persistErr := o.persistOpenPilotIssueState(run, OpenPilotPhaseFailed, "open-pilot.close-issue", err.Error(), state); persistErr != nil {
				return PipelineStepExecution{}, errors.Join(err, persistErr)
			}
			return PipelineStepExecution{}, err
		}
		gateStatus["issue_close"] = "closed"
		state["open-pilot.gate-status"] = gateStatus
		state["open-pilot.issue-close-result"] = closed
		state["open-pilot.issue-closed"] = true
		artifactBody, artifact := buildOpenPilotIssueCloseArtifact(run.Inputs["repo"], run.Inputs["issue"], closed, gateStatus)
		_ = artifactBody
		state["summary"] = fmt.Sprintf("Open Pilot completed %s#%s: PR merged and issue closed", run.Inputs["repo"], run.Inputs["issue"])
		if err := o.persistOpenPilotIssueState(run, OpenPilotPhaseIssueClosed, "open-pilot.close-issue", "", state); err != nil {
			return PipelineStepExecution{Summary: state["summary"].(string), Artifacts: []CreateArtifactInput{artifact}, CorrelationIDs: issueCloseCorrelationIDs(closed)}, err
		}
		return PipelineStepExecution{Summary: state["summary"].(string), Artifacts: []CreateArtifactInput{artifact}, CorrelationIDs: issueCloseCorrelationIDs(closed)}, nil
	default:
		return PipelineStepExecution{}, fmt.Errorf("unsupported OPEN_PILOT_CLOSE_ISSUE_MODE %q", mode)
	}
}

func mergeCorrelationIDs(pr GiteaPullRequest, result GiteaMergeResult) map[string]string {
	out := prCorrelationIDs(pr)
	if result.SHA != "" {
		out["merge_commit_sha"] = result.SHA
	}
	if result.Message != "" {
		out["merge_message"] = result.Message
	}
	return out
}

func issueCloseCorrelationIDs(issue GiteaIssue) map[string]string {
	out := map[string]string{}
	if issue.HTMLURL != "" {
		out["issue"] = issue.HTMLURL
	}
	if issue.State != "" {
		out["issue_state"] = issue.State
	}
	if issue.Number != 0 {
		out["issue_number"] = fmt.Sprintf("%d", issue.Number)
	}
	return out
}

func buildOpenPilotMergeArtifact(repo string, pr GiteaPullRequest, result GiteaMergeResult, gateStatus map[string]string) (string, CreateArtifactInput) {
	body := map[string]any{
		"repo":         repo,
		"pull_request": pr,
		"result":       result,
		"gate_status":  gateStatus,
	}
	encoded, _ := json.MarshalIndent(body, "", "  ")
	return string(encoded), CreateArtifactInput{Name: "open-pilot-merge.json", Kind: "json", URI: "data:application/json;charset=utf-8," + url.QueryEscape(string(encoded))}
}

func buildOpenPilotIssueCloseArtifact(repo, issue string, closed GiteaIssue, gateStatus map[string]string) (string, CreateArtifactInput) {
	body := map[string]any{
		"repo":        repo,
		"issue":       issue,
		"result":      closed,
		"gate_status": gateStatus,
	}
	encoded, _ := json.MarshalIndent(body, "", "  ")
	return string(encoded), CreateArtifactInput{Name: "open-pilot-issue-close.json", Kind: "json", URI: "data:application/json;charset=utf-8," + url.QueryEscape(string(encoded))}
}

func (o openPilotExecutor) resolveOpenPilotCIPullRequest(ctx context.Context, run Run, state map[string]any, allowMissing bool) (GiteaPullRequest, bool, error) {
	pr, ok := state["open-pilot.pull-request"].(GiteaPullRequest)
	if !ok {
		pr = GiteaPullRequest{}
	}
	if openPilotPullRequestKnown(pr) && pr.Head.SHA != "" {
		return pr, true, nil
	}
	head := openPilotPullRequestHeadCandidate(state, run.Inputs)
	if head == "" {
		if allowMissing {
			return pr, openPilotPullRequestKnown(pr), nil
		}
		return GiteaPullRequest{}, false, errors.New("open-pilot pull request head is required")
	}
	if o.env == nil || o.env.giteaReader == nil {
		if allowMissing {
			return pr, openPilotPullRequestKnown(pr), nil
		}
		return GiteaPullRequest{}, false, errors.New("gitea pull request reader not configured")
	}
	resolved, found, err := o.env.giteaReader.FindPullRequestByHead(ctx, run.Inputs["repo"], head)
	if err != nil {
		return GiteaPullRequest{}, false, err
	}
	if found {
		if resolved.Head.SHA == "" {
			resolved.Head.SHA = pr.Head.SHA
		}
		if resolved.Head.SHA == "" {
			if allowMissing {
				return pr, openPilotPullRequestKnown(pr), nil
			}
			return GiteaPullRequest{}, false, errors.New("open-pilot pull request head sha is required")
		}
		return resolved, true, nil
	}
	if pr.Head.SHA != "" {
		return pr, openPilotPullRequestKnown(pr), nil
	}
	if allowMissing {
		return pr, openPilotPullRequestKnown(pr), nil
	}
	return GiteaPullRequest{}, false, errors.New("open-pilot pull request not found by head")
}

func evaluateOpenPilotCIChecks(requiredChecks []string, snapshot GiteaCheckSummary) (openPilotCICheckResult, bool) {
	if len(requiredChecks) == 0 {
		return openPilotCICheckResult{status: "passed", summary: "CI checks passed: no required checks configured"}, true
	}
	hasPending := false
	for _, required := range requiredChecks {
		switch evaluateOpenPilotRequiredCheck(required, snapshot) {
		case "failed":
			return openPilotCICheckResult{status: "failed", summary: "CI checks failed: " + required}, true
		case "pending":
			hasPending = true
		}
	}
	if hasPending {
		return openPilotCICheckResult{}, false
	}
	return openPilotCICheckResult{status: "passed", summary: "CI checks passed: " + strings.Join(requiredChecks, ", ")}, true
}

func evaluateOpenPilotRequiredCheck(required string, snapshot GiteaCheckSummary) string {
	required = strings.TrimSpace(required)
	if required == "" {
		return "pending"
	}
	status := "pending"
	for _, item := range snapshot.Statuses {
		if item.Context != required {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(item.Status)) {
		case "success":
			status = "passed"
		case "failure", "error", "cancelled", "timed_out", "action_required":
			return "failed"
		default:
			status = "pending"
		}
	}
	for _, check := range snapshot.Checks {
		if check.Name != required {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(check.Conclusion)) {
		case "success":
			if strings.EqualFold(strings.TrimSpace(check.Status), "completed") || strings.TrimSpace(check.Status) == "" {
				status = "passed"
			} else {
				status = "pending"
			}
		case "failure", "error", "cancelled", "timed_out", "action_required":
			return "failed"
		default:
			status = "pending"
		}
	}
	return status
}

func buildOpenPilotCICheckArtifact(repo string, pr GiteaPullRequest, mode string, requiredChecks []string, snapshot GiteaCheckSummary, result, summary string) (string, CreateArtifactInput) {
	body := map[string]any{
		"repo":            repo,
		"mode":            mode,
		"result":          result,
		"summary":         summary,
		"required_checks": requiredChecks,
		"pull_request":    pr,
		"checks":          snapshot,
	}
	encoded, _ := json.MarshalIndent(body, "", "  ")
	return string(encoded), CreateArtifactInput{Name: "open-pilot-ci-checks.json", Kind: "json", URI: "data:application/json;charset=utf-8," + url.QueryEscape(string(encoded))}
}

func prCorrelationIDs(pr GiteaPullRequest) map[string]string {
	out := map[string]string{}
	if pr.HTMLURL != "" {
		out["pull_request"] = pr.HTMLURL
	}
	if pr.Number != 0 {
		out["pull_request_number"] = fmt.Sprintf("%d", pr.Number)
	}
	if pr.ID != 0 {
		out["pull_request_id"] = fmt.Sprintf("%d", pr.ID)
	}
	if pr.Head.SHA != "" {
		out["head_sha"] = pr.Head.SHA
	}
	return out
}

func (o openPilotExecutor) persistOpenPilotCICheckFailure(run Run, state map[string]any, gateStatus map[string]string, status string, pr GiteaPullRequest, snapshot *GiteaCheckSummary, err error) (PipelineStepExecution, error) {
	gateStatus["ci_checks"] = status
	state["open-pilot.gate-status"] = gateStatus
	state["open-pilot.pull-request"] = pr
	summary := "CI checks " + status
	if snapshot != nil {
		artifactBody, artifact := buildOpenPilotCICheckArtifact(run.Inputs["repo"], pr, o.env.openPilotCICheckMode, o.env.openPilotCIRequiredChecks, *snapshot, status, summary)
		_ = artifactBody
		if persistErr := o.persistOpenPilotIssueState(run, OpenPilotPhaseFailed, "open-pilot.ci-checks", err.Error(), state); persistErr != nil {
			return PipelineStepExecution{Summary: summary, Artifacts: []CreateArtifactInput{artifact}, CorrelationIDs: prCorrelationIDs(pr)}, errors.Join(err, persistErr)
		}
		return PipelineStepExecution{Summary: summary, Artifacts: []CreateArtifactInput{artifact}, CorrelationIDs: prCorrelationIDs(pr)}, err
	}
	if persistErr := o.persistOpenPilotIssueState(run, OpenPilotPhaseFailed, "open-pilot.ci-checks", err.Error(), state); persistErr != nil {
		return PipelineStepExecution{Summary: summary, CorrelationIDs: prCorrelationIDs(pr)}, errors.Join(err, persistErr)
	}
	return PipelineStepExecution{Summary: summary, CorrelationIDs: prCorrelationIDs(pr)}, err
}

func (e *EnvWorkflowExecutors) openPilotBranchName(repo string, issue string, fetched *GiteaIssue) string {
	base := "issue-" + issue
	if fetched != nil && fetched.Title != "" {
		base += "-" + slug(fetched.Title)
	}
	return e.openPilotBranchPrefix + base
}

func openCodeSessionID(inputs map[string]string) string {
	return firstNonEmpty(inputs["opencode_session_id"], inputs["ocq_session_id"])
}

func openPilotOpenCodeRequestID(run Run, stepName string) string {
	suffix := slug(firstNonEmpty(stepName, "opencode"))
	if suffix == "" {
		suffix = "opencode"
	}
	return run.ID + "-" + suffix
}

func (e *EnvWorkflowExecutors) openPilotPullRequestInput(repo string, issue string, branchName string, issueSummary string, response string, inputs map[string]string) GiteaPullRequestInput {
	base := firstNonEmpty(inputs["base_branch"], e.openPilotBaseBranch)
	title := firstNonEmpty(inputs["pr_title"], "Open Pilot: issue "+issue)
	var body strings.Builder
	body.WriteString("Automated Open Pilot run for `" + repo + "#" + issue + "`.\n\n")
	if issueSummary != "" {
		body.WriteString("Issue: " + issueSummary + "\n\n")
	}
	if response != "" {
		body.WriteString("## Agent summary\n\n" + response + "\n")
	}
	return GiteaPullRequestInput{Repo: repo, Base: base, Head: branchName, Title: title, Body: body.String(), Draft: e.openPilotPRDraft}
}

func (e *EnvWorkflowExecutors) openPilotPushPlan(branchPlan OpenPilotWorktreePlan) OpenPilotPushPlan {
	plan := OpenPilotPushPlan{Mode: e.openPilotGitPushMode, Remote: "origin", Branch: branchPlan.Branch}
	if branchPlan.Executed {
		plan.WorktreePath = branchPlan.WorktreePath
	}
	if e.openPilotGitPushMode == "dry-run" {
		plan.DryRun = true
	}
	if e.openPilotGitPushMode == "allow" && branchPlan.Executed {
		plan.Executed = true
	}
	return plan
}

func openPilotWorktreePlanFromResume(inputs map[string]string) OpenPilotWorktreePlan {
	prefix := "resume.open-pilot.branch-plan."
	plan := OpenPilotWorktreePlan{
		Repo:                inputs[prefix+"repo"],
		RepoPath:            inputs[prefix+"repo_path"],
		BaseBranch:          inputs[prefix+"base_branch"],
		Branch:              firstNonEmpty(inputs[prefix+"branch"], inputs["resume.open-pilot.branch-plan.correlation.branch"]),
		Mode:                inputs[prefix+"mode"],
		PlannedWorktreePath: inputs[prefix+"planned_worktree_path"],
		WorktreePath:        firstNonEmpty(inputs[prefix+"worktree_path"], inputs["resume.open-pilot.branch-plan.correlation.worktree_path"]),
		Executed:            strings.EqualFold(inputs[prefix+"executed"], "true"),
		DryRun:              strings.EqualFold(inputs[prefix+"dry_run"], "true"),
	}
	if plan.PlannedWorktreePath == "" {
		plan.PlannedWorktreePath = inputs["resume.open-pilot.branch-plan.correlation.planned_worktree_path"]
	}
	if plan.WorktreePath != "" {
		plan.Executed = true
	}
	return plan
}

func openPilotPushPlanFromResume(inputs map[string]string) OpenPilotPushPlan {
	prefix := "resume.open-pilot.push-branch."
	return OpenPilotPushPlan{
		Mode:             inputs[prefix+"mode"],
		Remote:           firstNonEmpty(inputs[prefix+"remote"], inputs["resume.open-pilot.push-branch.correlation.remote"]),
		Branch:           firstNonEmpty(inputs[prefix+"branch"], inputs["resume.open-pilot.push-branch.correlation.branch"]),
		WorktreePath:     inputs[prefix+"worktree_path"],
		Executed:         strings.EqualFold(inputs[prefix+"executed"], "true"),
		DryRun:           strings.EqualFold(inputs[prefix+"dry_run"], "true"),
		RemoteHeadExists: strings.EqualFold(firstNonEmpty(inputs[prefix+"remote_head_exists"], inputs["resume.open-pilot.push-branch.correlation.remote_head_exists"]), "true"),
	}
}

func (e *EnvWorkflowExecutors) executeOpenPilotPush(ctx context.Context, plan OpenPilotPushPlan) error {
	switch plan.Mode {
	case "", "disabled", "dry-run":
		return nil
	case "allow":
		if plan.WorktreePath == "" {
			return errors.New("OPEN_PILOT_GIT_PUSH_MODE=allow requires an executed worktree")
		}
		cmd := exec.CommandContext(ctx, "git", "-C", plan.WorktreePath, "push", plan.Remote, "HEAD:"+plan.Branch)
		output, err := cmd.CombinedOutput()
		if err != nil {
			message := strings.TrimSpace(string(output))
			if message == "" {
				message = err.Error()
			}
			return errors.New(message)
		}
		return nil
	default:
		return fmt.Errorf("unsupported OPEN_PILOT_GIT_PUSH_MODE %q", plan.Mode)
	}
}

func (e *EnvWorkflowExecutors) openPilotRemoteBranchExists(ctx context.Context, worktreePath string, remote string, branch string) (bool, error) {
	if strings.TrimSpace(worktreePath) == "" || strings.TrimSpace(remote) == "" || strings.TrimSpace(branch) == "" {
		return false, nil
	}
	cmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "ls-remote", "--exit-code", "--heads", remote, branch)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return strings.TrimSpace(string(output)) != "", nil
	}
	message := strings.TrimSpace(string(output))
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 2 {
		return false, nil
	}
	if strings.Contains(message, "does not appear to be a git repository") || strings.Contains(message, "Could not read from remote repository") || strings.Contains(message, "No such remote") {
		return false, nil
	}
	if message == "" {
		message = err.Error()
	}
	return false, errors.New(message)
}

func (e *EnvWorkflowExecutors) openPilotWorktreeStatus(ctx context.Context, worktreePath string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", worktreePath, "status", "--porcelain=v1")
	output, err := cmd.CombinedOutput()
	message := strings.TrimSpace(string(output))
	if err != nil {
		if message == "" {
			message = err.Error()
		}
		return "", errors.New(message)
	}
	if message == "" {
		return "clean\n", nil
	}
	return message + "\n", nil
}

func (e *EnvWorkflowExecutors) openPilotWorktreeAheadCount(ctx context.Context, plan OpenPilotWorktreePlan) (int, error) {
	if !plan.Executed || strings.TrimSpace(plan.WorktreePath) == "" {
		return 0, nil
	}
	baseBranch := strings.TrimSpace(plan.BaseBranch)
	if baseBranch == "" {
		baseBranch = e.openPilotBaseBranch
	}
	cmd := exec.CommandContext(ctx, "git", "-C", plan.WorktreePath, "rev-list", "--count", baseBranch+"..HEAD")
	output, err := cmd.CombinedOutput()
	message := strings.TrimSpace(string(output))
	if err != nil {
		if message == "" {
			message = err.Error()
		}
		return 0, errors.New(message)
	}
	var ahead int
	if _, err := fmt.Sscanf(message, "%d", &ahead); err != nil {
		return 0, fmt.Errorf("parse git ahead count %q: %w", message, err)
	}
	return ahead, nil
}

func slug(value string) string {
	value = strings.ToLower(value)
	var builder strings.Builder
	lastDash := false
	for _, r := range value {
		isWord := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if isWord {
			builder.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && builder.Len() > 0 {
			builder.WriteRune('-')
			lastDash = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

func (e *EnvWorkflowExecutors) fetchOpenPilotIssue(ctx context.Context, repo string, issue string) ([]byte, string, *GiteaIssue, error) {
	fetched, err := e.issueFetcher.FetchIssue(ctx, repo, issue)
	if err != nil {
		return nil, "", nil, err
	}
	data, err := json.MarshalIndent(fetched, "", "  ")
	if err != nil {
		return nil, "", nil, err
	}
	summary := "Fetched issue"
	if fetched.Title != "" {
		summary = "Fetched issue: " + fetched.Title
	}
	if fetched.State != "" {
		summary += " (" + fetched.State + ")"
	}
	return data, summary, &fetched, nil
}

func (e *EnvWorkflowExecutors) validateOpenPilotIssue(issue GiteaIssue) error {
	if issue.State != "" && issue.State != "open" {
		return fmt.Errorf("open-pilot issue is not open: %s", issue.State)
	}
	labels := map[string]bool{}
	for _, label := range issue.Labels {
		labels[label] = true
	}
	missing := []string{}
	for _, required := range e.openPilotLabels {
		if required != "" && !labels[required] {
			missing = append(missing, required)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("open-pilot issue missing required labels: %s", strings.Join(missing, ", "))
	}
	if e.requireIssueSpec {
		body := strings.ToLower(issue.Body)
		if !strings.Contains(body, "acceptance") || !strings.Contains(body, "test") {
			return fmt.Errorf("open-pilot issue must include acceptance criteria and test instructions")
		}
	}
	return nil
}

func buildOpenPilotPrompt(repo string, issue string, issueSummary string, fetchedIssue *GiteaIssue, inputs map[string]string, branchPlan ...OpenPilotWorktreePlan) string {
	var builder strings.Builder
	builder.WriteString("# Open Pilot Task\n\n")
	builder.WriteString("Repository: " + repo + "\n")
	builder.WriteString("Issue: " + issue + "\n")
	if issueSummary != "" {
		builder.WriteString("Issue summary: " + issueSummary + "\n")
	}
	if command := strings.TrimSpace(inputs["test_command"]); command != "" {
		builder.WriteString("Switchyard verification command: " + command + "\n")
	}
	if len(branchPlan) > 0 {
		builder.WriteString("Branch: " + branchPlan[0].Branch + "\n")
		if branchPlan[0].Executed && branchPlan[0].WorktreePath != "" {
			builder.WriteString("Worktree: " + branchPlan[0].WorktreePath + "\n")
		}
	}
	if fetchedIssue != nil {
		if fetchedIssue.Title != "" {
			builder.WriteString("\n## Issue title\n\n" + fetchedIssue.Title + "\n")
		}
		if len(fetchedIssue.Labels) > 0 {
			builder.WriteString("\n## Labels\n\n" + strings.Join(fetchedIssue.Labels, ", ") + "\n")
		}
		if strings.TrimSpace(fetchedIssue.Body) != "" {
			builder.WriteString("\n## Issue body\n\n" + strings.TrimSpace(fetchedIssue.Body) + "\n")
		}
	}
	builder.WriteString("\n## Instructions\n\nInspect the issue, preserve repository conventions, and make the smallest safe change. ")
	if openPilotAutonomyFullEnabled() {
		builder.WriteString("You may run implementation, inspection, formatting, and test commands inside the assigned worktree. Commit your completed changes on the assigned branch before finishing; Switchyard will verify that the branch is ahead of base, inspect worktree status, push the branch, and create the PR through audited follow-up steps. Do not push or create the PR yourself. Summarize commits, files changed, and verification results with file references.\n")
	} else {
		builder.WriteString("Do not run shell/test commands or push changes yourself; Switchyard will run verification and branch publication through policy-gated steps after agent work. Summarize results with file references.\n")
	}
	if feedback := firstNonEmpty(inputs["ai_review.feedback"], inputs["resume.open-pilot.ai-review.feedback"]); feedback != "" {
		builder.WriteString("\n## Prior AI review feedback\n\n" + feedback + "\n")
	}
	return builder.String()
}

func buildOpenPilotAIReviewPrompt(repo, issue string, inputs map[string]string, state map[string]any) string {
	var builder strings.Builder
	builder.WriteString("# Open Pilot AI Review\n\n")
	builder.WriteString("Repository: " + repo + "\n")
	builder.WriteString("Issue: " + issue + "\n")
	branch := firstNonEmpty(inputs["resume.open-pilot.branch-plan.branch"], inputs["branch"], inputs["open-pilot.branch-name"])
	if branch == "" {
		if branchPlan, ok := state["open-pilot.branch-plan"].(OpenPilotWorktreePlan); ok {
			branch = branchPlan.Branch
		}
	}
	if branch != "" {
		builder.WriteString("Branch: " + branch + "\n")
	}
	prURL := firstNonEmpty(inputs["resume.open-pilot.pr-plan.pull_request_url"], inputs["pull_request_url"])
	if prURL == "" {
		if pr, ok := state["open-pilot.pull-request"].(GiteaPullRequest); ok {
			prURL = pr.HTMLURL
		}
	}
	if prURL != "" {
		builder.WriteString("Pull request URL: " + prURL + "\n")
	} else {
		builder.WriteString("Pull request URL: not available\n")
	}
	verifySummary := firstNonEmpty(inputs["resume.open-pilot.verify-command.summary"], inputs["open-pilot.verify-summary"])
	if verifySummary == "" {
		if summary, ok := state["open-pilot.verify-summary"].(string); ok {
			verifySummary = summary
		}
	}
	if verifySummary != "" {
		builder.WriteString("Local verify summary: " + verifySummary + "\n")
	}
	agentSummary := firstNonEmpty(inputs["resume.open-pilot.agent-work.summary"], inputs["open-pilot.agent-summary"])
	if agentSummary == "" {
		if summary, ok := state["open-pilot.agent-summary"].(string); ok {
			agentSummary = summary
		}
	}
	if agentSummary != "" {
		builder.WriteString("Agent summary: " + agentSummary + "\n")
	}
	agentResponse := firstNonEmpty(inputs["resume.open-pilot.agent-work.response"], inputs["open-pilot.response"])
	if agentResponse == "" {
		if response, ok := state["open-pilot.response"].(string); ok {
			agentResponse = response
		}
	}
	if agentResponse != "" {
		builder.WriteString("Agent response: " + agentResponse + "\n")
	}
	if feedback := firstNonEmpty(inputs["ai_review.feedback"], inputs["resume.open-pilot.ai-review.feedback"]); feedback != "" {
		builder.WriteString("\n## Prior AI review feedback\n\n" + feedback + "\n")
	}
	if gateStatus, ok := state["open-pilot.gate-status"].(map[string]string); ok && len(gateStatus) > 0 {
		builder.WriteString("\n## Gate status\n\n")
		keys := make([]string, 0, len(gateStatus))
		for k := range gateStatus {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			builder.WriteString("- " + k + ": " + gateStatus[k] + "\n")
		}
	}
	builder.WriteString("\n## Output contract\n\nReply with exactly one paragraph using one of these formats:\n\nAPPROVED: <one paragraph>\nCHANGES_REQUESTED: <one paragraph>\n")
	return builder.String()
}

func parseOpenPilotAIReviewResponse(content string) (string, string, error) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "", "", errors.New("open pilot ai review returned empty output")
	}
	normalizedNewlines := strings.ReplaceAll(strings.ReplaceAll(trimmed, "\r\n", "\n"), "\r", "\n")
	upper := strings.ToUpper(trimmed)
	if strings.Count(upper, "APPROVED:")+strings.Count(upper, "CHANGES_REQUESTED:") != 1 {
		return "", "", errors.New("open pilot ai review output must contain exactly one verdict")
	}
	if strings.Contains(normalizedNewlines, "\n\n") {
		return "", "", errors.New("open pilot ai review output must be exactly one paragraph")
	}
	if strings.HasPrefix(upper, "APPROVED:") {
		feedback := normalizeOpenPilotParagraph(strings.TrimSpace(trimmed[len("APPROVED:"):]))
		if feedback == "" {
			return "", "", errors.New("open pilot ai review approved without rationale")
		}
		return "approved", feedback, nil
	}
	if strings.HasPrefix(upper, "CHANGES_REQUESTED:") {
		feedback := normalizeOpenPilotParagraph(strings.TrimSpace(trimmed[len("CHANGES_REQUESTED:"):]))
		if feedback == "" {
			return "", "", errors.New("open pilot ai review requested changes without feedback")
		}
		return "changes_requested", feedback, nil
	}
	return "", "", fmt.Errorf("open pilot ai review output must start with APPROVED: or CHANGES_REQUESTED:")
}

func normalizeOpenPilotParagraph(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func buildOpenPilotAIReviewArtifact(repo, issue string, inputs map[string]string, state map[string]any, verdict, feedback, raw string) string {
	var builder strings.Builder
	builder.WriteString("# Open Pilot AI Review\n\n")
	builder.WriteString("Repository: " + repo + "\n")
	builder.WriteString("Issue: " + issue + "\n")
	branch := firstNonEmpty(inputs["resume.open-pilot.branch-plan.branch"], inputs["branch"], inputs["open-pilot.branch-name"])
	if branch == "" {
		if branchPlan, ok := state["open-pilot.branch-plan"].(OpenPilotWorktreePlan); ok {
			branch = branchPlan.Branch
		}
	}
	if branch != "" {
		builder.WriteString("Branch: " + branch + "\n")
	}
	prURL := firstNonEmpty(inputs["resume.open-pilot.pr-plan.pull_request_url"], inputs["pull_request_url"])
	if prURL == "" {
		if pr, ok := state["open-pilot.pull-request"].(GiteaPullRequest); ok {
			prURL = pr.HTMLURL
		}
	}
	if prURL != "" {
		builder.WriteString("Pull request URL: " + prURL + "\n")
	}
	if verdict != "" {
		builder.WriteString("Verdict: " + strings.ToUpper(verdict) + "\n")
	}
	if feedback != "" {
		builder.WriteString("\n## Feedback\n\n" + feedback + "\n")
	}
	builder.WriteString("\n## Raw model output\n\n" + raw + "\n")
	return builder.String()
}

func splitCSV(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

type devlogExecutor struct {
	env *EnvWorkflowExecutors
}

func (d devlogExecutor) Execute(ctx context.Context, run Run) (WorkflowExecution, error) {
	return d.env.executeDevlog(ctx, run)
}

func (d devlogExecutor) ExecuteSteps(ctx context.Context, run Run, store Store) (WorkflowExecution, error) {
	collect, err := createAndStartStep(store, CreateRunStepInput{RunID: run.ID, Position: 1, Name: "devlog.collect-history", Executor: "internal"})
	if err != nil {
		return WorkflowExecution{}, err
	}
	seed := d.env.buildDevlogMarkdown(run)
	if _, err := store.FinishRunStep(collect.ID, StepSucceeded, "Collected recent Switchyard run history", "", nil); err != nil {
		return WorkflowExecution{}, err
	}

	model, err := d.env.resolveWorkflowModel(string(WorkflowDevlog), run.Inputs)
	if err != nil {
		return WorkflowExecution{}, err
	}
	generate, err := createAndStartStep(store, CreateRunStepInput{RunID: run.ID, Position: 2, Name: "devlog.generate-markdown", Executor: "model", Model: model})
	if err != nil {
		return WorkflowExecution{}, err
	}
	markdown := seed
	generated, genErr := d.env.generateDevlogWithModel(ctx, seed, model)
	if genErr == nil && strings.TrimSpace(generated.Content) != "" {
		markdown = generated.Content
		if _, err := store.FinishRunStep(generate.ID, StepSucceeded, "Generated Markdown with model", "", generated.CorrelationIDs); err != nil {
			return WorkflowExecution{}, err
		}
	} else {
		if _, err := store.FinishRunStep(generate.ID, StepSucceeded, "Used deterministic Markdown fallback", "", nil); err != nil {
			return WorkflowExecution{}, err
		}
		if genErr != nil {
			_, _ = store.AddEvent(run.ID, "devlog.generate.fallback", genErr.Error())
		}
	}

	attach, err := createAndStartStep(store, CreateRunStepInput{RunID: run.ID, Position: 3, Name: "devlog.attach-artifact", Executor: "internal"})
	if err != nil {
		return WorkflowExecution{}, err
	}
	uri := "data:text/markdown;charset=utf-8," + url.QueryEscape(markdown)
	if _, err := store.AddArtifact(CreateArtifactInput{RunID: run.ID, Name: "devlog.md", Kind: "markdown", URI: uri}); err != nil {
		_, _ = store.FinishRunStep(attach.ID, StepFailed, "", err.Error(), nil)
		return WorkflowExecution{}, err
	}
	if _, err := store.FinishRunStep(attach.ID, StepSucceeded, "Attached devlog.md artifact", "", nil); err != nil {
		return WorkflowExecution{}, err
	}

	return WorkflowExecution{Summary: "Generated Devlog markdown artifact", Events: []WorkflowExecutionEvent{{Type: "devlog.generated", Message: "Generated devlog from Switchyard run history"}}}, nil
}

func (d devlogExecutor) ResolvePipelineStepModel(workflowID string, step PipelineStep, inputs map[string]string) (string, error) {
	if step.Model == "" {
		return "", nil
	}
	if step.Model == "${model}" {
		return d.env.resolveWorkflowModel(workflowID, inputs)
	}
	return step.Model, nil
}

func (d devlogExecutor) ExecutePipelineStep(ctx context.Context, run Run, step PipelineStep, state map[string]any) (PipelineStepExecution, error) {
	switch step.ID {
	case "collect-history":
		seed := d.env.buildDevlogMarkdown(run)
		state["devlog.markdown"] = seed
		return PipelineStepExecution{Summary: "Collected recent Switchyard run history"}, nil
	case "generate-markdown":
		seed, _ := state["devlog.markdown"].(string)
		if seed == "" {
			seed = d.env.buildDevlogMarkdown(run)
		}
		markdown := seed
		model := firstNonEmpty(step.Model, d.env.devlogModel)
		generated, genErr := d.env.generateDevlogWithModel(ctx, seed, model)
		if genErr == nil && strings.TrimSpace(generated.Content) != "" {
			markdown = generated.Content
			state["devlog.markdown"] = markdown
			return PipelineStepExecution{Summary: "Generated Markdown with model", CorrelationIDs: generated.CorrelationIDs}, nil
		}
		state["devlog.markdown"] = markdown
		out := PipelineStepExecution{Summary: "Used deterministic Markdown fallback"}
		if genErr != nil {
			out.Events = append(out.Events, WorkflowExecutionEvent{Type: "devlog.generate.fallback", Message: genErr.Error()})
		}
		return out, nil
	case "attach-artifact":
		markdown, _ := state["devlog.markdown"].(string)
		if markdown == "" {
			markdown = d.env.buildDevlogMarkdown(run)
		}
		state["summary"] = "Generated Devlog markdown artifact"
		return PipelineStepExecution{
			Summary: "Attached devlog.md artifact",
			Events:  []WorkflowExecutionEvent{{Type: "devlog.generated", Message: "Generated devlog from Switchyard run history"}},
			Artifacts: []CreateArtifactInput{{
				Name: "devlog.md",
				Kind: "markdown",
				URI:  "data:text/markdown;charset=utf-8," + url.QueryEscape(markdown),
			}},
		}, nil
	default:
		return PipelineStepExecution{}, fmt.Errorf("unsupported devlog pipeline step %q", step.ID)
	}
}

func createAndStartStep(store Store, input CreateRunStepInput) (RunStep, error) {
	step, err := store.CreateRunStep(input)
	if err != nil {
		return RunStep{}, err
	}
	return store.StartRunStep(step.ID)
}

func stubWorkflowExecution(workflowID, message string) func(context.Context, Run) (WorkflowExecution, error) {
	return func(context.Context, Run) (WorkflowExecution, error) {
		return WorkflowExecution{
			Summary: "Recorded Switchyard run envelope for " + workflowID,
			Events:  []WorkflowExecutionEvent{{Type: "workflow.stub", Message: message}},
		}, nil
	}
}

func (e *EnvWorkflowExecutors) executeOpenPilot(ctx context.Context, run Run) (WorkflowExecution, error) {
	if e.openPilotCommand == "" {
		return WorkflowExecution{Summary: "Recorded Switchyard run envelope for open-pilot", Events: []WorkflowExecutionEvent{{Type: "workflow.stub", Message: "OPEN_PILOT_COMMAND is not configured; expected command contract: open-pilot run --repo <owner/repo> --issue <number> --json"}}}, nil
	}
	repo := run.Inputs["repo"]
	issue := run.Inputs["issue"]
	if repo == "" || issue == "" {
		return WorkflowExecution{}, fmt.Errorf("open-pilot requires run inputs repo and issue")
	}
	parts := strings.Fields(e.openPilotCommand)
	if len(parts) == 0 {
		return WorkflowExecution{}, fmt.Errorf("OPEN_PILOT_COMMAND is empty")
	}
	args := append(parts[1:], "run", "--repo", repo, "--issue", issue, "--json")
	cmd := exec.CommandContext(ctx, parts[0], args...)
	output, err := cmd.CombinedOutput()
	message := strings.TrimSpace(string(output))
	if err != nil {
		if message == "" {
			message = err.Error()
		}
		return WorkflowExecution{Events: []WorkflowExecutionEvent{{Type: "open-pilot.failed", Message: message}}}, fmt.Errorf("open-pilot dispatch failed: %w", err)
	}
	if message == "" {
		message = "Open Pilot accepted issue " + issue
	}
	return WorkflowExecution{Summary: "Dispatched Open Pilot issue " + issue, Events: []WorkflowExecutionEvent{{Type: "workflow.dispatch", Message: strings.Join(append([]string{parts[0]}, args...), " ")}, {Type: "open-pilot.accepted", Message: message}}}, nil
}

func (e *EnvWorkflowExecutors) executeChangemaker(ctx context.Context, run Run) (WorkflowExecution, error) {
	if e.changemakerBaseURL == "" {
		return WorkflowExecution{
			Summary: "Recorded Switchyard run envelope for changemaker",
			Events:  []WorkflowExecutionEvent{{Type: "workflow.stub", Message: "CHANGEMAKER_BASE_URL is not configured; Changemaker run was not dispatched"}},
		}, nil
	}
	requestBody := changemakerRunRequest(run.Inputs)
	body, err := json.Marshal(requestBody)
	if err != nil {
		return WorkflowExecution{}, err
	}
	endpoint, err := url.JoinPath(e.changemakerBaseURL, "/api/runs")
	if err != nil {
		return WorkflowExecution{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return WorkflowExecution{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.changemakerToken != "" {
		req.Header.Set("Authorization", "Bearer "+e.changemakerToken)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return WorkflowExecution{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return WorkflowExecution{}, fmt.Errorf("changemaker dispatch failed: %s", resp.Status)
	}
	return WorkflowExecution{
		Summary: "Dispatched Changemaker run",
		Events: []WorkflowExecutionEvent{
			{Type: "workflow.dispatch", Message: "POST " + endpoint},
			{Type: "changemaker.accepted", Message: resp.Status},
		},
	}, nil
}

func changemakerRunRequest(inputs map[string]string) map[string]string {
	request := map[string]string{"kind": "daily"}
	if inputs["kind"] != "" {
		request["kind"] = inputs["kind"]
	}
	if inputs["since"] != "" || inputs["until"] != "" {
		request["kind"] = "manual"
	}
	if inputs["since"] != "" {
		request["since"] = inputs["since"]
	}
	if inputs["until"] != "" {
		request["until"] = inputs["until"]
	}
	return request
}

func (e *EnvWorkflowExecutors) executeDevlog(ctx context.Context, run Run) (WorkflowExecution, error) {
	markdown := e.buildDevlogMarkdown(run)
	if generated, err := e.generateDevlogWithLLM(ctx, markdown); err == nil && strings.TrimSpace(generated) != "" {
		markdown = generated
	}
	uri := "data:text/markdown;charset=utf-8," + url.QueryEscape(markdown)
	return WorkflowExecution{Summary: "Generated Devlog markdown artifact", Events: []WorkflowExecutionEvent{{Type: "devlog.generated", Message: "Generated devlog from Switchyard run history"}}, Artifacts: []CreateArtifactInput{{Name: "devlog.md", Kind: "markdown", URI: uri}}}, nil
}

func (e *EnvWorkflowExecutors) buildDevlogMarkdown(current Run) string {
	var builder strings.Builder
	builder.WriteString("# Switchyard Devlog\n\n")
	builder.WriteString("Generated from Switchyard control-plane run history.\n\n")
	builder.WriteString("## Current run\n\n- Workflow: " + current.WorkflowID + "\n")
	if !current.CreatedAt.IsZero() {
		builder.WriteString("- Created: " + current.CreatedAt.UTC().Format(time.RFC3339) + "\n")
	}
	builder.WriteString("\n## Recent runs\n\n")
	if e.store == nil {
		builder.WriteString("No store was attached to the devlog executor.\n")
		return builder.String()
	}
	count := 0
	for _, recent := range e.store.ListRuns() {
		if recent.ID == current.ID || recent.WorkflowID == string(WorkflowDevlog) {
			continue
		}
		builder.WriteString("- `" + recent.ID + "` " + recent.WorkflowID + " → " + string(recent.Status))
		if recent.Summary != "" {
			builder.WriteString(": " + recent.Summary)
		}
		builder.WriteString("\n")
		if detail, err := e.store.RunDetail(recent.ID); err == nil {
			for _, event := range detail.Events {
				builder.WriteString("  - " + event.Type + ": " + event.Message + "\n")
			}
		}
		count++
		if count == 8 {
			break
		}
	}
	if count == 0 {
		builder.WriteString("No recent non-devlog runs recorded yet.\n")
	}
	return builder.String()
}

func (e *EnvWorkflowExecutors) generateDevlogWithLLM(ctx context.Context, seed string) (string, error) {
	response, err := e.generateDevlogWithModel(ctx, seed)
	return response.Content, err
}

func (e *EnvWorkflowExecutors) generateDevlogWithModel(ctx context.Context, seed string, modelOverride ...string) (ModelResponse, error) {
	model := e.devlogModel
	if len(modelOverride) > 0 && strings.TrimSpace(modelOverride[0]) != "" {
		model = strings.TrimSpace(modelOverride[0])
	}
	return e.modelExecutor.Generate(ctx, ModelRequest{Model: model, Messages: []ModelMessage{{Role: "system", Content: "Write a concise technical devlog in Markdown. Keep facts grounded in the provided run history."}, {Role: "user", Content: seed}}})
}

func (e *EnvWorkflowExecutors) resolveWorkflowModel(workflowID string, inputs map[string]string) (string, error) {
	if model := strings.TrimSpace(inputs["model"]); model != "" {
		allowed := e.devlogAllowedModels
		if workflowID == string(WorkflowOpenPilot) {
			allowed = e.openPilotAllowedModels
		}
		if !allowed[model] {
			return "", fmt.Errorf("model %q is not allowed for workflow %s", model, workflowID)
		}
		return model, nil
	}
	switch workflowID {
	case string(WorkflowOpenPilot):
		return e.openPilotModel, nil
	case string(WorkflowDevlog):
		return e.devlogModel, nil
	default:
		return firstNonEmpty(e.devlogModel, e.openPilotModel), nil
	}
}
