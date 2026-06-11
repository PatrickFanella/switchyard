import { describe, expect, it } from 'vitest';
import { createBackfillWindows } from './backfill.js';

describe('createBackfillWindows', () => {
  it('creates daily windows', () => {
    const windows = createBackfillWindows({
      start: '2026-05-01T00:00:00.000Z',
      end: '2026-05-03T00:00:00.000Z',
      cadence: 'daily',
      timeTravelStrict: true,
    });

    expect(windows).toHaveLength(2);
    expect(windows[0]?.timeTravelStrict).toBe(true);
  });

  it('rejects inverted ranges', () => {
    expect(() =>
      createBackfillWindows({
        start: '2026-05-03',
        end: '2026-05-01',
        cadence: 'daily',
        timeTravelStrict: false,
      }),
    ).toThrow('--start must be before --end');
  });
});
