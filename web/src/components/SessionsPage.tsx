import { useCallback, useEffect, useState } from 'react';
import { loadExecutorSessions, messageFrom } from '../api';
import { correlationKindLabels, formatEvidenceHash, resolveCorrelation } from '../correlation';
import { formatHash } from '../hooks/useHashNavigation';
import { useLiveRefresh, type LiveRefreshTicket } from '../hooks/useLiveRefresh';
import type { ExecutorAvailability, ExecutorSession, ExecutorSessionsResponse, ExecutorSessionStatus } from '../types';
import { CopyValueButton, CorrelationLink } from './CorrelationLinks';
import { formatTime, HudChip, HudEmptyState, HudErrorState, HudField, HudLabel, HudLoadingState, HudPanel, HudPanelStrong, HudSection, HudTag, statusClasses } from './hud';
import { LiveRefreshIndicator } from './LiveRefreshIndicator';
import { formatSessionHash, parseSessionHash } from './sessionLinks';

export const sessionsRefreshInterval = 15_000;

const sessionStatusLabel: Record<ExecutorSessionStatus, string> = {
  active: 'active',
  stale: 'stale',
  ended: 'ended',
  historical_compatibility: 'historical compatibility',
};

const sessionStatusTone: Record<ExecutorSessionStatus, string> = {
  active: statusClasses('running'),
  stale: statusClasses('timed_out'),
  ended: statusClasses('canceled'),
  historical_compatibility: statusClasses('seeded'),
};

const presenceLabel: Record<ExecutorSession['presence'], string> = {
  present: 'Executor holds session',
  missing: 'Executor lost session',
  unknown: 'Executor presence unknown',
  not_checked: 'Executor presence not checked',
};

const availabilityText: Record<ExecutorSessionsResponse['executor_availability'], string> = {
  available: 'All configured executors answered their status check.',
  partial: 'Some executors did not answer. Sessions on those executors show unknown presence.',
  unavailable: 'No configured executor answered its status check. Session presence cannot be confirmed.',
  not_configured: 'No OpenCode executor is configured.',
};

function useSessionSelection() {
  const [selected, setSelected] = useState(() => parseSessionHash(typeof window === 'undefined' ? '' : window.location.hash) ?? '');
  useEffect(() => {
    if (typeof window === 'undefined') return;
    const sync = () => {
      const parsed = parseSessionHash(window.location.hash);
      if (parsed !== null) setSelected(parsed);
    };
    window.addEventListener('hashchange', sync);
    return () => window.removeEventListener('hashchange', sync);
  }, []);
  return selected;
}

