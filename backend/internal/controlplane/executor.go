package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type WorkflowExecution struct {
	Summary   string
	Events    []WorkflowExecutionEvent
	Artifacts []CreateArtifactInput
	Status    RunStatus
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
	store                        Store
	openPilotCommand             string
	ocqBaseURL                   string
	ocqGatewayKey                string
	llamaLineBaseURL             string
	llamaLineAPIKey              string
	devlogModel                  string
	openPilotModel               string
	devlogAllowedModels          map[string]bool
	openPilotAllowedModels       map[string]bool
	openPilotLabels              []string
	openPilotCICheckMode         string
	openPilotCIAutoRepairMode    string
	openPilotMergeMode           string
	openPilotCloseIssueMode      string
	openPilotCIRequiredChecks    []string
	openPilotCICheckTimeout      time.Duration
	openPilotCICheckInterval     time.Duration
	requireIssueSpec             bool
	openPilotWriteMode           string
	openPilotGitPushMode         string
	openPilotAutoCommitMode      string
	openPilotBaseBranch          string
	openPilotBranchPrefix        string
	openPilotPRDraft             bool
	openPilotWorktreeMode        string
	openPilotWorktreeCleanupMode string
	modelExecutor                ModelBroker
	openCodeExecutor             OpenCodeExecutor
	issueFetcher                 IssueFetcher
	giteaReader                  GiteaPullRequestReader
	giteaMerger                  GiteaPullRequestMerger
	giteaWriter                  GiteaWriter
	commandRunner                CommandRunner
	worktreeManager              OpenPilotWorktreeManager
	client                       *http.Client
}

func NewEnvWorkflowExecutors(store ...Store) *EnvWorkflowExecutors {
	return NewEnvWorkflowExecutorsWithModel(nil, store...)
}

