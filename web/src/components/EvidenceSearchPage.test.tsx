// @vitest-environment jsdom

import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { EvidenceSearchPage } from './EvidenceSearchPage';
import { formatEvidenceSearchHash, parseEvidenceSearchHash } from './evidenceSearch';
import { button, expectText, jsonResponse, waitFor } from '../test-utils';
import type { EvidenceHit, EvidenceSearchResponse } from '../types';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const stepHit: EvidenceHit = { kind: 'step', id: 'step-1', run_id: 'run-1', workflow_id: 'devlog', status: 'failed', title: 'devlog.generate', summary: '', error: 'upstream refused token=[REDACTED]', correlation_ids: { trace_id: 'trace-9' }, created_at: '2026-09-01T03:00:00.000Z', finished_at: '2026-09-01T03:01:00.000Z' };
const requestHit: EvidenceHit = { kind: 'request', id: 'req-9', status: 'failed', title: 'llama', summary: '/api/generate cli', error: 'upstream unavailable', created_at: '2026-09-01T04:00:00.000Z' };
const retention = { window: '720h0m0s', floor: '2026-08-02T00:00:00.000Z', searched_at: '2026-09-01T05:00:00.000Z' };

function page(hits: EvidenceHit[], overrides: Partial<EvidenceSearchResponse> = {}): EvidenceSearchResponse {
  return { hits, next_cursor: '', query: '', scope: 'all', limit: 25, retention, ...overrides };
}

let root: Root | null = null;

async function render() {
  const container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => {
    root?.render(<EvidenceSearchPage />);
  });
  return container;
}

function setInput(container: HTMLElement, name: string, value: string) {
  const input = container.querySelector(`[name="${name}"]`) as HTMLInputElement | HTMLSelectElement | null;
  if (!input) throw new Error(`${name} control not found`);
  const prototype = input instanceof HTMLSelectElement ? HTMLSelectElement.prototype : HTMLInputElement.prototype;
  Object.getOwnPropertyDescriptor(prototype, 'value')?.set?.call(input, value);
  input.dispatchEvent(new Event(input instanceof HTMLSelectElement ? 'change' : 'input', { bubbles: true }));
}

async function submit(container: HTMLElement) {
  await act(async () => {
    container.querySelector('form')?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
  });
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
});

describe('evidence search deep links', () => {
  it('round-trips scope, query, and cursor', () => {
    const link = { scope: 'errors' as const, query: 'trace/9 needle', cursor: 'MjAyNg' };
    expect(formatEvidenceSearchHash(link)).toBe('#/logs/search/errors/trace%2F9%20needle/MjAyNg');
    expect(parseEvidenceSearchHash(formatEvidenceSearchHash(link))).toEqual(link);
    expect(parseEvidenceSearchHash('#/logs/search/all/')).toEqual({ scope: 'all', query: '', cursor: '' });
  });

  it('ignores unrelated, malformed, or unknown-scope links', () => {
    expect(parseEvidenceSearchHash('#/logs')).toBeNull();
    expect(parseEvidenceSearchHash('#/runs/run-1')).toBeNull();
    expect(parseEvidenceSearchHash('#/logs/search/artifacts/x')).toBeNull();
    expect(parseEvidenceSearchHash('#/logs/search/steps/%E0%A4%A')).toEqual({ scope: 'steps', query: '', cursor: '' });
    expect(parseEvidenceSearchHash('#/logs/search/steps/x/not%20a%20cursor')).toEqual({ scope: 'steps', query: 'x', cursor: '' });
  });
});

