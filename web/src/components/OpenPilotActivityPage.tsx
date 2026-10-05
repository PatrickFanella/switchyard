import { useCallback, useEffect, useMemo, useState } from 'react';
import { fetchJSON } from '../api';
import type { Run, RunDetail } from '../types';
import { useLiveRefresh } from '../hooks/useLiveRefresh';
import { LiveRefreshIndicator } from './LiveRefreshIndicator';
import { HudEmptyState, HudSection, statusClasses } from './hud';

export type AgentAction = { id: string; session_id: string; kind: string; title: string; status?: string; time?: { created?: number; ran?: number; completed?: number }; details: Record<string, unknown> };
export type ActivityResponse = { run_id: string; generated_at: string; detail: RunDetail; activity: { actions: AgentAction[]; warnings: string[]; truncated: boolean } };
type Entry = { id: string; kind: string; title: string; status?: string; stamp: number; details: unknown; session?: string };

function entriesFor(response: ActivityResponse): Entry[] {
  const detail = response.detail;
  return [
    ...(detail.events ?? []).map(event => ({ id: `event:${event.id}`, kind: 'lifecycle', title: event.message || event.type, stamp: Date.parse(event.created_at), details: event })),
    ...(detail.steps ?? []).map(step => ({ id: `step:${step.id}`, kind: 'step', title: step.name, status: step.status, stamp: Date.parse(step.started_at || step.created_at), details: step })),
    ...response.activity.actions.map(action => ({ id: `${action.session_id}:${action.id}`, kind: action.kind, title: action.title, status: action.status, stamp: action.time?.created ?? 0, details: action.details, session: action.session_id })),
  ].sort((a, b) => b.stamp - a.stamp || a.id.localeCompare(b.id));
}

function JsonDetail({ value }: { value: unknown }) {
  return <pre className="activity-json">{JSON.stringify(value, null, 2)}</pre>;
}

function actionPreview(details: unknown): string {
  if (!details || typeof details !== 'object') return '';
  const value = details as Record<string, unknown>;
  const state = value.state as Record<string, unknown> | undefined;
  const input = state?.input;
  if (value.text) return String(value.text);
  if (typeof input === 'string') return input;
  if (input && typeof input === 'object') return JSON.stringify(input);
  return String(value.output_summary || value.error || value.message || value.command || '');
}

