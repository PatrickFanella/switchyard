// @vitest-environment jsdom

import { act } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { button, controlByLabel, expectText, jsonResponse, mockDashboardFetch, renderApp, unmount, waitFor } from '../test-utils';
import type { WorkflowDefinition } from '../types';
import type { ControlAccess } from '../workflowDrafts';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

afterEach(() => {
  window.location.hash = '';
  document.body.innerHTML = '';
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

const profiles = {
  profiles: [
    { id: 'content-writer', name: 'Content Writer', role: 'writer', default_model: 'qwen3.5:latest', allowed_models: ['qwen3.5:latest'], prompt_contract: 'Write grounded content.', allowed_executors: ['model'] },
    { id: 'open-pilot-executor', name: 'Open Pilot Executor', role: 'executor', default_model: 'gpt-5.4', prompt_contract: 'Implement.', allowed_executors: ['opencode', 'command'] },
  ],
};

function digest(overrides: Partial<WorkflowDefinition> = {}): WorkflowDefinition {
  return {
    id: 'daily-digest',
    name: 'Daily Digest',
    description: 'Summarize the changelog.',
    status: 'active',
    version: 3,
    input_shape: { type: 'object', properties: { topic: { type: 'string', description: 'Digest topic' } }, required: ['topic'] },
    pipeline: {
      id: 'daily-digest-pipeline',
      workflow_id: 'daily-digest',
      name: 'Daily Digest',
      // Stored out of order on purpose: the view must follow position.
      steps: [
        { id: 'summarize', position: 2, name: 'digest.summarize', executor: 'model', profile: 'content-writer', model: '${model}', inputs: { prompt: 'Summarize {{steps.fetch.outputs.body}}' } },
        { id: 'fetch', position: 1, name: 'digest.fetch', executor: 'http', inputs: { url: 'https://docs.example.test/changelog.json', allow_http: 'true' }, output_shape: { type: 'object', properties: { body: { type: 'string', maxLength: 4096 } }, required: ['body'] } },
      ],
    },
    ...overrides,
  };
}

const draftReport: WorkflowDefinition = {
  id: 'weekly-report', name: 'Weekly Report', description: 'Draft report', status: 'draft', version: 1,
  pipeline: { id: 'weekly-report-pipeline', workflow_id: 'weekly-report', name: 'Weekly Report', steps: [{ id: 'collect', position: 1, name: 'report.collect', executor: 'internal' }] },
};

type BackendOptions = {
  definitions?: WorkflowDefinition[];
  access?: ControlAccess | number;
  listStatus?: number;
  putStatus?: (definition: WorkflowDefinition) => { status: number; message: string } | undefined;
};

type PutBody = WorkflowDefinition & { expected_version?: number };

/**
 * A small in-memory stand-in for the definition API: PUT stores a new draft
 * version, keeps the active version, and enforces expected_version like the
 * server.
 */
function workflowBackend(options: BackendOptions = {}) {
  const store = new Map((options.definitions ?? []).map((definition) => [definition.id, structuredClone(definition)]));
  const history = new Map((options.definitions ?? []).map((definition) => [definition.id, [structuredClone(definition)]]));
  const puts: PutBody[] = [];
  const mutations: string[] = [];
  let listStatus = options.listStatus;
  const fetch = mockDashboardFetch((url, init) => {
    const method = init.method ?? 'GET';
    if (method !== 'GET') mutations.push(`${method} ${url.pathname}`);
    if (url.pathname === '/api/control-access') {
      return typeof options.access === 'number' ? jsonResponse({ error: { message: 'not found' } }, { status: options.access }) : jsonResponse(options.access ?? { role: 'admin', can_write: true });
    }
    if (url.pathname === '/api/agent-profiles') return jsonResponse(profiles);
    if (url.pathname === '/api/workflow-definitions' && method === 'GET') {
      if (listStatus) return jsonResponse({ error: { message: 'unavailable' } }, { status: listStatus });
      return jsonResponse({ workflow_definitions: [...store.values()] });
    }
    const versionsMatch = /^\/api\/workflow-definitions\/([^/]+)\/versions$/.exec(url.pathname);
    if (versionsMatch && method === 'GET') {
      const versions = history.get(decodeURIComponent(versionsMatch[1]));
      return versions ? jsonResponse({ workflow_definitions: versions.slice().reverse() }) : jsonResponse({ error: { message: 'not found' } }, { status: 404 });
    }
    const match = /^\/api\/workflow-definitions\/([^/]+)$/.exec(url.pathname);
    if (!match) return undefined;
    const id = decodeURIComponent(match[1]);
    if (method === 'GET') {
      const stored = store.get(id);
      return stored ? jsonResponse({ workflow_definition: stored }) : jsonResponse({ error: { message: 'not found' } }, { status: 404 });
    }
    if (method === 'PUT') {
      const body = JSON.parse(String(init.body)) as PutBody;
      puts.push(body);
      const rejection = options.putStatus?.(body);
      if (rejection) return jsonResponse({ error: { message: rejection.message } }, { status: rejection.status });
      const existing = store.get(id);
      const latest = existing?.version ?? 0;
      if (body.expected_version !== undefined && body.expected_version !== latest) {
        return jsonResponse({ error: { message: `state conflict: workflow definition "${id}" latest version is ${latest}, expected_version was ${body.expected_version}` } }, { status: 409 });
      }
      const { expected_version: _expected, ...definition } = body;
      void _expected;
      const activeVersion = existing?.active_version ?? (existing?.status === 'active' ? existing.version : undefined);
      const saved = { ...definition, id, status: 'draft', version: latest + 1, ...(activeVersion ? { active_version: activeVersion } : {}), updated_at: '2026-09-24T12:00:00.000Z' };
      store.set(id, saved);
      history.set(id, [...(history.get(id) ?? []), saved]);
      return jsonResponse({ workflow_definition: saved });
    }
    return undefined;
  });
  return { fetch, store, puts, mutations, setListStatus: (status?: number) => { listStatus = status; } };
}

function setValue(element: Element, value: string) {
  const prototype = element instanceof HTMLSelectElement ? HTMLSelectElement.prototype : element instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  Object.getOwnPropertyDescriptor(prototype, 'value')?.set?.call(element, value);
  element.dispatchEvent(new Event(element instanceof HTMLSelectElement ? 'change' : 'input', { bubbles: true }));
}

async function fill(scope: HTMLElement, label: string, value: string) {
  await act(async () => { setValue(controlByLabel(scope, label), value); });
}

async function click(element: HTMLElement) {
  await act(async () => { element.click(); });
}

async function press(element: HTMLElement, key: string, init: KeyboardEventInit = {}) {
  await act(async () => { element.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true, ...init })); });
}

