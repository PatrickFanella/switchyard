// @vitest-environment jsdom
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, expect, it, vi } from 'vitest';
import { OpenPilotOverseerPage, type OverseerState } from './OpenPilotOverseerPage';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | null = null;
const fixture: OverseerState = { revision: 7, config: { enabled: false, queue_enabled: true, recovery_enabled: true, max_open_lanes: 3, interval_seconds: 900 }, history: [], report: { complete: false, status: 'disabled', available_lanes: 0, lanes: [], candidates: [{ repo: 'owner/repo', issue: 1, title: 'Implement feature', blockers: ['prerequisite open'] }], incidents: [] } };
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status });
async function render() {
  const container = document.createElement('div'); document.body.appendChild(container); root = createRoot(container);
  await act(async () => { root?.render(<OpenPilotOverseerPage />); });
  return container;
}
async function click(container: HTMLElement, label: string) {
  const button = [...container.querySelectorAll('button')].find((entry) => entry.textContent === label)!;
  await act(async () => { button.click(); });
}
afterEach(async () => { await act(async () => root?.unmount()); root = null; document.body.innerHTML = ''; vi.unstubAllGlobals(); });

it('shows incomplete coverage and ordered holds while enforcing read-only access', async () => {
  vi.stubGlobal('fetch', vi.fn(async (url) => json(String(url).includes('control-access') ? { can_write: false, role: 'read' } : fixture)));
  const container = await render();
  expect(container.textContent).toContain('Read-only access');
  expect(container.textContent).toContain('Coverage is incomplete');
  expect(container.textContent).toContain('prerequisite open');
  expect((container.querySelector('input[aria-label="Maximum open lanes"]') as HTMLInputElement).value).toBe('3');
  expect([...container.querySelectorAll('button')].filter((button) => button.disabled).map((button) => button.textContent)).toEqual(['Save policy', 'Preview check']);
});

it('saves with the observed revision and previews through a separate endpoint', async () => {
  const calls: { url: string; init: RequestInit }[] = [];
  vi.stubGlobal('fetch', vi.fn(async (url, init: RequestInit = {}) => {
    calls.push({ url: String(url), init });
    return json(String(url).includes('control-access') ? { can_write: true, role: 'admin' } : fixture);
  }));
  const container = await render();
  await click(container, 'Save policy'); await click(container, 'Preview check');
  const put = calls.find((call) => call.init.method === 'PUT')!;
  expect(JSON.parse(put.init.body as string)).toEqual({ expected_revision: 7, config: fixture.config });
  const preview = calls.find((call) => call.init.method === 'POST')!;
  expect(preview.url).toBe('/api/open-pilot/overseer/check'); expect(preview.init.body).toBeUndefined();
  expect(container.querySelector('[role="status"]')?.textContent).toContain('Preview did not admit or recover work');
});

it('keeps a failed revision reviewable without claiming success', async () => {
  vi.stubGlobal('fetch', vi.fn(async (url, init: RequestInit = {}) => init.method === 'PUT' ? json({ error: { message: 'conflict' } }, 409) : json(String(url).includes('control-access') ? { can_write: true, role: 'admin' } : fixture)));
  const container = await render(); await click(container, 'Save policy');
  expect(container.querySelector('[role="alert"]')?.textContent).toContain('Reload before saving again');
  expect(container.querySelector('[role="status"]')).toBeNull();
});

it('requires a review reason before releasing an uncertain reservation', async () => {
  vi.stubGlobal('fetch', vi.fn(async (url) => json(String(url).includes('control-access') ? { can_write: true, role: 'admin' } : { ...fixture, pending: { id: 'intent-1', kind: 'queue_label', repo: 'owner/repo', issue: 1, outcome: 'reserved' } })));
  const container = await render();
  expect(container.textContent).toContain('Its reservation remains held');
  expect([...container.querySelectorAll('button')].find((button) => button.textContent === 'Release reviewed reservation')?.disabled).toBe(true);
});

it('reads back a persisted partial preview and explains the guard that blocked it', async () => {
  let failed = false;
  vi.stubGlobal('fetch', vi.fn(async (url, init: RequestInit = {}) => {
    if (init.method === 'POST') { failed = true; return json({ error: { message: 'guard stopped inspection' } }, 500); }
    return json(String(url).includes('control-access') ? { can_write: true, role: 'admin' } : { ...fixture, report: { ...fixture.report, error: failed ? 'runtime is stopped or intentionally paused' : undefined } });
  }));
  const container = await render(); await click(container, 'Preview check');
  expect(container.querySelector('[role="alert"]')?.textContent).toContain('runtime is stopped or intentionally paused');
  expect(container.querySelector('[role="status"]')).toBeNull();
});

it('links the resulting coding run from the hand-back receipt', async () => {
  vi.stubGlobal('fetch', vi.fn(async (url) => json(String(url).includes('control-access') ? { can_write: false, role: 'read' } : { ...fixture, history: [{ id: 'repair-1', kind: 'coding_repair', repo: 'owner/repo', issue: 1, outcome: 'returned to coding with feedback', created_at: '2026-10-13T00:00:00Z', resulting_run_id: 'run-correction' }] })));
  const container = await render();
  expect(container.querySelector('a[href="#/runs/run-correction"]')?.textContent).toBe('Coding run');
});
