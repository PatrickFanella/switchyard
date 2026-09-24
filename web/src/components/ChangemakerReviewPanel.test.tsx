// @vitest-environment jsdom

import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ChangemakerReviewPanel } from './ChangemakerReviewPanel';
import type { ChangemakerDraftReview, ChangemakerDraftVersionView, ChangemakerPublicationReceipt, ChangemakerReviewDecision } from './changemakerReview';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const draftMarkdown = '# Changemaker draft: project activity\n\nThe parser crash was fixed [S1].\n';
const draftSHA = 'd'.repeat(64);
const evidenceSHA = 'e'.repeat(64);

function versionView(overrides: Partial<ChangemakerDraftVersionView> = {}): ChangemakerDraftVersionView {
  return {
    id: 'version-1',
    thread_id: 'run-1',
    version: 1,
    run_id: 'run-1',
    sources_run_id: 'run-1',
    source_mode: 'original',
    created_at: '2026-09-24T09:00:00Z',
    run_status: 'succeeded',
    draft: {
      id: 'artifact-1',
      run_id: 'run-1',
      name: 'changemaker-digest.md',
      kind: 'markdown',
      uri: `data:text/markdown;charset=utf-8,${encodeURIComponent(draftMarkdown)}`,
      storage_class: 'inline-data-uri',
      content_type: 'text/markdown',
      byte_size: new TextEncoder().encode(draftMarkdown).length,
      sha256: draftSHA,
      created_at: '2026-09-24T09:00:00Z',
    } as ChangemakerDraftVersionView['draft'],
    draft_sha256: draftSHA,
    evidence_sha256: evidenceSHA,
    grounding_status: 'complete',
    draft_generated: true,
    cited_sources: 'S1',
    unknown_citations: '',
    ready: true,
    state: 'awaiting-review',
    ...overrides,
  };
}

function reviewFixture(overrides: Partial<ChangemakerDraftReview> = {}): ChangemakerDraftReview {
  return {
    thread_id: 'run-1',
    selected_version: 1,
    latest_version: 1,
    current_version: 1,
    state: 'awaiting-review',
    versions: [versionView()],
    decisions: [],
    receipts: [],
    publication: { enabled: true, adapter: 'gitea-issue-comment', destination: 'owner/news#4', approval_ttl_seconds: 86400, max_draft_versions: 10, policy: 'Runs never publish by themselves.' },
    access: { role: 'admin', can_write: true },
    ...overrides,
  };
}

function approval(overrides: Partial<ChangemakerReviewDecision> = {}): ChangemakerReviewDecision {
  return {
    id: 'decision-1',
    thread_id: 'run-1',
    version: 1,
    run_id: 'run-1',
    artifact_id: 'artifact-1',
    draft_sha256: draftSHA,
    evidence_sha256: evidenceSHA,
    decision: 'approved',
    notes: '',
    actor: 'proxy:operator',
    adapter: 'gitea-issue-comment',
    destination: 'owner/news#4',
    created_at: '2026-09-24T12:00:00Z',
    expires_at: '2026-09-25T12:00:00Z',
    ...overrides,
  };
}

function receipt(status: ChangemakerPublicationReceipt['status'], extra: Partial<ChangemakerPublicationReceipt> = {}): ChangemakerPublicationReceipt {
  return {
    id: 'receipt-1',
    idempotency_key: 'k'.repeat(64),
    run_id: 'run-1',
    artifact_id: 'artifact-1',
    artifact_sha256: draftSHA,
    adapter: 'gitea-issue-comment',
    destination: 'owner/news#4',
    approval_id: 'decision-1',
    status,
    attempts: 1,
    reconciled: false,
    requested_by: 'proxy:operator',
    created_at: '2026-09-24T12:01:00Z',
    updated_at: '2026-09-24T12:01:00Z',
    ...extra,
  };
}

function json(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), { status, headers: { 'content-type': 'application/json' } });
}

type Route = (init?: RequestInit) => Response | Promise<Response>;

function stubRoutes(routes: Record<string, Route | Route[]>) {
  const calls: { url: string; init?: RequestInit }[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
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
    }),
  );
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

