package controlplane

import "context"

type Runner struct {
	store     Store
	executors ExecutorRegistry
}

func NewRunner(store Store, executors ...ExecutorRegistry) *Runner {
	registry := ExecutorRegistry(NewEnvWorkflowExecutors(store))
	if len(executors) > 0 && executors[0] != nil {
		registry = executors[0]
	}
	return &Runner{store: store, executors: registry}
}

func (r *Runner) RunOnce(ctx context.Context, input CreateRunInput) (Run, error) {
	run, err := r.store.CreateRun(input)
	if err != nil {
		return Run{}, err
	}
	if _, err := r.store.StartRun(run.ID); err != nil {
		return Run{}, err
	}
	select {
	case <-ctx.Done():
		return r.store.FinishRun(run.ID, RunCanceled, ctx.Err().Error())
	default:
	}
	executor := r.executors.ExecutorFor(run.WorkflowID)
	if executor == nil {
		executor = WorkflowExecutorFunc(stubWorkflowExecution(run.WorkflowID, "Workflow execution adapter is not wired yet"))
	}
	step, err := r.store.CreateRunStep(CreateRunStepInput{RunID: run.ID, Position: 1, Name: run.WorkflowID + ".execute", Executor: "workflow", Inputs: run.Inputs})
	if err != nil {
		_, _ = r.store.AddEvent(run.ID, "workflow.failed", err.Error())
		return r.store.FinishRun(run.ID, RunFailed, err.Error())
	}
	step, err = r.store.StartRunStep(step.ID)
	if err != nil {
		_, _ = r.store.AddEvent(run.ID, "workflow.failed", err.Error())
		return r.store.FinishRun(run.ID, RunFailed, err.Error())
	}
	execution, err := executor.Execute(ctx, run)
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
		return r.store.FinishRun(run.ID, RunFailed, err.Error())
	}
	summary := execution.Summary
	if summary == "" {
		summary = "Workflow dispatched"
	}
	_, _ = r.store.FinishRunStep(step.ID, StepSucceeded, summary, "", nil)
	return r.store.FinishRun(run.ID, RunSucceeded, summary)
}

func (r *Runner) RunTrigger(ctx context.Context, triggerID string) (Run, error) {
	trigger, ok := r.store.Trigger(triggerID)
	if !ok {
		return Run{}, ErrNotFound
	}
	return r.RunOnce(ctx, CreateRunInput{WorkflowID: trigger.WorkflowID, TriggerID: trigger.ID, Inputs: trigger.Inputs})
}
