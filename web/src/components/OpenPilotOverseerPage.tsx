import { useCallback, useEffect, useState } from 'react';
import { APIError, arrayOrEmpty, fetchJSON, formatTime, messageFrom } from '../api';
import { loadControlAccess } from '../workflowDrafts';
import { HudEmptyState, HudErrorState, HudField, HudLoadingState, HudPanel, HudPanelStrong, HudSection } from './hud';

type OverseerConfig = {
  enabled: boolean;
  queue_enabled: boolean;
  recovery_enabled: boolean;
  max_open_lanes: number;
  interval_seconds: number;
};

export type OverseerState = {
  revision: number;
  config: OverseerConfig;
  next_check_at?: string;
  pending?: { id: string; kind: string; repo: string; issue: number; outcome: string };
  history: { id: string; kind: string; repo: string; issue: number; outcome: string; created_at: string }[];
  report: {
    checked_at?: string;
    complete: boolean;
    status: string;
    error?: string;
    available_lanes: number;
    lanes: { repo: string; issue: string; reasons: string[] }[];
    candidates: { repo: string; issue: number; title: string; blockers: string[] }[];
    incidents: { key: string; repo: string; issue: number; kind: string; message: string; observations: number }[];
  };
};

function displayTime(value?: string) {
  return value?.startsWith('0001') ? 'Not scheduled' : formatTime(value);
}

