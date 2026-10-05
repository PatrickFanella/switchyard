// @vitest-environment jsdom
import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { OpenPilotActivityPage, type ActivityResponse } from './OpenPilotActivityPage';
import { fetchJSON } from '../api';
import type { RunDetail } from '../types';
vi.mock('../api', () => ({ fetchJSON: vi.fn() }));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root; let host: HTMLDivElement;
const detail = { run: { id: 'run-1', workflow_id: 'open-pilot', status: 'running', inputs: { repo: 'acme/demo', issue: '7' } }, steps: [{ id: 'step-1', name: 'coding', status: 'running', created_at: '2026-10-04T12:00:00Z', inputs: { prompt: 'Fix the issue' }, outputs: { log: 'retained output' } }], events: [{ id: 'event-1', message: 'Step started', created_at: '2026-10-04T12:00:00Z', type: 'step-started' }], artifacts: [] } as unknown as RunDetail;
function snapshot(status = 'running'): ActivityResponse { return { run_id: 'run-1', generated_at: '', detail, activity: { actions: [{ id: 'tool-1', session_id: 'ses_owned', kind: 'tool', title: 'read', status, time: { created: Date.parse('2026-10-04T12:00:01Z') }, details: { state: { status, input: { path: 'src/main.go' }, content: [{ type: 'text', text: 'Full tool output <script>unsafe()</script>' }] } } }], warnings: [], truncated: false } }; }
async function render(runID?: string) { await act(async () => root.render(<OpenPilotActivityPage runID={runID} />)); }
async function click(text: string) { const button = [...host.querySelectorAll('button')].find(node => node.textContent === text); expect(button).toBeDefined(); await act(async () => button!.click()); }
beforeEach(() => { vi.useFakeTimers(); host = document.createElement('div'); document.body.append(host); root = createRoot(host); vi.mocked(fetchJSON).mockReset(); });
afterEach(async () => { await act(async () => root.unmount()); host.remove(); vi.useRealTimers(); });
describe('Open Pilot live activity', () => {
 it('updates stable tool rows, preserves opened details, pauses, and exposes recorded output as text', async () => {
  let status = 'running'; vi.mocked(fetchJSON).mockImplementation(async () => snapshot(status));
  await render('run-1'); expect(host.textContent).toContain('src/main.go');
  await click('Expand all details'); const row = host.querySelector<HTMLDetailsElement>('.activity-row')!; expect(row.open).toBe(true);
  status = 'completed'; await act(async () => vi.advanceTimersByTimeAsync(3000));
  expect(host.querySelector('.activity-row')).toBe(row); expect(row.open).toBe(true); expect(row.textContent).toContain('completed'); expect(row.textContent).toContain('Full tool output'); expect(host.querySelector('script')).toBeNull();
  await click('Pause live feed'); const count = vi.mocked(fetchJSON).mock.calls.length; await act(async () => vi.advanceTimersByTimeAsync(10000)); expect(vi.mocked(fetchJSON).mock.calls.length).toBe(count);
  await click('Refresh activity'); expect(vi.mocked(fetchJSON).mock.calls.length).toBe(count + 1);
 });
 it('discovers newly active runs and retains successful evidence on failed refresh', async () => {
  let fail = false;
  vi.mocked(fetchJSON).mockImplementation(async (path) => {
   if (path === '/api/runs?limit=100') return { runs: [{ ...detail.run, id: 'run-1' }] };
   if (fail) throw new Error('transport unavailable');
   return snapshot();
  });
  await render(); expect(host.textContent).toContain('acme/demo'); expect(vi.mocked(fetchJSON).mock.calls[1][0]).toBe('/api/runs/run-1/activity');
  fail = true; await act(async () => vi.advanceTimersByTimeAsync(3000)); expect(host.textContent).toContain('showing retained evidence'); expect(host.textContent).toContain('src/main.go');
 });
 it('shows retained lifecycle evidence and explains missing executor messages', async () => {
  const response = snapshot(); response.activity.actions = []; response.activity.warnings = ['Executor generation is unavailable'];
  vi.mocked(fetchJSON).mockResolvedValue(response); await render('run-1'); expect(host.textContent).toContain('Executor generation is unavailable'); expect(host.textContent).toContain('Step started'); expect(host.textContent).toContain('retained output');
 });
});