func NewEnvWorkflowExecutorsWithModel(model ModelGeneration, store ...Store) *EnvWorkflowExecutors {
	var configuredStore Store
	if len(store) > 0 {
		configuredStore = store[0]
	}
	autonomyFull := openPilotAutonomyFullEnabled()
	openPilotWriteMode := firstNonEmpty(os.Getenv("OPEN_PILOT_GITEA_WRITE_MODE"), "disabled")
	openPilotGitPushMode := firstNonEmpty(os.Getenv("OPEN_PILOT_GIT_PUSH_MODE"), "disabled")
	openPilotAutoCommitMode := firstNonEmpty(os.Getenv("OPEN_PILOT_AUTO_COMMIT_MODE"), "disabled")
	if autonomyFull {
		openPilotWriteMode = firstNonEmpty(os.Getenv("OPEN_PILOT_GITEA_WRITE_MODE"), "allow")
		openPilotGitPushMode = firstNonEmpty(os.Getenv("OPEN_PILOT_GIT_PUSH_MODE"), "allow")
		openPilotAutoCommitMode = firstNonEmpty(os.Getenv("OPEN_PILOT_AUTO_COMMIT_MODE"), "allow")
	}
	ciCheckMode := firstNonEmpty(os.Getenv("OPEN_PILOT_CI_CHECK_MODE"), "disabled")
	if autonomyFull && strings.TrimSpace(os.Getenv("OPEN_PILOT_CI_CHECK_MODE")) == "" {
		ciCheckMode = "require-pass"
	}
	ciAutoRepairMode := firstNonEmpty(os.Getenv("OPEN_PILOT_CI_AUTO_REPAIR_MODE"), "disabled")
	worktreeMode := firstNonEmpty(os.Getenv("OPEN_PILOT_WORKTREE_MODE"), string(OpenPilotWorktreeDisabled))
	worktreeCleanup := firstNonEmpty(os.Getenv("OPEN_PILOT_WORKTREE_CLEANUP"), string(OpenPilotWorktreeCleanupDisabled))
	if autonomyFull {
		worktreeMode = firstNonEmpty(os.Getenv("OPEN_PILOT_WORKTREE_MODE"), string(OpenPilotWorktreeAllow))
		worktreeCleanup = firstNonEmpty(os.Getenv("OPEN_PILOT_WORKTREE_CLEANUP"), string(OpenPilotWorktreeCleanupOnSuccess))
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
	env := &EnvWorkflowExecutors{
		store:                        configuredStore,
		openPilotCommand:             strings.TrimSpace(os.Getenv("OPEN_PILOT_COMMAND")),
		devlogModel:                  firstNonEmpty(os.Getenv("DEVLOG_MODEL"), "gpt-5.4"),
		openPilotModel:               firstNonEmpty(os.Getenv("OPEN_PILOT_MODEL"), "gpt-5.4"),
		devlogAllowedModels:          setFromCSV(firstNonEmpty(os.Getenv("DEVLOG_ALLOWED_MODELS"), firstNonEmpty(os.Getenv("DEVLOG_MODEL"), "gpt-5.4"))),
		openPilotAllowedModels:       setFromCSV(firstNonEmpty(os.Getenv("OPEN_PILOT_ALLOWED_MODELS"), firstNonEmpty(os.Getenv("OPEN_PILOT_MODEL"), "gpt-5.4"))),
		openPilotLabels:              splitCSV(firstNonEmpty(os.Getenv("OPEN_PILOT_REQUIRED_LABELS"), "ready-for-agent,agent:queued")),
		openPilotCICheckMode:         ciCheckMode,
		openPilotCIAutoRepairMode:    ciAutoRepairMode,
		openPilotMergeMode:           mergeMode,
		openPilotCloseIssueMode:      closeIssueMode,
		openPilotCIRequiredChecks:    requiredChecks,
		openPilotCICheckTimeout:      ciCheckTimeout,
		openPilotCICheckInterval:     ciCheckInterval,
		requireIssueSpec:             strings.EqualFold(os.Getenv("OPEN_PILOT_REQUIRE_ISSUE_SPEC"), "true"),
		openPilotWriteMode:           openPilotWriteMode,
		openPilotGitPushMode:         openPilotGitPushMode,
		openPilotAutoCommitMode:      openPilotAutoCommitMode,
		openPilotBaseBranch:          firstNonEmpty(os.Getenv("OPEN_PILOT_BASE_BRANCH"), "main"),
		openPilotBranchPrefix:        firstNonEmpty(os.Getenv("OPEN_PILOT_BRANCH_PREFIX"), "open-pilot/"),
		openPilotPRDraft:             !strings.EqualFold(os.Getenv("OPEN_PILOT_PR_DRAFT"), "false"),
		openPilotWorktreeMode:        worktreeMode,
		openPilotWorktreeCleanupMode: worktreeCleanup,
		modelExecutor:                model,
		openCodeExecutor:             NewEnvOpenCodeExecutor(),
		issueFetcher:                 giteaClient,
		giteaReader:                  giteaClient,
		giteaMerger:                  giteaClient,
		giteaWriter:                  giteaClient,
		commandRunner:                NewEnvCommandRunner(),
		worktreeManager:              NewEnvOpenPilotWorktreeManager(),
		client:                       &http.Client{Timeout: 10 * time.Second},
	}
	if env.modelExecutor == nil {
		env.modelExecutor = NewEnvModelBroker()
	}
	return env
}

func (e *EnvWorkflowExecutors) ExecutorFor(workflowID string) WorkflowExecutor {
	switch workflowID {
	case string(WorkflowOpenPilot):
		return openPilotExecutor{env: e}
	case string(WorkflowDevlog):
		return devlogExecutor{env: e}
	default:
		return genericWorkflowExecutor{env: e}
	}
}

type genericWorkflowExecutor struct {
	env *EnvWorkflowExecutors
}

func (g genericWorkflowExecutor) Execute(ctx context.Context, run Run) (WorkflowExecution, error) {
	_ = ctx
	return WorkflowExecution{Summary: "Recorded Switchyard run envelope for " + run.WorkflowID}, nil
}

func (g genericWorkflowExecutor) ResolvePipelineStepModel(workflowID string, step PipelineStep, inputs map[string]string) (string, error) {
	if step.Model == "" {
		if model := strings.TrimSpace(inputs["model"]); model != "" {
			return model, nil
		}
		if g.env == nil {
			return "", fmt.Errorf("no model broker configured for workflow %s", workflowID)
		}
		return g.env.resolveWorkflowModel(workflowID, inputs)
	}
	if step.Model == "${model}" {
		if model := strings.TrimSpace(inputs["model"]); model != "" {
			return model, nil
		}
		if g.env == nil {
			return "", fmt.Errorf("no model broker configured for workflow %s", workflowID)
		}
		return g.env.resolveWorkflowModel(workflowID, inputs)
	}
	return step.Model, nil
}

func (g genericWorkflowExecutor) ExecutePipelineStep(ctx context.Context, run Run, step PipelineStep, state map[string]any) (PipelineStepExecution, error) {
	if state == nil {
		state = map[string]any{}
	}
	renderedInputs := renderPipelineStepInputs(step.Inputs, run.Inputs)
	switch step.Executor {
	case "internal":
		return g.executeGenericInternalStep(step, renderedInputs, state), nil
	case "model":
		return g.executeGenericModelStep(ctx, run, step, renderedInputs, state)
	case "command":
		return g.executeGenericCommandStep(ctx, run, step, renderedInputs, state)
	case "http":
		return g.executeGenericHTTPStep(ctx, step, renderedInputs)
	default:
		return PipelineStepExecution{}, fmt.Errorf("unsupported pipeline step executor %q", step.Executor)
	}
}

func (g genericWorkflowExecutor) executeGenericInternalStep(step PipelineStep, inputs map[string]string, state map[string]any) PipelineStepExecution {
	out := PipelineStepExecution{Inputs: inputs}
	if summary := strings.TrimSpace(firstNonEmpty(inputs["summary"], inputs["template"])); summary != "" {
		out.Summary = summary
	} else if len(inputs) > 0 {
		out.Summary = "Rendered step inputs"
	} else {
		out.Summary = "Completed internal step"
	}
	artifactName := strings.TrimSpace(inputs["artifact_name"])
	if artifactName != "" {
		artifactKind := firstNonEmpty(inputs["artifact_kind"], "json")
		artifactBody, _ := json.MarshalIndent(inputs, "", "  ")
		uriPrefix := "data:application/json;charset=utf-8,"
		if artifactKind == "markdown" {
			artifactBody = []byte(renderPipelineStepMarkdown(inputs))
			uriPrefix = "data:text/markdown;charset=utf-8,"
		}
		out.Artifacts = append(out.Artifacts, CreateArtifactInput{Name: artifactName, Kind: artifactKind, URI: uriPrefix + url.QueryEscape(string(artifactBody))})
	} else if len(inputs) > 0 {
		artifactKind := firstNonEmpty(inputs["artifact_kind"], "json")
		if artifactKind == "markdown" {
			artifactName = step.ID + ".md"
		} else {
			artifactName = step.ID + ".json"
		}
		artifactBody, _ := json.MarshalIndent(inputs, "", "  ")
		uriPrefix := "data:application/json;charset=utf-8,"
		if artifactKind == "markdown" {
			artifactBody = []byte(renderPipelineStepMarkdown(inputs))
			uriPrefix = "data:text/markdown;charset=utf-8,"
		}
		out.Artifacts = append(out.Artifacts, CreateArtifactInput{Name: artifactName, Kind: artifactKind, URI: uriPrefix + url.QueryEscape(string(artifactBody))})
	}
	state["summary"] = out.Summary
	return out
}

func (g genericWorkflowExecutor) executeGenericModelStep(ctx context.Context, run Run, step PipelineStep, inputs map[string]string, state map[string]any) (PipelineStepExecution, error) {
	if g.env == nil || g.env.modelExecutor == nil {
		return PipelineStepExecution{}, fmt.Errorf("no model broker configured")
	}
	prompt := strings.TrimSpace(firstNonEmpty(inputs["prompt"], inputs["template"]))
	if prompt == "" {
		return PipelineStepExecution{}, fmt.Errorf("model step %q requires prompt input", step.ID)
	}
	model := strings.TrimSpace(step.Model)
	if model == "" || model == "${model}" {
		resolved, err := g.ResolvePipelineStepModel(run.WorkflowID, step, run.Inputs)
		if err != nil {
			return PipelineStepExecution{}, err
		}
		model = resolved
	}
	prompt = applyPipelineProfilePrompt(step, prompt)
	response, err := g.env.modelExecutor.Generate(ctx, ModelRequest{Model: model, Messages: []ModelMessage{{Role: "user", Content: prompt}}, PreAttempt: preAttemptFromContext(ctx)})
	if err != nil {
		return PipelineStepExecution{CorrelationIDs: response.CorrelationIDs, Inputs: map[string]string{"model": model, "prompt": prompt}}, err
	}
	out := PipelineStepExecution{
		Summary:        "Generated model output",
		Artifacts:      []CreateArtifactInput{{Name: step.ID + ".md", Kind: "markdown", URI: "data:text/markdown;charset=utf-8," + url.QueryEscape(response.Content)}},
		CorrelationIDs: response.CorrelationIDs,
		Inputs:         map[string]string{"model": model, "prompt": prompt},
	}
	state["summary"] = out.Summary
	return out, nil
}

func (g genericWorkflowExecutor) executeGenericCommandStep(ctx context.Context, run Run, step PipelineStep, inputs map[string]string, state map[string]any) (PipelineStepExecution, error) {
	if g.env == nil || g.env.commandRunner == nil {
		return PipelineStepExecution{}, fmt.Errorf("no command runner configured")
	}
	command := strings.TrimSpace(firstNonEmpty(inputs["command"], run.Inputs["command"]))
	if command == "" {
		return PipelineStepExecution{}, fmt.Errorf("command step %q requires a command", step.ID)
	}
	result, err := g.env.commandRunner.Run(ctx, command)
	correlation := map[string]string{"policy_reason": result.Decision.Reason}
	if result.Decision.DryRun {
		correlation["policy_mode"] = string(CommandPolicyDryRun)
	}
	out := PipelineStepExecution{Inputs: map[string]string{"command": command}, CorrelationIDs: correlation, Summary: result.Output}
	if out.Summary == "" {
		out.Summary = "Command allowed"
	}
	state["summary"] = out.Summary
	if err != nil {
		return out, err
	}
	return out, nil
}

func (g genericWorkflowExecutor) executeGenericHTTPStep(ctx context.Context, step PipelineStep, inputs map[string]string) (PipelineStepExecution, error) {
	allowHTTP, _ := strconv.ParseBool(strings.TrimSpace(inputs["allow_http"]))
	if !allowHTTP {
		return PipelineStepExecution{}, fmt.Errorf("http executor requires allow_http=true for step %q", step.ID)
	}
	target, err := url.Parse(strings.TrimSpace(inputs["url"]))
	if err != nil || target.Hostname() == "" || target.User != nil {
		return PipelineStepExecution{}, fmt.Errorf("http executor requires a valid URL without userinfo")
	}
	if target.Scheme != "https" && !(target.Scheme == "http" && strings.EqualFold(strings.TrimSpace(os.Getenv("SWITCHYARD_HTTP_ALLOW_INSECURE")), "true")) {
		return PipelineStepExecution{}, fmt.Errorf("http executor requires HTTPS")
	}
	if !httpExecutorHostAllowed(target.Hostname()) {
		return PipelineStepExecution{}, fmt.Errorf("http executor host %q is not allowlisted", target.Hostname())
	}
	method := strings.ToUpper(strings.TrimSpace(firstNonEmpty(inputs["method"], http.MethodGet)))
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch:
	default:
		return PipelineStepExecution{}, fmt.Errorf("http executor method %q is not allowed", method)
	}
	body := strings.NewReader(inputs["body"])
	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return PipelineStepExecution{}, err
	}
	request.Header.Set("Accept", firstNonEmpty(strings.TrimSpace(inputs["accept"]), "application/json"))
	if method != http.MethodGet {
		request.Header.Set("Content-Type", firstNonEmpty(strings.TrimSpace(inputs["content_type"]), "application/json"))
	}
	timeout := 15 * time.Second
	if parsed, parseErr := time.ParseDuration(strings.TrimSpace(os.Getenv("SWITCHYARD_HTTP_TIMEOUT"))); parseErr == nil && parsed > 0 && parsed <= 2*time.Minute {
		timeout = parsed
	}
	client := &http.Client{Timeout: timeout, CheckRedirect: func(next *http.Request, via []*http.Request) error {
		if len(via) >= 3 || next.URL.Scheme != target.Scheme || !httpExecutorHostAllowed(next.URL.Hostname()) {
			return http.ErrUseLastResponse
		}
		return nil
	}}
	response, err := client.Do(request)
	if err != nil {
		return PipelineStepExecution{}, err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, int64(artifactMaxBytes())+1))
	if err != nil {
		return PipelineStepExecution{}, err
	}
	if len(responseBody) > artifactMaxBytes() {
		return PipelineStepExecution{}, fmt.Errorf("http executor response exceeds artifact limit")
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return PipelineStepExecution{}, fmt.Errorf("http executor returned %s", response.Status)
	}
	artifactName := firstNonEmpty(strings.TrimSpace(inputs["artifact_name"]), step.ID+".response")
	artifactKind := firstNonEmpty(strings.TrimSpace(inputs["artifact_kind"]), "http-response")
	uri := "data:application/octet-stream," + url.QueryEscape(string(responseBody))
	if len(uri) > artifactMaxBytes() {
		return PipelineStepExecution{}, fmt.Errorf("encoded http response exceeds artifact limit")
	}
	return PipelineStepExecution{Summary: fmt.Sprintf("HTTP %s returned %d", method, response.StatusCode), Inputs: map[string]string{"url": target.Redacted(), "method": method, "status": strconv.Itoa(response.StatusCode)}, Artifacts: []CreateArtifactInput{{Name: artifactName, Kind: artifactKind, URI: uri}}}, nil
}

