// @vitest-environment jsdom

import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { emptyAdapters, type RuntimeConfigResponse, type RuntimeConfigSetting } from '../types';
import { ConfigPage, effectiveValueLabel, hasDrift } from './ConfigPage';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const fileManaged: RuntimeConfigSetting = {
  key: 'SWITCHYARD_WORKER_COUNT', component: 'workers', kind: 'int', sensitivity: 'public', mutability: 'file-managed', restart: 'api', authorization: 'host-operator',
  description: 'Worker goroutines claiming runs.', source: 'environment', effective: '4', configured: true, default: '1', compose_default: '1', example_default: '1', overridden: true,
  instructions: 'Edit SWITCHYARD_WORKER_COUNT in the deployed environment file (the SWITCHYARD_ENV_FILE passed to deploy/install-systemd.sh; .env for local Compose), then restart the API: sudo systemctl reload switchyard, or docker compose up -d api locally.',
};
const matching: RuntimeConfigSetting = { ...fileManaged, key: 'SWITCHYARD_RETENTION', component: 'retention', source: 'default', effective: '720h', default: '720h', overridden: false, description: 'Run record retention window.', instructions: 'Edit SWITCHYARD_RETENTION …' };
const secret: RuntimeConfigSetting = { key: 'GITEA_TOKEN', component: 'gitea', kind: 'secret', sensitivity: 'secret', mutability: 'file-managed', restart: 'api', authorization: 'host-operator', description: 'Gitea API token.', source: 'environment', configured: true, instructions: 'Edit GITEA_TOKEN …' };
const pause: RuntimeConfigSetting = { key: 'scheduler.dispatch_paused', component: 'scheduler', kind: 'bool', sensitivity: 'public', mutability: 'runtime-mutable', restart: 'none', authorization: 'admin', description: 'Runtime pause for scheduled dispatch.', source: 'runtime', effective: 'false', default: 'false', overridden: false, available: true, instructions: 'PUT …' };
const unavailable: RuntimeConfigSetting = { ...pause, key: 'workers.claim_paused', component: 'workers', source: 'unavailable', effective: undefined, available: false };

function configResponse(revision = 0, settings: RuntimeConfigSetting[] = [fileManaged, matching, secret, pause, unavailable]): RuntimeConfigResponse {
  return { contract_version: 1, revision, generated_at: '2026-09-24T00:00:00.000Z', settings, changes: [] };
}

function json(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), { status });
}

let root: Root | null = null;

async function render() {
  const container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root?.render(<ConfigPage adapters={emptyAdapters} />);
  });
  await act(async () => {
    await Promise.resolve();
  });
  return container;
}

