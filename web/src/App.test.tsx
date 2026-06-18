// @vitest-environment jsdom

import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { renderToString } from 'react-dom/server';
import { afterEach, describe, expect, it, vi } from 'vitest';
import App, { openPilotStateFields } from './App';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

afterEach(() => {
  window.location.hash = '';
  document.body.innerHTML = '';
  vi.unstubAllGlobals();
});

function mockDashboardFetch() {
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    const payload = (() => {
      if (url.includes('/api/workflow-definitions')) return { workflow_definitions: [] };
      if (url.includes('/api/workflows')) return { workflows: [] };
      if (url.includes('/api/triggers')) return { triggers: [] };
      if (url.includes('/api/runs')) return { runs: [] };
      if (url.includes('/api/adapters')) {
        return {
          queues: [],
          sessions: [],
          logs: [],
          prompts: [],
          config: { environment: 'test', version: 'test', base_url: '', features: [], limits: {} },
          agents: [],
          skills: [],
          health: { status: 'ok', services: [], checked_at: '2026-06-17T00:00:00.000Z' },
          llama_line: { requests: [], stats: { active_requests: 0, queued_requests: 0, avg_latency_ms: 0, success_rate: 1 }, search: [], upstreams: [] },
          ocq: { requests: [], logs: [], sessions: [], settings: { workspace: 'test', provider: 'test', retention_days: 0, cors_allowed: [] } },
          blacktower: { tasks: [], reports: [], scheduler_health: { status: 'ok', next_run_at: '', last_tick_at: '', skew_seconds: 0 } },
          changemaker: { posts: [], service: { name: 'test', status: 'ok', version: 'test', healthy: true, updated_at: '2026-06-17T00:00:00.000Z' }, run_control: { enabled: false, mode: 'manual', queued_runs: 0 } },
        };
      }
      if (url.includes('/api/scheduler')) return { enabled: false, running: false, interval: 'disabled' };
      if (url.includes('/api/workers')) return { enabled: false, running: false, workers: 0, active: 0 };
      if (url.includes('/api/history/req-ok')) {
        return { id: 'req-ok', status: 'completed', model: 'qwen3.5:latest', path: '/api/generate', client: 'test', created_at: '2026-06-17T00:00:00.000Z', updated_at: '2026-06-17T00:00:01.000Z', started_at: '2026-06-17T00:00:00.000Z', finished_at: '2026-06-17T00:00:01.000Z', correlation_ids: { trace_id: 'abc' } };
      }
      if (url.includes('/api/history')) {
        return {
          requests: [
            { id: 'req-ok', status: 'completed', model: 'qwen3.5:latest', path: '/api/generate', client: 'test', created_at: '2026-06-17T00:00:00.000Z', updated_at: '2026-06-17T00:00:01.000Z', started_at: '2026-06-17T00:00:00.000Z', finished_at: '2026-06-17T00:00:01.000Z', correlation_ids: { trace_id: 'abc' } },
            { id: 'req-fail', status: 'failed', model: 'qwen3.5:latest', path: '/api/chat', client: 'test', error: 'upstream unavailable', created_at: '2026-06-17T00:00:00.000Z', updated_at: '2026-06-17T00:00:01.000Z' },
          ],
        };
      }
      if (url.includes('/api/observability/model-broker')) {
        return {
          status: { status: 'healthy', configured: true, backend: 'switchyard-model-broker', queue_depth: 0, active_request: false, upstreams: [{ name: 'ollama', url: 'http://ollama', status: 'healthy' }] },
          totals: { completed: 1, failed: 0 },
          recent_requests: [{ id: 'req-1', status: 'completed', model: 'qwen3.5:latest', path: '/api/generate', client: 'test', duration_ms: 42, correlation_ids: { trace_id: 'abc' }, created_at: '2026-06-17T00:00:00.000Z', updated_at: '2026-06-17T00:00:01.000Z' }],
          recent_errors: [],
          latency_ms: { count: 1, avg: 42, max: 42 },
          checked_at: '2026-06-17T00:00:01.000Z',
        };
      }
      return {};
    })();

    return {
      ok: true,
      json: async () => payload,
    } as Response;
  }));
}

describe('App', () => {
  it('renders the overview scan bands', () => {
    window.location.hash = '#/overview';

    const html = renderToString(<App />);

    expect(html).toContain('Switchyard control-plane suite');
    expect(html).toContain('Command strip');
    expect(html).toContain('Active work');
    expect(html).toContain('Selected run');
    expect(html).toContain('Evidence');
    expect(html).toContain('Run posture');
    expect(html).toContain('UI → Switchyard → ModelBroker → OpenCode executor → Blacktower');
    expect(openPilotStateFields).toContain('Next action');
  });

  it('honors hash navigation on initial render', () => {
    window.location.hash = '#/queues';

    const html = renderToString(<App />);

    expect(html).toContain('Requests cockpit');
    expect(html).toContain('Queues');
  });

  it('falls back safely for malformed run deep links', () => {
    window.location.hash = '#/overview/runs/%E0%A4%A';

    const html = renderToString(<App />);

    expect(html).toContain('Command strip');
    expect(html).toContain('Selected run');
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
});
