// @vitest-environment jsdom

import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { confirmNavigation } from '../navigationGuard';
import type { PromptContractEntry, PromptInventory, PromptPreview, PromptTemplate } from '../promptAuthoring';
import { button, controlByLabel, expectText, jsonResponse, waitFor } from '../test-utils';
import type { WorkflowDefinition } from '../types';
import { PromptsPage } from './PromptsPage';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const promptV1 = 'Summarize {{topic}}:\n{{steps.fetch.outputs.body}}';

function definitionVersion(version: number, prompt: string): WorkflowDefinition {
  return {
    id: 'digest',
    name: 'Digest',
    description: 'Summarize notes.',
    status: 'draft',
    version,
    input_shape: { type: 'object', properties: { topic: { type: 'string' } }, required: ['topic'] },
    pipeline: {
      id: 'digest-pipeline',
      workflow_id: 'digest',
      name: 'Digest',
      steps: [
        { id: 'fetch', position: 1, name: 'digest.fetch', executor: 'internal', inputs: { 'output.body': '{{topic}} notes' } },
        { id: 'write', position: 2, name: 'digest.write', executor: 'model', profile: 'content-writer', model: 'gpt-5.4', inputs: { prompt } },
      ],
    },
  };
}

const writerContract: PromptContractEntry = {
  owner: 'agent-profile',
  profile_id: 'content-writer',
  name: 'Content Writer',
  role: 'writer',
  version: 3,
  source: 'persisted',
  status: 'active',
  default_model: 'gpt-5.4',
  allowed_models: ['gpt-5.4', 'gpt-5.4-mini'],
  allowed_executors: ['model'],
  prompt_contract: 'Write clear Markdown grounded in the inputs.',
  prompt_contract_sha256: 'c'.repeat(64),
  referenced_by: [{ workflow_id: 'digest', step_id: 'write', version: 1 }],
};

type Request = { method: string; path: string; body?: Record<string, unknown> };
type BackendOptions = { access?: { role: string; can_write: boolean }; active?: number; preview?: (body: Record<string, unknown>) => PromptPreview; putStatus?: () => { status: number; message: string } | undefined };

