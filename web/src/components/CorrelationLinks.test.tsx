// @vitest-environment jsdom

import { act } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { expectText, jsonResponse, mockDashboardFetch, renderApp, unmount, waitFor } from '../test-utils';
import type { CorrelationTargets, OpenPilotStateResponse, RunDetail } from '../types';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

afterEach(() => {
  window.location.hash = '';
  document.body.innerHTML = '';
  vi.unstubAllGlobals();
});

const traceID = '4bf92f3577b34da6a3ce929d0e0e4736';
const ciJobURL = 'https://git.example.test/OWNER/REPO/actions/runs/42/jobs/0';
const foreignCIURL = 'https://evil.example.test/OWNER/REPO/actions/runs/1';
const prURL = 'https://git.example.test/OWNER/REPO/pulls/7';
const approvedTargets: CorrelationTargets = { gitea_base_url: 'https://git.example.test', trace_ui_base_url: 'https://trace.example.test/traces/{trace_id}' };

// A representative Open Pilot run whose implementation and local verification
// succeeded but whose required CI check failed on the pull request.
const failedCIRun: RunDetail = {
  run: { id: 'run-ci', workflow_id: 'open-pilot', trigger_id: 'trigger-1', status: 'failed', inputs: { repo: 'OWNER/REPO', issue: '42' }, created_at: '2026-09-24T00:00:00.000Z', started_at: '2026-09-24T00:00:01.000Z', finished_at: '2026-09-24T00:09:00.000Z', summary: 'Open Pilot OWNER/REPO#42' },
  steps: [
    { id: 's-impl', run_id: 'run-ci', position: 1, name: 'open-pilot.implement', executor: 'opencode', status: 'succeeded', attempt_count: 1, output_summary: 'Implemented change', correlation_ids: { trace_id: traceID, span_id: '00f067aa0ba902b7', opencode_session_id: 'ses_abc', model_broker_request_id: 'req-ok' }, created_at: '2026-09-24T00:00:01.000Z', started_at: '2026-09-24T00:00:02.000Z', finished_at: '2026-09-24T00:04:00.000Z' },
    { id: 's-verify', run_id: 'run-ci', position: 2, name: 'open-pilot.verify', executor: 'command', status: 'succeeded', attempt_count: 1, output_summary: 'make verify passed', created_at: '2026-09-24T00:04:00.000Z', started_at: '2026-09-24T00:04:01.000Z', finished_at: '2026-09-24T00:06:00.000Z' },
    {
      id: 's-ci', run_id: 'run-ci', position: 3, name: 'open-pilot.ci-checks', executor: 'gitea', status: 'failed', attempt_count: 1,
      error: `CI checks failed: verify\nRequired checks failed at head abc1234:\n- verify: ${ciJobURL}\n- mirror: ${foreignCIURL}`,
      correlation_ids: { pull_request_url: prURL, rogue_link: 'javascript:alert(1)' },
      created_at: '2026-09-24T00:06:00.000Z', started_at: '2026-09-24T00:06:01.000Z', finished_at: '2026-09-24T00:09:00.000Z',
    },
  ],
  events: [
    { id: 'ev-start', run_id: 'run-ci', type: 'run.started', message: 'Run started', created_at: '2026-09-24T00:00:01.000Z' },
    { id: 'ev-fail', run_id: 'run-ci', type: 'step.failed', message: 'open-pilot.ci-checks failed: CI checks failed: verify', created_at: '2026-09-24T00:09:00.000Z' },
  ],
  artifacts: [{ id: 'art-ci', run_id: 'run-ci', name: 'ci-feedback.txt', kind: 'evidence', uri: 'data:text/plain,verify%20failed', content_type: 'text/plain', created_at: '2026-09-24T00:09:00.000Z' }],
};

const failedCIState: OpenPilotStateResponse = {
  state: {
    id: 'op-42', repo: 'OWNER/REPO', issue: '42', phase: 'failed', run_id: 'run-old', branch: 'open-pilot/42', base: 'main', pull_request_url: prURL, pull_request_number: 7, issue_closed: false,
    gate_status: { ai_review: 'approved', ci_checks: 'failed' }, repair_attempts: 2, max_repair_attempts: 2, last_error: 'CI checks failed: verify', created_at: '2026-09-24T00:00:00.000Z', updated_at: '2026-09-24T00:09:00.000Z',
  },
  next_action: 'operator review',
};

