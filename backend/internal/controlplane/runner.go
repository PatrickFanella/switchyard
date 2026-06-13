package controlplane

import (
	"context"
	"sort"
)

type PipelineStepExecution struct {
	Summary        string
	Events         []WorkflowExecutionEvent
	Artifacts      []CreateArtifactInput
	Inputs         map[string]string
	CorrelationIDs map[string]string
	Halt           bool
}

type PipelineStepExecutor interface {
	ExecutePipelineStep(ctx context.Context, run Run, step PipelineStep, state map[string]any) (PipelineStepExecution, error)
}

type PipelineStepModelResolver interface {
	ResolvePipelineStepModel(workflowID string, step PipelineStep, inputs map[string]string) (string, error)
}

type PipelineStepDecider interface {
	ShouldExecutePipelineStep(run Run, step PipelineStep, state map[string]any) bool
}

type Runner struct {
	store     Store
	executors ExecutorRegistry
	pipelines PipelineRegistry
	profiles  AgentProfileRegistry
}

func NewRunner(store Store, executors ...ExecutorRegistry) *Runner {
	registry := ExecutorRegistry(NewEnvWorkflowExecutors(store))
	if len(executors) > 0 && executors[0] != nil {
		registry = executors[0]
	}
	return &Runner{store: store, executors: registry, pipelines: DefaultPipelineRegistry(), profiles: DefaultAgentProfileRegistry()}
}

func (r *Runner) Pipelines() []Pipeline {
	if r.pipelines == nil {
		return nil
	}
	return r.pipelines.ListPipelines()
}

func (r *Runner) AgentProfiles() []AgentProfile {
	if r.profiles == nil {
		return nil
	}
	return r.profiles.ListProfiles()
}

func (r *Runner) RunOnce(ctx context.Context, input CreateRunInput) (Run, error) {
	run, err := r.store.CreateRun(input)
	if err != nil {
		return Run{}, err
	}
	run, err = r.store.StartRun(run.ID)
	if err != nil {
		return Run{}, err
	}
	return r.ExecuteRun(ctx, run)
}

func (r *Runner) Enqueue(input CreateRunInput) (Run, error) {
	return r.store.CreateRun(input)
}

func (r *Runner) ExecuteRun(ctx context.Context, run Run) (Run, error) {
	return r.executeRun(ctx, run, "")
}

func (r *Runner) ExecuteClaimedRun(ctx context.Context, run Run, workerID string) (Run, error) {
	return r.executeRun(ctx, run, workerID)
}

func (r *Runner) executeRun(ctx context.Context, run Run, workerID string) (Run, error) {
	select {
	case <-ctx.Done():
		return r.finishRun(run.ID, workerID, RunCanceled, ctx.Err().Error())
	default:
	}
	executor := r.executors.ExecutorFor(run.WorkflowID)
	if executor == nil {
		executor = WorkflowExecutorFunc(stubWorkflowExecution(run.WorkflowID, "Workflow execution adapter is not wired yet"))
	}
	if r.pipelines != nil {
		if pipeline, ok := r.pipelines.PipelineForWorkflow(run.WorkflowID); ok {
			if stepExecutor, ok := executor.(PipelineStepExecutor); ok {
				execution, err := r.executePipeline(ctx, run, pipeline, stepExecutor, executor)
				return r.finishExecution(run, workerID, execution, err)
			}
		}
	}
	if stepwise, ok := executor.(StepwiseWorkflowExecutor); ok {
		execution, err := stepwise.ExecuteSteps(ctx, run, r.store)
		return r.finishExecution(run, workerID, execution, err)
	}
	step, err := r.store.CreateRunStep(CreateRunStepInput{RunID: run.ID, Position: 1, Name: run.WorkflowID + ".execute", Executor: "workflow", Inputs: run.Inputs})
	if err != nil {
		_, _ = r.store.AddEvent(run.ID, "workflow.failed", err.Error())
		return r.finishRun(run.ID, workerID, RunFailed, err.Error())
	}
	step, err = r.store.StartRunStep(step.ID)
	if err != nil {
		_, _ = r.store.AddEvent(run.ID, "workflow.failed", err.Error())
		return r.finishRun(run.ID, workerID, RunFailed, err.Error())
	}
	execution, err := executor.Execute(ctx, run)
	return r.finishSingleStepExecution(run, workerID, step, execution, err)
}

