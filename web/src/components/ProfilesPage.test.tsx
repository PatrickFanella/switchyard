// @vitest-environment jsdom

import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { confirmNavigation } from '../navigationGuard';
import type { AgentProfileIdentity, AgentProfileVersion } from '../profileVersions';
import { button, controlByLabel, jsonResponse, waitFor } from '../test-utils';
import type { AgentProfile } from '../types';
import { ProfilesPage } from './ProfilesPage';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const staticWriter: AgentProfile = { id: 'content-writer', name: 'Content Writer', role: 'writer', default_model: 'gpt-5.4', prompt_contract: 'Write clearly.', allowed_executors: ['model'], version: 0, status: 'static', source: 'static', prompt_contract_sha256: 'a'.repeat(64) };
const reviewer: AgentProfile = { id: 'operator-reviewer', name: 'Operator Reviewer', role: 'reviewer', default_model: 'gpt-5.4', prompt_contract: 'Review.', allowed_executors: ['model', 'opencode'], version: 0, status: 'static', source: 'static' };

type Backend = {
  profile: AgentProfile;
  identity: AgentProfileIdentity;
  versions: AgentProfileVersion[];
  requests: Array<{ method: string; path: string; body?: Record<string, unknown> }>;
};

function version(n: number, status: AgentProfileVersion['status'], extra: Partial<AgentProfileVersion> = {}): AgentProfileVersion {
  return { ...staticWriter, version: n, status, source: 'persisted', created_at: '2026-09-24T00:00:00.000Z', updated_at: '2026-09-24T00:00:00.000Z', provenance: { origin: 'operator', actor: 'token:admin' }, ...extra };
}

/** In-memory stand-in for the #73 profile routes with expected_version conflicts. */
function profileBackend(options: { access?: { role: string; can_write: boolean }; versions?: AgentProfileVersion[]; identity?: Partial<AgentProfileIdentity>; putStatus?: () => { status: number; message: string } | undefined } = {}) {
  const backend: Backend = {
    profile: staticWriter,
    identity: { id: 'content-writer', active_version: 0, draft_version: 0, latest_version: 0, static_default: true, ...options.identity },
    versions: options.versions ?? [],
    requests: [],
  };
  const fetch = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = new URL(String(input), 'http://switchyard.test');
    const method = init.method ?? 'GET';
    const body = init.body ? (JSON.parse(String(init.body)) as Record<string, unknown>) : undefined;
    backend.requests.push({ method, path: url.pathname, body });
    if (url.pathname === '/api/control-access') return jsonResponse(options.access ?? { role: 'admin', can_write: true });
    if (url.pathname === '/api/agent-profiles') return jsonResponse({ profiles: [backend.profile, reviewer], identities: backend.versions.length ? [backend.identity] : [] });
    if (url.pathname === '/api/agent-profiles/content-writer' && method === 'GET') return jsonResponse({ profile: backend.profile, identity: backend.identity });
    if (url.pathname === '/api/agent-profiles/content-writer/versions') return jsonResponse({ profile_versions: [...backend.versions].sort((a, b) => (b.version ?? 0) - (a.version ?? 0)) });
    if (url.pathname === '/api/agent-profiles/content-writer/audit') return jsonResponse({ audit: [] });
    if (url.pathname === '/api/agent-profiles/content-writer' && method === 'PUT') {
      const forced = options.putStatus?.();
      if (forced) return jsonResponse({ error: { message: forced.message } }, { status: forced.status });
      if (body?.expected_version !== backend.identity.latest_version) return jsonResponse({ error: { message: 'conflict' } }, { status: 409 });
      const next = version(backend.identity.latest_version + 1, 'draft', { name: String(body?.name), prompt_contract: String(body?.prompt_contract) });
      backend.versions = backend.versions.map((item) => (item.status === 'draft' ? { ...item, status: 'superseded' as const } : item)).concat(next);
      backend.identity = { ...backend.identity, draft_version: next.version ?? 0, latest_version: next.version ?? 0 };
      return jsonResponse({ profile_version: next, identity: backend.identity });
    }
    if (url.pathname === '/api/agent-profiles/content-writer/activate') {
      const target = backend.versions.find((item) => item.version === body?.version);
      if (!target || target.status !== 'draft') return jsonResponse({ error: { message: 'conflict' } }, { status: 409 });
      backend.versions = backend.versions.map((item) => (item.version === target.version ? { ...item, status: 'active' as const, activated_at: '2026-09-24T01:00:00.000Z' } : item.status === 'active' ? { ...item, status: 'superseded' as const } : item));
      backend.identity = { ...backend.identity, active_version: target.version ?? 0, draft_version: 0 };
      backend.profile = { ...target, status: 'active' };
      return jsonResponse({ profile: backend.profile, profile_version: { ...target, status: 'active' }, identity: backend.identity });
    }
    if (url.pathname === '/api/agent-profiles/content-writer/rollback') {
      const restored = version(backend.identity.latest_version + 1, 'active', { provenance: { origin: 'rollback', rolled_back_from_version: Number(body?.version) } });
      backend.versions = backend.versions.map((item) => (item.status === 'active' ? { ...item, status: 'superseded' as const } : item)).concat(restored);
      backend.identity = { ...backend.identity, active_version: restored.version ?? 0, latest_version: restored.version ?? 0 };
      return jsonResponse({ profile: restored, profile_version: restored, identity: backend.identity });
    }
    throw new Error(`Unexpected fetch: ${method} ${url.pathname}`);
  });
  vi.stubGlobal('fetch', fetch);
  return backend;
}

