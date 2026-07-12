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
      if (url.includes('/api/workflows')) return { workflows: [{ id: 'open-pilot', kind: 'open-pilot', name: 'Open Pilot', description: 'Open Pilot', owns: [], uses: [], status: 'seeded' }] };
      if (url.includes('/api/triggers')) return { triggers: [{ id: 'trigger-1', workflow_id: 'open-pilot', type: 'manual', name: 'Open Pilot trigger', description: 'Trigger for cockpit tests', enabled: true }] };
      if (url.endsWith('/api/runs')) {
        return {
          runs: [
            { id: 'run-1', workflow_id: 'open-pilot', trigger_id: 'trigger-1', status: 'running', inputs: { repo: 'OWNER/REPO', issue: '123' }, created_at: '2026-06-17T00:00:00.000Z', started_at: '2026-06-17T00:01:00.000Z', summary: 'Open Pilot run 1' },
          ],
        };
      }
      if (url.includes('/api/runs/run-1')) {
        return {
          run: { id: 'run-1', workflow_id: 'open-pilot', trigger_id: 'trigger-1', status: 'running', inputs: { repo: 'OWNER/REPO', issue: '123' }, created_at: '2026-06-17T00:00:00.000Z', started_at: '2026-06-17T00:01:00.000Z', summary: 'Open Pilot run 1' },
          steps: [
            { id: 'step-1', run_id: 'run-1', position: 1, name: 'plan', executor: 'internal', status: 'succeeded', attempt_count: 1, output_summary: 'Planning complete', correlation_ids: { trace_id: 'trace-1', model_broker_request_id: 'req-1' }, created_at: '2026-06-17T00:01:00.000Z', started_at: '2026-06-17T00:01:02.000Z', finished_at: '2026-06-17T00:01:05.000Z' },
            { id: 'step-2', run_id: 'run-1', position: 2, name: 'implement', executor: 'model', model: 'qwen3.5:latest', status: 'failed', attempt_count: 2, output_summary: 'Implementation stalled', error: 'model refused to continue', correlation_ids: { trace_id: 'trace-2', span_id: 'span-2', ocq_request_id: 'ocq-9' }, created_at: '2026-06-17T00:01:10.000Z', started_at: '2026-06-17T00:01:12.000Z', finished_at: '2026-06-17T00:01:20.000Z' },
          ],
          events: [
            { id: 'event-1', run_id: 'run-1', type: 'run.started', message: 'Run started for cockpit testing', created_at: '2026-06-17T00:01:00.000Z' },
            { id: 'event-2', run_id: 'run-1', type: 'artifact.created', message: 'artifact bundle.zip attached', created_at: '2026-06-17T00:01:30.000Z' },
          ],
          artifacts: [
            { id: 'artifact-1', run_id: 'run-1', name: 'bundle.zip', kind: 'artifact', uri: 'https://example.test/bundle.zip', created_at: '2026-06-17T00:01:30.000Z' },
            { id: 'artifact-2', run_id: 'run-1', name: 'trace.json', kind: 'evidence', uri: 'https://example.test/trace.json', created_at: '2026-06-17T00:01:40.000Z' },
          ],
        };
      }
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
      if (url.includes('/api/open-pilot/state?repo=OWNER%2FREPO&issue=123')) {
        return {
          state: {
            id: 'open-pilot-state-1',
            repo: 'OWNER/REPO',
            issue: '123',
            phase: 'paused',
            branch: 'open-pilot/run-1',
            base: 'main',
            worktree_path: '/tmp/open-pilot/run-1',
            pull_request_url: 'https://example.test/pr/1',
            merge_commit_sha: 'abc123',
            issue_closed: false,
            gate_status: { review: 'pass', tests: 'blocked' },
            repair_attempts: 1,
            max_repair_attempts: 3,
            last_error: 'Need another attempt',
            updated_at: '2026-06-17T00:02:00.000Z',
            created_at: '2026-06-17T00:01:00.000Z',
          },
          next_action: 'resume',
        };
      }
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
      await Promise.resolve();
    });

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

    await act(async () => {
      await Promise.resolve();
    });

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

    await act(async () => {
      await Promise.resolve();
    });

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

    await act(async () => {
      await Promise.resolve();
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

  it('filters steps and evidence in the cockpit', async () => {
    window.location.hash = '#/runs/run-1';
    mockDashboardFetch();
    const container = document.createElement('div');
    document.body.appendChild(container);

    const root = createRoot(container);

    await act(async () => {
      root.render(<App />);
    });

    const selects = Array.from(container.querySelectorAll('select')) as HTMLSelectElement[];
    expect(selects.length).toBeGreaterThanOrEqual(2);

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
      await Promise.resolve();
    });

    expect(container.textContent).toContain('trace.json');
    expect(container.textContent).not.toContain('bundle.zip attached');

    await act(async () => {
      root.unmount();
    });
  });
});
