// @vitest-environment jsdom

import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { renderToString } from 'react-dom/server';
import { afterEach, describe, expect, it, vi } from 'vitest';
import App, { openPilotStateFields } from './App';
import { button, controlByLabel, expectText, jsonResponse, mockDashboardFetch, openPilotStateResponse, renderApp, unmount, waitFor } from './test-utils';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

afterEach(() => {
  window.location.hash = '';
  document.body.innerHTML = '';
  vi.unstubAllGlobals();
});

describe('App', () => {
  it('renders the operator shell and overview hierarchy', () => {
    window.location.hash = '#/overview';

    const html = renderToString(<App />);

    expect(html).toContain('Automation runtime');
    expect(html).toContain('Operations overview');
    expect(html).toContain('System posture');
    expect(html).toContain('Workflows and triggers');
    expect(html).toContain('Selected run');
    expect(html).toContain('Run posture');
    expect(html).toContain('Primary navigation');
    expect(html).toContain('Skip to content');
    expect(openPilotStateFields).toContain('Next action');
  });

  it('shows a degraded state without seeded production data when loading fails', async () => {
    window.location.hash = '#/overview';
    vi.stubGlobal('fetch', vi.fn(async () => {
      throw new Error('api unavailable');
    }));
    const container = document.createElement('div');
    document.body.appendChild(container);
    const root = createRoot(container);

    await act(async () => {
      root.render(<App />);
    });
    await waitFor(() => expectText(container, 'Degraded'), container);

    expect(container.textContent).toContain('Degraded');
    expect(container.textContent).toContain('api unavailable');
    expect(container.textContent).toContain('No triggers returned by the API.');
    expect(container.textContent).not.toContain('Guided execution for high-signal Gitea issues');

    await act(async () => {
      root.unmount();
    });
  });

  it('opens the cockpit from overview', async () => {
    window.location.hash = '#/overview';
    mockDashboardFetch();
    const container = document.createElement('div');
    document.body.appendChild(container);

    const root = createRoot(container);

    await act(async () => {
      root.render(<App />);
    });

    await waitFor(() => expectText(container, 'Open cockpit'), container);

    const cockpitButton = Array.from(container.querySelectorAll('button')).find((button) => button.textContent?.includes('Open cockpit')) as HTMLButtonElement | undefined;
    expect(cockpitButton).toBeDefined();

    await act(async () => {
      cockpitButton?.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });

    expect(window.location.hash).toBe('#/runs/run-1');

    await act(async () => {
      root.unmount();
    });
  });

  it('uses the native operator summary and agent profile contracts', async () => {
    window.location.hash = '#/overview';
    const fetch = mockDashboardFetch();
    const { container, root } = await renderApp('Runtime operating normally');

    expect(container.textContent).toContain('Succeeded (24h)');
    expect(container.textContent).toContain('Model broker: healthy');
    expect(fetch).toHaveBeenCalledWith('/api/operator-summary', expect.anything());

    const agentButton = Array.from(container.querySelectorAll('button')).find((item) => item.textContent?.trim() === 'Agents');
    await act(async () => {
      agentButton?.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });
    await waitFor(() => expectText(container, 'Open Pilot Executor'), container);
    await waitFor(() => expectText(container, 'opencode, command, git, gitea'), container);
    expect(container.textContent).toContain('Implement the smallest safe change.');
    expect(container.textContent).toContain('static default');

    await unmount(root);
  });

  it('honors hash navigation on initial render', () => {
    window.location.hash = '#/queues';

    const html = renderToString(<App />);

    expect(html).toContain('Requests cockpit');
    expect(html).toContain('Queues');
  });

  it('renders the run cockpit deep link', async () => {
    window.location.hash = '#/runs/run-1';
    mockDashboardFetch();
    const container = document.createElement('div');
    document.body.appendChild(container);

    const root = createRoot(container);

    await act(async () => {
      root.render(<App />);
    });

    await waitFor(() => expectText(container, 'Resume Open Pilot'), container);

    expect(container.textContent).toContain('Run cockpit');
    expect(container.textContent).toContain('Open Pilot lifecycle');
    expect(container.textContent).toContain('Step timeline');
    expect(container.textContent).toContain('Evidence');
    expect(container.textContent).toContain('Correlation + traces');
    expect(container.textContent).toContain('Resume Open Pilot');

    await act(async () => {
      root.unmount();
    });
  });

  it('falls back safely for malformed run deep links', () => {
    window.location.hash = '#/runs/%E0%A4%A';

    const html = renderToString(<App />);

    expect(html).toContain('Run cockpit');
    expect(html).toContain('Select a run from Overview to open the run cockpit.');
  });

  it('updates the hash when a top-level tab is clicked', async () => {
    window.location.hash = '#/overview';
    mockDashboardFetch();
    const container = document.createElement('div');
    document.body.appendChild(container);

    const root = createRoot(container);

    await act(async () => {
      root.render(<App />);
    });

    await waitFor(() => expectText(container, 'Queues'), container);

    const queuesButton = Array.from(container.querySelectorAll('button')).find((button) => button.textContent?.includes('Queues')) as HTMLButtonElement | undefined;
    expect(queuesButton).toBeDefined();

    await act(async () => {
      queuesButton?.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    });

    expect(window.location.hash).toBe('#/queues');

    await act(async () => {
      root.unmount();
    });
  });

  it('renders model broker observability from hash navigation', async () => {
    window.location.hash = '#/observability';
    mockDashboardFetch();
    const container = document.createElement('div');
    document.body.appendChild(container);

    const root = createRoot(container);

    await act(async () => {
      root.render(<App />);
    });

    expect(container.textContent).toContain('Model broker signal');
    expect(container.textContent).toContain('req-1');

    await act(async () => {
      root.unmount();
    });
  });

  it('renders request cockpit and supports request deep links', async () => {
    window.location.hash = '#/queues/requests/req-ok';
    mockDashboardFetch();
    const container = document.createElement('div');
    document.body.appendChild(container);

    const root = createRoot(container);

    await act(async () => {
      root.render(<App />);
    });

    expect(container.textContent).toContain('Requests cockpit');
    expect(container.textContent).toContain('req-ok');
    expect(container.textContent).toContain('trace_id');

    await act(async () => {
      root.unmount();
    });
  });

  it('does not crash on malformed request deep links', () => {
    window.location.hash = '#/queues/requests/%E0%A4%A';

    const html = renderToString(<App />);

    expect(html).toContain('Requests cockpit');
  });

  it('filters steps and evidence in the cockpit', async () => {
    window.location.hash = '#/runs/run-1';
    mockDashboardFetch();
    const container = document.createElement('div');
    document.body.appendChild(container);

    const root = createRoot(container);

    await act(async () => {
      root.render(<App />);
    });

    const selects = Array.from(container.querySelectorAll('select')).filter(select => Array.from(select.options).some(option => option.value === 'failed')) as HTMLSelectElement[];
    expect(selects.length).toBe(1);

    await act(async () => {
      selects[0].value = 'failed';
      selects[0].dispatchEvent(new Event('change', { bubbles: true }));
    });

    expect(container.textContent).toContain('implement');
    expect(container.textContent).toContain('Visible steps1');
    expect(container.textContent).not.toContain('Planning complete');

    const searchInput = container.querySelector('input[placeholder*="artifact"]') as HTMLInputElement | null;
    expect(searchInput).toBeDefined();

    await act(async () => {
      if (searchInput) {
        const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set;
        setter?.call(searchInput, 'trace.json');
        searchInput.dispatchEvent(new Event('input', { bubbles: true }));
        searchInput.dispatchEvent(new Event('change', { bubbles: true }));
      }
    });
    await waitFor(() => expectText(container, 'trace.json'), container);

    expect(container.textContent).toContain('trace.json');
    expect(container.textContent).not.toContain('bundle.zip attached');

    await act(async () => {
      root.unmount();
    });
  });

  it('dispatches Open Pilot with the entered repository and issue', async () => {
    window.location.hash = '#/overview';
    const fetch = mockDashboardFetch((url, init) => {
      if (url.pathname === '/api/runs' && init.method === 'POST') {
        return jsonResponse({ run: { id: 'run-1' } });
      }
      return undefined;
    });
    const { container, root } = await renderApp();
    const repo = controlByLabel(container, 'Repo') as HTMLInputElement;
    const issue = controlByLabel(container, 'Issue') as HTMLInputElement;

    await act(async () => {
      for (const [input, value] of [[repo, 'acme/widget'], [issue, '42']] as const) {
        const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set;
        setter?.call(input, value);
        input.dispatchEvent(new Event('input', { bubbles: true }));
        input.dispatchEvent(new Event('change', { bubbles: true }));
      }
    });
    await act(async () => { button(container, 'Run now').click(); });
    await waitFor(() => expect(window.location.hash).toBe('#/overview/runs/run-1'), container);

    const call = fetch.mock.calls.find(([url, init]) => new URL(String(url), 'http://switchyard.test').pathname === '/api/runs' && (init as RequestInit | undefined)?.method === 'POST');
    expect(call?.[1]?.body).toBe(JSON.stringify({ workflow_id: 'open-pilot', trigger_id: 'trigger-1', inputs: { repo: 'acme/widget', issue: '42' } }));
    expect(window.location.hash).toBe('#/overview/runs/run-1');
    await unmount(root);
  });

  it('retries a run and retains the selected run', async () => {
    window.location.hash = '#/runs/run-1';
    let retried = false;
    const fetch = mockDashboardFetch((url, init) => {
      if (retried && url.pathname === '/api/runs/run-1') return jsonResponse({ run: { id: 'run-1', workflow_id: 'open-pilot', trigger_id: 'trigger-1', status: 'succeeded', inputs: { repo: 'OWNER/REPO', issue: '123' }, created_at: '2026-06-17T00:00:00.000Z', summary: 'Open Pilot run 1' }, steps: [], events: [], artifacts: [] });
      if (url.pathname === '/api/open-pilot/state') return jsonResponse(openPilotStateResponse({ phase: 'succeeded', gate_status: {}, resume_inputs: {} }, 'done'));
      if (url.pathname === '/api/runs/run-1/retry' && init.method === 'POST') { retried = true; return jsonResponse({ run: { id: 'run-1', status: 'succeeded' } }); }
      return undefined;
    });
    const { container, root } = await renderApp('Run cockpit');
    await act(async () => { button(container, 'Retry run').click(); });
    await waitFor(() => expectText(container, 'succeeded'), container);

    expect(fetch.mock.calls.some(([url, init]) => new URL(String(url), 'http://switchyard.test').pathname === '/api/runs/run-1/retry' && (init as RequestInit | undefined)?.method === 'POST')).toBe(true);
    expect(window.location.hash).toBe('#/overview/runs/run-1');
    await unmount(root);
  });

  it('cancels a run and retains the selected run', async () => {
    window.location.hash = '#/runs/run-1';
    let canceled = false;
    const fetch = mockDashboardFetch((url, init) => {
      if (url.pathname === '/api/runs/run-1/cancel' && init.method === 'POST') { canceled = true; return jsonResponse({ run: { id: 'run-1', status: 'canceled' } }); }
      if (canceled && url.pathname === '/api/runs/run-1') return jsonResponse({ run: { id: 'run-1', workflow_id: 'open-pilot', trigger_id: 'trigger-1', status: 'canceled', inputs: { repo: 'OWNER/REPO', issue: '123' }, created_at: '2026-06-17T00:00:00.000Z', summary: 'Open Pilot run 1' }, steps: [], events: [], artifacts: [] });
      return undefined;
    });
    const { container, root } = await renderApp('Run cockpit');
    await act(async () => { button(container, 'Cancel run').click(); });
    await waitFor(() => expectText(container, 'canceled'), container);

    expect(fetch.mock.calls.some(([url, init]) => new URL(String(url), 'http://switchyard.test').pathname === '/api/runs/run-1/cancel' && (init as RequestInit | undefined)?.method === 'POST')).toBe(true);
    expect(window.location.hash).toBe('#/overview/runs/run-1');
    await unmount(root);
  });

  it('resumes Open Pilot from the lifecycle action', async () => {
    window.location.hash = '#/runs/run-1';
    let resumed = false;
    const fetch = mockDashboardFetch((url, init) => {
      if (url.pathname === '/api/open-pilot/state/resume' && url.searchParams.get('repo') === 'OWNER/REPO' && url.searchParams.get('issue') === '123' && init.method === 'POST') {
        resumed = true;
        return jsonResponse({ run: { id: 'run-2' } });
      }
      if (resumed && (init.method ?? 'GET') === 'GET' && url.pathname === '/api/runs/run-2') return jsonResponse({ run: { id: 'run-2', workflow_id: 'open-pilot', trigger_id: 'trigger-1', status: 'running', inputs: { repo: 'OWNER/REPO', issue: '123' }, created_at: '2026-06-17T00:03:00.000Z', summary: 'Resumed Open Pilot run' }, steps: [], events: [], artifacts: [] });
      if (resumed && (init.method ?? 'GET') === 'GET' && url.pathname === '/api/open-pilot/state' && url.searchParams.get('repo') === 'OWNER/REPO' && url.searchParams.get('issue') === '123') return jsonResponse(openPilotStateResponse({ id: 'open-pilot-state-2', phase: 'running', gate_status: {} }, 'monitor'));
      return undefined;
    });
    const { container, root } = await renderApp('Run cockpit');
    await act(async () => { button(container, 'Resume Open Pilot').click(); });
    await waitFor(() => {
      expectText(container, 'Resumed Open Pilot run');
      expectText(container, 'Selected runrun-2');
      expectText(container, 'monitor');
    }, container);

    expect(fetch.mock.calls.some(([url, init]) => {
      const request = new URL(String(url), 'http://switchyard.test');
      return request.pathname === '/api/open-pilot/state/resume' && request.searchParams.get('repo') === 'OWNER/REPO' && request.searchParams.get('issue') === '123' && (init as RequestInit | undefined)?.method === 'POST';
    })).toBe(true);
    await unmount(root);
  });

  it('keeps the selected run visible when a mutation fails', async () => {
    window.location.hash = '#/runs/run-1';
    mockDashboardFetch((url, init) => url.pathname === '/api/runs/run-1/cancel' && init.method === 'POST' ? jsonResponse({}, { status: 503 }) : undefined);
    const { container, root } = await renderApp('Run cockpit');
    await act(async () => { button(container, 'Cancel run').click(); });
    await waitFor(() => expect(container.querySelector('[role="alert"]')?.textContent).toContain('Switchyard API returned 503'), container);

    expect(container.querySelector('[role="alert"]')?.textContent).toContain('Switchyard API returned 503');
    expect(container.textContent).toContain('Open Pilot run 1');
    expect(window.location.hash).toBe('#/runs/run-1');
    await unmount(root);
  });

  it('refreshes a running run live, keeps the evidence filter, and stops when the run finishes', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      window.location.hash = '#/runs/run-1';
      let detailLoads = 0;
      let finished = false;
      const fetch = mockDashboardFetch((url, init) => {
        if ((init.method ?? 'GET') === 'GET' && url.pathname === '/api/runs/run-1') {
          detailLoads += 1;
          if (finished) return jsonResponse({ run: { id: 'run-1', workflow_id: 'open-pilot', trigger_id: 'trigger-1', status: 'succeeded', inputs: { repo: 'OWNER/REPO', issue: '123' }, created_at: '2026-06-17T00:00:00.000Z', finished_at: '2026-06-17T00:05:00.000Z', summary: 'Open Pilot run 1' }, steps: [], events: [], artifacts: [] });
        }
        return undefined;
      });
      const { container, root } = await renderApp('Run cockpit');
      const indicator = () => container.querySelector('[data-live-status]') as HTMLElement | null;
      expect(indicator()?.dataset.liveStatus).toBe('live');
      expect(container.textContent).toContain('Live: every 3s');
      expect(container.textContent).toContain('Updated ');
      const initialLoads = detailLoads;

      const searchInput = container.querySelector('input[placeholder*="artifact"]') as HTMLInputElement;
      await act(async () => {
        Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set?.call(searchInput, 'trace.json');
        searchInput.dispatchEvent(new Event('input', { bubbles: true }));
      });

      await act(async () => { await vi.advanceTimersByTimeAsync(3_100); });
      expect(detailLoads).toBe(initialLoads + 1);
      expect(fetch.mock.calls.some(([url]) => new URL(String(url), 'http://switchyard.test').pathname === '/api/runs')).toBe(true);
      expect((container.querySelector('input[placeholder*="artifact"]') as HTMLInputElement).value).toBe('trace.json');
      expect(container.textContent).toContain('trace.json');
      expect(container.textContent).not.toContain('bundle.zip attached');

      finished = true;
      await act(async () => { await vi.advanceTimersByTimeAsync(3_100); });
      expect(detailLoads).toBe(initialLoads + 2);
      await waitFor(() => expectText(container, 'succeeded'), container);
      expect(indicator()?.dataset.liveStatus).toBe('idle');
      expect(container.textContent).toContain('run finished');

      await act(async () => { await vi.advanceTimersByTimeAsync(30_000); });
      expect(detailLoads).toBe(initialLoads + 2);
      expect(window.location.hash).toBe('#/runs/run-1');
      await unmount(root);
    } finally {
      vi.useRealTimers();
    }
  });
});
