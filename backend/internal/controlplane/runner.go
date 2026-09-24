package controlplane

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type PipelineStepExecution struct {
	Summary   string
	Events    []WorkflowExecutionEvent
	Artifacts []CreateArtifactInput
	Inputs    map[string]string
	// Outputs are the named values later steps may bind through
	// {{steps.<step_id>.outputs.<name>}}. The runner bounds and persists them
	// on the step record only when the step succeeds.
	Outputs        map[string]string
	CorrelationIDs map[string]string
	Halt           bool
	Handoff        bool
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
	store              Store
	executors          ExecutorRegistry
	pipelines          PipelineRegistry
	profiles           AgentProfileRegistry
	limiter            ExecutorLimiter
	activeMu           sync.Mutex
	active             map[string]activeRun
	openPilotGate      *OpenPilotAdmissionGate
	openPilotGateError error
}

type activeRun struct {
	cancel context.CancelFunc
	token  *struct{}
}

type preAttemptContextKey struct{}
type runStepCorrelationContextKey struct{}
type claimedWorkerContextKey struct{}
type recoveredRunStepContextKey struct{}
type runAttemptStoreContextKey struct{}

func withRunStepPreAttempt(ctx context.Context, store Store, runID, stepID string) context.Context {
	ctx = context.WithValue(ctx, preAttemptContextKey{}, PreAttemptCallback(func(decision BackendDecision) error {
		return store.RecordRunStepBackendDecision(runID, stepID, decision)
	}))
	return context.WithValue(ctx, runStepCorrelationContextKey{}, func(correlationIDs map[string]string) error {
		_, err := store.MergeRunStepCorrelationIDs(stepID, correlationIDs)
		return err
	})
}

func preAttemptFromContext(ctx context.Context) PreAttemptCallback {
	callback, _ := ctx.Value(preAttemptContextKey{}).(PreAttemptCallback)
	return callback
}

func runStepCorrelationFromContext(ctx context.Context) func(map[string]string) error {
	callback, _ := ctx.Value(runStepCorrelationContextKey{}).(func(map[string]string) error)
	return callback
}

func claimedWorkerFromContext(ctx context.Context) string {
	workerID, _ := ctx.Value(claimedWorkerContextKey{}).(string)
	return workerID
}

func NewRunner(store Store, executors ...ExecutorRegistry) *Runner {
	registry := ExecutorRegistry(NewEnvWorkflowExecutors(store))
	if len(executors) > 0 && executors[0] != nil {
		registry = executors[0]
	}
	gate, gateErr := NewEnvOpenPilotAdmissionGate()
	if gate != nil {
		gate.rollout = store
	}
	if env, ok := registry.(*EnvWorkflowExecutors); ok {
		env.openPilotGate, env.openPilotGateError = gate, gateErr
	}
	return &Runner{store: store, executors: registry, pipelines: NewStoreBackedPipelineRegistry(store, DefaultPipelineRegistry()), profiles: DefaultAgentProfileRegistry(), limiter: NewEnvExecutorLimiter(), active: map[string]activeRun{}, openPilotGate: gate, openPilotGateError: gateErr}
}