func httpExecutorHostAllowed(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, allowed := range strings.Split(os.Getenv("SWITCHYARD_HTTP_ALLOWED_HOSTS"), ",") {
		allowed = strings.ToLower(strings.TrimSpace(allowed))
		if allowed == host || (strings.HasPrefix(allowed, "*.") && strings.HasSuffix(host, strings.TrimPrefix(allowed, "*"))) {
			return true
		}
	}
	return false
}

func renderPipelineStepInputs(inputs, runInputs map[string]string) map[string]string {
	if len(inputs) == 0 {
		return nil
	}
	rendered := make(map[string]string, len(inputs))
	for key, value := range inputs {
		rendered[key] = renderTemplateValues(value, runInputs)
	}
	return rendered
}

func renderPipelineStepMarkdown(inputs map[string]string) string {
	if len(inputs) == 0 {
		return ""
	}
	keys := make([]string, 0, len(inputs))
	for key := range inputs {
		if key == "artifact_name" || key == "artifact_kind" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	for _, key := range keys {
		builder.WriteString("- ")
		builder.WriteString(key)
		builder.WriteString(": ")
		builder.WriteString(inputs[key])
		builder.WriteString("\n")
	}
	return strings.TrimSpace(builder.String())
}

func renderTemplateValues(template string, values map[string]string) string {
	if template == "" || len(values) == 0 {
		return template
	}
	rendered := template
	for key, value := range values {
		rendered = strings.ReplaceAll(rendered, "{{"+key+"}}", value)
	}
	return rendered
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

func createAndStartStep(store Store, input CreateRunStepInput) (RunStep, error) {
	step, err := store.CreateRunStep(input)
	if err != nil {
		return RunStep{}, err
	}
	return store.StartRunStep(step.ID)
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
