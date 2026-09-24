import type { LiveRefreshState } from '../hooks/useLiveRefresh';
import { HudLed } from './hud';

export type LiveRefreshIndicatorProps = {
  live: LiveRefreshState;
  now?: () => number;
};

function formatClock(value: number) {
  return new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' }).format(new Date(value));
}

function formatSeconds(ms: number) {
  return `${Math.max(1, Math.round(ms / 1000))}s`;
}

export function liveRefreshLabel(live: LiveRefreshState) {
  switch (live.status) {
    case 'off':
      return 'Live refresh off';
    case 'idle':
      return 'Live refresh paused: run finished';
    case 'paused-hidden':
      return 'Live refresh paused: tab hidden';
    case 'reconnecting':
      return live.nextDelayMs === null
        ? `Reconnecting (attempt ${live.failureCount})`
        : `Reconnecting: retry in ${formatSeconds(live.nextDelayMs)} (attempt ${live.failureCount})`;
    case 'refreshing':
      return 'Refreshing';
    case 'live':
      return `Live: every ${formatSeconds(live.intervalMs)}`;
  }
}

function ledClass(live: LiveRefreshState) {
  if (live.status === 'reconnecting') {
    return 'bg-[hsl(var(--danger))]';
  }
  if (live.stale || live.status === 'paused-hidden') {
    return 'bg-[hsl(var(--warn))]';
  }
  if (live.status === 'off' || live.status === 'idle') {
    return 'bg-[hsl(var(--faint))]';
  }
  return 'bg-[hsl(var(--confirm))]';
}

export function LiveRefreshIndicator({ live, now = Date.now }: LiveRefreshIndicatorProps) {
  const updated = live.lastUpdatedAt === null ? 'Updated: never' : `Updated ${formatClock(live.lastUpdatedAt)}`;
  const ageSeconds = live.lastUpdatedAt === null ? null : Math.max(0, now() - live.lastUpdatedAt);

  return (
    <div
      aria-live="polite"
      className="hud-chip flex items-center gap-2 border-[hsl(var(--border))] bg-[hsl(var(--panel-soft))] px-3 py-2 text-[0.62rem] text-[hsl(var(--muted))]"
      data-live-status={live.status}
      data-stale={live.stale ? 'true' : 'false'}
      role="status"
    >
      <HudLed className={ledClass(live)} />
      <span>{liveRefreshLabel(live)}</span>
      <span aria-hidden="true">·</span>
      <span title={ageSeconds === null ? undefined : `${formatSeconds(ageSeconds)} ago`}>{updated}</span>
      {live.stale ? <span className="text-[hsl(var(--warn))]">Stale</span> : null}
      {live.lastError ? <span className="text-[hsl(var(--danger))]" title={live.lastError}>Error: {live.lastError}</span> : null}
    </div>
  );
}