func (r *Runner) SetOpenPilotAdmissionGate(gate *OpenPilotAdmissionGate) {
	r.openPilotGate, r.openPilotGateError = gate, nil
	if env, ok := r.executors.(*EnvWorkflowExecutors); ok {
		env.openPilotGate, env.openPilotGateError = gate, nil
	}
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

func (r *Runner) SetAgentProfileRegistry(profiles AgentProfileRegistry) {
	if profiles == nil {
		profiles = DefaultAgentProfileRegistry()
	}
	r.profiles = profiles
}

func (r *Runner) RunOnce(ctx context.Context, input CreateRunInput) (Run, error) {
	prepared, err := r.prepareRunInput(input)
	if err != nil {
		return Run{}, err
	}
	run, err := r.createPreparedRun(ctx, prepared)
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
	return r.EnqueueContext(context.Background(), input)
}

func (r *Runner) EnqueueContext(ctx context.Context, input CreateRunInput) (Run, error) {
	prepared, err := r.prepareRunInput(input)
	if err != nil {
		return Run{}, err
	}
	return r.createPreparedRun(ctx, prepared)
}

func (r *Runner) createPreparedRun(ctx context.Context, input CreateRunInput) (Run, error) {
	if input.WorkflowID != string(WorkflowOpenPilot) {
		return r.store.CreateRunContext(ctx, input)
	}
	if input.Inputs["retry_from_step"] != "" || input.Inputs["retry_from_run"] != "" {
		state, err := r.store.OpenPilotIssueState(input.Inputs["repo"], input.Inputs["issue"])
		if err != nil && !errors.Is(err, ErrNotFound) {
			return Run{}, err
		}
		if err == nil {
			if _, err := r.openPilotGate.ValidateState(state); err != nil {
				return Run{}, err
			}
			return Run{}, ErrConflict
		}
	}
	store := r.store.WithContext(ctx)
	state, run, err := store.ClaimAndCreateOpenPilotInitialRun(input.Inputs["repo"], input.Inputs["issue"], input)
	if err == nil && input.Inputs["retry_from_step"] != "" {
		target := openPilotResumeClaimPhase(OpenPilotPhaseRequested, input.Inputs)
		for _, phase := range openPilotPhaseChain[1:] {
			if state.Phase == target {
				break
			}
			state, err = store.TransitionOpenPilotIssueState(state.Repo, state.Issue, phase)
			if err != nil || phase == target {
				break
			}
		}
	}
	return run, err
}

func (r *Runner) prepareRunInput(input CreateRunInput) (CreateRunInput, error) {
	if err := validateCreateRunInput(input); err != nil {
		return CreateRunInput{}, err
	}
	if input.WorkflowID == string(WorkflowOpenPilot) {
		if r.openPilotGateError != nil {
			return CreateRunInput{}, r.openPilotGateError
		}
		target, err := r.openPilotGate.Resolve(input.Inputs["repo"], input.Inputs["issue"])
		if err != nil {
			return CreateRunInput{}, err
		}
		input.Inputs = cloneMap(input.Inputs)
		input.Inputs["repo"], input.Inputs["issue"] = target.RepositorySlug, target.Issue
		setOpenPilotAffinityInputs(input.Inputs, target)
		input.OpenPilotTarget = &target
	}
	return r.withPipelineSnapshot(input), nil
}

func (r *Runner) prepareOpenPilotStateRunInput(state OpenPilotIssueState, input CreateRunInput) (CreateRunInput, error) {
	input.Inputs = cloneMap(input.Inputs)
	delete(input.Inputs, openPilotRepositoryIDInputKey)
	delete(input.Inputs, openPilotExecutorGenerationInputKey)
	delete(input.Inputs, openPilotLifecycleResumeInputKey)
	if err := validateCreateRunInput(input); err != nil {
		return CreateRunInput{}, err
	}
	if r.openPilotGateError != nil {
		return CreateRunInput{}, r.openPilotGateError
	}
	target, err := r.openPilotGate.ValidateState(state)
	if err != nil {
		return CreateRunInput{}, err
	}
	input.Inputs["repo"], input.Inputs["issue"] = target.RepositorySlug, target.Issue
	setOpenPilotAffinityInputs(input.Inputs, target)
	input.OpenPilotTarget = &target
	return r.withPipelineSnapshot(input), nil
}

func (r *Runner) withPipelineSnapshot(input CreateRunInput) CreateRunInput {
	if input.MaxAttempts == 0 {
		input.MaxAttempts = defaultRunMaxAttempts()
	}
	if input.PipelineSnapshot != nil || r.pipelines == nil {
		return input
	}
	if pipeline, ok := r.pipelines.PipelineForWorkflow(input.WorkflowID); ok {
		snapshot := clonePipeline(pipeline)
		input.PipelineSnapshot = &snapshot
	}
	return input
}

func defaultRunMaxAttempts() int {
	if value, err := strconv.Atoi(strings.TrimSpace(os.Getenv("SWITCHYARD_RUN_MAX_ATTEMPTS"))); err == nil && value >= 1 && value <= 10 {
		return value
	}
	return 3
}

func (r *Runner) ExecuteRun(ctx context.Context, run Run) (Run, error) {
	return r.executeRun(ctx, run, "")
}

func (r *Runner) ExecuteClaimedRun(ctx context.Context, run Run, workerID string) (Run, error) {
	ctx = withRunAttemptFence(ctx, RunAttemptFence{RunID: run.ID, WorkerID: workerID, Attempt: run.AttemptCount})
	return r.executeRun(ctx, run, workerID)
}

func (r *Runner) executeRun(ctx context.Context, run Run, workerID string) (Run, error) {
	store := r.store.WithContext(ctx)
	ctx = context.WithValue(ctx, runAttemptStoreContextKey{}, store)
	if run.WorkflowID == string(WorkflowOpenPilot) {
		for key := range run.Inputs {
			if reservedOpenPilotAffinityInput(key) && key != openPilotRepositoryIDInputKey && key != openPilotExecutorGenerationInputKey && key != openPilotLifecycleResumeInputKey {
				if workerID != "" {
					_, _ = store.FinishClaimedRun(run.ID, workerID, run.AttemptCount, RunPaused, "Open Pilot reserved affinity input rejected")
				} else {
					_, _ = store.FinishRun(run.ID, RunPaused, "Open Pilot reserved affinity input rejected")
				}
				return Run{}, ErrInvalid
			}
		}
		state, err := store.OpenPilotIssueState(run.Inputs["repo"], run.Inputs["issue"])
		if err != nil {
			r.pauseOpenPilotRun(store, run, workerID, "Open Pilot durable state missing or unreadable")
			return Run{}, ErrConflict
		}
		_, err = r.openPilotGate.ValidateRunState(run, state)
		if err != nil {
			r.pauseOpenPilotRun(store, run, workerID, "Open Pilot repository affinity rejected")
			return Run{}, err
		}
	}
	executionCtx, cancel := context.WithCancel(ctx)
	activeToken := &struct{}{}
	r.activeMu.Lock()
	r.active[run.ID] = activeRun{cancel: cancel, token: activeToken}
	r.activeMu.Unlock()
	defer func() {
		cancel()
		r.activeMu.Lock()
		if active := r.active[run.ID]; active.token == activeToken {
			delete(r.active, run.ID)
		}
		r.activeMu.Unlock()
	}()
	ctx = executionCtx
	if workerID != "" {
		ctx = context.WithValue(ctx, claimedWorkerContextKey{}, workerID)
	}
	select {
	case <-ctx.Done():
		return r.finishRun(store, run, workerID, RunCanceled, ctx.Err().Error())
	default:
	}
	executor := r.executors.ExecutorFor(run.WorkflowID)
	if executor == nil {
		return r.finishRun(store, run, workerID, RunFailed, "workflow execution adapter is not registered")
	}
	if r.pipelines != nil || run.PipelineSnapshot != nil {
		pipeline, ok := r.pipelineForRun(run)
		if ok {
			if stepExecutor, ok := executor.(PipelineStepExecutor); ok {
				execution, err := r.executePipelineWithStore(ctx, store, run, pipeline, stepExecutor, executor)
				return r.finishExecutionWithStore(store, run, workerID, execution, err)
			}
		}
	}
	if stepwise, ok := executor.(StepwiseWorkflowExecutor); ok {
		execution, err := stepwise.ExecuteSteps(ctx, run, store)
		return r.finishExecutionWithStore(store, run, workerID, execution, err)
	}
	step, skip, err := r.prepareRunStep(store, CreateRunStepInput{RunID: run.ID, Position: 1, Name: run.WorkflowID + ".execute", Executor: "workflow", Inputs: run.Inputs})
	if err != nil {
		_, _ = store.AddEvent(run.ID, "workflow.failed", err.Error())
		return r.finishRun(store, run, workerID, RunFailed, err.Error())
	}
	if skip {
		return r.finishRun(store, run, workerID, RunSucceeded, firstNonEmpty(step.OutputSummary, "Workflow already completed"))
	}
	step, err = store.StartRunStep(step.ID)
	if err != nil {
		_, _ = store.AddEvent(run.ID, "workflow.failed", err.Error())
		return r.finishRun(store, run, workerID, RunFailed, err.Error())
	}
	execution, err := executor.Execute(ctx, run)
	return r.finishSingleStepExecution(store, run, workerID, step, execution, err)
}

func (r *Runner) pauseOpenPilotRun(store Store, run Run, workerID, summary string) {
	if workerID != "" {
		_, _ = store.FinishClaimedRun(run.ID, workerID, run.AttemptCount, RunPaused, summary)
	} else {
		_, _ = store.FinishRun(run.ID, RunPaused, summary)
	}
}

// CancelRun interrupts an execution hosted by this process. Durable status is
// updated separately by the store so cancellation remains visible after restart.
func (r *Runner) CancelRun(id string) bool {
	r.activeMu.Lock()
	active := r.active[id]
	r.activeMu.Unlock()
	if active.cancel == nil {
		return false
	}
	active.cancel()
	return true
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
	return r.executePipelineWithStore(ctx, r.store.WithContext(ctx), run, pipeline, stepExecutor, executor)
}

func (r *Runner) executePipelineWithStore(ctx context.Context, store Store, run Run, pipeline Pipeline, stepExecutor PipelineStepExecutor, executor WorkflowExecutor) (WorkflowExecution, error) {
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
	outputs, err := r.restorePipelineStepOutputs(store, run, steps, resumePosition)
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
		profile, hasProfile, profileErr := r.profileForPipelineStep(pipelineStep)
		if profileErr != nil {
			return execution, profileErr
		}
		model, err := r.resolvePipelineStepModel(run, pipelineStep, executor, profile, hasProfile)
		if err != nil {
			step, _, createErr := r.prepareRunStep(store, CreateRunStepInput{RunID: run.ID, Position: pipelineStep.Position, Name: pipelineStep.Name, Executor: pipelineStep.Executor, Model: model, Inputs: pipelineStep.Inputs})
			if createErr != nil {
				return execution, createErr
			}
			step, startErr := store.StartRunStep(step.ID)
			if startErr != nil {
				if reporter, ok := executor.(PipelineStepFailureReporter); ok {
					reporter.HandlePipelineStepFailure(ctx, run, pipelineStep, state, startErr)
				}
				return execution, startErr
			}
			if reporter, ok := executor.(PipelineStepFailureReporter); ok {
				reporter.HandlePipelineStepFailure(ctx, run, pipelineStep, state, err)
			}
			_, _ = store.FinishRunStep(step.ID, StepFailed, "", err.Error(), nil)
			return execution, err
		}
		correlationIDs := map[string]string(nil)
		if provider, ok := executor.(PipelineStepCorrelationProvider); ok {
			correlationIDs = provider.PipelineStepCorrelationIDs(run, pipelineStep, state)
		}
		if hasProfile {
			correlationIDs = mergeStringMaps(correlationIDs, profileAuditCorrelation(profile, model))
			_, _ = store.AddEvent(run.ID, "agent-profile.resolved", profile.ID+" for "+pipelineStep.Name)
		}
		step, skip, err := r.prepareRunStep(store, CreateRunStepInput{RunID: run.ID, Position: pipelineStep.Position, Name: pipelineStep.Name, Executor: pipelineStep.Executor, Model: model, Inputs: pipelineStep.Inputs, CorrelationIDs: correlationIDs})
		if err != nil {
			return execution, err
		}
		if skip {
			if step.OutputSummary != "" {
				execution.Summary = step.OutputSummary
			}
			outputs.set(pipelineStep.ID, step.Outputs)
			continue
		}
		release, err := r.acquireExecutorSlot(ctx, pipelineStep.Executor)
		if err != nil {
			status := stepStatusForError(err)
			if reporter, ok := executor.(PipelineStepFailureReporter); ok {
				reporter.HandlePipelineStepFailure(ctx, run, pipelineStep, state, err)
			}
			_, _ = store.FinishRunStep(step.ID, status, "", err.Error(), nil)
			return execution, err
		}
		step, err = store.StartRunStep(step.ID)
		if err != nil {
			release()
			if reporter, ok := executor.(PipelineStepFailureReporter); ok {
				reporter.HandlePipelineStepFailure(ctx, run, pipelineStep, state, err)
			}
			return execution, err
		}
		resolvedStep := pipelineStep
		resolvedStep.Model = model
		if hasProfile {
			resolvedStep.profileContract = profile.PromptContract
		}
		resolvedInputs, resolveErr := resolvePipelineStepInputs(pipelineStep, outputs)
		if resolveErr != nil {
			release()
			if reporter, ok := executor.(PipelineStepFailureReporter); ok {
				reporter.HandlePipelineStepFailure(ctx, run, resolvedStep, state, resolveErr)
			}
			_, _ = store.FinishRunStep(step.ID, StepFailed, "", resolveErr.Error(), nil)
			return execution, resolveErr
		}
		resolvedStep.Inputs = resolvedInputs
		var stepResult PipelineStepExecution
		func() {
			defer release()
			stepCtx := withRunStepPreAttempt(ctx, store, run.ID, step.ID)
			if run.AttemptCount > 1 || step.AttemptCount > 1 {
				stepCtx = context.WithValue(stepCtx, recoveredRunStepContextKey{}, true)
			}
			if validateErr := validateAttemptStore(store); validateErr != nil {
				err = validateErr
				return
			}
			stepResult, err = stepExecutor.ExecutePipelineStep(stepCtx, run, resolvedStep, state)
		}()
		if stepResult.Handoff {
			execution.Status = RunFailed
			execution.Summary = stepResult.Summary
			return execution, nil
		}
		if stepResult.Inputs != nil {
			if _, inputErr := store.UpdateRunStep(step.ID, UpdateRunStepInput{Inputs: stepResult.Inputs}); inputErr != nil {
				if reporter, ok := executor.(PipelineStepFailureReporter); ok {
					reporter.HandlePipelineStepFailure(ctx, run, resolvedStep, state, inputErr)
				}
				_, _ = store.FinishRunStep(step.ID, StepFailed, stepResult.Summary, inputErr.Error(), stepResult.CorrelationIDs)
				return execution, inputErr
			}
		}
		for _, event := range stepResult.Events {
			_, _ = store.AddEvent(run.ID, event.Type, event.Message)
		}
		for _, artifact := range stepResult.Artifacts {
			artifact.RunID = run.ID
			if _, artifactErr := store.AddArtifact(artifact); artifactErr != nil {
				if reporter, ok := executor.(PipelineStepFailureReporter); ok {
					reporter.HandlePipelineStepFailure(ctx, run, resolvedStep, state, artifactErr)
				}
				_, _ = store.FinishRunStep(step.ID, StepFailed, stepResult.Summary, artifactErr.Error(), stepResult.CorrelationIDs)
				return execution, artifactErr
			}
		}
		if err != nil {
			status := stepStatusForError(err)
			if reporter, ok := executor.(PipelineStepFailureReporter); ok {
				reporter.HandlePipelineStepFailure(ctx, run, resolvedStep, state, err)
			}
			_, _ = store.FinishRunStep(step.ID, status, stepResult.Summary, err.Error(), stepResult.CorrelationIDs)
			return execution, err
		}
		stepStatus := StepSucceeded
		if stepResult.Halt {
			stepStatus = StepPaused
			execution.Status = RunPaused
		}
		if stepStatus == StepSucceeded {
			boundOutputs, omitted, boundErr := boundStepOutputs(pipelineStep.ID, stepResult.Outputs)
			if boundErr == nil && boundOutputs != nil {
				_, boundErr = store.UpdateRunStep(step.ID, UpdateRunStepInput{Outputs: boundOutputs})
			}
			if boundErr != nil {
				if reporter, ok := executor.(PipelineStepFailureReporter); ok {
					reporter.HandlePipelineStepFailure(ctx, run, resolvedStep, state, boundErr)
				}
				_, _ = store.FinishRunStep(step.ID, StepFailed, stepResult.Summary, boundErr.Error(), stepResult.CorrelationIDs)
				return execution, boundErr
			}
			for _, message := range omitted {
				_, _ = store.AddEvent(run.ID, stepOutputOmittedEvent, message)
			}
			outputs.set(pipelineStep.ID, boundOutputs)
		}
		if _, err := store.FinishRunStep(step.ID, stepStatus, stepResult.Summary, "", stepResult.CorrelationIDs); err != nil {
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

// restorePipelineStepOutputs rebuilds the output view from durable records so a
// retried, resumed, or restarted run never reruns a succeeded step to obtain
// its outputs. Same-run attempts read this run's succeeded steps. Operator
// resume runs carry retry_from_run; outputs of the source run's succeeded
// steps before the resume position are restored only when the source run
// executed the same immutable pipeline version.
func (r *Runner) restorePipelineStepOutputs(store Store, run Run, steps []PipelineStep, resumePosition int) (*pipelineStepOutputs, error) {
	outputs := newPipelineStepOutputs()
	outputs.loadPersisted(steps, store.ListRunSteps(run.ID), 0)
	sourceRunID := strings.TrimSpace(run.Inputs["retry_from_run"])
	if resumePosition <= 0 || sourceRunID == "" || sourceRunID == run.ID {
		return outputs, nil
	}
	source, err := store.RunDetail(sourceRunID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, stepOutputReferenceError{reference: "retry_from_run=" + sourceRunID, reason: "source run does not exist"}
		}
		return nil, err
	}
	if source.Run.PipelineVersion == "" || run.PipelineVersion == "" || source.Run.PipelineVersion != run.PipelineVersion {
		_, _ = store.AddEvent(run.ID, stepOutputsRestoredEvent, fmt.Sprintf("skipped: source run %s pipeline version %q differs from %q", sourceRunID, source.Run.PipelineVersion, run.PipelineVersion))
		return outputs, nil
	}
	if restored := outputs.loadPersisted(steps, source.Steps, resumePosition); restored > 0 {
		_, _ = store.AddEvent(run.ID, stepOutputsRestoredEvent, fmt.Sprintf("restored outputs of %d succeeded step(s) before position %d from run %s", restored, resumePosition, sourceRunID))
	}
	return outputs, nil
}

func (r *Runner) profileForPipelineStep(step PipelineStep) (AgentProfile, bool, error) {
	profileID := strings.TrimSpace(step.Profile)
	if profileID == "" {
		return AgentProfile{}, false, nil
	}
	profiles := r.profiles
	if profiles == nil {
		profiles = DefaultAgentProfileRegistry()
	}
	profile, ok := profiles.Profile(profileID)
	if !ok {
		return AgentProfile{}, false, fmt.Errorf("unknown agent profile %q", profileID)
	}
	if !profileAllowsExecutor(profile, step.Executor) {
		return AgentProfile{}, false, fmt.Errorf("agent profile %q does not allow executor %q", profileID, step.Executor)
	}
	return profile, true, nil
}

func (r *Runner) resolvePipelineStepModel(run Run, step PipelineStep, executor WorkflowExecutor, profile AgentProfile, hasProfile bool) (string, error) {
	model := strings.TrimSpace(step.Model)
	if hasProfile && model == "" {
		if override := strings.TrimSpace(run.Inputs["model"]); override != "" {
			model = override
		} else if strings.TrimSpace(profile.DefaultModel) != "" && (step.Executor == "model" || step.Executor == "opencode") {
			model = strings.TrimSpace(profile.DefaultModel)
		}
	}
	if model == "" || model == "${model}" {
		if resolver, ok := executor.(PipelineStepModelResolver); ok {
			resolved, err := resolver.ResolvePipelineStepModel(run.WorkflowID, step, run.Inputs)
			if err != nil {
				return "", err
			}
			model = strings.TrimSpace(resolved)
		}
	}
	if hasProfile && model != "" && !profileAllowsModel(profile, model) {
		return "", fmt.Errorf("agent profile %q does not allow model %q", profile.ID, model)
	}
	return model, nil
}

func profileAuditCorrelation(profile AgentProfile, model string) map[string]string {
	correlation := map[string]string{
		"agent_profile_id":              profile.ID,
		"agent_profile_role":            profile.Role,
		"agent_profile_contract_sha256": profileContractHash(profile),
	}
	if model != "" {
		correlation["agent_profile_model"] = model
	}
	return correlation
}

func (r *Runner) prepareRunStep(store Store, input CreateRunStepInput) (RunStep, bool, error) {
	for _, existing := range store.ListRunSteps(input.RunID) {
		if existing.Position != input.Position {
			continue
		}
		if existing.Name != input.Name || existing.Executor != input.Executor {
			return RunStep{}, false, ErrInvalid
		}
		switch existing.Status {
		case StepSucceeded:
			return existing, true, nil
		case StepFailed, StepCanceled, StepTimedOut, StepPaused:
			retried, err := store.RetryRunStep(existing.ID)
			if err == nil && len(input.CorrelationIDs) > 0 {
				retried, err = store.MergeRunStepCorrelationIDs(retried.ID, input.CorrelationIDs)
			}
			return retried, false, err
		case StepRunning:
			recovered, err := store.RecoverRunStep(existing.ID)
			if err == nil && len(input.CorrelationIDs) > 0 {
				recovered, err = store.MergeRunStepCorrelationIDs(recovered.ID, input.CorrelationIDs)
			}
			return recovered, false, err
		case StepQueued:
			return existing, false, nil
		default:
			return RunStep{}, false, ErrInvalid
		}
	}
	created, err := store.CreateRunStep(input)
	return created, false, err
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
	// Explicitly permanent executor failures need operator intervention. Persist
	// the pause so worker restarts cannot blindly retry invalid credentials.
	var classified interface{ Retryable() bool }
	if errors.As(err, &classified) && !classified.Retryable() {
		return RunPaused
	}
	return RunFailed
}

func (r *Runner) finishSingleStepExecution(store Store, run Run, workerID string, step RunStep, execution WorkflowExecution, err error) (Run, error) {
	for _, event := range execution.Events {
		_, _ = store.AddEvent(run.ID, event.Type, event.Message)
	}
	for _, artifact := range execution.Artifacts {
		artifact.RunID = run.ID
		_, _ = store.AddArtifact(artifact)
	}
	if err != nil {
		stepStatus := stepStatusForError(err)
		runStatus := runStatusForError(err)
		_, _ = store.FinishRunStep(step.ID, stepStatus, "", err.Error(), nil)
		_, _ = store.AddEvent(run.ID, "workflow."+string(runStatus), err.Error())
		return r.finishRun(store, run, workerID, runStatus, err.Error())
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
	_, _ = store.FinishRunStep(step.ID, stepStatus, summary, "", nil)
	return r.finishRun(store, run, workerID, status, summary)
}

func (r *Runner) finishExecution(run Run, workerID string, execution WorkflowExecution, err error) (Run, error) {
	return r.finishExecutionWithStore(r.store, run, workerID, execution, err)
}

func (r *Runner) finishExecutionWithStore(store Store, run Run, workerID string, execution WorkflowExecution, err error) (Run, error) {
	for _, event := range execution.Events {
		_, _ = store.AddEvent(run.ID, event.Type, event.Message)
	}
	for _, artifact := range execution.Artifacts {
		artifact.RunID = run.ID
		_, _ = store.AddArtifact(artifact)
	}
	if err != nil {
		status := runStatusForError(err)
		_, _ = store.AddEvent(run.ID, "workflow."+string(status), err.Error())
		return r.finishRun(store, run, workerID, status, err.Error())
	}
	summary := execution.Summary
	if summary == "" {
		summary = "Workflow dispatched"
	}
	status := execution.Status
	if status == "" {
		status = RunSucceeded
	}
	return r.finishRun(store, run, workerID, status, summary)
}

func (r *Runner) finishRun(store Store, run Run, workerID string, status RunStatus, summary string) (Run, error) {
	if workerID != "" {
		finished, err := store.FinishClaimedRun(run.ID, workerID, run.AttemptCount, status, summary)
		if err != nil {
			if detail, detailErr := store.RunDetail(run.ID); detailErr == nil && detail.Run.AttemptCount == run.AttemptCount && isTerminalRunStatus(detail.Run.Status) {
				return detail.Run, nil
			}
		}
		return finished, err
	}
	if detail, err := store.RunDetail(run.ID); err == nil && isTerminalRunStatus(detail.Run.Status) {
		return detail.Run, nil
	}
	return store.FinishRun(run.ID, status, summary)
}

func isTerminalRunStatus(status RunStatus) bool {
	switch status {
	case RunSucceeded, RunFailed, RunCanceled, RunPaused, RunTimedOut:
		return true
	default:
		return false
	}
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
	if input.MaxAttempts < 0 || input.MaxAttempts > 10 {
		return ErrInvalid
	}
	for key := range input.Inputs {
		if sensitiveJSONKey(key) {
			return ErrInvalid
		}
		if strings.HasPrefix(key, "resume.open-pilot.ci-repair.") {
			return ErrInvalid
		}
		if input.WorkflowID == string(WorkflowOpenPilot) && reservedOpenPilotAffinityInput(key) {
			return ErrInvalid
		}
		if input.WorkflowID == string(WorkflowOpenPilot) && (key == "retry_from_run" || key == "retry_from_step_id") {
			return ErrInvalid
		}
	}
	if input.WorkflowID != string(WorkflowOpenPilot) {
		return nil
	}
	if strings.TrimSpace(input.Inputs["repo"]) == "" || strings.TrimSpace(input.Inputs["issue"]) == "" {
		return ErrInvalid
	}
	return nil
}
