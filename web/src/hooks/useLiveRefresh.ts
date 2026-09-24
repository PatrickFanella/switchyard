import { useCallback, useEffect, useReducer, useRef, useState } from 'react';

/**
 * Bounded live refresh for the run cockpit.
 *
 * The runtime has no run event stream (its only SSE route is the model-compatible
 * ingress), so the cockpit polls with a single self-rescheduling timer:
 *
 * - `active`: poll every `activeInterval` while the selected run can still change.
 * - `idle`: stop polling; the run is terminal or nothing worth polling is loaded.
 * - `off`: stop polling; nothing is selected.
 * - hidden tab: pause; resume with an immediate refresh when it becomes visible.
 * - failures: exponential backoff from `activeInterval`, capped at `maxBackoff`.
 *
 * One request at a time: the next tick is scheduled only after the previous
 * request settles, so intervals never stack. Every request carries a ticket; a
 * newer request, a key change (selection), an operator action (`claimTicket`), or
 * unmount invalidates older tickets so out-of-order responses are discarded by the
 * loader instead of applied.
 */

export type LiveRefreshMode = 'active' | 'idle' | 'off';

export type LiveRefreshTicket = {
  sequence: number;
  isCurrent: () => boolean;
};

export type LiveRefreshLoad = (ticket: LiveRefreshTicket) => Promise<void>;

export type LiveRefreshStatus = 'off' | 'idle' | 'live' | 'refreshing' | 'paused-hidden' | 'reconnecting';

export type LiveRefreshState = {
  status: LiveRefreshStatus;
  /** Epoch milliseconds of the last successful load, or null when no load has succeeded yet. */
  lastUpdatedAt: number | null;
  /** Message from the most recent failed load; cleared on the next success. */
  lastError: string;
  /** Consecutive failed loads since the last success. */
  failureCount: number;
  /** Delay before the next scheduled load, or null when nothing is scheduled. */
  nextDelayMs: number | null;
  /** The polling cadence in milliseconds while active. */
  intervalMs: number;
  /** True when the last successful update is older than allowed or the last load failed. */
  stale: boolean;
  /** True while a load is in flight. */
  inFlight: boolean;
};

export type LiveRefreshOptions = {
  /** Selection key. Changing it invalidates in-flight tickets and resets backoff. */
  key: string;
  mode: LiveRefreshMode;
  load: LiveRefreshLoad;
  activeInterval?: number;
  maxBackoff?: number;
  /** Age after which a successful update is reported stale. Defaults to three active intervals. */
  staleAfter?: number;
  /** Injected for tests; defaults to `Date.now`. */
  now?: () => number;
};

export const defaultActiveInterval = 3_000;
export const defaultMaxBackoff = 60_000;

export function backoffDelay(base: number, failures: number, max: number) {
  if (failures <= 0) {
    return base;
  }
  const exponent = Math.min(failures, 16);
  return Math.min(base * 2 ** exponent, max);
}

function isDocumentHidden() {
  return typeof document !== 'undefined' && document.visibilityState === 'hidden';
}