async function render(onRevisionCreated?: (runID: string) => void, now = new Date('2026-09-24T13:00:00Z')) {
  const container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root?.render(<ChangemakerReviewPanel now={() => now} onRevisionCreated={onRevisionCreated} runID="run-1" runStatus="succeeded" />);
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

async function typeNotes(container: HTMLElement, value: string) {
  const textarea = container.querySelector('textarea') as HTMLTextAreaElement;
  await act(async () => {
    const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')?.set;
    setter?.call(textarea, value);
    textarea.dispatchEvent(new Event('input', { bubbles: true }));
  });
  await flush();
}

function body(init?: RequestInit) {
  return JSON.parse(String(init?.body ?? '{}')) as Record<string, unknown>;
}

afterEach(async () => {
  await act(async () => root?.unmount());
  root = null;
  document.body.innerHTML = '';
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const reviewURL = '/api/changemaker/drafts/run-1';

describe('ChangemakerReviewPanel', () => {
  it('shows the draft, its digests, and binds an approval to the exact draft and evidence', async () => {
    const approved = reviewFixture({ state: 'approved', versions: [versionView({ state: 'approved', current_decision: approval() })], decisions: [approval()] });
    const calls = stubRoutes({
      [`GET ${reviewURL}`]: [() => json(reviewFixture()), () => json(approved)],
      [`POST ${reviewURL}/decisions`]: () => json({ decision: approval() }, 201),
    });
    const container = await render();
    expect(container.querySelector('[data-testid="changemaker-review-state"]')?.textContent).toBe('Awaiting review');
    expect(container.querySelector('[data-testid="changemaker-draft-sha"]')?.textContent).toBe(draftSHA);
    expect(container.querySelector('[data-testid="changemaker-evidence-sha"]')?.textContent).toBe(evidenceSHA);
    expect(container.textContent).toContain('The parser crash was fixed');
    expect(button(container, 'Publish version 1').disabled).toBe(true);
    expect(button(container, 'Request revision').disabled).toBe(true);

    await click(button(container, 'Approve version 1'));
    const decision = calls.find((call) => call.init?.method === 'POST');
    expect(body(decision?.init)).toEqual({ version: 1, draft_sha256: draftSHA, evidence_sha256: evidenceSHA, decision: 'approved', notes: '' });
    expect(container.textContent).toContain("Approval recorded for version 1's exact draft and source evidence.");
    expect(container.querySelector('[data-testid="changemaker-review-state"]')?.textContent).toBe('Approved, not published');
    expect(button(container, 'Publish version 1').disabled).toBe(false);
  });

  it('requests a revision with notes and the chosen source mode, keeping earlier versions', async () => {
    const revised = reviewFixture({
      latest_version: 2,
      current_version: 2,
      versions: [
        versionView({ state: 'superseded', current_decision: approval({ decision: 'revision-requested', notes: 'Name the input.', revision_run_id: 'run-2' }) }),
        versionView({ id: 'version-2', version: 2, run_id: 'run-2', parent_version: 1, source_mode: 'recollected', sources_run_id: 'run-2', revision_notes: 'Name the input.', draft_sha256: 'f'.repeat(64), state: 'awaiting-review' }),
      ],
      decisions: [approval({ id: 'decision-r', decision: 'revision-requested', notes: 'Name the input.', revision_run_id: 'run-2' })],
    });
    const created: string[] = [];
    const calls = stubRoutes({
      [`GET ${reviewURL}`]: [() => json(reviewFixture()), () => json(revised)],
      [`POST ${reviewURL}/revisions`]: () =>
        json({ version: revised.versions[1], decision: revised.decisions[0], run: { id: 'run-2', workflow_id: 'changemaker-daily-digest', status: 'succeeded' } }, 201),
    });
    const container = await render((runID) => created.push(runID));
    await typeNotes(container, 'Name the input.');
    const recollect = container.querySelectorAll('input[type="radio"]')[1] as HTMLInputElement;
    await click(recollect);
    await click(button(container, 'Request revision'));
    const request = calls.find((call) => call.init?.method === 'POST');
    expect(body(request?.init)).toEqual({ version: 1, draft_sha256: draftSHA, evidence_sha256: evidenceSHA, notes: 'Name the input.', sources: 'recollect' });
    expect(created).toEqual(['run-2']);
    expect(container.textContent).toContain('Revision requested: version 2 is run run-2');
    expect(container.querySelector('[data-testid="changemaker-thread-summary"]')?.textContent).toContain('2 versions');
    expect(container.querySelector('[data-testid="changemaker-version-1"]')?.textContent).toContain('Superseded by a newer version');
    expect(container.querySelector('[data-testid="changemaker-version-2"]')?.textContent).toContain('Notes: Name the input.');
    // The panel now shows version 2; version 1 stays viewable but closed.
    expect(container.querySelector('[data-testid="changemaker-review-state"]')?.textContent).toBe('Awaiting review');
    await click(container.querySelector('[data-testid="changemaker-version-1"]')!);
    expect(container.querySelector('[data-testid="changemaker-review-state"]')?.textContent).toBe('Superseded by a newer version');
    expect(button(container, 'Approve version 1').disabled).toBe(true);
  });

  it('reports a stale approval refusal and a failed publication that is safe to retry', async () => {
    const approved = reviewFixture({ state: 'approved', versions: [versionView({ state: 'approved', current_decision: approval() })] });
    const failed = reviewFixture({ state: 'publication-failed', versions: [versionView({ state: 'publication-failed', current_decision: approval(), current_receipt: receipt('failed', { last_error: 'deliver: 503' }) })], receipts: [receipt('failed', { last_error: 'deliver: 503' })] });
    const published = reviewFixture({ state: 'published', published_version: 1, versions: [versionView({ state: 'published', current_decision: approval(), current_receipt: receipt('delivered', { attempts: 2, external_url: 'https://git.example.test/owner/news/issues/4#c1' }) })], receipts: [receipt('delivered', { attempts: 2, external_url: 'https://git.example.test/owner/news/issues/4#c1' })] });
    const calls = stubRoutes({
      [`GET ${reviewURL}`]: [() => json(approved), () => json(approved), () => json(failed), () => json(published)],
      [`POST ${reviewURL}/publications`]: [
        () => json({ error: { code: 'stale-approval', message: 'the approval is bound to a different draft or evidence digest' } }, 409),
        () => json({ outcome: 'failed', receipt: receipt('failed', { last_error: 'deliver: 503' }) }, 502),
        () => json({ outcome: 'published', receipt: receipt('delivered', { attempts: 2 }) }, 201),
      ],
    });
    const container = await render();
    await click(button(container, 'Publish version 1'));
    expect(container.textContent).toContain('Publication refused (stale-approval)');
    await click(button(container, 'Publish version 1'));
    expect(container.textContent).toContain('Publication failed: deliver: 503. Retrying is safe');
    expect(container.querySelector('[data-testid="changemaker-review-state"]')?.textContent).toBe('Publication failed; retry is safe');
    await click(button(container, 'Retry publication'));
    expect(container.textContent).toContain('Published version 1 to owner/news#4.');
    expect(container.querySelector('[data-testid="changemaker-thread-summary"]')?.textContent).toContain('published version 1');
    expect(container.querySelector('a[href="https://git.example.test/owner/news/issues/4#c1"]')).not.toBeNull();
    const publishes = calls.filter((call) => call.init?.method === 'POST');
    expect(publishes).toHaveLength(3);
    for (const call of publishes) {
      expect(body(call.init)).toEqual({ approval_id: 'decision-1', draft_sha256: draftSHA });
    }
    expect(button(container, 'Approve version 1').disabled).toBe(true);
  });

  it('keeps publication off when disabled and disables actions for read-only or expired access', async () => {
    const disabled = reviewFixture({ publication: { enabled: false, adapter: 'disabled', reason: 'CHANGEMAKER_PUBLICATION_ADAPTER is disabled', approval_ttl_seconds: 86400, max_draft_versions: 10, policy: 'p' } });
    stubRoutes({ [`GET ${reviewURL}`]: () => json(disabled) });
    let container = await render();
    expect(container.querySelector('[data-testid="changemaker-publication-disabled"]')?.textContent).toContain('CHANGEMAKER_PUBLICATION_ADAPTER is disabled');
    expect(Array.from(container.querySelectorAll('button')).some((candidate) => candidate.textContent?.includes('Publish'))).toBe(false);
    expect(button(container, 'Approve version 1').disabled).toBe(false);
    await act(async () => root?.unmount());
    root = null;
    document.body.innerHTML = '';

    const readOnly = reviewFixture({ access: { role: 'read', can_write: false }, versions: [versionView({ state: 'approved', current_decision: approval() })] });
    stubRoutes({ [`GET ${reviewURL}`]: () => json(readOnly) });
    container = await render();
    expect(container.querySelector('[data-testid="changemaker-read-only"]')).not.toBeNull();
    expect(button(container, 'Approve version 1').disabled).toBe(true);
    expect(button(container, 'Reject').disabled).toBe(true);
    expect(button(container, 'Publish version 1').disabled).toBe(true);
    await act(async () => root?.unmount());
    root = null;
    document.body.innerHTML = '';

    const expired = reviewFixture({ versions: [versionView({ state: 'approval-expired', current_decision: approval({ expires_at: '2026-09-24T12:30:00Z' }) })] });
    stubRoutes({ [`GET ${reviewURL}`]: () => json(expired) });
    container = await render();
    expect(button(container, 'Publish version 1').disabled).toBe(true);

    await act(async () => root?.unmount());
    root = null;
    document.body.innerHTML = '';
    const noDraft = reviewFixture({ state: 'no-draft', versions: [versionView({ state: 'no-draft', draft_generated: false })] });
    stubRoutes({ [`GET ${reviewURL}`]: () => json(noDraft) });
    container = await render();
    expect(button(container, 'Approve version 1').disabled).toBe(true);
    expect(button(container, 'Reject').disabled).toBe(false);
  });

  it('shows an error when the review cannot load', async () => {
    stubRoutes({ [`GET ${reviewURL}`]: () => json({ error: { code: 'not-changemaker', message: 'run is not a Changemaker draft' } }, 404) });
    const container = await render();
    expect(container.textContent).toContain('Changemaker review unavailable');
  });
});
