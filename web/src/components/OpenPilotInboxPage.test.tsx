// @vitest-environment jsdom

import { act } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { formatInboxHash, parseInboxHash, safeExternalURL, type OpenPilotInboxIssue, type OpenPilotInboxReport } from '../openPilotInbox';
import { button, controlByLabel, expectText, jsonResponse, mockDashboardFetch, renderApp, unmount, waitFor } from '../test-utils';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

afterEach(() => {
  window.location.hash = '';
  document.body.innerHTML = '';
  vi.unstubAllGlobals();
});

function issue(overrides: Partial<OpenPilotInboxIssue> = {}): OpenPilotInboxIssue {
  return {
    key: 'owner/alpha#1', repo: 'owner/alpha', repository_id: 'alpha', executor_generation: 'generation-alpha', issue: '1', issue_id: 101,
    title: 'Add inbox', state: 'open', labels: ['ready-for-agent', 'agent:queued'], assignees: [], source: 'upstream', pickup: 'labels',
    bot_assigned: false, labels_satisfied: true, missing_labels: [], spec_required: true, spec_ready: true, test_command: 'go test ./...',
    eligible: true, action: 'dispatch', reason_codes: [],
    checks: [{ name: 'pickup', code: 'pickup_labels', status: 'pass', message: 'carries every queue label' }, { name: 'dispatch', code: 'dispatch_eligible', status: 'pass', message: 'dispatch creates a new lifecycle' }],
    links: { issue: 'https://gitea.test/owner/alpha/issues/1', readiness: '/api/open-pilot/readiness?repo=owner/alpha&issue=1' },
    ...overrides,
  };
}

function report(overrides: Partial<OpenPilotInboxReport> = {}): OpenPilotInboxReport {
  return {
    checked_at: '2026-06-17T00:02:00.000Z', activation_mode: 'durable', upstream: 'available',
    policy: { required_labels: ['ready-for-agent', 'agent:queued'], assignee: 'open-pilot', require_issue_spec: true, poll_repositories: ['owner/alpha'] },
    repositories: [{ id: 'alpha', slug: 'owner/alpha', executor_generation: 'generation-alpha', admitted: true, admission_code: 'repository_admitted', polled: true, upstream: 'listed', listed: 1, claims: 0 }],
    issues: [], matched: 0, has_more: false, checks: [], blockers: [],
    ...overrides,
  };
}

const labelOnly = issue({
  key: 'owner/alpha#3', issue: '3', title: 'Needs queue label', labels: ['ready-for-agent'], pickup: 'none', labels_satisfied: false, missing_labels: ['agent:queued'],
  eligible: false, action: 'none', reason_codes: ['labels_missing'],
  checks: [{ name: 'pickup', code: 'labels_missing', status: 'fail', message: 'missing queue label(s): agent:queued', remediation: 'Add the missing label(s) after reviewing the issue body.' }],
});

function inboxRequests(fetch: ReturnType<typeof mockDashboardFetch>) {
  return fetch.mock.calls.map(([url]) => new URL(String(url), 'http://switchyard.test')).filter((url) => url.pathname === '/api/open-pilot/inbox');
}

describe('inbox deep links', () => {
  it('round-trips filters, cursor, and the selected issue', () => {
    const link = { repo: 'owner/alpha', eligibility: 'eligible' as const, pickup: 'assigned' as const, phase: 'failed', q: 'crash', cursor: 'abc_123-x', issue: 'owner/alpha#42' };
    const hash = formatInboxHash(link);
    expect(hash.startsWith('#/inbox/')).toBe(true);
    expect(hash.slice('#/inbox/'.length)).not.toContain('/');
    expect(parseInboxHash(hash)).toEqual(link);
    expect(formatInboxHash(parseInboxHash('#/inbox'))).toBe('#/inbox');
  });

  it('falls back to defaults for malformed values', () => {
    expect(parseInboxHash('#/inbox/eligibility=bogus&pickup=x&phase=DROP%20TABLE&cursor=%3Cscript%3E')).toEqual({ repo: '', eligibility: 'all', pickup: '', phase: '', q: '', cursor: '', issue: '' });
    expect(parseInboxHash('#/runs/run-1').eligibility).toBe('all');
  });

  it('renders only http(s) upstream links', () => {
    expect(safeExternalURL('https://gitea.test/owner/alpha/issues/1')).toBe('https://gitea.test/owner/alpha/issues/1');
    expect(safeExternalURL('javascript:alert(1)')).toBe('');
    expect(safeExternalURL('not a url')).toBe('');
  });
});

