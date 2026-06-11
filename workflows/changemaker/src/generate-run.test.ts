import { describe, expect, it } from 'vitest';
import { createGeneratePlan } from './generate-run.js';

describe('createGeneratePlan', () => {
  it('marks manual windows as authoritative', () => {
    const plan = createGeneratePlan({
      config: {
        now: new Date('2026-05-22T11:00:00.000Z'),
        since: new Date('2026-05-21T11:00:00.000Z'),
        lookbackHours: 36,
        cadenceHours: 24,
        nowWindow: false,
        manualWindow: true,
      },
      lastPostAt: new Date('2026-05-20T11:00:00.000Z'),
    });

    expect(plan.since.toISOString()).toBe('2026-05-21T11:00:00.000Z');
    expect(plan.until.toISOString()).toBe('2026-05-22T11:00:00.000Z');
  });

  it('uses cadence when no manual window is present', () => {
    const plan = createGeneratePlan({
      config: {
        now: new Date('2026-05-25T12:00:00.000Z'),
        since: new Date('2026-05-24T00:00:00.000Z'),
        lookbackHours: 36,
        cadenceHours: 24,
        nowWindow: false,
        manualWindow: false,
      },
      lastPostAt: new Date('2026-05-24T08:00:00.000Z'),
    });

    expect(plan.until.toISOString()).toBe('2026-05-25T08:00:00.000Z');
    expect(plan.since.toISOString()).toBe('2026-05-23T20:00:00.000Z');
  });
});
