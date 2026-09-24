// @vitest-environment jsdom

import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { SessionsPage } from './SessionsPage';
import { formatSessionHash, parseSessionHash } from './sessionLinks';
import { expectText, jsonResponse, waitFor } from '../test-utils';
import type { ExecutorSession, ExecutorSessionsResponse } from '../types';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const retention = { window: '720h0m0s', floor: '2026-08-25T12:00:00.000Z', searched_at: '2026-09-24T12:00:00.000Z' };

function session(overrides: Partial<ExecutorSession>): ExecutorSession {
  return {
    session_id: 'ses_x', correlation_key: 'opencode_session_id', origin: 'native', executor_key: 'default', executor_status: 'healthy',
    repository: {}, status: 'ended', status_reason: 'no related step is active', active: false, presence: 'not_checked', presence_reason: 'only active native sessions are checked against the executor',
    first_observed_at: '2026-09-24T09:00:00.000Z', last_observed_at: '2026-09-24T09:05:00.000Z', last_observed_source: 'run_step',
    runs: [], runs_truncated: false, requests: [], requests_truncated: false,
    ...overrides,
  };
}

const alpha = session({
  session_id: 'ses_alpha', executor_key: 'alpha/gen-1', repository: { repository_id: 'alpha', executor_generation: 'gen-1', repo: 'acme/alpha' },
  status: 'active', status_reason: 'a related step is running or queued', active: true, presence: 'present', presence_reason: 'executor holds this session', executor_activity: 'busy', last_observed_source: 'executor',
  runs: [{ run_id: 'run-alpha', workflow_id: 'open-pilot', run_status: 'running', step_id: 'step-alpha', step_name: 'open-pilot.agent-work', step_position: 3, step_status: 'running', active: true, cancelable: true, observed_at: '2026-09-24T11:50:00.000Z' }],
  requests: [
    { kind: 'executor_request', key: 'opencode_request_id', value: 'req-alpha', run_id: 'run-alpha' },
    { kind: 'model_request', key: 'model_broker_request_id', value: 'mb-alpha', run_id: 'run-alpha' },
  ],
});
const gone = session({ session_id: 'ses_gone', executor_key: 'alpha/gen-1', repository: { repository_id: 'alpha', executor_generation: 'gen-1', repo: 'acme/alpha' }, status: 'stale', status_reason: 'a related step is active but the executor no longer has this session', active: true, presence: 'missing', presence_reason: 'executor no longer has this session' });
const beta = session({ session_id: 'ses_beta', executor_key: 'beta/gen-1', executor_status: 'unreachable', repository: { repository_id: 'beta', executor_generation: 'gen-1', repo: 'acme/beta' }, status: 'active', status_reason: 'a related step is running or queued', active: true, presence: 'unknown', presence_reason: 'owning executor is unreachable' });
const done = session({ session_id: 'ses_done' });
const legacy = session({ session_id: 'ocq-legacy', correlation_key: 'ocq_session_id', origin: 'compatibility', status: 'historical_compatibility', status_reason: 'historical compatibility record; no related step is active' });

function view(overrides: Partial<ExecutorSessionsResponse> = {}): ExecutorSessionsResponse {
  return {
    generated_at: '2026-09-24T12:00:00.000Z', retention, limit: 50, scanned_steps: 5, scan_limit: 500, truncated: false,
    executor_availability: 'partial',
    executors: [
      { key: 'default', scope: 'default', backend: 'native', compatibility: false, status: 'healthy', available: true, checked_at: '2026-09-24T12:00:00.000Z' },
      { key: 'alpha/gen-1', scope: 'repository', repository_id: 'alpha', executor_generation: 'gen-1', repository_slug: 'acme/alpha', backend: 'native', compatibility: false, status: 'healthy', available: true, checked_at: '2026-09-24T12:00:00.000Z' },
      { key: 'beta/gen-1', scope: 'repository', repository_id: 'beta', executor_generation: 'gen-1', repository_slug: 'acme/beta', backend: 'native', compatibility: false, status: 'unreachable', available: false, checked_at: '2026-09-24T12:00:00.000Z' },
    ],
    warnings: [],
    counts: { total: 5, active: 2, stale: 1, ended: 1, historical_compatibility: 1 },
    sessions: [alpha, beta, gone, done, legacy],
    abort: { direct_abort: false, route: 'POST /api/runs/{id}/cancel', admin_required: true, note: 'Sessions cannot be aborted directly. Cancel the owning run.' },
    ...overrides,
  };
}

