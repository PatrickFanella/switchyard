import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { vi } from 'vitest';
import App from './App';
import type { OpenPilotIssueState, OpenPilotStateResponse } from './types';

type FetchHandler = (url: URL, init: RequestInit) => Response | undefined;

const run = { id: 'run-1', workflow_id: 'open-pilot', trigger_id: 'trigger-1', status: 'running', inputs: { repo: 'OWNER/REPO', issue: '123' }, created_at: '2026-06-17T00:00:00.000Z', started_at: '2026-06-17T00:01:00.000Z', summary: 'Open Pilot run 1' };
const runDetail = { run, steps: [{ id: 'step-1', run_id: 'run-1', position: 1, name: 'plan', executor: 'internal', status: 'succeeded', attempt_count: 1, output_summary: 'Planning complete', correlation_ids: { trace_id: 'trace-1', model_broker_request_id: 'req-1' }, created_at: '2026-06-17T00:01:00.000Z', started_at: '2026-06-17T00:01:02.000Z', finished_at: '2026-06-17T00:01:05.000Z' }, { id: 'step-2', run_id: 'run-1', position: 2, name: 'implement', executor: 'model', model: 'qwen3.5:latest', status: 'failed', attempt_count: 2, output_summary: 'Implementation stalled', error: 'model refused to continue', correlation_ids: { trace_id: 'trace-2', span_id: 'span-2', ocq_request_id: 'ocq-9' }, created_at: '2026-06-17T00:01:10.000Z', started_at: '2026-06-17T00:01:12.000Z', finished_at: '2026-06-17T00:01:20.000Z' }], events: [{ id: 'event-1', run_id: 'run-1', type: 'run.started', message: 'Run started for cockpit testing', created_at: '2026-06-17T00:01:00.000Z' }, { id: 'event-2', run_id: 'run-1', type: 'artifact.created', message: 'artifact bundle.zip attached', created_at: '2026-06-17T00:01:30.000Z' }], artifacts: [{ id: 'artifact-1', run_id: 'run-1', name: 'bundle.zip', kind: 'artifact', uri: 'https://example.test/bundle.zip', created_at: '2026-06-17T00:01:30.000Z' }, { id: 'artifact-2', run_id: 'run-1', name: 'trace.json', kind: 'evidence', uri: 'https://example.test/trace.json', created_at: '2026-06-17T00:01:40.000Z' }] };
const adapters = { queues: [], sessions: [], logs: [], prompts: [], config: { environment: 'test', version: 'test', base_url: '', features: [], limits: {} }, agents: [], skills: [], health: { status: 'ok', services: [], checked_at: '2026-06-17T00:00:00.000Z' }, llama_line: { requests: [], stats: { active_requests: 0, queued_requests: 0, avg_latency_ms: 0, success_rate: 1 }, search: [], upstreams: [] }, ocq: { requests: [], logs: [], sessions: [], settings: { workspace: 'test', provider: 'test', retention_days: 0, cors_allowed: [] } }, blacktower: { tasks: [], reports: [], scheduler_health: { status: 'ok', next_run_at: '', last_tick_at: '', skew_seconds: 0 } }, changemaker: { posts: [], service: { name: 'test', status: 'ok', version: 'test', healthy: true, updated_at: '2026-06-17T00:00:00.000Z' }, run_control: { enabled: false, mode: 'manual', queued_runs: 0 } } };
const operatorSummary = {
  schema_version: '1.0', service: { id: 'switchyard', name: 'Switchyard' }, deployment: { source_revision: '0123456789abcdef0123456789abcdef01234567', deployment_id: 'production-a' }, status: { level: 'healthy', summary: 'Runtime operating normally' }, generated_at: '2026-06-17T00:02:00.000Z', observed_at: '2026-06-17T00:02:00.000Z', partial: false, alerts: [], capabilities: { details: true, actions: [] },
  data: { scheduler: { state: 'disabled', enabled: false }, workers: { configured: 0, active: 0, busy: 0, draining: 0 }, runs: { queued: 0, running: 1, failed_24h: 1, succeeded_24h: 2, recent: [{ id: 'run-1', workflow_id: 'open-pilot', workflow_name: 'Open Pilot', state: 'running', started_at: '2026-06-17T00:01:00.000Z', summary: 'Open Pilot run 1' }] }, components: [{ id: 'model-broker', name: 'Model broker', status: 'healthy', summary: 'Accepting requests' }] },
};
const agentProfiles = { profiles: [{ id: 'open-pilot-executor', name: 'Open Pilot Executor', role: 'executor', default_model: 'gpt-5.4', prompt_contract: 'Implement the smallest safe change.', allowed_executors: ['opencode', 'command', 'git', 'gitea'] }] };
const historyRequest = { id: 'req-ok', status: 'completed', model: 'qwen3.5:latest', path: '/api/generate', client: 'test', created_at: '2026-06-17T00:00:00.000Z', updated_at: '2026-06-17T00:00:01.000Z', started_at: '2026-06-17T00:00:00.000Z', finished_at: '2026-06-17T00:00:01.000Z', correlation_ids: { trace_id: 'abc' } };
const openPilotState = {
  state: {
    id: 'open-pilot-state-1', repo: 'OWNER/REPO', issue: '123', phase: 'paused', branch: 'open-pilot/run-1', base: 'main', worktree_path: '/tmp/open-pilot/run-1', pull_request_url: 'https://example.test/pr/1', merge_commit_sha: 'abc123', issue_closed: false, gate_status: { review: 'pass', tests: 'blocked' }, repair_attempts: 1, max_repair_attempts: 3, last_error: 'Need another attempt', updated_at: '2026-06-17T00:02:00.000Z', created_at: '2026-06-17T00:01:00.000Z',
  },
  next_action: 'resume',
} satisfies OpenPilotStateResponse;