function step(container: HTMLElement, index: number) {
  const element = container.querySelector<HTMLElement>(`[data-step-index="${index}"]`);
  if (!element) throw new Error(`step ${index} not found`);
  return element;
}

function labelled(container: HTMLElement, label: string) {
  const element = container.querySelector<HTMLElement>(`[aria-label="${label}"]`);
  if (!element) throw new Error(`element labelled ${label} not found`);
  return element;
}

function catalogEntries(container: HTMLElement) {
  return Array.from(container.querySelectorAll<HTMLButtonElement>('button[data-catalog-entry]')).map((item) => item.dataset.catalogEntry);
}

describe('WorkflowCatalogPage catalog', () => {
  it('searches and filters definitions and shows the ordered step view', async () => {
    window.location.hash = '#/workflows';
    workflowBackend({ definitions: [digest(), draftReport] });
    const { container, root } = await renderApp('2 of 2 workflows shown');
    expect(catalogEntries(container)).toEqual(['daily-digest', 'weekly-report']);
    expect(container.querySelector('[data-catalog-entry="daily-digest"]')?.textContent).toContain('active');
    expect(container.querySelector('[data-catalog-entry="weekly-report"]')?.textContent).toContain('draft');

    await fill(container, 'Search workflows', 'summarize');
    expect(catalogEntries(container)).toEqual(['daily-digest']);
    await fill(container, 'Search workflows', 'nothing-matches');
    expectText(container, 'No workflows match this search.');
    await fill(container, 'Search workflows', '');
    await fill(container, 'Status', 'draft');
    expect(catalogEntries(container)).toEqual(['weekly-report']);
    await fill(container, 'Status', 'all');

    await click(container.querySelector<HTMLButtonElement>('[data-catalog-entry="daily-digest"]')!);
    await waitFor(() => expect(container.querySelector('[data-workflow-detail="daily-digest"]')).not.toBeNull(), container);
    expect(window.location.hash).toBe('#/workflows/view/daily-digest');
    const steps = Array.from(container.querySelectorAll<HTMLElement>('[data-step-id]')).map((item) => item.dataset.stepId);
    expect(steps).toEqual(['fetch', 'summarize']);
    expectText(container, 'Reads outputs: fetch.body');
    expectText(container, 'Declared outputs: body (string, required)');
    expectText(container, 'topic · string · required · Digest topic');
    expectText(container, 'This version serves new runs.');
    // Version 3 is already active, so the version history offers no activation.
    await waitFor(() => expect(container.querySelector('[data-version-history="daily-digest"]')).not.toBeNull(), container);
    expect(Array.from(container.querySelectorAll('button')).some((item) => /activate/i.test(item.textContent ?? ''))).toBe(false);
    await unmount(root);
  });

  it('shows empty, loading-error and retry states', async () => {
    window.location.hash = '#/workflows';
    let backend = workflowBackend();
    let rendered = await renderApp('No workflow definitions yet. Create a draft to start.');
    await unmount(rendered.root);
    document.body.innerHTML = '';

    backend = workflowBackend({ definitions: [draftReport], listStatus: 503 });
    rendered = await renderApp('Workflow definitions unavailable');
    expectText(rendered.container, 'Switchyard API returned 503');
    backend.setListStatus(undefined);
    await click(button(rendered.container, 'Retry'));
    await waitFor(() => expect(catalogEntries(rendered.container)).toEqual(['weekly-report']), rendered.container);
    await unmount(rendered.root);
  });

  it('reports a missing definition deep link', async () => {
    window.location.hash = '#/workflows/view/missing';
    workflowBackend({ definitions: [draftReport] });
    const { container, root } = await renderApp('Workflow definition "missing" was not found.');
    expect(container.querySelector('[role="alert"]')).not.toBeNull();
    await unmount(root);
  });
});