export function OpenPilotOverseerPage() {
  const [state, setState] = useState<OverseerState | null>(null);
  const [draft, setDraft] = useState<OverseerConfig | null>(null);
  const [canWrite, setCanWrite] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [reviewReason, setReviewReason] = useState('');

  const reload = useCallback(async () => {
    try {
      const [loaded, access] = await Promise.all([fetchJSON<OverseerState>('/api/open-pilot/overseer'), loadControlAccess()]);
      setState(loaded);
      setDraft(loaded.config);
      setCanWrite(access.can_write);
      setError('');
    } catch (err) { setError(messageFrom(err)); }
  }, []);

  useEffect(() => { void reload(); }, [reload]);

  async function action(check: boolean) {
    if (!state || !draft || !canWrite || busy) return;
    setBusy(true); setError(''); setNotice('');
    try {
      const updated = await fetchJSON<OverseerState>(check ? '/api/open-pilot/overseer/check' : '/api/open-pilot/overseer', {
        method: check ? 'POST' : 'PUT',
        body: check ? undefined : JSON.stringify({ expected_revision: state.revision, config: draft }),
      });
      setState(updated); setDraft(updated.config);
      setNotice(check ? 'Observation completed. Preview did not admit or recover work.' : 'Overseer configuration saved. Changes apply through the native scheduler.');
    } catch (err) {
      setError(err instanceof APIError && err.status === 409 ? 'The overseer state changed or another check is running. Reload before saving again.' : messageFrom(err));
      if (check && !(err instanceof APIError && err.status === 409)) {
        try {
          const observed = await fetchJSON<OverseerState>('/api/open-pilot/overseer');
          setState(observed); setDraft(observed.config);
          setError(observed.report.error || messageFrom(err));
        } catch { /* Keep the original transport error when readback is unavailable. */ }
      }
    } finally { setBusy(false); }
  }

  async function releaseReviewedReservation() {
    if (!state?.pending || !reviewReason.trim() || !canWrite || busy) return;
    setBusy(true); setError('');
    try {
      const updated = await fetchJSON<OverseerState>('/api/open-pilot/overseer/pending/release', { method: 'POST', body: JSON.stringify({ expected_revision: state.revision, action_id: state.pending.id, reason: reviewReason }) });
      setState(updated); setDraft(updated.config); setReviewReason(''); setNotice('Reviewed reservation released. Its action and consumed budget remain in history.');
    } catch (err) { setError(messageFrom(err)); } finally { setBusy(false); }
  }

  return (
    <HudSection kicker="Open Pilot" title="Native overseer" note="Switchyard watches issue intake and lifecycle progress, admits work into bounded lanes, and records recovery decisions. Configuration and action evidence persist across restarts.">
      {error ? <HudErrorState role="alert">{error}</HudErrorState> : null}
      {notice ? <p role="status" className="mt-3 text-sm text-[hsl(var(--confirm))]">{notice}</p> : null}
      {!state && !error ? <HudLoadingState>Loading overseer…</HudLoadingState> : null}
      {state && draft ? <>
        <div className="grid gap-3 md:grid-cols-4">
          <HudField label="Status" value={state.report.status || (state.config.enabled ? 'Waiting for check' : 'Disabled')} />
          <HudField label="Open lanes" value={`${arrayOrEmpty(state.report.lanes).length} / ${state.config.max_open_lanes}`} />
          <HudField label="Last check" value={displayTime(state.report.checked_at)} />
          <HudField label="Next check" value={state.config.enabled ? displayTime(state.next_check_at) : 'Disabled'} />
        </div>
        <HudPanelStrong className="mt-4 p-4">
          <h3 className="hud-label">Admission and recovery policy</h3>
          <p className="mt-2 text-sm text-[hsl(var(--muted))]">One lane per repository. Closed prerequisites come first, followed by priority, milestone, and oldest observed ready issue. Admission adds one label at a time and waits for native pickup.</p>
          {!canWrite ? <p className="mt-2 text-sm">Read-only access. An administrator can change this policy.</p> : null}
          <div className="mt-4 grid gap-3 sm:grid-cols-2">
            {(['enabled', 'queue_enabled', 'recovery_enabled'] as const).map((key) => <label key={key} className="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={draft[key]} disabled={!canWrite || busy} onChange={(event) => setDraft({ ...draft, [key]: event.target.checked })} />
              {{ enabled: 'Enable native overseer', queue_enabled: 'Admit ready issues', recovery_enabled: 'Recover confirmed transient failures' }[key]}
            </label>)}
            <label className="text-sm">Maximum open lanes
              <input aria-label="Maximum open lanes" className="ml-2 w-20 border border-[hsl(var(--border-faint))] bg-[hsl(var(--void))] p-2" type="number" min={1} max={100} value={draft.max_open_lanes} disabled={!canWrite || busy} onChange={(event) => setDraft({ ...draft, max_open_lanes: Number(event.target.value) })} />
            </label>
            <label className="text-sm">Check interval (minutes)
              <input aria-label="Check interval (minutes)" className="ml-2 w-20 border border-[hsl(var(--border-faint))] bg-[hsl(var(--void))] p-2" type="number" min={1} max={60} value={draft.interval_seconds / 60} disabled={!canWrite || busy} onChange={(event) => setDraft({ ...draft, interval_seconds: Number(event.target.value) * 60 })} />
            </label>
          </div>
          <div className="mt-4 flex flex-wrap gap-2">
            <button className="hud-chip px-3 py-2 disabled:opacity-50" type="button" disabled={!canWrite || busy} onClick={() => void action(false)}>Save policy</button>
            <button className="hud-chip px-3 py-2 disabled:opacity-50" type="button" disabled={!canWrite || busy} onClick={() => void action(true)}>Preview check</button>
            <button className="hud-chip px-3 py-2 disabled:opacity-50" type="button" disabled={busy} onClick={() => void reload()}>Reload</button>
          </div>
          <p className="mt-3 text-xs text-[hsl(var(--muted))]">Recovery requires two observations at least 15 minutes apart. Existing attempt limits, cancellations, review and verification gates remain in force. Global runtime changes require an operator.</p>
        </HudPanelStrong>
        {state.pending ? <HudPanelStrong className="mt-4 p-4"><HudErrorState>An outcome needs review: {state.pending.kind} for {state.pending.repo}#{state.pending.issue}. Its reservation remains held; verify the provider and run state before releasing it.</HudErrorState><label className="mt-3 block text-sm">Outcome review reason<input aria-label="Outcome review reason" value={reviewReason} maxLength={512} onChange={(event) => setReviewReason(event.target.value)} disabled={!canWrite || busy} className="mt-1 w-full border border-[hsl(var(--border-faint))] bg-[hsl(var(--void))] p-2" /></label><button className="hud-chip mt-3 px-3 py-2 disabled:opacity-50" type="button" disabled={!canWrite || busy || !reviewReason.trim()} onClick={() => void releaseReviewedReservation()}>Release reviewed reservation</button></HudPanelStrong> : null}
        {state.report.error ? <HudErrorState className="mt-4">{state.report.error}</HudErrorState> : null}
        {!state.report.complete ? <p className="mt-3 text-sm text-[hsl(var(--warn))]">Coverage is incomplete or no check has completed. An empty queue does not prove there is no work.</p> : null}
        <HudPanelStrong className="mt-4 p-4">
          <h3 className="hud-label">Occupied lanes</h3>
          {arrayOrEmpty(state.report.lanes).length === 0 ? <HudEmptyState>No occupied lanes in the last observation.</HudEmptyState> : <ul className="mt-3 grid gap-2">{arrayOrEmpty(state.report.lanes).map((lane) => <li key={`${lane.repo}#${lane.issue}`} className="text-sm">{lane.repo}#{lane.issue} · {lane.reasons.join(', ')}</li>)}</ul>}
        </HudPanelStrong>
        <HudPanelStrong className="mt-4 p-4">
          <h3 className="hud-label">Ordered ready issues</h3>
          {arrayOrEmpty(state.report.candidates).length === 0 ? <HudEmptyState>No ready candidates in the last observation.</HudEmptyState> : <ol className="mt-3 grid gap-2">{arrayOrEmpty(state.report.candidates).map((candidate) => <li key={`${candidate.repo}#${candidate.issue}`}><HudPanel className="p-3"><p className="text-sm font-semibold">{candidate.repo}#{candidate.issue} · {candidate.title}</p><p className="mt-1 text-xs text-[hsl(var(--muted))]">{candidate.blockers.length ? candidate.blockers.join('; ') : 'Eligible when a lane is available'}</p></HudPanel></li>)}</ol>}
        </HudPanelStrong>
        <HudPanelStrong className="mt-4 p-4">
          <h3 className="hud-label">Incidents and action history</h3>
          {arrayOrEmpty(state.report.incidents).map((incident) => <p className="mt-2 text-sm" key={incident.key}>{incident.repo}#{incident.issue} · {incident.message} · {incident.observations} observations</p>)}
          {arrayOrEmpty(state.history).length === 0 ? <HudEmptyState>No actions recorded.</HudEmptyState> : <ul className="mt-3 grid gap-2">{state.history.slice(-20).reverse().map((entry) => <li className="text-xs text-[hsl(var(--muted))]" key={entry.id}>{displayTime(entry.created_at)} · {entry.kind} {entry.repo ? `${entry.repo}#${entry.issue}` : ''} · {entry.outcome}</li>)}</ul>}
        </HudPanelStrong>
      </> : null}
    </HudSection>
  );
}