let root: Root | null = null;

async function render() {
  const container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root?.render(<SessionsPage />);
  });
  return container;
}

function stubSessions(...responses: Array<ExecutorSessionsResponse | Response>) {
  const fetch = vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input), 'http://switchyard.test');
    expect(url.pathname).toBe('/api/executor-sessions');
    const next = responses.length > 1 ? responses.shift() : responses[0];
    return next instanceof Response ? next : jsonResponse(next);
  });
  vi.stubGlobal('fetch', fetch);
  return fetch;
}

function card(container: HTMLElement, id: string) {
  const element = container.querySelector(`[data-session-id="${id}"]`) as HTMLElement | null;
  if (!element) throw new Error(`session ${id} not rendered`);
  return element;
}

afterEach(async () => {
  if (root) {
    const current = root;
    await act(async () => { current.unmount(); });
    root = null;
  }
  window.location.hash = '';
  document.body.innerHTML = '';
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe('session deep links', () => {
  it('round-trips a session ID and ignores other routes', () => {
    expect(formatSessionHash('ses 1/x')).toBe('#/sessions/ses%201%2Fx');
    expect(parseSessionHash(formatSessionHash('ses 1/x'))).toBe('ses 1/x');
    expect(formatSessionHash('')).toBe('#/sessions');
    expect(parseSessionHash('#/sessions')).toBe('');
    expect(parseSessionHash('#/runs/run-1')).toBeNull();
    expect(parseSessionHash('#/sessions/%E0%A4%A')).toBe('');
  });
});

describe('SessionsPage', () => {
  it('shows loading, then native sessions with repository, status, presence, and related evidence links', async () => {
    window.location.hash = '#/sessions';
    let release: (() => void) | undefined;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    const fetch = vi.fn(async (input: RequestInfo | URL) => {
      await gate;
      const url = new URL(String(input), 'http://switchyard.test');
      expect(url.pathname).toBe('/api/executor-sessions');
      expect(url.searchParams.has('session_id')).toBe(false);
      return jsonResponse(view());
    });
    vi.stubGlobal('fetch', fetch);
    const container = await render();
    expect(container.textContent).toContain('Loading executor sessions…');

    await act(async () => { release?.(); });
    await waitFor(() => expectText(container, 'ses_alpha'), container);

    expect(fetch).toHaveBeenCalledTimes(1);
    expect(fetch.mock.calls.some(([input]) => String(input).includes('/api/adapters'))).toBe(false);
    const alphaCard = card(container, 'ses_alpha');
    expect(alphaCard.dataset.sessionStatus).toBe('active');
    expect(alphaCard.textContent).toContain('acme/alpha · gen-1');
    expect(alphaCard.textContent).toContain('Executor holds session (busy)');
    expect(alphaCard.textContent).toContain('Cancel from the run cockpit to abort this session.');
    expect(alphaCard.querySelector('a[href="#/sessions/ses_alpha"]')).not.toBeNull();
    expect(alphaCard.querySelector('a[href="#/runs/run-alpha"]')).not.toBeNull();
    expect(alphaCard.querySelector('a[href="#/runs/run-alpha/steps/step-alpha"]')).not.toBeNull();
    expect(alphaCard.querySelector('a[href="#/queues/requests/mb-alpha"]')).not.toBeNull();
    expect(alphaCard.querySelector('a[href="#/logs/search/correlations/req-alpha"]')).not.toBeNull();

    expect(card(container, 'ses_gone').dataset.sessionStatus).toBe('stale');
    expect(card(container, 'ses_gone').textContent).toContain('Executor lost session');
    expect(card(container, 'ses_beta').textContent).toContain('beta/gen-1 · unreachable');
    expect(card(container, 'ses_beta').textContent).toContain('Executor presence unknown');
    expect(card(container, 'ses_done').dataset.sessionStatus).toBe('ended');
    const legacyCard = card(container, 'ocq-legacy');
    expect(legacyCard.dataset.sessionStatus).toBe('historical_compatibility');
    expect(legacyCard.textContent).toContain('Historical compatibility record');

    expect(container.querySelector('[data-testid="executor-availability"]')?.textContent).toContain('Some executors did not answer');
    expect(container.querySelector('[data-executor-key="beta/gen-1"]')?.textContent).toContain('unreachable');
  });

  it('offers no direct abort control and states the run cancellation path', async () => {
    window.location.hash = '#/sessions';
    stubSessions(view());
    const container = await render();
    await waitFor(() => expectText(container, 'ses_alpha'), container);

    const labels = Array.from(container.querySelectorAll('button')).map((button) => button.textContent ?? '');
    expect(labels.some((label) => /abort|cancel|delete|stop/i.test(label))).toBe(false);
    const policy = container.querySelector('[data-testid="abort-policy"]')?.textContent ?? '';
    expect(policy).toContain('No direct session abort is available');
    expect(policy).toContain('POST /api/runs/{id}/cancel (admin only)');
  });

  it('never reports an empty list as healthy when executors are unavailable', async () => {
    window.location.hash = '#/sessions';
    stubSessions(view({
      executor_availability: 'unavailable', sessions: [], counts: { total: 0, active: 0, stale: 0, ended: 0, historical_compatibility: 0 },
      executors: [{ key: 'default', scope: 'default', backend: 'native', compatibility: false, status: 'unreachable', available: false, checked_at: '2026-09-24T12:00:00.000Z' }],
      warnings: ['No sessions are listed and executor availability is unavailable; an empty list does not mean the executor is idle or healthy.'],
    }));
    const container = await render();
    await waitFor(() => expectText(container, 'An empty list does not mean the executor is healthy or idle.'), container);
    expect(container.textContent).toContain('No configured executor answered its status check.');
    expect(container.querySelector('[role="status"]')?.textContent).toBeTruthy();
    expect(container.querySelector('[data-session-id]')).toBeNull();
  });

  it('keeps an empty list neutral about idleness when executors are available', async () => {
    window.location.hash = '#/sessions';
    stubSessions(view({ executor_availability: 'available', sessions: [], executors: [], counts: { total: 0, active: 0, stale: 0, ended: 0, historical_compatibility: 0 } }));
    const container = await render();
    await waitFor(() => expectText(container, 'This does not show whether the executors are idle.'), container);
  });

  it('shows the error state and retries', async () => {
    window.location.hash = '#/sessions';
    const fetch = stubSessions(new Response('{}', { status: 503 }), view());
    const container = await render();
    await waitFor(() => expectText(container, 'Executor sessions unavailable'), container);
    expect(container.textContent).toContain('Switchyard API returned 503');

    const retry = Array.from(container.querySelectorAll('button')).find((button) => button.textContent === 'Retry');
    await act(async () => { retry?.dispatchEvent(new MouseEvent('click', { bubbles: true })); });
    await waitFor(() => expectText(container, 'ses_alpha'), container);
    expect(fetch).toHaveBeenCalledTimes(2);
    expect(container.textContent).not.toContain('Executor sessions unavailable');
  });

  it('follows a session deep link, filters server-side, and reports sessions outside retention', async () => {
    window.location.hash = '#/sessions/ses_beta';
    const fetch = stubSessions(view({ session_id: 'ses_beta', sessions: [beta], counts: { total: 1, active: 1, stale: 0, ended: 0, historical_compatibility: 0 } }), view({ session_id: 'ses_missing', sessions: [], counts: { total: 0, active: 0, stale: 0, ended: 0, historical_compatibility: 0 } }));
    const container = await render();
    await waitFor(() => expectText(container, 'Showing session'), container);
    await waitFor(() => expect(container.querySelector('[data-session-id="ses_beta"]')).not.toBeNull(), container);
    expect(new URL(String(fetch.mock.calls[0][0]), 'http://switchyard.test').searchParams.get('session_id')).toBe('ses_beta');
    expect(container.querySelector('a[href="#/sessions"]')?.textContent).toBe('All sessions');

    await act(async () => {
      window.location.hash = '#/sessions/ses_missing';
      window.dispatchEvent(new HashChangeEvent('hashchange'));
    });
    await waitFor(() => expectText(container, 'has no run-step record in the last 720h0m0s'), container);
    expect(new URL(String(fetch.mock.calls[1][0]), 'http://switchyard.test').searchParams.get('session_id')).toBe('ses_missing');
  });

  it('polls on the live refresh interval', async () => {
    vi.useFakeTimers();
    window.location.hash = '#/sessions';
    const fetch = stubSessions(view());
    await render();
    await act(async () => { await vi.advanceTimersByTimeAsync(0); });
    expect(fetch).toHaveBeenCalledTimes(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(15_000); });
    expect(fetch).toHaveBeenCalledTimes(2);
  });
});