/** In-memory stand-in for /api/prompts and the workflow-definition version routes. */
function promptBackend(options: BackendOptions = {}) {
  const versions: WorkflowDefinition[] = [definitionVersion(1, promptV1)];
  let active = options.active ?? 1;
  const requests: Request[] = [];
  const latest = () => versions[versions.length - 1];
  const withStatus = (item: WorkflowDefinition): WorkflowDefinition => ({ ...item, status: item.version === active ? 'active' : 'draft', ...(active ? { active_version: active } : {}) });
  const promptOf = (item: WorkflowDefinition) => item.pipeline.steps[1].inputs?.prompt ?? '';
  const inventory = (): PromptInventory => {
    const current = latest();
    const activeDefinition = versions.find((item) => item.version === active);
    const template: PromptTemplate = {
      owner: 'workflow-definition', workflow_id: 'digest', workflow_name: 'Digest', version: current.version ?? 0, active_version: active,
      step_id: 'write', step_name: 'digest.write', position: 2, executor: 'model', profile_id: 'content-writer', model: 'gpt-5.4', input: 'prompt',
      template: promptOf(current), template_sha256: `sha-${current.version}`, active_template_sha256: activeDefinition ? `sha-${activeDefinition.version}` : undefined,
      changed_since_active: activeDefinition ? promptOf(activeDefinition) !== promptOf(current) : true,
      variables: [{ name: 'topic', kind: 'input', declared: true, required: true }, { name: 'steps.fetch.outputs.body', kind: 'step-output', step_id: 'fetch', output: 'body', declared: true, required: true }],
      problems: [{ severity: 'warning', message: 'stored template warning' }],
    };
    return { templates: [template], contracts: [writerContract], built_in: [{ owner: 'built-in', workflow_id: 'devlog', pipeline_id: 'manual-devlog', step_id: 'generate-markdown', step_name: 'devlog.generate-markdown', executor: 'model', profile_id: 'devlog-summarizer', note: 'The devlog executor assembles this prompt in code.' }] };
  };
  const fetch = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = new URL(String(input), 'http://switchyard.test');
    const method = init.method ?? 'GET';
    const body = init.body ? (JSON.parse(String(init.body)) as Record<string, unknown>) : undefined;
    requests.push({ method, path: url.pathname, body });
    if (url.pathname === '/api/control-access') return jsonResponse(options.access ?? { role: 'admin', can_write: true });
    if (url.pathname === '/api/prompts') return jsonResponse(inventory());
    if (url.pathname === '/api/workflow-definitions/digest' && method === 'GET') return jsonResponse({ workflow_definition: withStatus(latest()) });
    if (url.pathname === '/api/workflow-definitions/digest/versions') return jsonResponse({ workflow_definitions: versions.slice().reverse().map(withStatus) });
    if (url.pathname === '/api/workflow-definitions/digest' && method === 'PUT') {
      const forced = options.putStatus?.();
      if (forced) return jsonResponse({ error: { message: forced.message } }, { status: forced.status });
      if (body?.expected_version !== latest().version) return jsonResponse({ error: { message: 'state conflict: latest version changed' } }, { status: 409 });
      const saved = { ...(body as unknown as WorkflowDefinition), version: (latest().version ?? 0) + 1 };
      versions.push(saved);
      return jsonResponse({ workflow_definition: withStatus(saved) });
    }
    if (url.pathname === '/api/workflow-definitions/digest/activate') {
      active = latest().version ?? 0;
      return jsonResponse({ workflow_definition: withStatus(latest()), previous_active_version: 1 });
    }
    if (url.pathname === '/api/workflow-definitions/digest/rollback') {
      const target = versions.find((item) => item.version === body?.version)!;
      const previous = active;
      versions.push({ ...structuredClone(target), version: (latest().version ?? 0) + 1 });
      active = latest().version ?? 0;
      return jsonResponse({ workflow_definition: withStatus(latest()), previous_active_version: previous, rolled_back_from_version: body?.version });
    }
    if (url.pathname === '/api/prompts/preview' && method === 'POST') {
      if (options.access && !options.access.can_write) return jsonResponse({ error: { message: 'admin authorization required' } }, { status: 403 });
      return jsonResponse({ preview: options.preview ? options.preview(body ?? {}) : defaultPreview(body ?? {}) });
    }
    throw new Error(`Unexpected fetch: ${method} ${url.pathname}`);
  });
  vi.stubGlobal('fetch', fetch);
  return { requests, versions, active: () => active };
}

/** Mirrors the server's missing-variable reporting for the fixture template. */
function defaultPreview(body: Record<string, unknown>): PromptPreview {
  const inputs = (body.inputs ?? {}) as Record<string, string>;
  const outputs = (body.step_outputs ?? {}) as Record<string, Record<string, string>>;
  const missing: PromptPreview['missing_variables'] = [];
  if (!('topic' in inputs)) missing.push({ name: 'topic', kind: 'input', message: 'missing variable {{topic}}: no sample run input supplied; a run without it sends the literal token to the model' });
  if (!outputs.fetch || !('body' in outputs.fetch)) missing.push({ name: 'steps.fetch.outputs.body', kind: 'step-output', message: 'missing variable {{steps.fetch.outputs.body}}: no sample output supplied; a run without this recorded output pauses before the model call' });
  const request = String(body.template ?? '').replace('{{topic}}', inputs.topic ?? '{{topic}}').replace('{{steps.fetch.outputs.body}}', outputs.fetch?.body ?? '{{steps.fetch.outputs.body}}');
  return {
    workflow_id: 'digest', version: 1, active_version: 1, step_id: 'write', step_name: 'digest.write', input: 'prompt', draft: true, template_sha256: 'x',
    variables: [], problems: [], missing_variables: missing, ok: missing.length === 0, workflow_request: request,
    rendered: `Profile contract:\n${writerContract.prompt_contract}\n\nWorkflow request:\n${request.replace('sk-live-secret-value', '[REDACTED]')}`, redacted: request.includes('sk-live-secret-value'),
  };
}