describe('OpenPilotInboxPage', () => {
  it('shows an empty queue only when upstream data is available', async () => {
    window.location.hash = '#/inbox';
    mockDashboardFetch((url) => url.pathname === '/api/open-pilot/inbox' ? jsonResponse(report()) : undefined);
    const { container, root } = await renderApp('The queue is empty');
    expect(container.querySelector('[data-upstream-status]')).toBeNull();
    expect(container.textContent).toContain('owner/alpha: listed · polled');
    await unmount(root);
  });

  it('distinguishes unavailable upstream data from an empty queue', async () => {
    window.location.hash = '#/inbox';
    mockDashboardFetch((url) => url.pathname === '/api/open-pilot/inbox'
      ? jsonResponse(report({ upstream: 'unavailable', upstream_error: 'owner/alpha: gitea issue list failed: 502 Bad Gateway', repositories: [{ ...report().repositories[0], upstream: 'unavailable', upstream_error: '502' }], checks: [{ name: 'upstream', code: 'upstream_unavailable', status: 'warn', message: 'listing failed' }] }))
      : undefined);
    const { container, root } = await renderApp('This is not an empty queue');
    expect(container.querySelector('[data-upstream-status]')?.getAttribute('data-upstream-status')).toBe('unavailable');
    expect(container.textContent).toContain('502 Bad Gateway');
    expect(container.textContent).not.toContain('The queue is empty');
    await unmount(root);
  });

  it('reports configuration blockers and API failures', async () => {
    window.location.hash = '#/inbox';
    mockDashboardFetch((url) => url.pathname === '/api/open-pilot/inbox'
      ? jsonResponse(report({ upstream: 'not_queried', repositories: [], blockers: ['gate_unavailable'], checks: [{ name: 'admission_gate', code: 'gate_unavailable', status: 'fail', message: 'invalid Open Pilot activation mode', remediation: 'Fix the runtime configuration' }] }))
      : undefined);
    let rendered = await renderApp('Inbox blocked: gate_unavailable');
    expect(rendered.container.textContent).toContain('invalid Open Pilot activation mode');
    expect(rendered.container.textContent).toContain('No issues can be listed until the blockers above are resolved.');
    await unmount(rendered.root);
    document.body.innerHTML = '';

    mockDashboardFetch((url) => url.pathname === '/api/open-pilot/inbox' ? jsonResponse({}, { status: 503 }) : undefined);
    rendered = await renderApp('Inbox unavailable');
    expect(rendered.container.textContent).toContain('Switchyard API returned 503');
    await unmount(rendered.root);
  });

  it('applies filters from and to the URL and paginates with the server cursor', async () => {
    window.location.hash = '#/inbox/eligibility=ineligible&issue=owner%2Falpha%233';
    const fetch = mockDashboardFetch((url) => {
      if (url.pathname !== '/api/open-pilot/inbox') return undefined;
      if (url.searchParams.get('cursor') === 'page-2') return jsonResponse(report({ issues: [issue({ key: 'owner/alpha#9', issue: '9', title: 'Second page issue' })], matched: 2 }));
      return jsonResponse(report({ issues: [labelOnly], matched: 2, has_more: true, next_cursor: 'page-2' }));
    });
    const { container, root } = await renderApp('Needs queue label');

    const first = inboxRequests(fetch)[0];
    expect(first.searchParams.get('eligibility')).toBe('ineligible');
    expect(first.searchParams.get('limit')).toBe('25');
    // The deep-linked issue is selected and explains its reason code and remediation.
    await waitFor(() => expectText(container, 'Selected issue'), container);
    expect(container.textContent).toContain('missing queue label(s): agent:queued');
    expect(container.textContent).toContain('Add the missing label(s) after reviewing the issue body.');
    expect(Array.from(container.querySelectorAll('button')).some((item) => item.textContent === 'Dispatch')).toBe(false);

    await act(async () => {
      const pickup = controlByLabel(container, 'Pickup') as HTMLSelectElement;
      pickup.value = 'none';
      pickup.dispatchEvent(new Event('change', { bubbles: true }));
    });
    await waitFor(() => expect(inboxRequests(fetch).some((url) => url.searchParams.get('pickup') === 'none')).toBe(true), container);
    expect(window.location.hash).toBe('#/inbox/eligibility=ineligible&pickup=none');

    await act(async () => { button(container, 'Next page').click(); });
    await waitFor(() => expectText(container, 'Second page issue'), container);
    expect(inboxRequests(fetch).at(-1)?.searchParams.get('cursor')).toBe('page-2');
    expect(window.location.hash).toBe('#/inbox/eligibility=ineligible&pickup=none&cursor=page-2');
    await act(async () => { button(container, 'First page').click(); });
    await waitFor(() => expect(inboxRequests(fetch).at(-1)?.searchParams.has('cursor')).toBe(false), container);
    await unmount(root);
  });

  it('surfaces a rejected dispatch without navigating away', async () => {
    window.location.hash = '#/inbox/issue=owner%2Falpha%231';
    mockDashboardFetch((url, init) => {
      if (url.pathname === '/api/open-pilot/inbox') return jsonResponse(report({ issues: [issue()], matched: 1 }));
      if (url.pathname === '/api/workflows/open-pilot/run' && init.method === 'POST') return jsonResponse({ error: { message: 'admin authorization required' } }, { status: 403 });
      return undefined;
    });
    const { container, root } = await renderApp('Selected issue');
    await act(async () => { button(container, 'Dispatch').click(); });
    await waitFor(() => expectText(container, 'Action rejected'), container);
    expect(container.textContent).toContain('Dispatch of owner/alpha#1 was rejected: Switchyard API returned 403');
    expect(window.location.hash).toBe('#/inbox/issue=owner%2Falpha%231');
    await unmount(root);
  });

  it('follows an issue from intake through dispatch to run and pull request evidence', async () => {
    window.location.hash = '#/overview';
    let dispatched = false;
    const claimed = issue({
      eligible: false, action: 'none', reason_codes: ['issue_run_active'], pickup: 'labels',
      claim: { state_id: 'opi-1', phase: 'pr_created', run_id: 'run-9', run_status: 'running', active_run: true, next_action: 'AI review', last_error: 'CI flake on first attempt', pull_request_url: 'https://gitea.test/owner/alpha/pulls/12', pull_request_number: 12, issue_closed: false, repair_attempts: 1, max_repair_attempts: 2, updated_at: '2026-06-17T00:04:00.000Z' },
      links: { issue: 'https://gitea.test/owner/alpha/issues/1', pull_request: 'https://gitea.test/owner/alpha/pulls/12', run: '/api/runs/run-9', state: '/api/open-pilot/state?repo=owner/alpha&issue=1', readiness: '/api/open-pilot/readiness?repo=owner/alpha&issue=1' },
      checks: [{ name: 'issue_state', code: 'issue_run_active', status: 'fail', message: 'a queued or running Open Pilot run already owns this issue (run-9)', remediation: 'Wait for the run to finish, or cancel it, before dispatching again.' }],
    });
    const run9 = { id: 'run-9', workflow_id: 'open-pilot', trigger_id: 'manual-open-pilot', status: 'running', inputs: { repo: 'owner/alpha', issue: '1' }, created_at: '2026-06-17T00:03:00.000Z', summary: 'Open Pilot owner/alpha#1' };
    const fetch = mockDashboardFetch((url, init) => {
      const method = init.method ?? 'GET';
      if (url.pathname === '/api/open-pilot/inbox') return jsonResponse(report({ issues: [dispatched ? claimed : issue()], matched: 1 }));
      if (url.pathname === '/api/workflows/open-pilot/run' && method === 'POST') { dispatched = true; return jsonResponse({ run: run9 }, { status: 202 }); }
      if (method === 'GET' && url.pathname === '/api/runs/run-9') return jsonResponse({ run: run9, steps: [], events: [], artifacts: [] });
      if (method === 'GET' && url.pathname === '/api/open-pilot/state' && url.searchParams.get('repo') === 'owner/alpha') return jsonResponse({ state: { id: 'opi-1', repo: 'owner/alpha', issue: '1', phase: 'pr_created', run_id: 'run-9', pull_request_url: 'https://gitea.test/owner/alpha/pulls/12', issue_closed: false, gate_status: {}, repair_attempts: 1, max_repair_attempts: 2, updated_at: '2026-06-17T00:04:00.000Z', created_at: '2026-06-17T00:03:00.000Z' }, next_action: 'AI review' });
      return undefined;
    });
    const { container, root } = await renderApp();

    // Intake: the overview links to the inbox, which lists the eligible issue.
    await act(async () => { button(container, 'Open Pilot inbox').click(); });
    await waitFor(() => expectText(container, 'Add inbox'), container);
    expect(window.location.hash).toBe('#/inbox');
    await act(async () => { (container.querySelector('[data-inbox-issue="owner/alpha#1"]') as HTMLButtonElement).click(); });
    await waitFor(() => expectText(container, 'dispatch_eligible'), container);
    expect(window.location.hash).toBe('#/inbox/issue=owner%2Falpha%231');
    expect((Array.from(container.querySelectorAll('a')).find((a) => a.textContent === 'Gitea issue') as HTMLAnchorElement).href).toBe('https://gitea.test/owner/alpha/issues/1');

    // Dispatch goes through the existing manual Open Pilot route and opens the run cockpit.
    await act(async () => { button(container, 'Dispatch').click(); });
    await waitFor(() => expect(window.location.hash).toBe('#/runs/run-9'), container);
    const post = fetch.mock.calls.find(([url, init]) => new URL(String(url), 'http://switchyard.test').pathname === '/api/workflows/open-pilot/run' && (init as RequestInit | undefined)?.method === 'POST');
    expect(JSON.parse(String(post?.[1]?.body))).toEqual({ repo: 'owner/alpha', issue: '1', test_command: 'go test ./...' });
    await waitFor(() => expectText(container, 'Open Pilot owner/alpha#1'), container);
    expect(container.textContent).toContain('https://gitea.test/owner/alpha/pulls/12');

    // Back in the inbox the issue is claimed, links to its PR and run, and cannot be dispatched twice.
    await act(async () => { window.location.hash = '#/inbox/issue=owner%2Falpha%231'; window.dispatchEvent(new HashChangeEvent('hashchange')); });
    await waitFor(() => expectText(container, 'CI flake on first attempt'), container);
    expect(container.querySelector('[data-inbox-claim]')?.textContent).toContain('pr_created');
    expect(container.textContent).toContain('run-9 (running)');
    expect(container.textContent).toContain('issue_run_active');
    expect(Array.from(container.querySelectorAll('button')).some((item) => item.textContent === 'Dispatch')).toBe(false);
    const prLink = Array.from(container.querySelectorAll('a')).find((a) => a.textContent === 'Pull request') as HTMLAnchorElement;
    expect(prLink.href).toBe('https://gitea.test/owner/alpha/pulls/12');
    expect(prLink.rel).toContain('noopener');
    const runLink = Array.from(container.querySelectorAll('a')).find((a) => a.textContent === 'Open run') as HTMLAnchorElement;
    expect(runLink.getAttribute('href')).toBe('#/runs/run-9');
    await act(async () => { runLink.click(); });
    await waitFor(() => expect(window.location.hash).toBe('#/runs/run-9'), container);
    await unmount(root);
  });
});