function ActionRow({ entry, expanded }: { entry: Entry; expanded: boolean }) {
  const [open, setOpen] = useState(expanded);
  const preview = actionPreview(entry.details);
  useEffect(() => setOpen(expanded), [expanded]);
  return <details className="activity-row" open={open} onToggle={event => setOpen(event.currentTarget.open)}>
    <summary>
      <time dateTime={entry.stamp ? new Date(entry.stamp).toISOString() : undefined}>{entry.stamp ? new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit' }).format(entry.stamp) : 'Time not recorded'}</time>
      <span className="activity-kind">{entry.kind}</span>
      <strong>{entry.title}</strong>
      {entry.status && <span className={`hud-chip ${statusClasses(entry.status === 'error' ? 'failed' : entry.status === 'completed' ? 'succeeded' : entry.status === 'streaming' ? 'running' : entry.status)}`}>{entry.status}</span>}
      {preview && <span className="activity-preview">{preview.slice(0, 240)}{preview.length > 240 ? '…' : ''}</span>}
    </summary>
    <div className="activity-details">
      <p className="activity-identity">{entry.id}{entry.session ? ` · Session ${entry.session}` : ''}</p>
      <JsonDetail value={entry.details} />
    </div>
  </details>;
}

export function OpenPilotActivityPage({ runID = '' }: { runID?: string }) {
  const [responses, setResponses] = useState<ActivityResponse[]>([]);
  const [paused, setPaused] = useState(false);
  const [expanded, setExpanded] = useState(false);
  const [query, setQuery] = useState('');
  const [kind, setKind] = useState('all');
  const [errors, setErrors] = useState<string[]>([]);
  const load = useCallback(async (ticket: { isCurrent: () => boolean }) => {
    let ids = runID ? [runID] : [];
    if (!runID) {
      const page = await fetchJSON<{ runs: Run[] }>('/api/runs?limit=100');
      const runs = page.runs.filter(run => run.workflow_id === 'open-pilot');
      // Active work comes first; recently finished work stays visible after settlement.
      ids = [...runs.filter(run => run.status === 'running' || run.status === 'queued'), ...runs.filter(run => run.status !== 'running' && run.status !== 'queued')].slice(0, 6).map(run => run.id);
    }
    const results = await Promise.allSettled(ids.map(id => fetchJSON<ActivityResponse>(`/api/runs/${encodeURIComponent(id)}/activity`)));
    if (!ticket.isCurrent()) return;
    const failures: string[] = [];
    const next: ActivityResponse[] = [];
    results.forEach((result, index) => {
      if (result.status === 'fulfilled') next.push(result.value);
      else failures.push(`${ids[index]}: ${String(result.reason)}`);
    });
    setErrors(failures);
    // Keep the last good evidence on transport failures; never turn failure into an empty feed.
    setResponses(previous => ids.flatMap(id => next.find(item => item.run_id === id) ?? previous.find(item => item.run_id === id) ?? []));
    if (failures.length) throw new Error(failures.join('; '));
  }, [runID]);
  const live = useLiveRefresh({ key: `activity:${runID}`, mode: paused ? 'idle' : 'active', load, activeInterval: 3000 });
  const { refreshNow } = live;
  useEffect(() => { setResponses([]); setErrors([]); void refreshNow(); }, [runID, refreshNow]);
  const prepared = useMemo(() => responses.map(response => ({ response, entries: entriesFor(response).filter(entry => (kind === 'all' || entry.kind === kind) && `${entry.title} ${entry.session ?? ''} ${JSON.stringify(entry.details)}`.toLowerCase().includes(query.toLowerCase())) })), [responses, kind, query]);

  return <HudSection kicker="Open Pilot" title="Live activity" note={runID ? 'Agent tools, responses, step execution, and runtime events for this run.' : 'Follow active Open Pilot work and the latest completed runs. New runs appear automatically.'}>
    <div className="activity-toolbar">
      {paused ? <span className="hud-chip">Live feed paused</span> : <LiveRefreshIndicator live={live} />}
      <button className="hud-chip" type="button" onClick={() => setPaused(!paused)}>{paused ? 'Resume live feed' : 'Pause live feed'}</button>
      <button className="hud-chip" type="button" onClick={() => void refreshNow()}>Refresh activity</button>
      <button className="hud-chip" type="button" onClick={() => setExpanded(!expanded)}>{expanded ? 'Collapse details' : 'Expand all details'}</button>
      <label>Action type <select value={kind} onChange={event => setKind(event.target.value)}>{['all', 'tool', 'text', 'step', 'lifecycle', 'user', 'shell', 'idle'].map(value => <option key={value} value={value}>{value === 'all' ? 'All actions' : value}</option>)}</select></label>
      <label className="activity-search">Search actions <input value={query} onChange={event => setQuery(event.target.value)} placeholder="Tool, file, output, error, or ID" /></label>
    </div>
    <p className="activity-note">Refreshes every 3 seconds while visible. Each action opens its recorded arguments, output, metadata, and error. Credentials are redacted.</p>
    {!runID && <p className="activity-note">Up to six runs from the latest 100 runtime runs; active work is shown first. Open any run for its dedicated feed.</p>}
    {errors.map(error => <p role="alert" key={error} className="text-[hsl(var(--danger))]">Activity update failed; showing retained evidence. {error}</p>)}
    {!responses.length && <HudEmptyState>{live.inFlight ? 'Loading Open Pilot activity…' : 'No Open Pilot runs are available in this window.'}</HudEmptyState>}
    {prepared.map(({ response, entries }) => <section className="activity-run" key={response.run_id} aria-label={`Activity for ${response.run_id}`}>
      <header className="activity-run-header">
        <div><a href={`#/runs/${encodeURIComponent(response.run_id)}`}>{response.detail.run.inputs?.repo ?? response.detail.run.workflow_id} {response.detail.run.inputs?.issue ? `#${response.detail.run.inputs.issue}` : ''}</a><p>{response.run_id}</p></div>
        <span className={`hud-chip ${statusClasses(response.detail.run.status)}`}>{response.detail.run.status}</span>
      </header>
      {response.detail.steps.filter(step => step.status === 'running').map(step => <p className="activity-current" key={step.id}><strong>Current action:</strong> {step.name} · {step.executor}{step.model ? ` · ${step.model}` : ''}</p>)}
      <details className="activity-context"><summary>Run context, inputs, outputs, and artifacts</summary><JsonDetail value={response.detail} /></details>
      {response.activity.warnings.map(warning => <p className="activity-warning" key={warning}>{warning}</p>)}
      {!response.activity.actions.length && <p className="activity-note">No live agent messages are available yet. Recorded steps and runtime events remain below.</p>}
      {entries.length ? entries.map(entry => <ActionRow key={entry.id} entry={entry} expanded={expanded} />) : <HudEmptyState>No actions match these filters.</HudEmptyState>}
    </section>)}
  </HudSection>;
}