function ExecutorList({ executors }: { executors: ExecutorAvailability[] }) {
  if (executors.length === 0) {
    return <p className="mt-3 text-sm text-[hsl(var(--muted))]">No executor status was returned.</p>;
  }
  return (
    <ul className="mt-3 grid gap-2 md:grid-cols-2 xl:grid-cols-3" aria-label="Executors">
      {executors.map((executor) => (
        <li key={executor.key} className="hud-panel flex flex-wrap items-center justify-between gap-2 bg-[hsl(var(--panel-soft))] px-3 py-2" data-executor-key={executor.key}>
          <span className="break-all text-sm text-[hsl(var(--ink))]">
            {executor.scope === 'default' ? 'Default executor' : executor.repository_slug || executor.key}
            <span className="block text-[0.62rem] uppercase tracking-[0.18em] text-[hsl(var(--faint))]">
              {executor.key} · {executor.compatibility ? 'compatibility backend' : 'native'}
            </span>
          </span>
          <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(executor.available ? 'succeeded' : executor.status === 'not_configured' ? 'canceled' : 'failed')}`}>{executor.status}</span>
        </li>
      ))}
    </ul>
  );
}

function SessionCard({ session, focused }: { session: ExecutorSession; focused: boolean }) {
  const repository = session.repository.repo || session.repository.repository_id;
  return (
    <HudPanel className={`bg-[hsl(var(--panel-strong))] p-4 ${focused ? 'outline outline-2 outline-offset-2 outline-[hsl(var(--signal))]' : ''}`.trim()}>
      <div data-session-id={session.session_id} data-session-status={session.status}>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <HudLabel className="text-[hsl(var(--faint))]">
              {session.origin === 'compatibility' ? 'Historical compatibility record' : 'Native OpenCode session'} · {session.correlation_key}
            </HudLabel>
            <div className="mt-2 flex flex-wrap items-center gap-2">
              <a className="break-all text-lg font-semibold text-[hsl(var(--signal))] underline decoration-dotted underline-offset-4" data-correlation-kind="session" href={formatSessionHash(session.session_id)}>
                {session.session_id}
              </a>
              <CopyValueButton label={`session ID ${session.session_id}`} value={session.session_id} />
            </div>
          </div>
          <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${sessionStatusTone[session.status]}`}>{sessionStatusLabel[session.status]}</span>
        </div>
        <p className="mt-2 text-sm text-[hsl(var(--muted))]">{session.status_reason}</p>

        <div className="mt-4 grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
          <HudField label="Repository" value={repository ? `${repository}${session.repository.executor_generation ? ` · ${session.repository.executor_generation}` : ''}` : 'Default executor'} />
          <HudField label="Executor" value={`${session.executor_key} · ${session.executor_status}`} />
          <HudField label="Presence" value={`${presenceLabel[session.presence]}${session.executor_activity ? ` (${session.executor_activity})` : ''}`} />
          <HudField label="Last observed" value={`${formatTime(session.last_observed_at)} · ${session.last_observed_source === 'executor' ? 'executor' : 'run step'}`} />
        </div>
        <p className="mt-2 text-xs text-[hsl(var(--muted))]">{session.presence_reason} · first observed {formatTime(session.first_observed_at)}</p>

        <div className="mt-4 grid gap-4 lg:grid-cols-2">
          <div>
            <HudLabel className="text-[hsl(var(--faint))]">Related runs</HudLabel>
            <ul className="mt-2 grid gap-2">
              {session.runs.map((run) => (
                <li key={`${run.run_id}-${run.step_id}`} className="flex flex-wrap items-center gap-2 text-sm">
                  <a className="break-all text-[hsl(var(--signal))] underline decoration-dotted underline-offset-4" data-correlation-kind="run" href={formatHash('runs', run.run_id)}>
                    {run.run_id}
                  </a>
                  <span className={`hud-chip px-2 py-0.5 text-[0.58rem] ${statusClasses(run.run_status)}`}>{run.run_status}</span>
                  <a className="text-xs text-[hsl(var(--muted))] underline decoration-dotted underline-offset-4" data-evidence-link="steps" href={formatEvidenceHash(run.run_id, 'steps', run.step_id)}>
                    Step {run.step_position} · {run.step_name} ({run.step_status})
                  </a>
                  {run.active && run.cancelable ? <span className="text-xs text-[hsl(var(--warn))]">Cancel from the run cockpit to abort this session.</span> : null}
                </li>
              ))}
            </ul>
            {session.runs_truncated ? <p className="mt-2 text-xs text-[hsl(var(--faint))]">More related runs exist; only the newest are listed.</p> : null}
          </div>
          <div>
            <HudLabel className="text-[hsl(var(--faint))]">Related requests</HudLabel>
            {session.requests.length === 0 ? (
              <p className="mt-2 text-sm text-[hsl(var(--muted))]">No request IDs were recorded with this session.</p>
            ) : (
              <ul className="mt-2 grid gap-2">
                {session.requests.map((request) => {
                  const destination = resolveCorrelation(request.key, request.value, { runID: request.run_id, source: 'Executor session', targets: null });
                  return (
                    <li key={`${request.key}-${request.value}`} className="flex flex-wrap items-center gap-2">
                      <span className="text-[0.62rem] uppercase tracking-[0.18em] text-[hsl(var(--faint))]">{correlationKindLabels[destination.kind]}</span>
                      <CorrelationLink destination={destination} />
                      <CopyValueButton label={`${request.key} ${request.value}`} value={request.value} />
                    </li>
                  );
                })}
              </ul>
            )}
            {session.requests_truncated ? <p className="mt-2 text-xs text-[hsl(var(--faint))]">More request IDs exist; only the first are listed.</p> : null}
          </div>
        </div>
      </div>
    </HudPanel>
  );
}

function emptyMessage(response: ExecutorSessionsResponse) {
  if (response.executor_availability === 'available') {
    return `No executor sessions were recorded by run steps in the last ${response.retention.window}. This does not show whether the executors are idle.`;
  }
  return `No executor sessions were recorded by run steps in the last ${response.retention.window}, and executor availability is ${response.executor_availability.replace('_', ' ')}. An empty list does not mean the executor is healthy or idle.`;
}

export function SessionsPage() {
  const selected = useSessionSelection();
  const [data, setData] = useState<ExecutorSessionsResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [attempt, setAttempt] = useState(0);

  const fetchView = useCallback(async (ticket: LiveRefreshTicket) => {
    const response = await loadExecutorSessions({ sessionID: selected });
    if (ticket.isCurrent()) {
      setData(response);
      setError('');
    }
  }, [selected]);

  const live = useLiveRefresh({ key: selected, mode: 'active', load: fetchView, activeInterval: sessionsRefreshInterval });
  const { claimTicket, markUpdated } = live;

  useEffect(() => {
    const ticket = claimTicket();
    setLoading(true);
    setError('');
    fetchView(ticket)
      .then(() => {
        if (ticket.isCurrent()) markUpdated();
      })
      .catch((caught: unknown) => {
        if (ticket.isCurrent()) {
          setData(null);
          setError(messageFrom(caught));
        }
      })
      .finally(() => {
        if (ticket.isCurrent()) setLoading(false);
      });
  }, [attempt, claimTicket, fetchView, markUpdated]);

  const sessions = data?.sessions ?? [];

  return (
    <HudSection
      kicker="OpenCode executor"
      title="Executor sessions"
      note="Sessions recorded by workflow run steps, joined with the executor that owns them. Read-only: sessions cannot be prompted, edited, or aborted here."
      actions={[
        <LiveRefreshIndicator key="live" live={live} />,
        <HudChip key="refresh" className="px-3 py-2 text-[0.62rem] text-[hsl(var(--signal))]" disabled={loading} onClick={() => setAttempt((value) => value + 1)} type="button">
          Refresh
        </HudChip>,
      ]}
    >
      {selected ? (
        <HudPanelStrong className="mb-4 flex flex-wrap items-center justify-between gap-3 p-3">
          <span className="text-sm text-[hsl(var(--ink))]">
            Showing session <code className="break-all">{selected}</code>
          </span>
          <a className="text-xs text-[hsl(var(--signal))]" href={formatSessionHash()}>All sessions</a>
        </HudPanelStrong>
      ) : null}

      {loading && !data ? <HudLoadingState>Loading executor sessions…</HudLoadingState> : null}
      {!loading && error ? (
        <HudErrorState role="alert">
          <strong>Executor sessions unavailable</strong>
          <span>{error}</span>
          <HudChip className="mt-2 w-fit px-3 py-2 text-[0.62rem] text-[hsl(var(--signal))]" onClick={() => setAttempt((value) => value + 1)} type="button">Retry</HudChip>
        </HudErrorState>
      ) : null}

      {data ? (
        <div className="grid gap-4">
          <HudPanelStrong className="p-4">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <h3 className="hud-label text-[hsl(var(--signal))]">Executor availability</h3>
              <HudTag className={`px-2.5 py-1 text-[0.62rem] ${statusClasses(data.executor_availability === 'available' ? 'succeeded' : data.executor_availability === 'not_configured' ? 'canceled' : data.executor_availability === 'partial' ? 'paused' : 'failed')}`}>
                {data.executor_availability.replace('_', ' ')}
              </HudTag>
            </div>
            <p className="mt-2 text-sm text-[hsl(var(--muted))]" data-testid="executor-availability">{availabilityText[data.executor_availability]}</p>
            <ExecutorList executors={data.executors} />
          </HudPanelStrong>

          {data.warnings.length > 0 ? (
            <div className="hud-panel border-[hsl(var(--warn))] bg-[hsl(var(--panel-soft))] px-4 py-3 text-sm text-[hsl(var(--ink))]" role="status">
              <ul className="grid gap-1">
                {data.warnings.map((warning) => <li key={warning}>{warning}</li>)}
              </ul>
            </div>
          ) : null}

          <div className="grid gap-3 sm:grid-cols-3 xl:grid-cols-6">
            <HudField label="Sessions" value={data.counts.total} />
            <HudField label="Active" value={data.counts.active} />
            <HudField label="Stale" value={data.counts.stale} />
            <HudField label="Ended" value={data.counts.ended} />
            <HudField label="Compatibility" value={data.counts.historical_compatibility} />
            <HudField label="Window" value={data.retention.window} />
          </div>

          <p className="text-xs text-[hsl(var(--muted))]" data-testid="abort-policy">
            {data.abort.direct_abort ? data.abort.note : `No direct session abort is available. ${data.abort.note} Route: ${data.abort.route}${data.abort.admin_required ? ' (admin only)' : ''}.`}
          </p>

          {sessions.length === 0 ? (
            selected ? (
              <HudEmptyState>
                Session <code className="break-all text-[hsl(var(--ink))]">{selected}</code> has no run-step record in the last {data.retention.window}. It is unknown or was removed by retention.{' '}
                <CopyValueButton label={`session ID ${selected}`} value={selected} />
              </HudEmptyState>
            ) : (
              <HudEmptyState>{emptyMessage(data)}</HudEmptyState>
            )
          ) : (
            <div className="grid gap-3">
              {sessions.map((session) => <SessionCard focused={session.session_id === selected} key={`${session.executor_key}-${session.session_id}`} session={session} />)}
            </div>
          )}
          {data.truncated ? <p className="text-xs text-[hsl(var(--faint))]">Results are bounded: showing up to {data.limit} sessions from the newest {data.scan_limit} session records.</p> : null}
        </div>
      ) : null}
    </HudSection>
  );
}