export function jsonResponse<T>(payload: T, init?: ResponseInit) {
  return new Response(JSON.stringify(payload), init);
}

export function openPilotStateResponse(state: Partial<OpenPilotIssueState> = {}, nextAction = 'resume'): OpenPilotStateResponse {
  return { state: { ...openPilotState.state, ...state }, next_action: nextAction };
}

export function mockDashboardFetch(handler?: FetchHandler) {
  const fetch = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = new URL(String(input), 'http://switchyard.test');
    const custom = handler?.(url, init);
    if (custom) return custom;
    const method = init.method ?? 'GET';
    let payload: unknown;
    if (method === 'GET' && url.pathname === '/api/workflow-definitions') payload = { workflow_definitions: [] };
    else if (method === 'GET' && url.pathname === '/api/workflows') payload = { workflows: [{ id: 'open-pilot', kind: 'open-pilot', name: 'Open Pilot', description: 'Open Pilot', owns: [], uses: [], status: 'seeded' }] };
    else if (method === 'GET' && url.pathname === '/api/triggers') payload = { triggers: [{ id: 'trigger-1', workflow_id: 'open-pilot', type: 'manual', name: 'Open Pilot trigger', description: 'Trigger for cockpit tests', enabled: true }] };
    else if (method === 'GET' && url.pathname === '/api/runs') payload = { runs: [run] };
    else if (method === 'GET' && url.pathname === '/api/runs/run-1') payload = runDetail;
    else if (method === 'GET' && url.pathname === '/api/adapters') payload = adapters;
    else if (method === 'GET' && url.pathname === '/api/scheduler') payload = { enabled: false, running: false, interval: 'disabled' };
    else if (method === 'GET' && url.pathname === '/api/workers') payload = { enabled: false, running: false, workers: 0, active: 0 };
    else if (method === 'GET' && url.pathname === '/api/operator-summary') payload = operatorSummary;
    else if (method === 'GET' && url.pathname === '/api/agent-profiles') payload = agentProfiles;
    else if (method === 'GET' && url.pathname === '/api/open-pilot/state' && url.searchParams.get('repo') === 'OWNER/REPO' && url.searchParams.get('issue') === '123') payload = openPilotState;
    else if (method === 'GET' && url.pathname === '/api/history/req-ok') payload = historyRequest;
    else if (method === 'GET' && url.pathname === '/api/history') payload = { requests: [historyRequest, { id: 'req-fail', status: 'failed', model: 'qwen3.5:latest', path: '/api/chat', client: 'test', error: 'upstream unavailable', created_at: '2026-06-17T00:00:00.000Z', updated_at: '2026-06-17T00:00:01.000Z' }] };
    else if (method === 'GET' && url.pathname === '/api/observability/model-broker') payload = { status: { status: 'healthy', configured: true, backend: 'switchyard-model-broker', queue_depth: 0, active_request: false, upstreams: [{ name: 'ollama', url: 'http://ollama', status: 'healthy' }] }, totals: { completed: 1, failed: 0 }, recent_requests: [{ ...historyRequest, id: 'req-1', duration_ms: 42 }], recent_errors: [], latency_ms: { count: 1, avg: 42, max: 42 }, checked_at: '2026-06-17T00:00:01.000Z' };
    else throw new Error(`Unexpected fetch: ${method} ${url.pathname}${url.search}`);
    return jsonResponse(payload);
  });
  vi.stubGlobal('fetch', fetch);
  return fetch;
}

export async function waitFor(assertion: () => void, container: HTMLElement = document.body) {
  await new Promise<void>((resolve, reject) => {
    const check = () => { try { assertion(); observer.disconnect(); clearTimeout(timeout); resolve(); } catch { /* wait for an observable DOM update */ } };
    const observer = new MutationObserver(check);
    const timeout = setTimeout(() => { observer.disconnect(); try { assertion(); resolve(); } catch (error) { reject(error); } }, 1_000);
    observer.observe(container, { childList: true, subtree: true, characterData: true });
    check();
  });
}

export async function renderApp(readyText = 'Open Pilot trigger') {
  const container = document.createElement('div');
  document.body.appendChild(container);
  const root = createRoot(container);
  await act(async () => { root.render(<App />); });
  await waitFor(() => expectText(container, readyText), container);
  return { container, root };
}

export function expectText(container: HTMLElement, text: string) {
  if (!container.textContent?.includes(text)) throw new Error(`text ${text} not found`);
}

export function button(container: HTMLElement, name: string) {
  const element = Array.from(container.querySelectorAll('button')).find((item) => item.textContent?.trim() === name);
  if (!element) throw new Error(`button ${name} not found`);
  return element;
}

export function controlByLabel(container: HTMLElement, label: string) {
  const element = Array.from(container.querySelectorAll('label')).find((item) => item.textContent?.includes(label))?.control;
  if (!element) throw new Error(`control labelled ${label} not found`);
  return element as HTMLInputElement | HTMLSelectElement;
}

export async function unmount(root: Root) {
  await act(async () => { root.unmount(); });
}
