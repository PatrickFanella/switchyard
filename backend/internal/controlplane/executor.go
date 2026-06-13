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
	store                 Store
	changemakerBaseURL    string
	changemakerToken      string
	openPilotCommand      string
	ocqBaseURL            string
	ocqGatewayKey         string
	llamaLineBaseURL      string
	llamaLineAPIKey       string
	devlogModel           string
	openPilotModel        string
	openPilotLabels       []string
	requireIssueSpec      bool
	openPilotWriteMode    string
	openPilotBaseBranch   string
	openPilotBranchPrefix string
	openPilotPRDraft      bool
	modelExecutor         ModelExecutor
	ocqExecutor           OCQExecutor
	issueFetcher          IssueFetcher
	giteaWriter           GiteaWriter
	commandRunner         CommandRunner
	client                *http.Client
}

func NewEnvWorkflowExecutors(store ...Store) *EnvWorkflowExecutors {
	var configuredStore Store
	if len(store) > 0 {
		configuredStore = store[0]
	}
	return &EnvWorkflowExecutors{
		store:                 configuredStore,
		changemakerBaseURL:    strings.TrimRight(os.Getenv("CHANGEMAKER_BASE_URL"), "/"),
		changemakerToken:      os.Getenv("CHANGEMAKER_ADMIN_TOKEN"),
		openPilotCommand:      strings.TrimSpace(os.Getenv("OPEN_PILOT_COMMAND")),
		ocqBaseURL:            strings.TrimRight(os.Getenv("OCQ_BASE_URL"), "/"),
		ocqGatewayKey:         os.Getenv("OCQ_GATEWAY_KEY"),
		llamaLineBaseURL:      strings.TrimRight(os.Getenv("LLAMA_LINE_BASE_URL"), "/"),
		llamaLineAPIKey:       os.Getenv("LLAMA_LINE_API_KEY"),
		devlogModel:           firstNonEmpty(os.Getenv("DEVLOG_MODEL"), "opencode:gpt-5.5"),
		openPilotModel:        firstNonEmpty(os.Getenv("OPEN_PILOT_MODEL"), "opencode:gpt-5.5"),
		openPilotLabels:       splitCSV(firstNonEmpty(os.Getenv("OPEN_PILOT_REQUIRED_LABELS"), "ready-for-agent,agent:queued")),
		requireIssueSpec:      strings.EqualFold(os.Getenv("OPEN_PILOT_REQUIRE_ISSUE_SPEC"), "true"),
		openPilotWriteMode:    firstNonEmpty(os.Getenv("OPEN_PILOT_GITEA_WRITE_MODE"), "disabled"),
		openPilotBaseBranch:   firstNonEmpty(os.Getenv("OPEN_PILOT_BASE_BRANCH"), "main"),
		openPilotBranchPrefix: firstNonEmpty(os.Getenv("OPEN_PILOT_BRANCH_PREFIX"), "open-pilot/"),
		openPilotPRDraft:      !strings.EqualFold(os.Getenv("OPEN_PILOT_PR_DRAFT"), "false"),
		modelExecutor:         NewEnvModelExecutor(),
		ocqExecutor:           NewEnvOCQExecutor(),
		issueFetcher:          NewEnvGiteaClient(),
		giteaWriter:           NewEnvGiteaClient(),
		commandRunner:         NewEnvCommandRunner(),
		client:                &http.Client{Timeout: 10 * time.Second},
	}
}

