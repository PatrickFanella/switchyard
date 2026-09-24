// @vitest-environment jsdom

import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it } from 'vitest';
import type { RunModelPolicy } from '../types';
import { RunPolicyEvidence } from './RunPolicyEvidence';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root | null = null;

async function mount(policy: RunModelPolicy) {
  const container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root?.render(<RunPolicyEvidence policy={policy} />);
  });
  return container;
}

afterEach(async () => {
  await act(async () => root?.unmount());
  root = null;
  document.body.innerHTML = '';
});

const base: RunModelPolicy = { workflow_id: 'digest', override_status: 'none', accepts_override: false, override_restricted: false, steps: [] };

describe('RunPromptEvidence', () => {
  it('shows the workflow-definition version and each step template and contract version the run captured', async () => {
    const container = await mount({
      ...base,
      definition_id: 'digest',
      definition_version: 2,
      steps: [
        { step_id: 'fetch', step_name: 'digest.fetch', position: 1, executor: 'internal', profile_version: 0, accepts_override: false, override_restricted: false, inherited_model_source: 'none', model_source: 'none', recorded: true },
        { step_id: 'write', step_name: 'digest.write', position: 2, executor: 'model', profile_id: 'content-writer', profile_version: 3, profile_source: 'persisted', profile_contract_sha256: 'b'.repeat(64), accepts_override: false, override_restricted: false, inherited_model_source: 'workflow', model: 'gpt-5.4', model_source: 'workflow', recorded: true, prompt_owner: 'workflow-definition', prompt_input: 'prompt', prompt_template_sha256: 'a'.repeat(64) },
      ],
    });
    const evidence = container.querySelector('[data-testid="run-prompt-evidence"]')!.textContent ?? '';
    expect(evidence).toContain('Workflow definition digest version 2.');
    const write = container.querySelector('[data-testid="prompt-step-write"]')!.textContent ?? '';
    expect(write).toContain('template input "prompt" · sha256 aaaaaaaaaaaa');
    expect(write).toContain('contract content-writer v3 · sha256 bbbbbbbbbbbb');
    expect(container.querySelector('[data-testid="prompt-step-fetch"]')).toBeNull();
  });

  it('marks built-in prompts and runs without a recorded definition version', async () => {
    const container = await mount({
      ...base,
      workflow_id: 'devlog',
      definition_version: 0,
      steps: [{ step_id: 'generate-markdown', step_name: 'devlog.generate-markdown', position: 2, executor: 'model', profile_id: 'devlog-summarizer', profile_version: 0, profile_source: 'static', accepts_override: true, override_restricted: false, inherited_model_source: 'executor-default', model_source: 'executor-default', recorded: false, prompt_owner: 'built-in' }],
    });
    const evidence = container.querySelector('[data-testid="run-prompt-evidence"]')!.textContent ?? '';
    expect(evidence).toContain('No workflow-definition version recorded');
    expect(evidence).toContain('prompt assembled by the executor in code · contract devlog-summarizer static default');
  });
});
