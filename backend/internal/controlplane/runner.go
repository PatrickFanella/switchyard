package controlplane

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
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

type PipelineStepCorrelationProvider interface {
	PipelineStepCorrelationIDs(run Run, step PipelineStep, state map[string]any) map[string]string
}

type PipelineStepFailureReporter interface {
	HandlePipelineStepFailure(ctx context.Context, run Run, step PipelineStep, state map[string]any, err error)
}

type PipelineResumeHydrator interface {
	HydratePipelineResumeState(run Run, pipeline Pipeline, state map[string]any)
}

type Runner struct {
	store     Store
	executors ExecutorRegistry
	pipelines PipelineRegistry
	profiles  AgentProfileRegistry
	limiter   ExecutorLimiter
}

func NewRunner(store Store, executors ...ExecutorRegistry) *Runner {
	registry := ExecutorRegistry(NewEnvWorkflowExecutors(store))
	if len(executors) > 0 && executors[0] != nil {
		registry = executors[0]
	}
	return &Runner{store: store, executors: registry, pipelines: NewStoreBackedPipelineRegistry(store, DefaultPipelineRegistry()), profiles: DefaultAgentProfileRegistry(), limiter: NewEnvExecutorLimiter()}
}

func (r *Runner) SetExecutorLimiter(limiter ExecutorLimiter) {
	if limiter == nil {
		limiter = NoopExecutorLimiter{}
	}
	r.limiter = limiter
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
	if err := validateCreateRunInput(input); err != nil {
		return Run{}, err
	}
	run, err := r.store.CreateRun(r.withPipelineSnapshot(input))
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
	if err := validateCreateRunInput(input); err != nil {
		return Run{}, err
	}
	return r.store.CreateRun(r.withPipelineSnapshot(input))
}

