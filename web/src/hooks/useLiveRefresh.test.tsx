// @vitest-environment jsdom

import { act, useState } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { backoffDelay, isTerminalRunStatus, useLiveRefresh, type LiveRefreshLoad, type LiveRefreshMode, type LiveRefreshTicket } from './useLiveRefresh';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

type HookValue = ReturnType<typeof useLiveRefresh>;

type HarnessProps = {
  runKey: string;
  mode: LiveRefreshMode;
  load: LiveRefreshLoad;
  activeInterval?: number;
  maxBackoff?: number;
  capture: (value: HookValue) => void;
};

function Harness({ runKey, mode, load, activeInterval, maxBackoff, capture }: HarnessProps) {
  const value = useLiveRefresh({ key: runKey, mode, load, activeInterval, maxBackoff });
  capture(value);
  return null;
}

async function mount(props: Omit<HarnessProps, 'capture'>) {
  const container = document.createElement('div');
  document.body.appendChild(container);
  const root: Root = createRoot(container);
  let latest: HookValue | null = null;
  const capture = (value: HookValue) => {
    latest = value;
  };
  const render = async (next: Omit<HarnessProps, 'capture'>) => {
    await act(async () => {
      root.render(<Harness {...next} capture={capture} />);
    });
  };
  await render(props);
  return {
    latest: () => {
      if (!latest) throw new Error('hook value not captured');
      return latest as HookValue;
    },
    render,
    unmount: async () => {
      await act(async () => {
        root.unmount();
      });
    },
  };
}

async function advance(ms: number) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

function setVisibility(state: 'visible' | 'hidden') {
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => state });
  document.dispatchEvent(new Event('visibilitychange'));
}

/** A run that advances through the requested statuses on every poll, the way the cockpit derives its mode. */
function StatusScenario({ statuses, capture, onLoad }: { statuses: string[]; capture: (value: HookValue, status: string) => void; onLoad: () => void }) {
  const [status, setStatus] = useState(statuses[0]);
  const [index, setIndex] = useState(0);
  const load = async (ticket: LiveRefreshTicket) => {
    onLoad();
    const nextIndex = Math.min(index + 1, statuses.length - 1);
    await Promise.resolve();
    if (!ticket.isCurrent()) return;
    setIndex(nextIndex);
    setStatus(statuses[nextIndex]);
  };
  const mode: LiveRefreshMode = isTerminalRunStatus(status) ? 'idle' : 'active';
  const value = useLiveRefresh({ key: 'run-1', mode, load, activeInterval: 3_000 });
  capture(value, status);
  return null;
}

beforeEach(() => {
  vi.useFakeTimers();
  setVisibility('visible');
});

afterEach(() => {
  vi.useRealTimers();
  document.body.innerHTML = '';
});

describe('backoffDelay', () => {
  it('doubles from the base and caps at the maximum', () => {
    expect(backoffDelay(1_000, 0, 8_000)).toBe(1_000);
    expect(backoffDelay(1_000, 1, 8_000)).toBe(2_000);
    expect(backoffDelay(1_000, 2, 8_000)).toBe(4_000);
    expect(backoffDelay(1_000, 3, 8_000)).toBe(8_000);
    expect(backoffDelay(1_000, 40, 8_000)).toBe(8_000);
  });
});

