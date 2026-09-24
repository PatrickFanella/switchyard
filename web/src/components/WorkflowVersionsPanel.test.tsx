// @vitest-environment jsdom

import { act } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { button, controlByLabel, expectText, jsonResponse, mockDashboardFetch, renderApp, unmount, waitFor } from '../test-utils';
import type { PipelineStepDefinition, WorkflowDefinition } from '../types';
import type { ControlAccess } from '../workflowDrafts';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

afterEach(() => {
  window.location.hash = '';
  document.body.innerHTML = '';
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

const fetchStep: PipelineStepDefinition = { id: 'fetch', position: 1, name: 'digest.fetch', executor: 'http', inputs: { url: 'https://docs.example.test/changelog.json', allow_http: 'true' }, output_shape: { type: 'object', properties: { body: { type: 'string', maxLength: 4096 } }, required: ['body'] } };

function version(number: number, name: string, steps: PipelineStepDefinition[]): WorkflowDefinition {
  return { id: 'daily-digest', name, description: 'Summarize the changelog.', status: 'draft', version: number, pipeline: { id: 'daily-digest-pipeline', workflow_id: 'daily-digest', name: 'Daily Digest', steps }, updated_at: `2026-09-2${number}T12:00:00.000Z` };
}

const v1 = version(1, 'Digest', [
  fetchStep,
  { id: 'summarize', position: 2, name: 'digest.summarize', executor: 'model', inputs: { prompt: 'Summarize {{steps.fetch.outputs.body}}' } },
  { id: 'legacy', position: 3, name: 'digest.legacy', executor: 'internal' },
]);
const v2 = version(2, 'Daily Digest', [
  // Same output shape with a different key order: not a change.
  { ...fetchStep, output_shape: { required: ['body'], properties: { body: { maxLength: 4096, type: 'string' } }, type: 'object' } },
  { id: 'summarize', position: 2, name: 'digest.summarize', executor: 'model', inputs: { prompt: 'Summarize {{steps.fetch.outputs.body}} in three bullets' } },
  { id: 'publish', position: 3, name: 'digest.publish', executor: 'internal', inputs: { summary: 'Published' } },
]);

type Post = { path: string; body: Record<string, unknown> };
type Options = { versions: WorkflowDefinition[]; active: number; access?: ControlAccess | number; activate?: () => { status: number; message: string } | undefined };

/** In-memory version API: immutable versions, an active pointer, and the server's stale checks. */
function versionBackend(options: Options) {
  const versions = options.versions.map((item) => structuredClone(item));
  let active = options.active;
  const posts: Post[] = [];
  const latest = () => versions.reduce((max, item) => Math.max(max, item.version ?? 0), 0);
  const withStatus = (item: WorkflowDefinition): WorkflowDefinition => ({ ...item, status: item.version === active ? 'active' : 'draft', ...(active ? { active_version: active } : {}) });
  const current = () => withStatus(versions.find((item) => item.version === latest())!);
  mockDashboardFetch((url, init) => {
    const method = init.method ?? 'GET';
    if (url.pathname === '/api/control-access') {
      return typeof options.access === 'number' ? jsonResponse({ error: { message: 'not found' } }, { status: options.access }) : jsonResponse(options.access ?? { role: 'admin', can_write: true });
    }
    if (url.pathname === '/api/agent-profiles') return jsonResponse({ profiles: [] });
    if (url.pathname === '/api/workflow-definitions' && method === 'GET') return jsonResponse({ workflow_definitions: [current()] });
    if (url.pathname === '/api/workflow-definitions/daily-digest' && method === 'GET') return jsonResponse({ workflow_definition: current() });
    if (url.pathname === '/api/workflow-definitions/daily-digest/versions' && method === 'GET') {
      return jsonResponse({ workflow_definitions: versions.slice().sort((left, right) => (right.version ?? 0) - (left.version ?? 0)).map(withStatus) });
    }
    if (method !== 'POST') return undefined;
    const body = JSON.parse(String(init.body ?? '{}')) as Record<string, unknown>;
    posts.push({ path: url.pathname, body });
    if (url.pathname.endsWith('/activate')) {
      const rejection = options.activate?.();
      if (rejection) return jsonResponse({ error: { message: rejection.message } }, { status: rejection.status });
      if (body.version !== latest()) return jsonResponse({ error: { message: `state conflict: workflow definition "daily-digest" version ${String(body.version)} is not the latest saved version (latest is ${latest()})` } }, { status: 409 });
      const previous = active;
      active = latest();
      return jsonResponse({ workflow_definition: current(), previous_active_version: previous });
    }
    if (url.pathname.endsWith('/rollback')) {
      if (body.expected_version !== latest()) return jsonResponse({ error: { message: `state conflict: workflow definition "daily-digest" latest version is ${latest()}, expected_version was ${String(body.expected_version)}` } }, { status: 409 });
      const target = versions.find((item) => item.version === body.version)!;
      const previous = active;
      versions.push({ ...structuredClone(target), version: latest() + 1 });
      active = latest();
      return jsonResponse({ workflow_definition: current(), rolled_back_from_version: body.version, previous_active_version: previous });
    }
    return undefined;
  });
  return { posts, save: (definition: WorkflowDefinition) => versions.push(structuredClone(definition)), active: () => active };
}

async function openHistory() {
  window.location.hash = '#/workflows/view/daily-digest';
  const rendered = await renderApp('Workflow catalog');
  await waitFor(() => expect(rendered.container.querySelector('[data-version-history="daily-digest"] ol')).not.toBeNull(), rendered.container);
  return rendered;
}

async function click(element: HTMLElement) {
  await act(async () => { element.click(); });
}

function dialog(container: HTMLElement) {
  return container.querySelector<HTMLElement>('[role="dialog"]');
}

function versionRoles(container: HTMLElement) {
  return Array.from(container.querySelectorAll<HTMLElement>('[data-version]')).map((item) => `${item.dataset.version}:${item.dataset.versionRole}`);
}

describe('WorkflowVersionsPanel comparison', () => {
  it('lists immutable versions, distinguishes the active version from drafts, and diffs metadata and steps', async () => {
    versionBackend({ versions: [v1, v2], active: 1 });
    const { container, root } = await openHistory();
    expect(versionRoles(container)).toEqual(['2:draft', '1:active']);
    expectText(container, 'Version 1 is active and serves new runs. Version 2 is a saved draft and does not serve runs until it is activated.');
    expectText(container, 'draft · v1 active');

    const comparison = container.querySelector<HTMLElement>('[data-version-comparison="1-2"]');
    expect(comparison).not.toBeNull();
    expect(comparison!.querySelector('[data-field-change="name"]')?.textContent).toContain('Digest');
    expect(comparison!.querySelector('[data-field-change="name"]')?.textContent).toContain('Daily Digest');
    const steps = Array.from(comparison!.querySelectorAll<HTMLElement>('[data-step-change]')).map((item) => `${item.dataset.stepId}:${item.dataset.stepChange}`);
    expect(steps).toEqual(['fetch:unchanged', 'summarize:changed', 'publish:added', 'legacy:removed']);
    const summarize = comparison!.querySelector<HTMLElement>('[data-step-id="summarize"]')!;
    expect(summarize.querySelector('[data-field-change="inputs.prompt"]')?.textContent).toContain('in three bullets');

    await act(async () => {
      const select = controlByLabel(container, 'Compare from') as HTMLSelectElement;
      Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value')?.set?.call(select, '2');
      select.dispatchEvent(new Event('change', { bubbles: true }));
    });
    expectText(container, 'Versions 2 and 2 have the same metadata and steps.');
    await unmount(root);
  });
});

describe('WorkflowVersionsPanel activation', () => {
  it('states the target and consequences, cancels without a request, and activates on confirmation', async () => {
    const backend = versionBackend({ versions: [v1, v2], active: 1 });
    const { container, root } = await openHistory();
    await click(button(container, 'Activate version 2…'));
    const open = dialog(container)!;
    expect(open.getAttribute('aria-modal')).toBe('true');
    expect(document.activeElement).toBe(open);
    expectText(open, 'Activate version 2 of daily-digest?');
    expectText(open, 'Workflow daily-digest · target version 2 · active now: version 1');
    expectText(open, 'New runs of daily-digest will use version 2 instead of version 1.');
    expectText(open, 'keep the pipeline snapshot they were created with');
    expectText(open, 'Activation checks passed.');

    await click(button(open, 'Cancel'));
    expect(dialog(container)).toBeNull();
    await click(button(container, 'Activate version 2…'));
    await act(async () => { dialog(container)!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); });
    expect(dialog(container)).toBeNull();
    expect(backend.posts).toEqual([]);

    await click(button(container, 'Activate version 2…'));
    await click(button(dialog(container)!, 'Activate version 2'));
    await waitFor(() => expectText(container, 'Activated version 2 of daily-digest. New runs use it instead of version 1; existing runs keep their snapshot.'), container);
    expect(backend.posts).toEqual([{ path: '/api/workflow-definitions/daily-digest/activate', body: { version: 2 } }]);
    await waitFor(() => expect(versionRoles(container)).toEqual(['2:active', '1:earlier']), container);
    await unmount(root);
  });

  it('blocks activating a draft that fails the activation checks', async () => {
    const invalid = version(2, 'Daily Digest', [{ ...fetchStep, inputs: { url: 'https://docs.example.test/changelog.json' } }]);
    const backend = versionBackend({ versions: [v1, invalid], active: 1 });
    const { container, root } = await openHistory();
    await click(button(container, 'Activate version 2…'));
    const open = dialog(container)!;
    expectText(open, 'Version 2 cannot be activated');
    expectText(open, 'the http executor needs allow_http set to "true".');
    expect(button(open, 'Activate version 2').disabled).toBe(true);
    expect(backend.posts).toEqual([]);
    await unmount(root);
  });

  it('reports a failed activation from the server and leaves the active version in place', async () => {
    const backend = versionBackend({ versions: [v1, v2], active: 1, activate: () => ({ status: 400, message: 'invalid input: step "summarize": profile "content-writer" does not allow executor "model"' }) });
    const { container, root } = await openHistory();
    await click(button(container, 'Activate version 2…'));
    await click(button(dialog(container)!, 'Activate version 2'));
    const outcome = () => container.querySelector<HTMLElement>('[data-version-outcome="validation"]');
    await waitFor(() => expect(outcome()).not.toBeNull(), container);
    expectText(outcome()!, 'Activation rejected');
    expectText(outcome()!, 'does not allow executor "model". Nothing changed; version 1 is still active.');
    expect(document.activeElement).toBe(outcome());
    expect(backend.active()).toBe(1);
    expect(versionRoles(container)).toEqual(['2:draft', '1:active']);
    await unmount(root);
  });

  it('surfaces a stale-version conflict and reloads the latest history', async () => {
    const backend = versionBackend({ versions: [v1, v2], active: 1 });
    const { container, root } = await openHistory();
    await click(button(container, 'Activate version 2…'));
    // Another operator saves version 3 while the dialog is open.
    backend.save(version(3, 'Daily Digest v3', v2.pipeline.steps));
    await click(button(dialog(container)!, 'Activate version 2'));
    const outcome = () => container.querySelector<HTMLElement>('[data-version-outcome="conflict"]');
    await waitFor(() => expect(outcome()).not.toBeNull(), container);
    expectText(outcome()!, 'The workflow changed');
    expectText(outcome()!, 'version 2 is not the latest saved version (latest is 3)');
    expect(backend.active()).toBe(1);
    await click(button(outcome()!, 'Reload workflow'));
    await waitFor(() => expect(versionRoles(container)).toEqual(['3:draft', '2:draft', '1:active']), container);
    expect(button(container, 'Activate version 3…')).toBeTruthy();
    await unmount(root);
  });
});

describe('WorkflowVersionsPanel rollback', () => {
  it('rolls back through the backend contract as a new active version and keeps history', async () => {
    const backend = versionBackend({ versions: [v1, v2], active: 2 });
    const { container, root } = await openHistory();
    expect(Array.from(container.querySelectorAll('button')).some((item) => /^Activate/.test(item.textContent ?? ''))).toBe(false);
    await click(button(container, 'Roll back to v1…'));
    const open = dialog(container)!;
    expectText(open, 'Roll back to version 1 of daily-digest?');
    expectText(open, 'Creates version 3 as a copy of version 1 and makes it active. New runs of daily-digest will use version 3 instead of version 2.');
    expectText(open, 'No version is changed or deleted');
    await click(button(open, 'Roll back to version 1'));
    await waitFor(() => expectText(container, 'Rolled daily-digest back to version 1 as new active version 3. New runs use it instead of version 2'), container);
    expect(backend.posts).toEqual([{ path: '/api/workflow-definitions/daily-digest/rollback', body: { version: 1, expected_version: 2 } }]);
    await waitFor(() => expect(versionRoles(container)).toEqual(['3:active', '2:earlier', '1:earlier']), container);
    await unmount(root);
  });
});

describe('WorkflowVersionsPanel access', () => {
  it('shows history and comparison without activation or rollback for a read credential', async () => {
    const backend = versionBackend({ versions: [v1, v2], active: 1, access: { role: 'read', can_write: false } });
    const { container, root } = await openHistory();
    expectText(container, 'Read-only access: you can compare versions, but activation and rollback need an admin credential.');
    expect(Array.from(container.querySelectorAll('button')).some((item) => /Activate|Roll back/.test(item.textContent ?? ''))).toBe(false);
    expect(container.querySelector('[data-version-comparison="1-2"]')).not.toBeNull();
    await click(button(container.querySelector<HTMLElement>('[data-version="1"]')!, 'Compare'));
    expect(backend.posts).toEqual([]);
    await unmount(root);
  });

  it('switches to read-only when the server rejects activation with 403', async () => {
    const backend = versionBackend({ versions: [v1, v2], active: 1, access: 404, activate: () => ({ status: 403, message: 'admin authorization required' }) });
    const { container, root } = await openHistory();
    await click(button(container, 'Activate version 2…'));
    await click(button(dialog(container)!, 'Activate version 2'));
    await waitFor(() => expect(container.querySelector('[data-version-outcome="forbidden"]')?.textContent).toContain('An admin credential is required to activate or roll back'), container);
    expect(Array.from(container.querySelectorAll('button')).some((item) => /Activate version|Roll back/.test(item.textContent ?? ''))).toBe(false);
    expect(backend.active()).toBe(1);
    await unmount(root);
  });
});