func (r *Runner) executePipeline(ctx context.Context, run Run, pipeline Pipeline, stepExecutor PipelineStepExecutor, executor WorkflowExecutor) (WorkflowExecution, error) {
	steps := append([]PipelineStep(nil), pipeline.Steps...)
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].Position < steps[j].Position })
	state := map[string]any{}
	var execution WorkflowExecution
	for _, pipelineStep := range steps {
		select {
		case <-ctx.Done():
			return execution, ctx.Err()
		default:
		}
		if decider, ok := executor.(PipelineStepDecider); ok && !decider.ShouldExecutePipelineStep(run, pipelineStep, state) {
			continue
		}
		model := pipelineStep.Model
		if resolver, ok := executor.(PipelineStepModelResolver); ok {
			resolved, err := resolver.ResolvePipelineStepModel(run.WorkflowID, pipelineStep, run.Inputs)
			if err != nil {
				model = ""
				step, createErr := r.store.CreateRunStep(CreateRunStepInput{RunID: run.ID, Position: pipelineStep.Position, Name: pipelineStep.Name, Executor: pipelineStep.Executor, Model: model, Inputs: pipelineStep.Inputs})
				if createErr != nil {
					return execution, createErr
				}
				step, startErr := r.store.StartRunStep(step.ID)
				if startErr != nil {
					return execution, startErr
				}
				_, _ = r.store.FinishRunStep(step.ID, StepFailed, "", err.Error(), nil)
				return execution, err
			}
			model = resolved
		}
		step, err := r.store.CreateRunStep(CreateRunStepInput{RunID: run.ID, Position: pipelineStep.Position, Name: pipelineStep.Name, Executor: pipelineStep.Executor, Model: model, Inputs: pipelineStep.Inputs})
		if err != nil {
			return execution, err
		}
		step, err = r.store.StartRunStep(step.ID)
		if err != nil {
			return execution, err
		}
		resolvedStep := pipelineStep
		resolvedStep.Model = model
		stepResult, err := stepExecutor.ExecutePipelineStep(ctx, run, resolvedStep, state)
		if stepResult.Inputs != nil {
			if _, inputErr := r.store.UpdateRunStep(step.ID, UpdateRunStepInput{Inputs: stepResult.Inputs}); inputErr != nil {
				_, _ = r.store.FinishRunStep(step.ID, StepFailed, stepResult.Summary, inputErr.Error(), stepResult.CorrelationIDs)
				return execution, inputErr
			}
		}
		for _, event := range stepResult.Events {
			_, _ = r.store.AddEvent(run.ID, event.Type, event.Message)
		}
		for _, artifact := range stepResult.Artifacts {
			artifact.RunID = run.ID
			if _, artifactErr := r.store.AddArtifact(artifact); artifactErr != nil {
				_, _ = r.store.FinishRunStep(step.ID, StepFailed, stepResult.Summary, artifactErr.Error(), stepResult.CorrelationIDs)
				return execution, artifactErr
			}
		}
		if err != nil {
			_, _ = r.store.FinishRunStep(step.ID, StepFailed, stepResult.Summary, err.Error(), stepResult.CorrelationIDs)
			return execution, err
		}
		if _, err := r.store.FinishRunStep(step.ID, StepSucceeded, stepResult.Summary, "", stepResult.CorrelationIDs); err != nil {
			return execution, err
		}
		if stepResult.Halt {
			break
		}
	}
	if summary, ok := state["summary"].(string); ok && summary != "" {
		execution.Summary = summary
	}
	return execution, nil
}

func (r *Runner) finishSingleStepExecution(run Run, workerID string, step RunStep, execution WorkflowExecution, err error) (Run, error) {
	for _, event := range execution.Events {
		_, _ = r.store.AddEvent(run.ID, event.Type, event.Message)
	}
	for _, artifact := range execution.Artifacts {
		artifact.RunID = run.ID
		_, _ = r.store.AddArtifact(artifact)
	}
	if err != nil {
		_, _ = r.store.FinishRunStep(step.ID, StepFailed, "", err.Error(), nil)
		_, _ = r.store.AddEvent(run.ID, "workflow.failed", err.Error())
		return r.finishRun(run.ID, workerID, RunFailed, err.Error())
	}
	summary := execution.Summary
	if summary == "" {
		summary = "Workflow dispatched"
	}
	_, _ = r.store.FinishRunStep(step.ID, StepSucceeded, summary, "", nil)
	return r.finishRun(run.ID, workerID, RunSucceeded, summary)
}

func (r *Runner) finishExecution(run Run, workerID string, execution WorkflowExecution, err error) (Run, error) {
	for _, event := range execution.Events {
		_, _ = r.store.AddEvent(run.ID, event.Type, event.Message)
	}
	for _, artifact := range execution.Artifacts {
		artifact.RunID = run.ID
		_, _ = r.store.AddArtifact(artifact)
	}
	if err != nil {
		_, _ = r.store.AddEvent(run.ID, "workflow.failed", err.Error())
		return r.finishRun(run.ID, workerID, RunFailed, err.Error())
	}
	summary := execution.Summary
	if summary == "" {
		summary = "Workflow dispatched"
	}
	return r.finishRun(run.ID, workerID, RunSucceeded, summary)
}

func (r *Runner) finishRun(id string, workerID string, status RunStatus, summary string) (Run, error) {
	if workerID != "" {
		return r.store.FinishClaimedRun(id, workerID, status, summary)
	}
	return r.store.FinishRun(id, status, summary)
}

func (r *Runner) RunTrigger(ctx context.Context, triggerID string) (Run, error) {
	trigger, ok := r.store.Trigger(triggerID)
	if !ok {
		return Run{}, ErrNotFound
	}
	return r.RunOnce(ctx, CreateRunInput{WorkflowID: trigger.WorkflowID, TriggerID: trigger.ID, Inputs: trigger.Inputs})
}

func (r *Runner) EnqueueTrigger(triggerID string) (Run, error) {
	trigger, ok := r.store.Trigger(triggerID)
	if !ok {
		return Run{}, ErrNotFound
	}
	return r.Enqueue(CreateRunInput{WorkflowID: trigger.WorkflowID, TriggerID: trigger.ID, Inputs: trigger.Inputs})
}