describe('WorkflowCatalogPage keyboard use', () => {
  it('moves between search and results with arrow keys and saves or leaves the editor with shortcuts', async () => {
    window.location.hash = '#/workflows';
    const backend = workflowBackend({ definitions: [digest(), draftReport] });
    const { container, root } = await renderApp('2 of 2 workflows shown');
    const search = controlByLabel(container, 'Search workflows') as HTMLInputElement;
    search.focus();
    await press(search, 'ArrowDown');
    expect(document.activeElement?.getAttribute('data-catalog-entry')).toBe('daily-digest');
    await press(document.activeElement as HTMLElement, 'ArrowDown');
    expect(document.activeElement?.getAttribute('data-catalog-entry')).toBe('weekly-report');
    await press(document.activeElement as HTMLElement, 'ArrowUp');
    await press(document.activeElement as HTMLElement, 'ArrowUp');
    expect(document.activeElement).toBe(search);

    window.location.hash = '#/workflows/edit/weekly-report';
    await act(async () => { window.dispatchEvent(new HashChangeEvent('hashchange')); });
    await waitFor(() => expectText(container, 'Editing weekly-report'), container);
    const name = controlByLabel(container, 'Workflow name') as HTMLInputElement;
    await fill(container, 'Workflow name', 'Weekly Report v2');
    await press(name, 's', { ctrlKey: true });
    await waitFor(() => expectText(container, 'Saved weekly-report as draft version 2.'), container);
    expect(backend.puts).toHaveLength(1);
    expect(backend.puts[0].name).toBe('Weekly Report v2');

    // Escape on a clean form leaves without asking.
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    window.location.hash = '#/workflows/edit/weekly-report';
    await act(async () => { window.dispatchEvent(new HashChangeEvent('hashchange')); });
    await waitFor(() => expectText(container, 'Editing weekly-report'), container);
    await press(controlByLabel(container, 'Workflow name'), 'Escape');
    await waitFor(() => expect(window.location.hash).toBe('#/workflows/view/weekly-report'), container);
    expect(confirm).not.toHaveBeenCalled();
    await unmount(root);
  });
});