describe('EvidenceSearchPage', () => {
  it('renders the idle query state without calling the API', async () => {
    window.location.hash = '#/logs';
    const fetch = vi.fn();
    vi.stubGlobal('fetch', fetch);
    const container = await render();

    expect(container.textContent).toContain('Enter a query or choose a scope');
    expect(container.querySelector('form[aria-label="Evidence search"]')).not.toBeNull();
    expect(container.querySelector('select[name="scope"]')).not.toBeNull();
    expect(fetch).not.toHaveBeenCalled();
  });

  it('submits a bounded search, shows loading then results, and writes a deep link', async () => {
    window.location.hash = '#/logs';
    let release: (() => void) | undefined;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    const fetch = vi.fn(async (input: RequestInfo | URL) => {
      await gate;
      const url = new URL(String(input), 'http://switchyard.test');
      expect(url.pathname).toBe('/api/evidence/search');
      expect(url.searchParams.get('q')).toBe('token');
      expect(url.searchParams.get('scope')).toBe('errors');
      expect(url.searchParams.get('limit')).toBe('25');
      expect(url.searchParams.has('cursor')).toBe(false);
      return jsonResponse(page([stepHit, requestHit], { query: 'token', scope: 'errors' }));
    });
    vi.stubGlobal('fetch', fetch);
    const container = await render();

    await act(async () => {
      setInput(container, 'q', 'token');
      setInput(container, 'scope', 'errors');
    });
    await submit(container);

    expect(container.textContent).toContain('Searching retained evidence…');
    expect(window.location.hash).toBe('#/logs/search/errors/token');

    await act(async () => { release?.(); });
    await waitFor(() => expectText(container, 'devlog.generate'), container);

    expect(fetch).toHaveBeenCalledTimes(1);
    expect(container.textContent).toContain('upstream refused token=[REDACTED]');
    expect(container.textContent).toContain('trace_id: trace-9');
    expect(container.textContent).toContain('720h0m0s');
    expect(container.textContent).toContain('2 on this page · last page');
    expect(container.querySelector('a[href="#/runs/run-1"]')).not.toBeNull();
    expect(container.querySelector('a[href="#/queues/requests/req-9"]')).not.toBeNull();
    expect(container.textContent).not.toContain('Searching retained evidence…');
  });

  it('shows the no-results state with the retention window', async () => {
    window.location.hash = '#/logs';
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse(page([], { query: 'nothing' }))));
    const container = await render();

    await act(async () => { setInput(container, 'q', 'nothing'); });
    await submit(container);
    await waitFor(() => expectText(container, 'No retained evidence matches'), container);

    expect(container.textContent).toContain('“nothing”');
    expect(container.textContent).toContain('720h0m0s retention window');
    expect(container.querySelector('[role="alert"]')).toBeNull();
  });

  it('shows an error state and retries on demand', async () => {
    window.location.hash = '#/logs';
    const fetch = vi.fn()
      .mockRejectedValueOnce(new Error('search backend offline'))
      .mockResolvedValueOnce(jsonResponse(page([requestHit], { query: 'llama' })));
    vi.stubGlobal('fetch', fetch);
    const container = await render();

    await act(async () => { setInput(container, 'q', 'llama'); });
    await submit(container);
    await waitFor(() => expectText(container, 'search backend offline'), container);

    const alert = container.querySelector('[role="alert"]');
    expect(alert?.textContent).toContain('Search failed');

    await act(async () => {
      button(container, 'Retry').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    await waitFor(() => expectText(container, 'upstream unavailable'), container);

    expect(fetch).toHaveBeenCalledTimes(2);
    expect(container.querySelector('[role="alert"]')).toBeNull();
  });

  it('honors a deep link on mount and paginates with the opaque cursor', async () => {
    window.location.hash = '#/logs/search/steps/trace-9';
    const calls: string[] = [];
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), 'http://switchyard.test');
      calls.push(url.search);
      if (url.searchParams.get('cursor') === 'cursor-two') {
        return jsonResponse(page([requestHit], { query: 'trace-9', scope: 'steps' }));
      }
      return jsonResponse(page([stepHit], { query: 'trace-9', scope: 'steps', next_cursor: 'cursor-two' }));
    }));
    const container = await render();
    await waitFor(() => expectText(container, 'devlog.generate'), container);

    expect(calls[0]).toContain('q=trace-9');
    expect(calls[0]).toContain('scope=steps');
    expect((container.querySelector('select[name="scope"]') as HTMLSelectElement).value).toBe('steps');
    expect((container.querySelector('input[name="q"]') as HTMLInputElement).value).toBe('trace-9');
    expect(container.textContent).toContain('more available');

    await act(async () => {
      button(container, 'Next page').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    await waitFor(() => expectText(container, 'llama'), container);

    expect(calls[1]).toContain('cursor=cursor-two');
    expect(window.location.hash).toBe('#/logs/search/steps/trace-9/cursor-two');
    expect(container.textContent).toContain('last page');

    await act(async () => {
      button(container, 'Previous page').dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    await waitFor(() => expectText(container, 'devlog.generate'), container);
    expect(window.location.hash).toBe('#/logs/search/steps/trace-9');
    expect(calls).toHaveLength(3);
  });
});