async function click(element: Element) {
  await act(async () => {
    element.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
  await act(async () => {
    await Promise.resolve();
  });
}

afterEach(async () => {
  await act(async () => root?.unmount());
  root = null;
  document.body.innerHTML = '';
  vi.unstubAllGlobals();
});

describe('ConfigPage', () => {
  it('shows effective values, drift, sources, and file-managed instructions without secret values', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => json(configResponse())));
    const container = await render();
    const text = container.textContent ?? '';
    expect(text).toContain('SWITCHYARD_WORKER_COUNT');
    expect(text).toContain('drift');
    expect(text).toContain('configured (value hidden)');
    expect(container.querySelector('[data-testid="instructions-SWITCHYARD_WORKER_COUNT"]')?.textContent).toContain('systemctl reload switchyard');
    expect(text).toContain('5 settings · 1 differ from the defaults contract · 1 secrets (values hidden)');
    expect(text).not.toContain('Features');
    expect(text).not.toContain('Limits');
  });

  it('filters to drifted settings', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => json(configResponse())));
    const container = await render();
    expect(container.textContent).toContain('SWITCHYARD_RETENTION');
    await click(container.querySelector('input[type="checkbox"]')!);
    expect(container.textContent).not.toContain('SWITCHYARD_RETENTION');
    expect(container.textContent).toContain('SWITCHYARD_WORKER_COUNT');
  });

  it('renders a real control only for available runtime-mutable settings', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => json(configResponse())));
    const container = await render();
    const active = container.querySelector('button[aria-label="Pause scheduler.dispatch_paused"]') as HTMLButtonElement;
    const disabled = container.querySelector('button[aria-label="Pause workers.claim_paused"]') as HTMLButtonElement;
    expect(active.disabled).toBe(false);
    expect(disabled.disabled).toBe(true);
    expect(container.textContent).toContain('not running in this API process');
    expect(container.querySelectorAll('button').length).toBe(2);
  });

  it('sends the observed revision and reloads after an accepted change', async () => {
    const calls: Array<{ method: string; body?: string }> = [];
    let revision = 0;
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const method = init.method ?? 'GET';
      calls.push({ method, body: init.body as string | undefined });
      if (method === 'PUT') {
        revision = 1;
        return json({ setting: { ...pause, effective: 'true' }, revision: 1, change: { revision: 1, key: pause.key, previous: 'false', value: 'true', actor: 'token:admin', at: '2026-09-24T00:00:01.000Z' } });
      }
      expect(String(input)).toContain('/api/runtime-config');
      return json({ ...configResponse(revision, [{ ...pause, effective: revision ? 'true' : 'false' }]), changes: revision ? [{ revision: 1, key: pause.key, previous: 'false', value: 'true', actor: 'token:admin', at: '2026-09-24T00:00:01.000Z' }] : [] });
    }));
    const container = await render();
    await click(container.querySelector('button[aria-label="Pause scheduler.dispatch_paused"]')!);
    const put = calls.find((call) => call.method === 'PUT');
    expect(JSON.parse(put?.body ?? '{}')).toEqual({ value: true, expected_revision: 0, reason: '' });
    expect(container.querySelector('button[aria-label="Resume scheduler.dispatch_paused"]')).not.toBeNull();
    expect(container.textContent).toContain('r1 · scheduler.dispatch_paused false → true by token:admin');
  });

  it('explains a stale revision conflict and reloads', async () => {
    let gets = 0;
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init: RequestInit = {}) => {
      if (init.method === 'PUT') return json({ error: { code: 'stale-revision', message: 'configuration revision changed; reload and retry' }, revision: 3 }, 409);
      gets++;
      return json(configResponse(gets > 1 ? 3 : 0));
    }));
    const container = await render();
    await click(container.querySelector('button[aria-label="Pause scheduler.dispatch_paused"]')!);
    expect(container.querySelector('[role="alert"]')?.textContent).toContain('Another operator changed configuration first');
    expect(gets).toBe(2);
    expect(container.textContent).toContain('3 ·');
  });

  it('explains a denied write for read-only credentials', async () => {
    vi.stubGlobal('fetch', vi.fn(async (_input: RequestInfo | URL, init: RequestInit = {}) => (init.method === 'PUT' ? json({ error: { message: 'admin authorization required' } }, 403) : json(configResponse()))));
    const container = await render();
    await click(container.querySelector('button[aria-label="Pause scheduler.dispatch_paused"]')!);
    expect(container.querySelector('[role="alert"]')?.textContent).toContain('Admin authorization is required');
  });

  it('shows an authentication error when configuration cannot be read', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => json({ error: { message: 'authentication required' } }, 401)));
    const container = await render();
    expect(container.querySelector('[role="alert"]')?.textContent).toContain('rejected this request');
    expect(container.querySelectorAll('button').length).toBe(0);
  });

  it('labels values without ever rendering a secret value', () => {
    expect(effectiveValueLabel({ ...secret, effective: 'leaked' })).toBe('configured (value hidden)');
    expect(effectiveValueLabel({ ...secret, configured: false })).toBe('not configured');
    expect(effectiveValueLabel({ ...fileManaged, source: 'not-observable', effective: undefined })).toBe('not observable by the API process');
    expect(effectiveValueLabel({ ...fileManaged, effective: '' })).toBe('(empty)');
    expect(hasDrift(matching)).toBe(false);
    expect(hasDrift({ ...matching, deployment_override: 'Compose enables it.' })).toBe(true);
  });
});