describe('WorkflowCatalogPage draft journey', () => {
  it('creates, binds outputs, saves, edits, and reloads a draft without activating it', async () => {
    window.location.hash = '#/workflows';
    const backend = workflowBackend({ definitions: [draftReport] });
    let rendered = await renderApp('1 of 1 workflows shown');
    let { container } = rendered;

    await click(button(container, 'New draft'));
    await waitFor(() => expectText(container, 'New draft'), container);
    expect(window.location.hash).toBe('#/workflows/new');

    await fill(container, 'Workflow ID', 'release-notes');
    await fill(container, 'Workflow name', 'Release Notes');
    await fill(container, 'Description', 'Summarize releases');
    await click(button(container, 'Add input field'));
    const inputRow = container.querySelector<HTMLElement>('[data-input-field="0"]')!;
    await fill(inputRow, 'Field name', 'version');
    await act(async () => { (controlByLabel(inputRow, 'Required') as HTMLInputElement).click(); });

    // Step 1: an internal step that declares a required string output.
    await fill(step(container, 0), 'Step ID', 'collect');
    await fill(step(container, 0), 'Step name', 'release.collect');
    await fill(step(container, 0), 'Summary', 'Release {{version}}');
    await click(labelled(container, 'Declare output for step collect'));
    await fill(step(container, 0), 'Output name', 'headline');
    await fill(step(container, 0), 'maxLength', '200');
    await act(async () => { (controlByLabel(step(container, 0), 'Required output') as HTMLInputElement).click(); });
    expect(step(container, 0).textContent).toContain('The first step has no earlier outputs to bind.');

    // Step 2: a model step with a compatible profile that binds the output.
    await click(button(container, 'Add step'));
    await fill(step(container, 1), 'Step ID', 'write');
    await fill(step(container, 1), 'Step name', 'release.write');
    await fill(step(container, 1), 'Executor', 'model');
    const profileOptions = Array.from((controlByLabel(step(container, 1), 'Agent profile') as HTMLSelectElement).options).map((option) => option.value);
    expect(profileOptions).toEqual(['', 'content-writer']);
    await fill(step(container, 1), 'Agent profile', 'content-writer');
    await fill(step(container, 1), 'Model', '${model}');
    await fill(step(container, 1), 'Prompt', 'Write notes for ');
    const references = Array.from((controlByLabel(step(container, 1), 'Earlier step output') as HTMLSelectElement).options).map((option) => option.value);
    expect(references[0]).toBe('{{steps.collect.outputs.headline}}');
    expect(references).toContain('{{steps.collect.outputs.summary}}');
    await fill(step(container, 1), 'Earlier step output', '{{steps.collect.outputs.headline}}');
    await fill(step(container, 1), 'Insert into input', 'prompt');
    await click(button(step(container, 1), 'Insert reference'));
    expect((controlByLabel(step(container, 1), 'Prompt') as unknown as HTMLTextAreaElement).value).toBe('Write notes for {{steps.collect.outputs.headline}}');

    await click(button(container, 'Save draft'));
    await waitFor(() => expectText(container, 'Saved release-notes as draft version 1.'), container);
    expect(window.location.hash).toBe('#/workflows/view/release-notes');
    expect(backend.puts[0]).toEqual({
      id: 'release-notes', name: 'Release Notes', description: 'Summarize releases', status: 'draft', expected_version: 0, owns: [], uses: [],
      input_shape: { type: 'object', properties: { version: { type: 'string' } }, required: ['version'] },
      pipeline: {
        id: 'release-notes-pipeline', workflow_id: 'release-notes', name: 'Release Notes',
        steps: [
          { id: 'collect', position: 1, name: 'release.collect', executor: 'internal', inputs: { summary: 'Release {{version}}' }, output_shape: { type: 'object', properties: { headline: { type: 'string', maxLength: 200 } }, required: ['headline'] } },
          { id: 'write', position: 2, name: 'release.write', executor: 'model', profile: 'content-writer', model: '${model}', inputs: { prompt: 'Write notes for {{steps.collect.outputs.headline}}' } },
        ],
      },
    });
    await waitFor(() => expectText(container, 'Reads outputs: collect.headline'), container);
    expectText(container, 'This is a saved draft. No version is active');

    // Edit: reorder is allowed, but here change metadata and add a second output.
    await click(button(container, 'Edit draft'));
    await waitFor(() => expectText(container, 'Editing release-notes · based on version 1'), container);
    expect((controlByLabel(container, 'Workflow ID') as HTMLInputElement).disabled).toBe(true);
    await fill(container, 'Workflow name', 'Release Notes (weekly)');
    await click(labelled(container, 'Declare output for step write'));
    await fill(step(container, 1), 'Output name', 'notes');
    await click(button(container, 'Save draft'));
    await waitFor(() => expectText(container, 'Saved release-notes as draft version 2.'), container);

    // Reload the dashboard from scratch: the saved draft comes back from the API.
    await unmount(rendered.root);
    document.body.innerHTML = '';
    window.location.hash = '#/workflows/view/release-notes';
    rendered = await renderApp('Release Notes (weekly)');
    container = rendered.container;
    await waitFor(() => expect(container.querySelector('[data-workflow-detail="release-notes"]')?.textContent).toContain('version 2'), container);
    expectText(container, 'Declared outputs: notes (string)');
    await click(button(container, 'Edit draft'));
    await waitFor(() => expectText(container, 'Editing release-notes · based on version 2'), container);
    expect((controlByLabel(container, 'Workflow name') as HTMLInputElement).value).toBe('Release Notes (weekly)');
    expect((controlByLabel(step(container, 1), 'Prompt') as unknown as HTMLTextAreaElement).value).toBe('Write notes for {{steps.collect.outputs.headline}}');

    expect(backend.mutations).toEqual(['PUT /api/workflow-definitions/release-notes', 'PUT /api/workflow-definitions/release-notes']);
    await unmount(rendered.root);
  });

  it('duplicates a definition under a new ID and reorders steps', async () => {
    window.location.hash = '#/workflows/view/daily-digest';
    const backend = workflowBackend({ definitions: [digest()] });
    const { container, root } = await renderApp('Steps in run order');
    await click(button(container, 'Duplicate'));
    await waitFor(() => expect((controlByLabel(container, 'Workflow ID') as HTMLInputElement).value).toBe('daily-digest-copy'), container);
    expect((controlByLabel(container, 'Workflow name') as HTMLInputElement).value).toBe('Daily Digest (copy)');
    expect((controlByLabel(step(container, 0), 'Step ID') as HTMLInputElement).value).toBe('fetch');
    expect((controlByLabel(step(container, 0), 'Allow HTTP request') as HTMLInputElement).checked).toBe(true);

    // Moving summarize first makes its reference point at a later step; the editor flags it.
    await click(labelled(container, 'Move step summarize up'));
    expect((controlByLabel(step(container, 0), 'Step ID') as HTMLInputElement).value).toBe('summarize');
    expect(labelled(container, 'Reference problems in step summarize').textContent).toContain('step "fetch" does not run before this step.');
    await click(labelled(container, 'Move step summarize down'));

    await click(button(container, 'Save draft'));
    await waitFor(() => expectText(container, 'Saved daily-digest-copy as draft version 1.'), container);
    expect(backend.puts[0].pipeline.id).toBe('daily-digest-copy-pipeline');
    expect(backend.puts[0].pipeline.steps.map((item) => `${item.position}:${item.id}`)).toEqual(['1:fetch', '2:summarize']);
    expect(backend.store.get('daily-digest')?.version).toBe(3);
    await unmount(root);
  });
});