export function useLiveRefresh({
  key,
  mode,
  load,
  activeInterval = defaultActiveInterval,
  maxBackoff = defaultMaxBackoff,
  staleAfter,
  now = Date.now,
}: LiveRefreshOptions) {
  const staleThreshold = staleAfter ?? activeInterval * 3;

  const sequenceRef = useRef(0);
  const inFlightRef = useRef(false);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const failuresRef = useRef(0);
  const mountedRef = useRef(true);
  const loadRef = useRef(load);
  const modeRef = useRef(mode);
  const hiddenRef = useRef(isDocumentHidden());
  loadRef.current = load;
  modeRef.current = mode;

  const [lastUpdatedAt, setLastUpdatedAt] = useState<number | null>(null);
  const [lastError, setLastError] = useState('');
  const [failureCount, setFailureCount] = useState(0);
  const [nextDelayMs, setNextDelayMs] = useState<number | null>(null);
  const [inFlight, setInFlight] = useState(false);
  const [hidden, setHidden] = useState(hiddenRef.current);
  const [, rerender] = useReducer((count: number) => count + 1, 0);

  const clearTimer = useCallback(() => {
    if (timerRef.current !== null) {
      clearTimeout(timerRef.current);
      timerRef.current = null;
    }
  }, []);

  const invalidate = useCallback(() => {
    sequenceRef.current += 1;
  }, []);

  const makeTicket = useCallback((): LiveRefreshTicket => {
    sequenceRef.current += 1;
    const sequence = sequenceRef.current;
    return { sequence, isCurrent: () => mountedRef.current && sequenceRef.current === sequence };
  }, []);

  /** Claim a ticket for a load driven outside the poller (selection change, operator action). */
  const claimTicket = useCallback(() => makeTicket(), [makeTicket]);

  /** Record a successful load performed outside the poller. */
  const markUpdated = useCallback(() => {
    failuresRef.current = 0;
    setFailureCount(0);
    setLastError('');
    setLastUpdatedAt(now());
  }, [now]);

  // Forward declaration so schedule and tick can reference each other.
  const tickRef = useRef<() => Promise<void>>(async () => {});

  const schedule = useCallback((delay: number) => {
    clearTimer();
    if (!mountedRef.current || modeRef.current !== 'active' || hiddenRef.current) {
      setNextDelayMs(null);
      return;
    }
    setNextDelayMs(delay);
    timerRef.current = setTimeout(() => {
      timerRef.current = null;
      setNextDelayMs(null);
      void tickRef.current();
    }, delay);
  }, [clearTimer]);

  const runLoad = useCallback(async () => {
    if (inFlightRef.current) {
      return false;
    }
    inFlightRef.current = true;
    setInFlight(true);
    const ticket = makeTicket();

    let ok = false;
    try {
      await loadRef.current(ticket);
      ok = true;
    } catch (caught) {
      if (ticket.isCurrent()) {
        failuresRef.current += 1;
        setFailureCount(failuresRef.current);
        setLastError(caught instanceof Error ? caught.message : 'Live refresh failed');
      }
    } finally {
      inFlightRef.current = false;
      if (mountedRef.current) {
        setInFlight(false);
      }
    }

    if (ok && ticket.isCurrent()) {
      failuresRef.current = 0;
      setFailureCount(0);
      setLastError('');
      setLastUpdatedAt(now());
    }
    return ok;
  }, [makeTicket, now]);

  tickRef.current = async () => {
    if (!mountedRef.current || modeRef.current !== 'active' || hiddenRef.current) {
      setNextDelayMs(null);
      return;
    }
    await runLoad();
    schedule(backoffDelay(activeInterval, failuresRef.current, maxBackoff));
  };

  // Selection changes: drop in-flight responses and reset backoff.
  useEffect(() => {
    invalidate();
    failuresRef.current = 0;
    setFailureCount(0);
    setLastError('');
    setLastUpdatedAt(null);
  }, [invalidate, key]);

  // Mode or visibility changes: (re)start or stop the timer chain.
  useEffect(() => {
    if (mode !== 'active' || hidden) {
      clearTimer();
      setNextDelayMs(null);
      return;
    }
    schedule(backoffDelay(activeInterval, failuresRef.current, maxBackoff));
    return clearTimer;
  }, [activeInterval, clearTimer, hidden, key, maxBackoff, mode, schedule]);

  useEffect(() => {
    if (typeof document === 'undefined') {
      return;
    }
    const onVisibility = () => {
      const nowHidden = isDocumentHidden();
      const wasHidden = hiddenRef.current;
      hiddenRef.current = nowHidden;
      setHidden(nowHidden);
      if (wasHidden && !nowHidden && modeRef.current === 'active') {
        // Resume with an immediate refresh so the operator sees current data.
        void tickRef.current();
      }
    };
    document.addEventListener('visibilitychange', onVisibility);
    return () => document.removeEventListener('visibilitychange', onVisibility);
  }, []);

  // Advance a coarse clock so the stale indicator flips without a new load.
  useEffect(() => {
    if (mode !== 'active' || hidden || lastUpdatedAt === null) {
      return;
    }
    const timer = setInterval(rerender, Math.max(1_000, Math.floor(staleThreshold / 3)));
    return () => clearInterval(timer);
  }, [hidden, lastUpdatedAt, mode, staleThreshold]);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      sequenceRef.current += 1;
      clearTimer();
    };
  }, [clearTimer]);

  const status: LiveRefreshStatus =
    mode === 'off' ? 'off'
      : mode === 'idle' ? 'idle'
        : hidden ? 'paused-hidden'
          : failureCount > 0 ? 'reconnecting'
            : inFlight ? 'refreshing'
              : 'live';

  const age = lastUpdatedAt === null ? null : Math.max(0, now() - lastUpdatedAt);
  const stale = mode === 'active' && !hidden && (failureCount > 0 || age === null || age > staleThreshold);

  const state: LiveRefreshState = {
    status,
    lastUpdatedAt,
    lastError,
    failureCount,
    nextDelayMs,
    intervalMs: activeInterval,
    stale,
    inFlight,
  };

  return { ...state, claimTicket, markUpdated, invalidate };
}

export function isTerminalRunStatus(status: string) {
  return ['succeeded', 'failed', 'canceled', 'timed_out', 'paused'].includes(status);
}
