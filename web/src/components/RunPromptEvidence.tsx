import { definitionBindingLabel, promptPolicySteps, shortHash } from '../promptAuthoring';
import { profileVersionLabel } from '../profileVersions';
import type { RunModelPolicy } from '../types';
import { HudLabel } from './hud';

/**
 * Shows the prompt versions a run is bound to: the workflow-definition version
 * that owns its step templates and, per step, the template hash and the
 * profile contract version captured in the run snapshot.
 */
export function RunPromptEvidence({ policy }: { policy: RunModelPolicy }) {
  const steps = promptPolicySteps(policy);
  return (
    <div className="grid gap-2" data-testid="run-prompt-evidence">
      <HudLabel className="text-[hsl(var(--signal))]">Prompt versions</HudLabel>
      <p className="text-sm text-[hsl(var(--muted))]">{definitionBindingLabel(policy)}.</p>
      {steps.length === 0 ? (
        <p className="text-xs text-[hsl(var(--faint))]">No step in this run sends a prompt.</p>
      ) : (
        <ul className="grid gap-1 text-sm">
          {steps.map((step) => (
            <li key={`${step.position}-${step.step_id}`} data-testid={`prompt-step-${step.step_id}`} className="border-t border-[hsl(var(--border-faint))] pt-1">
              <span className="text-[hsl(var(--ink))]">{step.position}. {step.step_name}</span>
              <span className="block text-xs text-[hsl(var(--muted))]">
                {step.prompt_owner === 'workflow-definition'
                  ? `template input "${step.prompt_input ?? 'prompt'}" · sha256 ${shortHash(step.prompt_template_sha256)}`
                  : 'prompt assembled by the executor in code'}
                {step.profile_id ? ` · contract ${step.profile_id} ${profileVersionLabel({ version: step.profile_version, source: step.profile_source })} · sha256 ${shortHash(step.profile_contract_sha256)}` : ' · no profile contract'}
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