function mockFailedCIRun(safety: () => Response = () => jsonResponse({ reverse_proxy_auth: true, open_pilot_autonomy_mode: 'manual', correlation_targets: approvedTargets, warnings: [] })) {
  return mockDashboardFetch((url, init) => {
    if ((init.method ?? 'GET') !== 'GET') return undefined;
    if (url.pathname === '/api/operator-safety') return safety();
    if (url.pathname === '/api/runs') return jsonResponse({ runs: [failedCIRun.run] });
    if (url.pathname === '/api/runs/run-ci') return jsonResponse(failedCIRun);
    if (url.pathname === '/api/runs/run-old') return jsonResponse({ error: { message: 'run not found' } }, { status: 404 });
    if (url.pathname === '/api/history/req-gone') return jsonResponse({ error: { message: 'not found' } }, { status: 404 });
    if (url.pathname === '/api/open-pilot/state' && url.searchParams.get('issue') === '42') return jsonResponse(failedCIState);
    if (url.pathname === '/api/evidence/search') return jsonResponse({ hits: [], next_cursor: '', query: url.searchParams.get('q') ?? '', scope: 'correlations', limit: 25, retention: { window: '720h', floor: '2026-08-25T00:00:00Z', searched_at: '2026-09-24T00:10:00Z' } });
    return undefined;
  });
}

function anchor(container: HTMLElement, href: string) {
  const element = Array.from(container.querySelectorAll('a')).find((item) => item.getAttribute('href') === href);
  if (!element) throw new Error(`link ${href} not found`);
  return element;
}

async function click(element: HTMLElement) {
  await act(async () => {
    element.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
  });
}

function hashBecomes(expectedHash: string) {
  return new Promise<void>((resolve) => {
    const started = Date.now();
    const poll = () => (window.location.hash === expectedHash || Date.now() - started > 1_000 ? resolve() : setTimeout(poll, 5));
    poll();
  });
}

// Follows a link the way a browser does; jsdom performs fragment navigation asynchronously.
async function followLink(element: HTMLAnchorElement) {
  const expectedHash = element.getAttribute('href') ?? '';
  await act(async () => {
    element.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
    await hashBecomes(expectedHash);
  });
  expect(window.location.hash).toBe(expectedHash);
}

async function navigateHistory(direction: 'back' | 'forward', expectedHash: string) {
  await act(async () => {
    if (direction === 'back') window.history.back();
    else window.history.forward();
    await hashBecomes(expectedHash);
  });
  expect(window.location.hash).toBe(expectedHash);
}

// Every link rendered in the page body (the shell's skip link is outside <main>).
function hrefs(container: HTMLElement) {
  return Array.from(container.querySelectorAll('main a')).map((item) => item.getAttribute('href') ?? '');
}