describe('useLiveRefresh', () => {
  it('polls through queued, running, CI and terminal states, then stops', async () => {
    const container = document.createElement('div');
    document.body.appendChild(container);
    const root = createRoot(container);
    let latest: HookValue | null = null;
    let latestStatus = '';
    const loads = vi.fn();
    const statuses = ['queued', 'running', 'running', 'succeeded'];

    await act(async () => {
      root.render(<StatusScenario statuses={statuses} capture={(value, status) => { latest = value; latestStatus = status; }} onLoad={loads} />);
    });
    const value = () => latest as unknown as HookValue;

    expect(value().status).toBe('live');
    expect(value().nextDelayMs).toBe(3_000);
    expect(loads).toHaveBeenCalledTimes(0);

    await advance(3_000);
    expect(loads).toHaveBeenCalledTimes(1);
    expect(latestStatus).toBe('running');
    expect(value().lastUpdatedAt).not.toBeNull();

    await advance(3_000);
    expect(loads).toHaveBeenCalledTimes(2);
    expect(latestStatus).toBe('running');

    await advance(3_000);
    expect(loads).toHaveBeenCalledTimes(3);
    expect(latestStatus).toBe('succeeded');
    expect(value().status).toBe('idle');
    expect(value().nextDelayMs).toBeNull();

    await advance(120_000);
    expect(loads).toHaveBeenCalledTimes(3);
    expect(value().stale).toBe(false);

    await act(async () => {
      root.unmount();
    });
  });

  it('stops polling once a run is canceled and resumes only for a new selection', async () => {
    const load = vi.fn(async () => {});
    const harness = await mount({ runKey: 'run-1', mode: 'active', load, activeInterval: 3_000 });

    await advance(6_000);
    expect(load).toHaveBeenCalledTimes(2);

    // The cancel action claims a ticket and the reload marks the run canceled: mode becomes idle.
    harness.latest().claimTicket();
    await harness.render({ runKey: 'run-1', mode: 'idle', load, activeInterval: 3_000 });
    expect(harness.latest().status).toBe('idle');

    await advance(60_000);
    expect(load).toHaveBeenCalledTimes(2);

    await harness.render({ runKey: 'run-2', mode: 'active', load, activeInterval: 3_000 });
    await advance(3_000);
    expect(load).toHaveBeenCalledTimes(3);

    await harness.unmount();
  });

  it('performs no requests when nothing is selected', async () => {
    const load = vi.fn(async () => {});
    const harness = await mount({ runKey: '', mode: 'off', load, activeInterval: 1_000 });

    await advance(30_000);
    expect(load).not.toHaveBeenCalled();
    expect(harness.latest().status).toBe('off');
    expect(harness.latest().nextDelayMs).toBeNull();

    await harness.unmount();
  });

  it('pauses while the tab is hidden and refreshes immediately when it becomes visible', async () => {
    const load = vi.fn(async () => {});
    const harness = await mount({ runKey: 'run-1', mode: 'active', load, activeInterval: 3_000 });

    await advance(3_000);
    expect(load).toHaveBeenCalledTimes(1);

    await act(async () => {
      setVisibility('hidden');
    });
    expect(harness.latest().status).toBe('paused-hidden');
    expect(harness.latest().nextDelayMs).toBeNull();

    await advance(60_000);
    expect(load).toHaveBeenCalledTimes(1);

    await act(async () => {
      setVisibility('visible');
    });
    await advance(0);
    expect(load).toHaveBeenCalledTimes(2);
    expect(harness.latest().status).toBe('live');
    expect(harness.latest().nextDelayMs).toBe(3_000);

    await advance(3_000);
    expect(load).toHaveBeenCalledTimes(3);

    await harness.unmount();
  });

  it('backs off on network failure, caps the delay, reports the error and recovers', async () => {
    let failing = true;
    const load = vi.fn(async () => {
      if (failing) throw new Error('network down');
    });
    const harness = await mount({ runKey: 'run-1', mode: 'active', load, activeInterval: 1_000, maxBackoff: 8_000 });

    const expectedDelays = [2_000, 4_000, 8_000, 8_000, 8_000];
    let elapsed = 1_000;
    await advance(elapsed);
    for (let attempt = 1; attempt <= expectedDelays.length; attempt += 1) {
      expect(load).toHaveBeenCalledTimes(attempt);
      expect(harness.latest().failureCount).toBe(attempt);
      expect(harness.latest().status).toBe('reconnecting');
      expect(harness.latest().lastError).toBe('network down');
      expect(harness.latest().stale).toBe(true);
      expect(harness.latest().nextDelayMs).toBe(expectedDelays[attempt - 1]);
      elapsed = expectedDelays[attempt - 1];
      await advance(elapsed);
    }
    // Delay never exceeds maxBackoff, so a long wait yields a bounded number of attempts.
    const before = load.mock.calls.length;
    await advance(80_000);
    expect(load.mock.calls.length - before).toBe(10);

    failing = false;
    await advance(8_000);
    expect(harness.latest().failureCount).toBe(0);
    expect(harness.latest().lastError).toBe('');
    expect(harness.latest().status).toBe('live');
    expect(harness.latest().stale).toBe(false);
    expect(harness.latest().nextDelayMs).toBe(1_000);

    await harness.unmount();
  });

  it('never overlaps requests when a load outlives the interval', async () => {
    let inFlight = 0;
    let maxInFlight = 0;
    const resolvers: Array<() => void> = [];
    const load = vi.fn(() => new Promise<void>((resolve) => {
      inFlight += 1;
      maxInFlight = Math.max(maxInFlight, inFlight);
      resolvers.push(() => {
        inFlight -= 1;
        resolve();
      });
    }));
    const harness = await mount({ runKey: 'run-1', mode: 'active', load, activeInterval: 3_000 });

    await advance(3_000);
    expect(load).toHaveBeenCalledTimes(1);
    expect(harness.latest().status).toBe('refreshing');

    await advance(30_000);
    expect(load).toHaveBeenCalledTimes(1);
    expect(harness.latest().nextDelayMs).toBeNull();

    await act(async () => {
      resolvers[0]();
    });
    expect(harness.latest().nextDelayMs).toBe(3_000);

    await advance(3_000);
    expect(load).toHaveBeenCalledTimes(2);
    expect(maxInFlight).toBe(1);

    await act(async () => {
      resolvers[1]();
    });
    await harness.unmount();
  });

  it('rejects stale out-of-order responses after a newer ticket is claimed', async () => {
    const tickets: LiveRefreshTicket[] = [];
    const resolvers: Array<() => void> = [];
    const load = vi.fn((ticket: LiveRefreshTicket) => new Promise<void>((resolve) => {
      tickets.push(ticket);
      resolvers.push(resolve);
    }));
    const harness = await mount({ runKey: 'run-1', mode: 'active', load, activeInterval: 3_000 });

    await advance(3_000);
    expect(tickets).toHaveLength(1);
    expect(tickets[0].isCurrent()).toBe(true);

    // An operator action (retry/cancel/refresh) claims a newer ticket while the poll is still in flight.
    const actionTicket = harness.latest().claimTicket();
    expect(tickets[0].isCurrent()).toBe(false);
    expect(actionTicket.isCurrent()).toBe(true);
    expect(actionTicket.sequence).toBeGreaterThan(tickets[0].sequence);

    await act(async () => {
      resolvers[0]();
    });
    // The stale poll neither counts as a success nor records freshness.
    expect(harness.latest().lastUpdatedAt).toBeNull();

    harness.latest().markUpdated();
    await advance(0);
    expect(harness.latest().lastUpdatedAt).not.toBeNull();

    await advance(3_000);
    expect(tickets).toHaveLength(2);
    expect(tickets[1].isCurrent()).toBe(true);
    expect(actionTicket.isCurrent()).toBe(false);

    await act(async () => {
      resolvers[1]();
    });
    await harness.unmount();
  });

  it('invalidates in-flight tickets and resets backoff when the selection changes', async () => {
    const tickets: LiveRefreshTicket[] = [];
    let failing = true;
    const load = vi.fn(async (ticket: LiveRefreshTicket) => {
      tickets.push(ticket);
      if (failing) throw new Error('network down');
    });
    const harness = await mount({ runKey: 'run-1', mode: 'active', load, activeInterval: 1_000, maxBackoff: 8_000 });

    await advance(1_000);
    await advance(2_000);
    expect(harness.latest().failureCount).toBe(2);
    expect(harness.latest().nextDelayMs).toBe(4_000);

    failing = false;
    await harness.render({ runKey: 'run-2', mode: 'active', load, activeInterval: 1_000, maxBackoff: 8_000 });
    expect(harness.latest().failureCount).toBe(0);
    expect(harness.latest().lastError).toBe('');
    expect(tickets[tickets.length - 1].isCurrent()).toBe(false);
    expect(harness.latest().nextDelayMs).toBe(1_000);

    await advance(1_000);
    expect(load).toHaveBeenCalledTimes(3);
    expect(harness.latest().status).toBe('live');

    await harness.unmount();
  });

  it('marks a live run stale when updates stop arriving', async () => {
    const resolvers: Array<() => void> = [];
    const load = vi.fn(() => new Promise<void>((resolve) => {
      resolvers.push(resolve);
    }));
    const harness = await mount({ runKey: 'run-1', mode: 'active', load, activeInterval: 1_000 });

    await advance(1_000);
    await act(async () => {
      resolvers[0]();
    });
    expect(harness.latest().stale).toBe(false);

    // The second poll hangs; the last good update ages past three intervals.
    await advance(1_000);
    expect(load).toHaveBeenCalledTimes(2);
    await advance(4_000);
    expect(harness.latest().stale).toBe(true);
    expect(load).toHaveBeenCalledTimes(2);

    await act(async () => {
      resolvers[1]();
    });
    expect(harness.latest().stale).toBe(false);

    await harness.unmount();
  });

  it('stops all timers on unmount', async () => {
    const load = vi.fn(async () => {});
    const harness = await mount({ runKey: 'run-1', mode: 'active', load, activeInterval: 1_000 });
    await advance(1_000);
    expect(load).toHaveBeenCalledTimes(1);

    await harness.unmount();
    await advance(10_000);
    expect(load).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0);
  });
});
