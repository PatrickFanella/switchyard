import { useEffect, useState } from 'react';
import { arrayOrEmpty, messageFrom } from '../api';
import { describeInheritedModel, describeOverrideRule, dispatchRunWithModel, isAPIStatus, loadWorkflowModelPolicy, overrideSelectionProblem } from '../profileVersions';
import type { RunModelPolicy, Trigger } from '../types';
import { loadControlAccess } from '../workflowDrafts';
import { HudLabel, HudPanelStrong } from './hud';

const fieldClass = 'mt-1 w-full border border-[hsl(var(--border-faint))] bg-[hsl(var(--void))] px-2 py-1 text-sm text-[hsl(var(--ink))] disabled:opacity-70';
const chipClass = 'hud-chip px-3 py-1.5 text-[0.65rem] disabled:opacity-50';
const labelClass = 'block text-xs text-[hsl(var(--muted))]';

/**
 * Dispatches a trigger's workflow with an optional per-run model override.
 * Choices are limited to the policy the server reports; the server checks the
 * override again at dispatch and its rejection reason is shown verbatim.
 */
export function RunModelOverrideDispatch({ triggers, onDispatched }: { triggers: Trigger[]; onDispatched: (runID: string) => void }) {
  // Open Pilot has its own dispatch form; its overrides are still checked by the server.
  const candidates = triggers.filter((trigger) => trigger.enabled && trigger.workflow_id !== 'open-pilot');
  return candidates.length > 0 ? <DispatchForm candidates={candidates} onDispatched={onDispatched} /> : null;
}

function DispatchForm({ candidates, onDispatched }: { candidates: Trigger[]; onDispatched: (runID: string) => void }) {
  const [triggerID, setTriggerID] = useState('');
  const [policy, setPolicy] = useState<RunModelPolicy | null>(null);
  const [policyMessage, setPolicyMessage] = useState('');
  const [model, setModel] = useState('');
  const [canWrite, setCanWrite] = useState(true);
  const [dispatching, setDispatching] = useState(false);
  const [result, setResult] = useState<{ tone: 'error' | 'ok'; message: string } | null>(null);
  const trigger = candidates.find((item) => item.id === triggerID);
  const workflowID = trigger?.workflow_id ?? '';

  const chooseTrigger = (id: string) => {
    setTriggerID(id);
    setPolicy(null);
    setPolicyMessage('');
    setResult(null);
    setModel(candidates.find((item) => item.id === id)?.inputs?.model ?? '');
  };

  useEffect(() => {
    let active = true;
    void loadControlAccess().then((access) => {
      if (active) setCanWrite(access.can_write);
    });
    return () => {
      active = false;
    };
  }, []);

  useEffect(() => {
    if (!workflowID) return;
    let active = true;
    loadWorkflowModelPolicy(workflowID)
      .then((loaded) => {
        if (active) setPolicy(loaded);
      })
      .catch((error: unknown) => {
        if (!active) return;
        setPolicyMessage(isAPIStatus(error, 404) ? `Workflow ${workflowID} has no pipeline, so run-level model overrides are not evaluated for it.` : `Could not load the model policy: ${messageFrom(error)}`);
      });
    return () => {
      active = false;
    };
  }, [triggerID, workflowID]);

  const problem = policy ? overrideSelectionProblem(policy, model) : '';
  const allowed = arrayOrEmpty(policy?.allowed_overrides);

  const dispatch = async () => {
    if (!trigger) return;
    setDispatching(true);
    setResult(null);
    const outcome = await dispatchRunWithModel(trigger.workflow_id, trigger.id, trigger.inputs ?? {}, model);
    setDispatching(false);
    if (outcome.ok) {
      setResult({ tone: 'ok', message: `Dispatched ${outcome.run.id}${model.trim() ? ` with model ${model.trim()}` : ' with inherited models'}.` });
      onDispatched(outcome.run.id);
      return;
    }
    if (outcome.kind === 'forbidden') setCanWrite(false);
    setResult({ tone: 'error', message: outcome.kind === 'rejected' ? `Rejected by the server: ${outcome.message}` : outcome.message });
  };

  return (
    <HudPanelStrong className="p-4">
      <HudLabel className="text-[hsl(var(--faint))]">Dispatch with model override</HudLabel>
      <form
        aria-label="Dispatch with model override"
        className="mt-3 grid gap-3"
        onSubmit={(event) => {
          event.preventDefault();
          void dispatch();
        }}
      >
        <label className={labelClass}>
          Trigger
          <select className={fieldClass} value={triggerID} onChange={(event) => chooseTrigger(event.target.value)}>
            <option value="">Choose a trigger…</option>
            {candidates.map((item) => (
              <option key={item.id} value={item.id}>
                {item.name} ({item.workflow_id})
              </option>
            ))}
          </select>
        </label>
        {policyMessage ? <p className="text-xs text-[hsl(var(--muted))]">{policyMessage}</p> : null}
        {policy ? (
          <>
            <ul className="grid gap-1 text-xs text-[hsl(var(--muted))]" aria-label="Inherited defaults">
              {policy.steps
                .filter((step) => step.profile_id || step.inherited_model)
                .map((step) => (
                  <li key={step.step_id}>
                    <span className="text-[hsl(var(--ink))]">{step.step_name}</span>: {describeInheritedModel(step)} {describeOverrideRule(step)}
                  </li>
                ))}
            </ul>
            {!policy.accepts_override ? (
              <p className="text-xs text-[hsl(var(--muted))]">No step of this workflow accepts a run-level model override; every run uses the inherited models above.</p>
            ) : policy.override_restricted ? (
              <label className={labelClass}>
                Model override
                <select className={fieldClass} value={model} onChange={(event) => setModel(event.target.value)}>
                  <option value="">Inherit defaults</option>
                  {allowed.map((item) => (
                    <option key={item} value={item}>
                      {item}
                    </option>
                  ))}
                  {model && !allowed.includes(model) ? <option value={model}>{model} (not allowed)</option> : null}
                </select>
              </label>
            ) : (
              <label className={labelClass}>
                Model override (any single model name; empty inherits defaults)
                <input className={fieldClass} value={model} onChange={(event) => setModel(event.target.value)} />
              </label>
            )}
            {trigger?.inputs?.model ? <p className="text-xs text-[hsl(var(--faint))]">This trigger sets model {trigger.inputs.model} as its run input.</p> : null}
            {problem ? <p role="alert" className="text-xs text-[hsl(var(--danger))]">{problem}</p> : null}
          </>
        ) : null}
        {!canWrite ? <p className="text-xs text-[hsl(var(--warn))]">Read-only access. An admin credential is required to dispatch runs.</p> : null}
        {result ? (
          <p role={result.tone === 'error' ? 'alert' : 'status'} className={`text-sm ${result.tone === 'error' ? 'text-[hsl(var(--danger))]' : 'text-[hsl(var(--confirm))]'}`}>
            {result.message}
          </p>
        ) : null}
        <div>
          <button className={`${chipClass} border-[hsl(var(--signal))] text-[hsl(var(--signal))]`} disabled={!trigger || !canWrite || dispatching || Boolean(problem) || (!policy && !policyMessage)} type="submit">
            {dispatching ? 'Dispatching…' : 'Dispatch run'}
          </button>
        </div>
      </form>
    </HudPanelStrong>
  );
}
