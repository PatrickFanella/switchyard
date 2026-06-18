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

    expect(html).toContain('Queue cockpit');
    expect(html).toContain('Queues');
    expect(html).toContain('Recent requests');
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
});
