import { arrayOrEmpty } from '../api';
import { describeOverrideRule, modelSourceLabel, profileVersionLabel } from '../profileVersions';
import type { RunModelPolicy } from '../types';
import { HudEmptyState, HudLabel, HudPanel, HudSection } from './hud';
import { RunPromptEvidence } from './RunPromptEvidence';

/** Shows the profile version, model, and the source of each value a run resolved. */
export function RunPolicyEvidence({ policy }: { policy?: RunModelPolicy }) {
  return (
    <HudSection kicker="Policy" title="Resolved policy" note="Profile and prompt versions come from the run's snapshot, so later profile or workflow changes never alter this run. Each model shows where it came from.">
      {!policy ? (
        <HudEmptyState>This run has no pipeline snapshot, so no profile or model policy was recorded.</HudEmptyState>
      ) : (
        <div className="grid gap-3" data-testid="run-policy-evidence">
          <p className="text-sm text-[hsl(var(--muted))]">
            {policy.override ? (
              <>
                Run override <strong className="text-[hsl(var(--ink))]">{policy.override}</strong> ({policy.override_status}).
              </>
            ) : (
              'No run-level model override; steps use inherited models.'
            )}
            {policy.pipeline_version ? ` Pipeline ${policy.pipeline_id ?? ''} version ${policy.pipeline_version}.` : ''}
          </p>
          {arrayOrEmpty(policy.rejections).map((reason) => (
            <p key={reason} role="alert" className="text-sm text-[hsl(var(--danger))]">{reason}</p>
          ))}
          <div className="overflow-x-auto">
            <table className="w-full text-left text-sm">
              <thead>
                <tr className="text-xs uppercase tracking-[0.14em] text-[hsl(var(--faint))]">
                  <th className="py-1 pr-3">Step</th>
                  <th className="py-1 pr-3">Profile</th>
                  <th className="py-1 pr-3">Model</th>
                  <th className="py-1 pr-3">Model source</th>
                  <th className="py-1">Overrides</th>
                </tr>
              </thead>
              <tbody>
                {policy.steps.map((step) => (
                  <tr key={`${step.position}-${step.step_id}`} className="border-t border-[hsl(var(--border-faint))] align-top" data-testid={`policy-step-${step.step_id}`}>
                    <td className="py-2 pr-3 text-[hsl(var(--ink))]">
                      {step.position}. {step.step_name}
                      <span className="block text-xs text-[hsl(var(--faint))]">{step.executor}{step.recorded ? ' · recorded' : ' · not run yet'}</span>
                    </td>
                    <td className="py-2 pr-3">
                      {step.profile_id ? (
                        <>
                          {step.profile_id} · {profileVersionLabel({ version: step.profile_version, source: step.profile_source })}
                          <span className="block text-xs text-[hsl(var(--faint))]">
                            chosen by workflow · {step.profile_captured === 'registry' ? 'resolved from current registry (pre-snapshot run)' : 'captured in run snapshot'}
                          </span>
                        </>
                      ) : (
                        <span className="text-[hsl(var(--faint))]">none</span>
                      )}
                    </td>
                    <td className="py-2 pr-3 text-[hsl(var(--ink))]">{step.model || '—'}</td>
                    <td className="py-2 pr-3">
                      {modelSourceLabel(step.model_source)}
                      {step.model_source === 'override' && step.inherited_model ? <span className="block text-xs text-[hsl(var(--faint))]">would inherit {step.inherited_model} ({modelSourceLabel(step.inherited_model_source)})</span> : null}
                    </td>
                    <td className="py-2 text-xs text-[hsl(var(--muted))]">
                      {describeOverrideRule(step)}
                      {step.error ? <span className="block text-[hsl(var(--danger))]">{step.error}</span> : null}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {policy.steps.length === 0 ? (
            <HudPanel className="p-3">
              <HudLabel>No pipeline steps</HudLabel>
            </HudPanel>
          ) : null}
          <RunPromptEvidence policy={policy} />
        </div>
      )}
    </HudSection>
  );
}
