package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
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
	store              Store
	changemakerBaseURL string
	changemakerToken   string
	openPilotCommand   string
	ocqBaseURL         string
	ocqGatewayKey      string
	llamaLineBaseURL   string
	llamaLineAPIKey    string
	devlogModel        string
	modelExecutor      ModelExecutor
	client             *http.Client
}

func NewEnvWorkflowExecutors(store ...Store) *EnvWorkflowExecutors {
	var configuredStore Store
	if len(store) > 0 {
		configuredStore = store[0]
	}
	return &EnvWorkflowExecutors{
		store:              configuredStore,
		changemakerBaseURL: strings.TrimRight(os.Getenv("CHANGEMAKER_BASE_URL"), "/"),
		changemakerToken:   os.Getenv("CHANGEMAKER_ADMIN_TOKEN"),
		openPilotCommand:   strings.TrimSpace(os.Getenv("OPEN_PILOT_COMMAND")),
		ocqBaseURL:         strings.TrimRight(os.Getenv("OCQ_BASE_URL"), "/"),
		ocqGatewayKey:      os.Getenv("OCQ_GATEWAY_KEY"),
		llamaLineBaseURL:   strings.TrimRight(os.Getenv("LLAMA_LINE_BASE_URL"), "/"),
		llamaLineAPIKey:    os.Getenv("LLAMA_LINE_API_KEY"),
		devlogModel:        firstNonEmpty(os.Getenv("DEVLOG_MODEL"), "opencode:gpt-5.5"),
		modelExecutor:      NewEnvModelExecutor(),
		client:             &http.Client{Timeout: 10 * time.Second},
	}
}

func (e *EnvWorkflowExecutors) ExecutorFor(workflowID string) WorkflowExecutor {
	switch workflowID {
	case string(WorkflowChangemaker):
		return WorkflowExecutorFunc(e.executeChangemaker)
	case string(WorkflowOpenPilot):
		return WorkflowExecutorFunc(e.executeOpenPilot)
	case string(WorkflowDevlog):
		return devlogExecutor{env: e}
	default:
		return WorkflowExecutorFunc(stubWorkflowExecution(workflowID, "Workflow execution adapter is not wired yet"))
	}
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