let root: Root | null = null;

async function render() {
  const container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root?.render(<ProfilesPage />);
  });
  await waitFor(() => expect(container.textContent).toContain('Write clearly.'), container);
  return container;
}

function setValue(element: Element, value: string) {
  const prototype = element instanceof HTMLSelectElement ? HTMLSelectElement.prototype : element instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  Object.getOwnPropertyDescriptor(prototype, 'value')?.set?.call(element, value);
  element.dispatchEvent(new Event(element instanceof HTMLSelectElement ? 'change' : 'input', { bubbles: true }));
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

afterEach(async () => {
  await act(async () => root?.unmount());
  root = null;
  document.body.innerHTML = '';
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe('ProfilesPage', () => {
  it('lists effective profiles and inspects the serving version, policy, and history', async () => {
    profileBackend({ identity: { active_version: 1, latest_version: 2, draft_version: 2 }, versions: [version(1, 'active', { activated_at: '2026-09-24T00:30:00.000Z' }), version(2, 'draft', { provenance: { origin: 'operator', actor: 'proxy:ops', note: 'tighten', derived_from_version: 1 } })] });
    const container = await render();
    const text = container.textContent ?? '';
    expect(text).toContain('Content Writer');
    expect(text).toContain('Operator Reviewer');
    expect(text).toContain('draft v2 pending');
    expect(text).toContain('active 1 · draft 2 · latest 2');
    expect(container.querySelector('[data-testid="profile-version-2"]')?.textContent).toContain('by proxy:ops from v1');
    expect(button(container, 'Activate v2')).toBeTruthy();
  });

  it('is read-only for read credentials', async () => {
    profileBackend({ access: { role: 'read', can_write: false }, identity: { active_version: 2, latest_version: 2 }, versions: [version(1, 'superseded', { activated_at: '2026-09-24T00:10:00.000Z' }), version(2, 'active', { activated_at: '2026-09-24T00:30:00.000Z' })] });
    const container = await render();
    await waitFor(() => expect(container.textContent).toContain('Read-only access'), container);
    expect(() => button(container, 'Edit as new draft')).toThrow();
    expect(container.querySelector('button[aria-label="Roll back to version 1"]')).toBeNull();
  });

  it('saves a draft with expected_version and reports it does not serve until activated', async () => {
    const backend = profileBackend();
    const container = await render();
    await click(button(container, 'Edit as new draft'));
    await fill(container, 'Prompt contract', 'Write clearly, with citations.');
    await click(button(container, 'Save draft'));
    await waitFor(() => expect(container.textContent).toContain('Saved draft v1 of content-writer. It does not serve runs until activated.'), container);
    const put = backend.requests.find((request) => request.method === 'PUT');
    expect(put?.body).toMatchObject({ expected_version: 0, prompt_contract: 'Write clearly, with citations.', allowed_executors: ['model'] });
    expect(button(container, 'Activate v1')).toBeTruthy();
  });

  it('keeps edits on a stale-version conflict and saves against the reloaded version', async () => {
    const backend = profileBackend();
    const container = await render();
    await click(button(container, 'Edit as new draft'));
    await fill(container, 'Name', 'Content Writer v2');
    // Another operator saves first.
    backend.identity = { ...backend.identity, latest_version: 1, draft_version: 1 };
    backend.versions = [version(1, 'draft')];
    await click(button(container, 'Save draft'));
    await waitFor(() => expect(container.textContent).toContain('changed since you opened it'), container);
    expect((controlByLabel(container, 'Name') as HTMLInputElement).value).toBe('Content Writer v2');
    await click(button(container, 'Reload latest version'));
    await waitFor(() => expect(container.textContent).toContain('New draft from version v1'), container);
    await click(button(container, 'Save draft'));
    await waitFor(() => expect(container.textContent).toContain('Saved draft v2'), container);
    const puts = backend.requests.filter((request) => request.method === 'PUT');
    expect(puts.map((request) => request.body?.expected_version)).toEqual([0, 1]);
  });

  it('shows server validation messages verbatim and checks locally first', async () => {
    const backend = profileBackend({ putStatus: () => ({ status: 400, message: 'invalid input: agent profile executor "shell" is not a known pipeline executor' }) });
    const container = await render();
    await click(button(container, 'Edit as new draft'));
    await fill(container, 'Allowed models', 'model-a\nmodel-b');
    await click(button(container, 'Save draft'));
    await waitFor(() => expect(container.textContent).toContain('Default model "gpt-5.4" is not in the allowed models.'), container);
    expect(backend.requests.some((request) => request.method === 'PUT')).toBe(false);
    await fill(container, 'Default model', 'model-b');
    await click(button(container, 'Save draft'));
    await waitFor(() => expect(container.textContent).toContain('invalid input: agent profile executor "shell" is not a known pipeline executor'), container);
  });

  it('guards unsaved edits when navigating away', async () => {
    profileBackend();
    const container = await render();
    await click(button(container, 'Edit as new draft'));
    await fill(container, 'Role', 'editor');
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    expect(confirmNavigation()).toBe(false);
    expect(confirm).toHaveBeenCalled();
  });

  it('activates a draft and rolls back to a previously active version with an audited reason', async () => {
    const backend = profileBackend({ identity: { active_version: 2, latest_version: 3, draft_version: 3 }, versions: [version(1, 'superseded', { activated_at: '2026-09-24T00:10:00.000Z' }), version(2, 'active', { activated_at: '2026-09-24T00:30:00.000Z' }), version(3, 'draft')] });
    const container = await render();
    await fill(container, 'Reason for activation', 'ship tighter contract');
    await click(button(container, 'Activate v3'));
    await waitFor(() => expect(container.textContent).toContain('Activated v3 of content-writer'), container);
    expect(backend.requests.find((request) => request.path.endsWith('/activate'))?.body).toEqual({ version: 3, reason: 'ship tighter contract' });

    await waitFor(() => expect(container.querySelector('button[aria-label="Roll back to version 1"]')).not.toBeNull(), container);
    await click(container.querySelector('button[aria-label="Roll back to version 1"]') as HTMLElement);
    await waitFor(() => expect(container.textContent).toContain('Restored v1 of content-writer as v4'), container);
    expect(backend.requests.find((request) => request.path.endsWith('/rollback'))?.body).toEqual({ version: 1 });
  });

  it('switches to read-only when a mutation is forbidden', async () => {
    profileBackend({ putStatus: () => ({ status: 403, message: 'admin authorization required' }) });
    const container = await render();
    await click(button(container, 'Edit as new draft'));
    await fill(container, 'Role', 'editor');
    await click(button(container, 'Save draft'));
    await waitFor(() => expect(container.textContent).toContain('An admin credential is required to change agent profiles'), container);
    await waitFor(() => expect(container.textContent).toContain('Read-only access'), container);
  });
});
