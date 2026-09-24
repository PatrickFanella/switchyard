// @vitest-environment jsdom

import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { button, controlByLabel, jsonResponse, waitFor } from '../test-utils';
import type { RunModelPolicy, Trigger } from '../types';
import { RunModelOverrideDispatch } from './RunModelOverrideDispatch';
import { RunPolicyEvidence } from './RunPolicyEvidence';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const devlogPolicy: RunModelPolicy = {
  workflow_id: 'devlog',
  pipeline_id: 'devlog-pipeline',
  pipeline_version: 'abc123',
  override_status: 'none',
  accepts_override: true,
  override_restricted: true,
  allowed_overrides: ['model-a', 'model-b'],
  steps: [
    { step_id: 'collect', step_name: 'devlog.collect', position: 1, executor: 'internal', profile_version: 0, accepts_override: false, override_restricted: false, inherited_model_source: 'none', model_source: 'none', recorded: false },
    { step_id: 'generate-markdown', step_name: 'devlog.generate-markdown', position: 2, executor: 'model', profile_id: 'devlog-summarizer', profile_version: 0, profile_source: 'static', profile_captured: 'run-snapshot', workflow_model: '${model}', accepts_override: true, override_restricted: true, allowed_overrides: ['model-a', 'model-b'], inherited_model: 'model-a', inherited_model_source: 'executor-default', model: 'model-a', model_source: 'executor-default', recorded: false },
  ],
};

const triggers: Trigger[] = [
  { id: 'devlog-manual', workflow_id: 'devlog', type: 'manual', name: 'Devlog now', description: '', enabled: true, inputs: { window: '24h' } },
  { id: 'open-pilot-manual', workflow_id: 'open-pilot', type: 'manual', name: 'Open Pilot', description: '', enabled: true },
];

let root: Root | null = null;

async function mount(node: React.ReactNode) {
  const container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root?.render(node);
  });
  return container;
}

async function choose(container: HTMLElement, label: string, value: string) {
  await act(async () => {
    const element = controlByLabel(container, label) as HTMLSelectElement;
    Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value')?.set?.call(element, value);
    element.dispatchEvent(new Event('change', { bubbles: true }));
  });
}

afterEach(async () => {
  await act(async () => root?.unmount());
  root = null;
  document.body.innerHTML = '';
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('RunPolicyEvidence', () => {
  it('shows the profile version, model, and the source of each value', async () => {
    const policy: RunModelPolicy = {
      ...devlogPolicy,
      override: 'model-b',
      override_status: 'accepted',
      steps: [{ ...devlogPolicy.steps[1], profile_version: 3, profile_source: 'persisted', model: 'model-b', model_source: 'override', recorded: true }],
    };
    const container = await mount(<RunPolicyEvidence policy={policy} />);
    const row = container.querySelector('[data-testid="policy-step-generate-markdown"]')?.textContent ?? '';
    expect(row).toContain('devlog-summarizer · v3');
    expect(row).toContain('chosen by workflow');
    expect(row).toContain('captured in run snapshot');
    expect(row).toContain('model-b');
    expect(row).toContain('run override');
    expect(row).toContain('would inherit model-a (workflow default)');
    expect(row).toContain('recorded');
    expect(container.textContent).toContain('Run override model-b (accepted)');
  });

  it('explains runs without a pipeline snapshot', async () => {
    const container = await mount(<RunPolicyEvidence />);
    expect(container.textContent).toContain('no pipeline snapshot');
  });
});

describe('RunModelOverrideDispatch', () => {
  function backend(options: { access?: { role: string; can_write: boolean }; dispatch?: () => Response } = {}) {
    const calls: Array<{ method: string; path: string; body?: unknown }> = [];
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = new URL(String(input), 'http://switchyard.test');
      const method = init.method ?? 'GET';
      calls.push({ method, path: url.pathname + url.search, body: init.body ? JSON.parse(String(init.body)) : undefined });
      if (url.pathname === '/api/control-access') return jsonResponse(options.access ?? { role: 'admin', can_write: true });
      if (url.pathname === '/api/workflows/devlog/model-policy') return jsonResponse({ policy: devlogPolicy });
      if (url.pathname === '/api/runs' && method === 'POST') return options.dispatch?.() ?? jsonResponse({ run: { id: 'run-9', workflow_id: 'devlog', status: 'queued', created_at: '' } }, { status: 201 });
      throw new Error(`Unexpected fetch: ${method} ${url.pathname}`);
    }));
    return calls;
  }

  it('renders nothing without eligible triggers', async () => {
    const calls = backend();
    const container = await mount(<RunModelOverrideDispatch triggers={[triggers[1]]} onDispatched={() => undefined} />);
    expect(container.textContent).toBe('');
    expect(calls).toHaveLength(0);
  });

  it('explains inherited defaults and offers only allowed overrides', async () => {
    const calls = backend();
    const dispatched = vi.fn();
    const container = await mount(<RunModelOverrideDispatch triggers={triggers} onDispatched={dispatched} />);
    expect(container.textContent).not.toContain('Open Pilot (open-pilot)');
    await choose(container, 'Trigger', 'devlog-manual');
    await waitFor(() => expect(container.textContent).toContain("Inherits model-a, the workflow's configured default model."), container);
    expect(container.textContent).toContain('Accepts overrides: model-a, model-b.');
    const options = Array.from((controlByLabel(container, 'Model override') as HTMLSelectElement).options).map((option) => option.value);
    expect(options).toEqual(['', 'model-a', 'model-b']);
    await choose(container, 'Model override', 'model-b');
    await act(async () => {
      button(container, 'Dispatch run').click();
    });
    await waitFor(() => expect(dispatched).toHaveBeenCalledWith('run-9'), container);
    expect(calls.find((call) => call.method === 'POST')?.body).toEqual({ workflow_id: 'devlog', trigger_id: 'devlog-manual', inputs: { window: '24h', model: 'model-b' } });
    expect(container.textContent).toContain('Dispatched run-9 with model model-b.');
  });

  it('shows the server rejection reason verbatim', async () => {
    backend({ dispatch: () => jsonResponse({ error: { message: 'invalid input: model override "model-b" rejected: step "devlog.generate-markdown": model "model-b" is not allowed for workflow devlog' } }, { status: 400 }) });
    const dispatched = vi.fn();
    const container = await mount(<RunModelOverrideDispatch triggers={triggers} onDispatched={dispatched} />);
    await choose(container, 'Trigger', 'devlog-manual');
    await waitFor(() => expect(container.textContent).toContain('Accepts overrides'), container);
    await choose(container, 'Model override', 'model-b');
    await act(async () => {
      button(container, 'Dispatch run').click();
    });
    await waitFor(() => expect(container.textContent).toContain('Rejected by the server: invalid input: model override "model-b" rejected'), container);
    expect(dispatched).not.toHaveBeenCalled();
  });

  it('disables dispatch for read-only credentials', async () => {
    backend({ access: { role: 'read', can_write: false } });
    const container = await mount(<RunModelOverrideDispatch triggers={triggers} onDispatched={() => undefined} />);
    await choose(container, 'Trigger', 'devlog-manual');
    await waitFor(() => expect(container.textContent).toContain('Read-only access'), container);
    expect((button(container, 'Dispatch run') as HTMLButtonElement).disabled).toBe(true);
  });
});