func (r *Runner) withPipelineSnapshot(input CreateRunInput) CreateRunInput {
	if input.PipelineSnapshot != nil || r.pipelines == nil {
		return input
	}
	if pipeline, ok := r.pipelines.PipelineForWorkflow(input.WorkflowID); ok {
		snapshot := clonePipeline(pipeline)
		input.PipelineSnapshot = &snapshot
	}
	return input
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
	if r.pipelines != nil || run.PipelineSnapshot != nil {
		pipeline, ok := r.pipelineForRun(run)
		if ok {
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

func (r *Runner) pipelineForRun(run Run) (Pipeline, bool) {
	if run.PipelineSnapshot != nil {
		return clonePipeline(*run.PipelineSnapshot), true
	}
	if r.pipelines == nil {
		return Pipeline{}, false
	}
	return r.pipelines.PipelineForWorkflow(run.WorkflowID)
}

func (r *Runner) executePipeline(ctx context.Context, run Run, pipeline Pipeline, stepExecutor PipelineStepExecutor, executor WorkflowExecutor) (WorkflowExecution, error) {
	steps := append([]PipelineStep(nil), pipeline.Steps...)
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].Position < steps[j].Position })
	state := map[string]any{}
	if hydrator, ok := executor.(PipelineResumeHydrator); ok {
		hydrator.HydratePipelineResumeState(run, pipeline, state)
	}
	var execution WorkflowExecution
	resumePosition, err := pipelineResumePosition(run, steps)
	if err != nil {
		return execution, err
	}
	for _, pipelineStep := range steps {
		if resumePosition > 0 && pipelineStep.Position < resumePosition {
			continue
		}
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
					if reporter, ok := executor.(PipelineStepFailureReporter); ok {
						reporter.HandlePipelineStepFailure(ctx, run, pipelineStep, state, startErr)
					}
					return execution, startErr
				}
				if reporter, ok := executor.(PipelineStepFailureReporter); ok {
					reporter.HandlePipelineStepFailure(ctx, run, pipelineStep, state, err)
				}
				_, _ = r.store.FinishRunStep(step.ID, StepFailed, "", err.Error(), nil)
				return execution, err
			}
			model = resolved
		}
		correlationIDs := map[string]string(nil)
		if provider, ok := executor.(PipelineStepCorrelationProvider); ok {
			correlationIDs = provider.PipelineStepCorrelationIDs(run, pipelineStep, state)
		}
		step, err := r.store.CreateRunStep(CreateRunStepInput{RunID: run.ID, Position: pipelineStep.Position, Name: pipelineStep.Name, Executor: pipelineStep.Executor, Model: model, Inputs: pipelineStep.Inputs, CorrelationIDs: correlationIDs})
		if err != nil {
			return execution, err
		}
		release, err := r.acquireExecutorSlot(ctx, pipelineStep.Executor)
		if err != nil {
			status := stepStatusForError(err)
			if reporter, ok := executor.(PipelineStepFailureReporter); ok {
				reporter.HandlePipelineStepFailure(ctx, run, pipelineStep, state, err)
			}
			_, _ = r.store.FinishRunStep(step.ID, status, "", err.Error(), nil)
			return execution, err
		}
		step, err = r.store.StartRunStep(step.ID)
		if err != nil {
			release()
			if reporter, ok := executor.(PipelineStepFailureReporter); ok {
				reporter.HandlePipelineStepFailure(ctx, run, pipelineStep, state, err)
			}
			return execution, err
		}
		resolvedStep := pipelineStep
		resolvedStep.Model = model
		var stepResult PipelineStepExecution
		func() {
			defer release()
			stepResult, err = stepExecutor.ExecutePipelineStep(ctx, run, resolvedStep, state)
		}()
		if stepResult.Inputs != nil {
			if _, inputErr := r.store.UpdateRunStep(step.ID, UpdateRunStepInput{Inputs: stepResult.Inputs}); inputErr != nil {
				if reporter, ok := executor.(PipelineStepFailureReporter); ok {
					reporter.HandlePipelineStepFailure(ctx, run, resolvedStep, state, inputErr)
				}
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
				if reporter, ok := executor.(PipelineStepFailureReporter); ok {
					reporter.HandlePipelineStepFailure(ctx, run, resolvedStep, state, artifactErr)
				}
				_, _ = r.store.FinishRunStep(step.ID, StepFailed, stepResult.Summary, artifactErr.Error(), stepResult.CorrelationIDs)
				return execution, artifactErr
			}
		}
		if err != nil {
			status := stepStatusForError(err)
			if reporter, ok := executor.(PipelineStepFailureReporter); ok {
				reporter.HandlePipelineStepFailure(ctx, run, resolvedStep, state, err)
			}
			_, _ = r.store.FinishRunStep(step.ID, status, stepResult.Summary, err.Error(), stepResult.CorrelationIDs)
			return execution, err
		}
		stepStatus := StepSucceeded
		if stepResult.Halt {
			stepStatus = StepPaused
			execution.Status = RunPaused
		}
		if _, err := r.store.FinishRunStep(step.ID, stepStatus, stepResult.Summary, "", stepResult.CorrelationIDs); err != nil {
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

func (r *Runner) acquireExecutorSlot(ctx context.Context, executor string) (func(), error) {
	limiter := r.limiter
	if limiter == nil {
		limiter = NoopExecutorLimiter{}
	}
	return limiter.Acquire(ctx, executor)
}

func pipelineResumePosition(run Run, steps []PipelineStep) (int, error) {
	stepIdentity := strings.TrimSpace(run.Inputs["retry_from_step"])
	legacyPosition, legacyErr := strconv.Atoi(strings.TrimSpace(run.Inputs["retry_from_step_position"]))
	if stepIdentity == "" {
		if legacyErr != nil || legacyPosition <= 0 {
			return 0, nil
		}
		for _, step := range steps {
			if step.Position == legacyPosition {
				return legacyPosition, nil
			}
		}
		return 0, fmt.Errorf("resume position %d does not exist in pipeline", legacyPosition)
	}
	for _, step := range steps {
		if step.ID != stepIdentity && step.Name != stepIdentity {
			continue
		}
		if legacyErr == nil && legacyPosition > 0 && legacyPosition != step.Position {
			return 0, fmt.Errorf("resume step %q is position %d, not %d", stepIdentity, step.Position, legacyPosition)
		}
		return step.Position, nil
	}
	return 0, fmt.Errorf("resume step %q does not exist in pipeline", stepIdentity)
}

func stepStatusForError(err error) RunStepStatus {
	if errors.Is(err, context.DeadlineExceeded) {
		return StepTimedOut
	}
	if errors.Is(err, context.Canceled) {
		return StepCanceled
	}
	return StepFailed
}

func runStatusForError(err error) RunStatus {
	if errors.Is(err, context.DeadlineExceeded) {
		return RunTimedOut
	}
	if errors.Is(err, context.Canceled) {
		return RunCanceled
	}
	return RunFailed
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
		stepStatus := stepStatusForError(err)
		runStatus := runStatusForError(err)
		_, _ = r.store.FinishRunStep(step.ID, stepStatus, "", err.Error(), nil)
		_, _ = r.store.AddEvent(run.ID, "workflow."+string(runStatus), err.Error())
		return r.finishRun(run.ID, workerID, runStatus, err.Error())
	}
	summary := execution.Summary
	if summary == "" {
		summary = "Workflow dispatched"
	}
	status := execution.Status
	if status == "" {
		status = RunSucceeded
	}
	stepStatus := StepSucceeded
	if status == RunPaused {
		stepStatus = StepPaused
	}
	_, _ = r.store.FinishRunStep(step.ID, stepStatus, summary, "", nil)
	return r.finishRun(run.ID, workerID, status, summary)
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
		status := runStatusForError(err)
		_, _ = r.store.AddEvent(run.ID, "workflow."+string(status), err.Error())
		return r.finishRun(run.ID, workerID, status, err.Error())
	}
	summary := execution.Summary
	if summary == "" {
		summary = "Workflow dispatched"
	}
	status := execution.Status
	if status == "" {
		status = RunSucceeded
	}
	return r.finishRun(run.ID, workerID, status, summary)
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

func validateCreateRunInput(input CreateRunInput) error {
	if input.WorkflowID != string(WorkflowOpenPilot) {
		return nil
	}
	if strings.TrimSpace(input.Inputs["repo"]) == "" || strings.TrimSpace(input.Inputs["issue"]) == "" {
		return ErrInvalid
	}
	return nil
}
