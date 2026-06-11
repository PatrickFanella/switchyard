package controlplane

import "context"

type Runner struct {
	store Store
}

func NewRunner(store Store) *Runner {
	return &Runner{store: store}
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
	_, _ = r.store.AddEvent(run.ID, "workflow.dispatched", "Workflow dispatch boundary reached; external adapters are not wired yet")
	return r.store.FinishRun(run.ID, RunSucceeded, "Recorded Switchyard workflow run envelope")
}

func (r *Runner) RunTrigger(ctx context.Context, triggerID string) (Run, error) {
	trigger, ok := r.store.Trigger(triggerID)
	if !ok {
		return Run{}, ErrNotFound
	}
	return r.RunOnce(ctx, CreateRunInput{WorkflowID: trigger.WorkflowID, TriggerID: trigger.ID, Inputs: trigger.Inputs})
}