describe('WorkflowCatalogPage validation', () => {
  it('surfaces server validation errors verbatim and client encoding errors before saving', async () => {
    window.location.hash = '#/workflows/edit/weekly-report';
    const backend = workflowBackend({
      definitions: [draftReport],
      putStatus: (definition) => definition.pipeline.steps.some((item) => item.output_shape?.properties?.count?.type === 'object')
        ? { status: 400, message: 'invalid input: step "collect" output_shape is not a supported output contract: output_shape.properties.count: unsupported keyword "pattern"' }
        : undefined,
    });
    const { container, root } = await renderApp('Editing weekly-report');

    await click(labelled(container, 'Declare output for step collect'));
    await fill(step(container, 0), 'Output name', 'count');
    await fill(step(container, 0), 'Output type', 'object');
    await fill(step(container, 0), 'Nested keywords (JSON)', '{"pattern": ');
    await click(button(container, 'Save draft'));
    const alert = () => container.querySelector('[data-save-feedback]');
    await waitFor(() => expect(alert()?.textContent).toContain('additional keywords must be a JSON object'), container);
    expect(alert()?.getAttribute('data-save-feedback')).toBe('client');
    expect(document.activeElement).toBe(alert());
    expect(backend.puts).toHaveLength(0);

    await fill(step(container, 0), 'Nested keywords (JSON)', '{"pattern": "^[0-9]+$"}');
    await click(button(container, 'Save draft'));
    await waitFor(() => expect(alert()?.getAttribute('data-save-feedback')).toBe('validation'), container);
    expect(alert()?.textContent).toContain('The server rejected this draft');
    expect(alert()?.textContent).toContain('output_shape.properties.count: unsupported keyword "pattern"');
    expect(backend.puts[0].pipeline.steps[0].output_shape).toEqual({ type: 'object', properties: { count: { type: 'object', pattern: '^[0-9]+$' } } });
    expect(backend.store.get('weekly-report')?.version).toBe(1);
    // The rejected edits stay in the form.
    expect((controlByLabel(step(container, 0), 'Output name') as HTMLInputElement).value).toBe('count');
    await unmount(root);
  });

  it('rejects an ID that already exists and a stale edit', async () => {
    window.location.hash = '#/workflows/new';
    const backend = workflowBackend({ definitions: [draftReport] });
    const { container, root } = await renderApp('New draft');
    await click(button(container, 'Save draft'));
    await waitFor(() => expect(container.querySelector('[data-save-feedback]')?.textContent).toContain('Workflow ID is required.'), container);
    await fill(container, 'Workflow ID', 'weekly-report');
    await fill(container, 'Workflow name', 'Clash');
    await fill(step(container, 0), 'Step name', 'start');
    await click(button(container, 'Save draft'));
    await waitFor(() => expect(container.querySelector('[data-save-feedback]')?.textContent).toContain('A workflow definition with ID "weekly-report" already exists.'), container);

    // Discard the new draft (confirm) and open the existing one.
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    window.location.hash = '#/workflows/edit/weekly-report';
    await act(async () => { window.dispatchEvent(new HashChangeEvent('hashchange')); });
    await waitFor(() => expectText(container, 'Editing weekly-report · based on version 1'), container);
    await fill(container, 'Workflow name', 'Mine');
    backend.store.set('weekly-report', { ...draftReport, name: 'Theirs', version: 2 });
    await click(button(container, 'Save draft'));
    await waitFor(() => expect(container.querySelector('[data-save-feedback="conflict"]')?.textContent).toContain('changed since you opened it (version 1 → 2)'), container);
    expect(backend.puts).toHaveLength(0);
    await click(button(container, 'Reload latest version (discard my edits)'));
    await waitFor(() => expectText(container, 'Editing weekly-report · based on version 2'), container);
    expect((controlByLabel(container, 'Workflow name') as HTMLInputElement).value).toBe('Theirs');
    await unmount(root);
  });

  it('saves a draft over an active definition while the active version keeps serving', async () => {
    window.location.hash = '#/workflows/edit/daily-digest';
    const backend = workflowBackend({ definitions: [digest()] });
    const { container, root } = await renderApp('Editing daily-digest');
    expectText(container, 'Version 3 of daily-digest is active. It keeps serving new runs after you save');
    await fill(container, 'Description', 'Updated');
    await click(button(container, 'Save draft'));
    await waitFor(() => expectText(container, 'Saved daily-digest as draft version 4. It is not active; version 3 keeps serving new runs until you activate the draft.'), container);
    expect(backend.puts[0].status).toBe('draft');
    expect(backend.puts[0].expected_version).toBe(3);
    await waitFor(() => expect(container.querySelector('[data-catalog-entry="daily-digest"]')?.textContent).toContain('draft · v3 active'), container);
    expectText(container, 'Version 3 is active and keeps serving new runs until this draft is activated.');
    await unmount(root);
  });

  it('reports a server-side stale-version conflict and keeps the edits', async () => {
    window.location.hash = '#/workflows/edit/weekly-report';
    const backend = workflowBackend({
      definitions: [draftReport],
      // Another writer saved between the editor's re-read and its PUT.
      putStatus: () => ({ status: 409, message: 'state conflict: workflow definition "weekly-report" latest version is 2, expected_version was 1; reload and reapply your changes' }),
    });
    const { container, root } = await renderApp('Editing weekly-report');
    await fill(container, 'Workflow name', 'Mine');
    await click(button(container, 'Save draft'));
    await waitFor(() => expect(container.querySelector('[data-save-feedback="conflict"]')?.textContent).toContain('latest version is 2, expected_version was 1'), container);
    expect(backend.puts[0].expected_version).toBe(1);
    expect((controlByLabel(container, 'Workflow name') as HTMLInputElement).value).toBe('Mine');
    expect(button(container, 'Reload latest version (discard my edits)')).toBeTruthy();
    await unmount(root);
  });

  it('creates with expected_version 0 so the server rejects an existing ID', async () => {
    window.location.hash = '#/workflows/new';
    const backend = workflowBackend();
    const { container, root } = await renderApp('New draft');
    await fill(container, 'Workflow ID', 'fresh');
    await fill(container, 'Workflow name', 'Fresh');
    await fill(step(container, 0), 'Step name', 'start');
    await click(button(container, 'Save draft'));
    await waitFor(() => expectText(container, 'Saved fresh as draft version 1.'), container);
    expect(backend.puts[0].expected_version).toBe(0);
    await unmount(root);
  });
});