func (e *EnvWorkflowExecutors) ExecutorFor(workflowID string) WorkflowExecutor {
	switch workflowID {
	case string(WorkflowChangemaker):
		return WorkflowExecutorFunc(e.executeChangemaker)
	case string(WorkflowOpenPilot):
		return openPilotExecutor{env: e}
	case string(WorkflowDevlog):
		return devlogExecutor{env: e}
	default:
		return WorkflowExecutorFunc(stubWorkflowExecution(workflowID, "Workflow execution adapter is not wired yet"))
	}
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
	branchPlan := map[string]string{"repo": repo, "base_branch": firstNonEmpty(run.Inputs["base_branch"], o.env.openPilotBaseBranch), "branch": branchName, "mode": o.env.openPilotWriteMode}
	branch, err := createAndStartStep(store, CreateRunStepInput{RunID: run.ID, Position: 2, Name: "open-pilot.branch-plan", Executor: "internal", Inputs: branchPlan})
	if err != nil {
		return WorkflowExecution{}, err
	}
	branchBytes, _ := json.MarshalIndent(branchPlan, "", "  ")
	if _, err := store.AddArtifact(CreateArtifactInput{RunID: run.ID, Name: "open-pilot-branch-plan.json", Kind: "json", URI: "data:application/json;charset=utf-8," + url.QueryEscape(string(branchBytes))}); err != nil {
		_, _ = store.FinishRunStep(branch.ID, StepFailed, "", err.Error(), nil)
		return WorkflowExecution{}, err
	}
	if _, err := store.FinishRunStep(branch.ID, StepSucceeded, "Planned branch "+branchName, "", nil); err != nil {
		return WorkflowExecution{}, err
	}

	prepare, err := createAndStartStep(store, CreateRunStepInput{RunID: run.ID, Position: 3, Name: "open-pilot.prepare-prompt", Executor: "internal"})
	if err != nil {
		return WorkflowExecution{}, err
	}
	prompt := buildOpenPilotPrompt(repo, issue, issueSummary)
	if _, err := store.AddArtifact(CreateArtifactInput{RunID: run.ID, Name: "open-pilot-prompt.md", Kind: "markdown", URI: "data:text/markdown;charset=utf-8," + url.QueryEscape(prompt)}); err != nil {
		_, _ = store.FinishRunStep(prepare.ID, StepFailed, "", err.Error(), nil)
		return WorkflowExecution{}, err
	}
	if _, err := store.FinishRunStep(prepare.ID, StepSucceeded, "Prepared Open Pilot implementation prompt", "", nil); err != nil {
		return WorkflowExecution{}, err
	}

	agent, err := createAndStartStep(store, CreateRunStepInput{RunID: run.ID, Position: 4, Name: "open-pilot.agent-work", Executor: "opencode", Model: o.env.openPilotModel})
	if err != nil {
		return WorkflowExecution{}, err
	}
	response, err := o.env.ocqExecutor.SendPrompt(ctx, OCQRequest{Model: o.env.openPilotModel, Prompt: prompt, SessionID: run.Inputs["ocq_session_id"]})
	if err != nil {
		_, _ = store.FinishRunStep(agent.ID, StepSucceeded, "OCQ not configured; prompt prepared for operator handoff", "", nil)
		_, _ = store.AddEvent(run.ID, "open-pilot.agent.fallback", err.Error())
		return WorkflowExecution{Summary: "Prepared Open Pilot prompt for issue " + issue, Events: []WorkflowExecutionEvent{{Type: "open-pilot.prepared", Message: "Prompt artifact ready for " + repo + "#" + issue}}}, nil
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
		result, err := o.env.commandRunner.Run(ctx, command)
		correlation := map[string]string{"policy_reason": result.Decision.Reason}
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
	prInput := o.env.openPilotPullRequestInput(repo, issue, branchName, issueSummary, response.Content, run.Inputs)
	prStep, err := createAndStartStep(store, CreateRunStepInput{RunID: run.ID, Position: 6, Name: "open-pilot.pr-plan", Executor: "gitea", Inputs: map[string]string{"mode": o.env.openPilotWriteMode, "head": prInput.Head, "base": prInput.Base}})
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

func (e *EnvWorkflowExecutors) openPilotBranchName(repo string, issue string, fetched *GiteaIssue) string {
	base := "issue-" + issue
	if fetched != nil && fetched.Title != "" {
		base += "-" + slug(fetched.Title)
	}
	return e.openPilotBranchPrefix + base
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

func buildOpenPilotPrompt(repo string, issue string, issueSummary string) string {
	var builder strings.Builder
	builder.WriteString("# Open Pilot Task\n\n")
	builder.WriteString("Repository: " + repo + "\n")
	builder.WriteString("Issue: " + issue + "\n")
	if issueSummary != "" {
		builder.WriteString("Issue summary: " + issueSummary + "\n")
	}
	builder.WriteString("\nInspect the issue, preserve repository conventions, make the smallest safe change, run the requested verification, and summarize results with file references.\n")
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

	generate, err := createAndStartStep(store, CreateRunStepInput{RunID: run.ID, Position: 2, Name: "devlog.generate-markdown", Executor: "model", Model: d.env.devlogModel})
	if err != nil {
		return WorkflowExecution{}, err
	}
	markdown := seed
	generated, genErr := d.env.generateDevlogWithModel(ctx, seed)
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

func (e *EnvWorkflowExecutors) generateDevlogWithModel(ctx context.Context, seed string) (ModelResponse, error) {
	return e.modelExecutor.Generate(ctx, ModelRequest{Model: e.devlogModel, Messages: []ModelMessage{{Role: "system", Content: "Write a concise technical devlog in Markdown. Keep facts grounded in the provided run history."}, {Role: "user", Content: seed}}})
}
