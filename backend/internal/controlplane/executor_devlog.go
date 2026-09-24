package controlplane

import (
	"context"
	"fmt"
	"strings"
	"time"
)

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
		if _, err := store.FinishRunStep(generate.ID, StepSucceeded, "Used deterministic Markdown fallback", "", generated.CorrelationIDs); err != nil {
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
	uri := inlineArtifactURI("text/markdown;charset=utf-8", markdown)
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
		generated, genErr := d.env.generateDevlogWithModel(ctx, applyPipelineProfilePrompt(step, seed), model)
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
				URI:  inlineArtifactURI("text/markdown;charset=utf-8", markdown),
			}},
		}, nil
	default:
		return PipelineStepExecution{}, fmt.Errorf("unsupported devlog pipeline step %q", step.ID)
	}
}

func (e *EnvWorkflowExecutors) executeDevlog(ctx context.Context, run Run) (WorkflowExecution, error) {
	markdown := e.buildDevlogMarkdown(run)
	if generated, err := e.generateDevlogWithLLM(ctx, markdown); err == nil && strings.TrimSpace(generated) != "" {
		markdown = generated
	}
	uri := inlineArtifactURI("text/markdown;charset=utf-8", markdown)
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
	return e.modelExecutor.Generate(ctx, ModelRequest{Model: model, Messages: []ModelMessage{{Role: "system", Content: "Write a concise technical devlog in Markdown. Keep facts grounded in the provided run history."}, {Role: "user", Content: seed}}, PreAttempt: preAttemptFromContext(ctx)})
}