describe('WorkflowCatalogPage read-only access', () => {
  it('hides authoring actions and disables the editor for a read credential', async () => {
    window.location.hash = '#/workflows/view/weekly-report';
    const backend = workflowBackend({ definitions: [draftReport], access: { role: 'read', can_write: false } });
    const { container, root } = await renderApp('Read-only access: you can browse and compare workflow definitions');
    await waitFor(() => expect(container.querySelector('[data-workflow-detail="weekly-report"]')).not.toBeNull(), container);
    for (const name of ['New draft', 'Edit draft', 'Duplicate']) {
      expect(Array.from(container.querySelectorAll('button')).some((item) => item.textContent?.trim() === name)).toBe(false);
    }

    window.location.hash = '#/workflows/edit/weekly-report';
    await act(async () => { window.dispatchEvent(new HashChangeEvent('hashchange')); });
    await waitFor(() => expect(container.querySelector('form [data-read-only="true"]')).not.toBeNull(), container);
    expect((controlByLabel(container, 'Workflow name') as HTMLInputElement).disabled).toBe(true);
    expect(button(container, 'Save draft').disabled).toBe(true);

    window.location.hash = '#/workflows/new';
    await act(async () => { window.dispatchEvent(new HashChangeEvent('hashchange')); });
    await waitFor(() => expectText(container, 'An admin credential is required to create workflow drafts.'), container);
    expect(backend.mutations).toEqual([]);
    await unmount(root);
  });

  it('switches to read-only when the server rejects a save with 403', async () => {
    window.location.hash = '#/workflows/edit/weekly-report';
    const backend = workflowBackend({ definitions: [draftReport], access: 404, putStatus: () => ({ status: 403, message: 'admin authorization required' }) });
    const { container, root } = await renderApp('Editing weekly-report');
    await fill(container, 'Workflow name', 'Denied');
    await click(button(container, 'Save draft'));
    await waitFor(() => expect(container.querySelector('[data-save-feedback="forbidden"]')?.textContent).toContain('An admin credential is required to save workflow drafts.'), container);
    expect((controlByLabel(container, 'Workflow name') as HTMLInputElement).disabled).toBe(true);
    expect(backend.store.get('weekly-report')?.name).toBe('Weekly Report');
    await unmount(root);
  });
});

