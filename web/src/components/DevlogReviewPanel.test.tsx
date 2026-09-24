// @vitest-environment jsdom

import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { DevlogReviewPanel } from './DevlogReviewPanel';
import type { DevlogDeliveryApproval, DevlogDeliveryReceipt, DevlogReportReview } from './devlogReview';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const markdown = '# Switchyard Devlog\n\nShipped delivery review.\n';

async function sha256(text: string) {
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(text));
  return Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, '0')).join('');
}

async function reviewFixture(overrides: Partial<DevlogReportReview> = {}): Promise<DevlogReportReview> {
  const digest = await sha256(markdown);
  return {
    run: { id: 'run-1', workflow_id: 'devlog', status: 'succeeded', created_at: '2026-09-24T12:00:00Z' } as DevlogReportReview['run'],
    state: 'awaiting-approval',
    report: {
      id: 'artifact-1',
      run_id: 'run-1',
      name: 'devlog.md',
      kind: 'markdown',
      uri: `data:text/markdown;charset=utf-8,${encodeURIComponent(markdown)}`,
      storage_class: 'inline-data-uri',
      content_type: 'text/markdown',
      byte_size: new TextEncoder().encode(markdown).length,
      sha256: digest,
      created_at: '2026-09-24T12:00:00Z',
    },
    evidence: null,
    source: { mode: 'repository-evidence', description: 'Repository evidence', since: '2026-09-14T00:00:00Z', until: '2026-09-21T00:00:00Z', repositories: ['owner/alpha'], complete: false, items: 3, missing: ['owner/alpha tags: failed'] },
    exports: [{ format: 'markdown', name: 'devlog.md', path: '/api/devlog/reports/run-1/export/markdown', sha256: digest, byte_size: markdown.length }],
    delivery: { enabled: true, adapter: 'gitea-issue-comment', destination: 'owner/devlog#12', approval_ttl_seconds: 86400, policy: 'An admin approval bound to the exact report SHA-256 is required.' },
    idempotency_key: 'k'.repeat(64),
    approvals: [],
    receipts: [],
    ...overrides,
  };
}

function approvalFor(review: DevlogReportReview, decision: 'approved' | 'rejected' = 'approved'): DevlogDeliveryApproval {
  return {
    id: 'approval-1',
    run_id: 'run-1',
    artifact_id: 'artifact-1',
    artifact_sha256: review.report?.sha256 ?? '',
    adapter: 'gitea-issue-comment',
    destination: 'owner/devlog#12',
    decision,
    actor: 'proxy:operator',
    reason: '',
    created_at: '2026-09-24T12:01:00Z',
    expires_at: '2026-09-25T12:01:00Z',
  };
}

function receiptFor(review: DevlogReportReview, status: DevlogDeliveryReceipt['status'], extra: Partial<DevlogDeliveryReceipt> = {}): DevlogDeliveryReceipt {
  return {
    id: 'receipt-1',
    idempotency_key: 'k'.repeat(64),
    run_id: 'run-1',
    artifact_id: 'artifact-1',
    artifact_sha256: review.report?.sha256 ?? '',
    adapter: 'gitea-issue-comment',
    destination: 'owner/devlog#12',
    approval_id: 'approval-1',
    status,
    attempts: 1,
    reconciled: false,
    requested_by: 'proxy:operator',
    created_at: '2026-09-24T12:02:00Z',
    updated_at: '2026-09-24T12:02:00Z',
    ...extra,
  };
}

function json(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), { status, headers: { 'content-type': 'application/json' } });
}

type Route = (init?: RequestInit) => Response | Promise<Response>;

function stubRoutes(routes: Record<string, Route | Route[]>) {
  const calls: { url: string; init?: RequestInit }[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? 'GET';
    calls.push({ url, init });
    const route = routes[`${method} ${url}`];
    if (!route) throw new Error(`unexpected request ${method} ${url}`);
    if (Array.isArray(route)) {
      const next = route.length > 1 ? route.shift()! : route[0];
      return next(init);
    }
    return route(init);
  });
  vi.stubGlobal('fetch', fetchMock);
  return calls;
}

let root: Root | null = null;

async function flush() {
  for (let index = 0; index < 5; index += 1) {
    await act(async () => {
      await Promise.resolve();
    });
  }
}

async function render(now = new Date('2026-09-24T13:00:00Z')) {
  const container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root?.render(<DevlogReviewPanel now={() => now} runID="run-1" runStatus="succeeded" />);
  });
  await flush();
  return container;
}

function button(container: HTMLElement, label: string) {
  const match = Array.from(container.querySelectorAll('button')).find((candidate) => candidate.textContent?.includes(label));
  if (!match) throw new Error(`button ${label} not found in ${container.textContent}`);
  return match as HTMLButtonElement;
}