describe('run correlation links', () => {
  it('deep links into failed CI evidence and survives browser back and forward', async () => {
    window.location.hash = '#/runs/run-ci/steps/s-ci';
    mockFailedCIRun();
    const { container, root } = await renderApp('Failure summary');
    await waitFor(() => anchor(container, ciJobURL), container);

    // Failure summary explains the failure and links to its evidence.
    expect(container.textContent).toContain('open-pilot.ci-checks failed');
    expect(container.textContent).toContain('CI checks failed: verify');
    expect(container.textContent).toContain('ci_checks failed');
    expect(anchor(container, '#/runs/run-ci/steps/s-ci').dataset.evidenceLink).toBe('steps');
    expect(anchor(container, '#/runs/run-ci/events/ev-fail').dataset.evidenceLink).toBe('events');
    expect(anchor(container, '#/runs/run-ci/artifacts/art-ci').dataset.evidenceLink).toBe('artifacts');

    // The step deep link focuses the failed step.
    const failedStep = container.querySelector('[id="evidence-steps-s-ci"]') as HTMLElement;
    expect(failedStep.dataset.evidenceFocused).toBe('true');
    expect(document.activeElement).toBe(failedStep);

    // Typed destinations: approved Gitea/trace links, in-app request/session/run links.
    expect(anchor(container, ciJobURL).getAttribute('rel')).toBe('noopener noreferrer');
    expect(anchor(container, ciJobURL).getAttribute('target')).toBe('_blank');
    expect(anchor(container, prURL).dataset.correlationKind).toBe('gitea-pull-request');
    expect(anchor(container, 'https://git.example.test/OWNER/REPO/issues/42').dataset.correlationKind).toBe('gitea-issue');
    expect(anchor(container, 'https://git.example.test/OWNER/REPO/actions').dataset.correlationKind).toBe('gitea-ci');
    expect(anchor(container, `https://trace.example.test/traces/${traceID}`).dataset.correlationKind).toBe('trace');
    expect(anchor(container, '#/queues/requests/req-ok').dataset.correlationKind).toBe('request');
    expect(anchor(container, '#/sessions/ses_abc').dataset.correlationKind).toBe('session');
    expect(anchor(container, '#/runs/run-old').dataset.correlationKind).toBe('run');

    // Unapproved hosts and schemes are never linked but remain copyable text.
    expect(hrefs(container).every((href) => href.startsWith('#/') || href.startsWith('https://git.example.test/') || href.startsWith('https://trace.example.test/traces/'))).toBe(true);
    expect(container.textContent).toContain(foreignCIURL);
    expect(container.textContent).toContain('javascript:alert(1)');
    expect(container.textContent).toContain('outside the approved destinations');

    // Follow the model broker request, then use browser history.
    await followLink(anchor(container, '#/queues/requests/req-ok'));
    expect(window.location.hash).toBe('#/queues/requests/req-ok');
    await waitFor(() => expectText(container, 'Requests cockpit'), container);

    await navigateHistory('back', '#/runs/run-ci/steps/s-ci');
    await waitFor(() => expectText(container, 'Failure summary'), container);
    expect(container.textContent).toContain('open-pilot.ci-checks failed');

    await navigateHistory('forward', '#/queues/requests/req-ok');
    await waitFor(() => expectText(container, 'Requests cockpit'), container);

    await navigateHistory('back', '#/runs/run-ci/steps/s-ci');
    await waitFor(() => expectText(container, 'Failure summary'), container);

    // Evidence links in the summary move focus within the same run.
    await followLink(anchor(container, '#/runs/run-ci/events/ev-fail'));
    expect(window.location.hash).toBe('#/runs/run-ci/events/ev-fail');
    await waitFor(() => {
      if ((container.querySelector('[id="evidence-events-ev-fail"]') as HTMLElement | null)?.dataset.evidenceFocused !== 'true') throw new Error('event not focused');
    }, container);
    expect(document.activeElement?.id).toBe('evidence-events-ev-fail');

    await unmount(root);
  });

  it('handles retained-out runs, unknown evidence, and unknown requests without broken navigation', async () => {
    window.location.hash = '#/runs/run-ci';
    mockFailedCIRun();
    const { container, root } = await renderApp('Failure summary');
    await waitFor(() => anchor(container, '#/runs/run-old'), container);

    await followLink(anchor(container, '#/runs/run-old'));
    await waitFor(() => expectText(container, 'was not found'), container);
    expect(container.textContent).toContain('run-old');
    expect(container.textContent).toContain('removed by retention');
    expect(container.querySelector('button[aria-label="Copy run ID run-old"]')).not.toBeNull();

    await navigateHistory('back', '#/runs/run-ci');
    await waitFor(() => expectText(container, 'Failure summary'), container);

    await act(async () => {
      window.location.hash = '#/runs/run-ci/steps/step-pruned';
    });
    await waitFor(() => expectText(container, 'is not shown in this run'), container);
    expect(container.textContent).toContain('step-pruned');
    expect(container.textContent).toContain('Failure summary');

    await act(async () => {
      window.location.hash = '#/queues/requests/req-gone';
    });
    await waitFor(() => expectText(container, 'Request req-gone is not in model broker history'), container);

    await navigateHistory('back', '#/runs/run-ci/steps/step-pruned');
    await waitFor(() => expectText(container, 'is not shown in this run'), container);

    await unmount(root);
  });

  it('renders copyable fallbacks when no destination is configured', async () => {
    window.location.hash = '#/runs/run-ci';
    mockFailedCIRun(() => jsonResponse({ reverse_proxy_auth: true, open_pilot_autonomy_mode: 'manual', correlation_targets: {}, warnings: [] }));
    const writeText = vi.fn(async () => undefined);
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } });
    const { container, root } = await renderApp('Failure summary');
    await waitFor(() => expectText(container, 'No Gitea or trace UI base is configured'), container);

    expect(hrefs(container).every((href) => href.startsWith('#/'))).toBe(true);
    expect(container.textContent).toContain(prURL);
    expect(container.textContent).toContain(ciJobURL);
    expect(container.textContent).toContain(traceID);
    expect(container.textContent).toContain('No destination is configured for this ID');
    expect(anchor(container, '#/queues/requests/req-ok')).toBeDefined();

    const copyTrace = container.querySelector(`button[aria-label="Copy trace_id ${traceID}"]`) as HTMLButtonElement;
    await click(copyTrace);
    expect(writeText).toHaveBeenCalledWith(traceID);
    await waitFor(() => expectText(container, 'Copied'), container);

    await unmount(root);
  });

  it('keeps IDs copyable when link destinations cannot be loaded or the clipboard is unavailable', async () => {
    window.location.hash = '#/runs/run-ci';
    mockFailedCIRun(() => jsonResponse({ error: { message: 'unavailable' } }, { status: 503 }));
    vi.stubGlobal('navigator', { ...navigator, clipboard: undefined });
    const { container, root } = await renderApp('Failure summary');
    await waitFor(() => expectText(container, 'External link destinations could not be loaded'), container);

    expect(hrefs(container).every((href) => href.startsWith('#/'))).toBe(true);
    await click(container.querySelector(`button[aria-label="Copy trace_id ${traceID}"]`) as HTMLButtonElement);
    await waitFor(() => expectText(container, 'Clipboard unavailable'), container);

    await unmount(root);
  });
});