describe('WorkflowCatalogPage unsaved changes', () => {
  it('asks before leaving a dirty draft through the sidebar, browser navigation, or cancel', async () => {
    window.location.hash = '#/workflows/edit/weekly-report';
    workflowBackend({ definitions: [draftReport] });
    const { container, root } = await renderApp('Editing weekly-report');
    expectText(container, 'no unsaved changes');
    await fill(container, 'Workflow name', 'Unsaved');
    expectText(container, 'unsaved changes');
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);

    // Sidebar navigation is refused.
    await click(button(container, 'Overview'));
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(window.location.hash).toBe('#/workflows/edit/weekly-report');
    expectText(container, 'Editing weekly-report');

    // Browser navigation to another view is refused and the hash restored.
    window.location.hash = '#/runs';
    await act(async () => { window.dispatchEvent(new HashChangeEvent('hashchange')); });
    expect(confirm).toHaveBeenCalledTimes(2);
    expect(window.location.hash).toBe('#/workflows/edit/weekly-report');
    expect((controlByLabel(container, 'Workflow name') as HTMLInputElement).value).toBe('Unsaved');

    // Cancel inside the page is refused too.
    await click(button(container, 'Cancel'));
    expect(confirm).toHaveBeenCalledTimes(3);
    expectText(container, 'Editing weekly-report');

    // The page blocks unload while dirty.
    const unload = new Event('beforeunload', { cancelable: true });
    window.dispatchEvent(unload);
    expect(unload.defaultPrevented).toBe(true);

    // Confirming discards the edits.
    confirm.mockReturnValue(true);
    await click(button(container, 'Cancel'));
    await waitFor(() => expect(container.querySelector('[data-workflow-detail="weekly-report"]')).not.toBeNull(), container);
    expect(window.location.hash).toBe('#/workflows/view/weekly-report');
    expectText(container, 'Weekly Report');
    const cleanUnload = new Event('beforeunload', { cancelable: true });
    window.dispatchEvent(cleanUnload);
    expect(cleanUnload.defaultPrevented).toBe(false);
    await unmount(root);
  });
});