async function click(element: Element) {
  await act(async () => {
    element.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
  await flush();
}

afterEach(async () => {
  await act(async () => root?.unmount());
  root = null;
  document.body.innerHTML = '';
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const reviewURL = '/api/devlog/reports/run-1';

describe('DevlogReviewPanel', () => {
  it('shows the readable preview and source window before any delivery', async () => {
    const review = await reviewFixture();
    const calls = stubRoutes({ [`GET ${reviewURL}`]: () => json(review) });
    const container = await render();
    expect(container.querySelector('[data-testid="devlog-source-window"]')?.textContent).toBe('2026-09-14T00:00:00Z → 2026-09-21T00:00:00Z');
    expect(container.querySelector('[data-testid="devlog-source-missing"]')?.textContent).toContain('owner/alpha tags: failed');
    expect(container.querySelector('[data-artifact-preview="artifact-1"]')?.textContent).toContain('Shipped delivery review.');
    expect(container.querySelector('[data-testid="devlog-review-state"]')?.getAttribute('data-state')).toBe('awaiting-approval');
    expect(container.textContent).toContain(review.report?.sha256);
    expect(button(container, 'Deliver to owner/devlog#12').disabled).toBe(true);
    expect(calls.every((call) => (call.init?.method ?? 'GET') === 'GET')).toBe(true);
  });

  it('exports the exact bytes after verifying the digest', async () => {
    const review = await reviewFixture();
    const created: Blob[] = [];
    vi.stubGlobal('URL', Object.assign(URL, { createObjectURL: vi.fn((blob: Blob) => (created.push(blob), 'blob:devlog')), revokeObjectURL: vi.fn() }));
    stubRoutes({
      [`GET ${reviewURL}`]: () => json(review),
      [`GET ${reviewURL}/export/markdown`]: () => new Response(markdown, { status: 200, headers: { 'X-Switchyard-Artifact-SHA256': review.report?.sha256 ?? '' } }),
    });
    const container = await render();
    await click(button(container, 'Export Markdown'));
    // The digest check uses crypto.subtle, which settles on a real async tick
    // rather than a microtask, so wait for the outcome instead of a fixed flush.
    await vi.waitFor(() => expect(created).toHaveLength(1));
    await flush();
    expect(await created[0].text()).toBe(markdown);
    expect(container.textContent).toContain('sha256 verified');
    expect(container.textContent).toContain('Nothing was sent to any destination.');
  });

  it('refuses an export whose bytes do not match the reviewed digest', async () => {
    const review = await reviewFixture();
    const createObjectURL = vi.fn(() => 'blob:devlog');
    vi.stubGlobal('URL', Object.assign(URL, { createObjectURL, revokeObjectURL: vi.fn() }));
    stubRoutes({
      [`GET ${reviewURL}`]: () => json(review),
      [`GET ${reviewURL}/export/markdown`]: () => new Response('tampered', { status: 200 }),
    });
    const container = await render();
    await click(button(container, 'Export Markdown'));
    await vi.waitFor(() => expect(container.textContent).toContain('Export refused'));
    expect(createObjectURL).not.toHaveBeenCalled();
  });

  it('records an approval bound to the report digest and then delivers once', async () => {
    const review = await reviewFixture();
    const approved = { ...review, state: 'approved' as const, current_approval: approvalFor(review), approvals: [approvalFor(review)] };
    const delivered = { ...approved, state: 'delivered' as const, current_receipt: receiptFor(review, 'delivered', { external_url: 'https://git.example/owner/devlog/issues/12#issuecomment-9' }), receipts: [receiptFor(review, 'delivered', { external_url: 'https://git.example/owner/devlog/issues/12#issuecomment-9' })] };
    const calls = stubRoutes({
      [`GET ${reviewURL}`]: [() => json(review), () => json(approved), () => json(delivered)],
      [`POST ${reviewURL}/approvals`]: () => json({ approval: approvalFor(review) }, 201),
      [`POST ${reviewURL}/deliveries`]: () => json({ outcome: 'delivered', receipt: delivered.current_receipt }, 201),
    });
    const container = await render();
    await click(button(container, 'Approve this version'));
    const approvalCall = calls.find((call) => call.url.endsWith('/approvals'));
    expect(JSON.parse(String(approvalCall?.init?.body))).toMatchObject({ artifact_sha256: review.report?.sha256, destination: 'owner/devlog#12', decision: 'approved' });
    expect(container.querySelector('[data-testid="devlog-review-state"]')?.getAttribute('data-state')).toBe('approved');

    await click(button(container, 'Deliver to owner/devlog#12'));
    const deliveryCall = calls.find((call) => call.url.endsWith('/deliveries'));
    expect(JSON.parse(String(deliveryCall?.init?.body))).toEqual({ approval_id: 'approval-1', artifact_sha256: review.report?.sha256 });
    expect(container.textContent).toContain('Delivered to owner/devlog#12.');
    expect(container.querySelector('[data-testid="devlog-review-state"]')?.getAttribute('data-state')).toBe('delivered');
    expect(container.querySelector('a[href^="https://git.example"]')).not.toBeNull();
    expect(button(container, 'Deliver to owner/devlog#12').disabled).toBe(true);
    expect(button(container, 'Approve this version').disabled).toBe(true);
  });

  it('shows a refused delivery for a stale approval and a failed attempt as retryable', async () => {
    const review = await reviewFixture();
    const approved = { ...review, state: 'approved' as const, current_approval: approvalFor(review) };
    const failed = { ...approved, state: 'delivery-failed' as const, current_receipt: receiptFor(review, 'failed', { last_error: 'deliver: 503' }), receipts: [receiptFor(review, 'failed', { last_error: 'deliver: 503' })] };
    stubRoutes({
      [`GET ${reviewURL}`]: [() => json(approved), () => json(approved), () => json(failed)],
      [`POST ${reviewURL}/deliveries`]: [
        () => json({ error: { code: 'stale-approval', message: 'the approval is bound to a different report version' } }, 409),
        () => json({ outcome: 'failed', receipt: failed.current_receipt }, 502),
      ],
    });
    const container = await render();
    await click(button(container, 'Deliver to owner/devlog#12'));
    expect(container.textContent).toContain('Delivery refused (stale-approval)');
    await click(button(container, 'Deliver to owner/devlog#12'));
    expect(container.textContent).toContain('Delivery failed: deliver: 503');
    expect(container.querySelector('[data-testid="devlog-review-state"]')?.textContent).toContain('retry is safe');
    expect(button(container, 'Retry delivery').disabled).toBe(false);
  });

  it('blocks delivery after a rejection or expiry and explains disabled delivery', async () => {
    const review = await reviewFixture();
    const rejected = { ...review, state: 'rejected' as const, current_approval: approvalFor(review, 'rejected') };
    stubRoutes({ [`GET ${reviewURL}`]: () => json(rejected) });
    let container = await render();
    expect(button(container, 'Deliver to owner/devlog#12').disabled).toBe(true);
    expect(container.querySelector('[data-testid="devlog-current-decision"]')?.textContent).toContain('rejected');
    await act(async () => root?.unmount());
    document.body.innerHTML = '';

    const expired = { ...review, state: 'approval-expired' as const, current_approval: approvalFor(review) };
    stubRoutes({ [`GET ${reviewURL}`]: () => json(expired) });
    container = await render(new Date('2026-09-26T00:00:00Z'));
    expect(button(container, 'Deliver to owner/devlog#12').disabled).toBe(true);
    await act(async () => root?.unmount());
    document.body.innerHTML = '';

    const disabled = { ...review, state: 'delivery-disabled' as const, delivery: { enabled: false, adapter: 'disabled', reason: 'DEVLOG_DELIVERY_ADAPTER is disabled', approval_ttl_seconds: 86400, policy: 'p' } };
    stubRoutes({ [`GET ${reviewURL}`]: () => json(disabled) });
    container = await render();
    expect(container.querySelector('[data-testid="devlog-delivery-disabled"]')?.textContent).toContain('DEVLOG_DELIVERY_ADAPTER is disabled');
    expect(container.textContent).not.toContain('Approve this version');
    expect(button(container, 'Export Markdown').disabled).toBe(false);
  });

  it('disables approval and delivery for read-only viewers', async () => {
    const review = await reviewFixture({ access: { role: 'read', can_write: false } });
    const approved = { ...review, state: 'approved' as const, current_approval: approvalFor(review) };
    stubRoutes({ [`GET ${reviewURL}`]: () => json(approved) });
    const container = await render();
    expect(container.querySelector('[data-testid="devlog-read-only"]')).not.toBeNull();
    expect(button(container, 'Approve this version').disabled).toBe(true);
    expect(button(container, 'Deliver to owner/devlog#12').disabled).toBe(true);
    expect(button(container, 'Export Markdown').disabled).toBe(false);
  });

  it('reports a denied or failed review load', async () => {
    stubRoutes({ [`GET ${reviewURL}`]: () => json({ error: { message: 'authentication required' } }, 401) });
    const container = await render();
    expect(container.textContent).toContain('Devlog review unavailable: Switchyard API returned 401');
  });
});