let root: Root | null = null;

async function render() {
  const container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root?.render(<PromptsPage />);
  });
  await waitFor(() => expectText(container, 'Digest · write'), container);
  return container;
}

function setValue(element: Element, value: string) {
  const prototype = element instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  Object.getOwnPropertyDescriptor(prototype, 'value')?.set?.call(element, value);
  element.dispatchEvent(new Event('input', { bubbles: true }));
}

async function fill(scope: HTMLElement, label: string, value: string) {
  await act(async () => {
    setValue(controlByLabel(scope, label), value);
  });
}

async function click(element: HTMLElement) {
  await act(async () => {
    element.click();
  });
  await act(async () => {
    await Promise.resolve();
  });
}

function templateField(container: HTMLElement) {
  return container.querySelector<HTMLTextAreaElement>('textarea[aria-label="Template"]')!;
}

async function openTemplate(container: HTMLElement) {
  await click(container.querySelector<HTMLElement>('button[aria-label="Open digest/write"]')!);
  await waitFor(() => expect(templateField(container)?.value).toBe(promptV1), container);
}

afterEach(async () => {
  await act(async () => root?.unmount());
  root = null;
  document.body.innerHTML = '';
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('PromptsPage', () => {
  it('lists templates by owner and shows the referenced profile constraints', async () => {
    promptBackend();
    const container = await render();
    expectText(container, 'Who owns each prompt');
    expectText(container, 'active v1');
    expect(container.querySelector('[data-testid="prompt-contracts"]')?.textContent).toContain('content-writer · v3 active · used by 1 steps');
    expect(container.querySelector('[data-testid="prompt-built-in"]')?.textContent).toContain('devlog · generate-markdown');
    await openTemplate(container);
    const constraints = container.querySelector('[data-testid="prompt-profile-constraints"]')?.textContent ?? '';
    expect(constraints).toContain('allowed: gpt-5.4, gpt-5.4-mini · default gpt-5.4 · step model gpt-5.4');
    expect(constraints).toContain('Executors: model');
    expect(constraints).toContain('Write clear Markdown grounded in the inputs.');
    expect(container.querySelector('a[href="#/agents"]')).not.toBeNull();
    expectText(container, 'warning: stored template warning');
    expect(controlByLabel(container, 'Run input topic')).toBeTruthy();
    expect(controlByLabel(container, 'Step output steps.fetch.outputs.body')).toBeTruthy();
  });

  it('reports missing variables explicitly and renders a preview with samples without running anything', async () => {
    const backend = promptBackend();
    const container = await render();
    await openTemplate(container);
    await click(button(container, 'Preview'));
    await waitFor(() => expect(container.querySelector('[data-testid="prompt-missing-variables"]')).not.toBeNull(), container);
    const missing = container.querySelector('[data-testid="prompt-missing-variables"]')!.textContent ?? '';
    expect(missing).toContain('missing variable {{topic}}');
    expect(missing).toContain('missing variable {{steps.fetch.outputs.body}}');
    expectText(container, 'Preview is incomplete');

    await fill(container, 'Run input topic', 'sk-live-secret-value');
    await fill(container, 'Step output steps.fetch.outputs.body', 'Release notes');
    await click(button(container, 'Preview'));
    await waitFor(() => expectText(container, 'Preview rendered with every variable filled.'), container);
    const previews = backend.requests.filter((request) => request.path === '/api/prompts/preview');
    expect(previews.at(-1)?.body).toEqual({ workflow_id: 'digest', step_id: 'write', template: promptV1, inputs: { topic: 'sk-live-secret-value' }, step_outputs: { fetch: { body: 'Release notes' } } });
    const rendered = container.querySelector('[data-testid="prompt-preview"]')!.textContent ?? '';
    expect(rendered).toContain('No model was called and nothing was run.');
    expect(rendered).toContain('Credential-shaped values are redacted.');
    expect(rendered).not.toContain('sk-live-secret-value');
    expect(rendered).toContain('Release notes');
    expect(backend.requests.some((request) => request.path.startsWith('/api/runs') || request.path.endsWith('/activate'))).toBe(false);
  });

  it('saves an edited template as a draft version that does not activate or run', async () => {
    const backend = promptBackend();
    const container = await render();
    await openTemplate(container);
    await fill(container, 'Template', 'Write about {{topic}}.');
    vi.spyOn(window, 'confirm').mockReturnValue(false);
    expect(confirmNavigation()).toBe(false);
    vi.mocked(window.confirm).mockRestore();
    await click(button(container, 'Save draft'));
    await waitFor(() => expectText(container, 'Saved digest draft version 2. Version 1 still serves new runs; activate the draft below to use it. Nothing was run.'), container);
    const put = backend.requests.find((request) => request.method === 'PUT');
    expect(put?.body?.expected_version).toBe(1);
    expect(put?.body?.status).toBe('draft');
    const steps = (put?.body?.pipeline as WorkflowDefinition['pipeline']).steps;
    expect(steps[1].inputs).toEqual({ prompt: 'Write about {{topic}}.' });
    expect(steps[0].inputs).toEqual({ 'output.body': '{{topic}} notes' });
    expect(backend.active()).toBe(1);
    expect(backend.requests.some((request) => request.path.startsWith('/api/runs') || request.path.endsWith('/activate'))).toBe(false);
    await waitFor(() => expectText(container, 'draft v2 differs from active v1'), container);
  });

  it('promotes a draft and rolls back through workflow versions', async () => {
    const backend = promptBackend();
    const container = await render();
    await openTemplate(container);
    await fill(container, 'Template', 'Write about {{topic}}.');
    await click(button(container, 'Save draft'));
    await waitFor(() => expect(button(container, 'Activate version 2…')).toBeTruthy(), container);
    await click(button(container, 'Activate version 2…'));
    await click(button(container.querySelector<HTMLElement>('[role="dialog"]')!, 'Activate version 2'));
    await waitFor(() => expect(backend.active()).toBe(2), container);
    await waitFor(() => expect(button(container, 'Roll back to v1…')).toBeTruthy(), container);
    await click(button(container, 'Roll back to v1…'));
    await click(button(container.querySelector<HTMLElement>('[role="dialog"]')!, 'Roll back to version 1'));
    await waitFor(() => expect(backend.active()).toBe(3), container);
    expect(backend.versions.at(-1)?.pipeline.steps[1].inputs?.prompt).toBe(promptV1);
    await waitFor(() => expect(templateField(container).value).toBe(promptV1), container);
  });

  it('keeps edits and offers a reload when the definition changed since it was opened', async () => {
    promptBackend({ putStatus: () => ({ status: 409, message: 'state conflict: workflow definition "digest" latest version is 2, expected_version was 1' }) });
    const container = await render();
    await openTemplate(container);
    await fill(container, 'Template', 'Edited {{topic}}');
    await click(button(container, 'Save draft'));
    await waitFor(() => expectText(container, 'latest version is 2, expected_version was 1'), container);
    expect(templateField(container).value).toBe('Edited {{topic}}');
    expect(button(container, 'Reload latest version')).toBeTruthy();
  });

  it('is read-only for read credentials', async () => {
    const backend = promptBackend({ access: { role: 'read', can_write: false } });
    const container = await render();
    await waitFor(() => expect(container.querySelector('[data-testid="prompts-read-only"]')).not.toBeNull(), container);
    await openTemplate(container);
    expect(templateField(container).readOnly).toBe(true);
    for (const name of ['Preview', 'Save draft', 'Revert']) expect(() => button(container, name)).toThrow();
    await waitFor(() => expect(container.querySelector('[data-version-history="digest"]')).not.toBeNull(), container);
    expect(Array.from(container.querySelectorAll('button')).some((item) => /Activate version|Roll back/.test(item.textContent ?? ''))).toBe(false);
    expect(backend.requests.some((request) => request.method !== 'GET')).toBe(false);
  });
});
