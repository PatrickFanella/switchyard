import { describe, expect, it } from 'vitest';
import { createRunController } from './run-controller.js';

describe('run controller', () => {
  it('prevents concurrent runs', async () => {
    let release!: () => void;
    const controller = createRunController({
      run: () =>
        new Promise<void>((resolve) => {
          release = resolve;
        }),
    });

    const first = controller.start({ kind: 'daily' });
    expect(controller.getStatus().state).toBe('running');

    await expect(controller.start({ kind: 'daily' })).rejects.toThrow('A run is already in progress');

    release();
    await first;
    expect(controller.getStatus().state).toBe('succeeded');
  });

  it('keeps a log tail and exposes run metadata', async () => {
    const controller = createRunController({
      run: async (_request, log) => {
        log('line 1');
        log('line 2');
      },
    });

    await controller.start({ kind: 'manual', since: '2026-05-26T00:00:00.000Z', until: '2026-05-27T00:00:00.000Z' });

    const status = controller.getStatus();
    expect(status.request).toEqual({
      kind: 'manual',
      since: '2026-05-26T00:00:00.000Z',
      until: '2026-05-27T00:00:00.000Z',
    });
    expect(status.logs).toEqual(['started', 'line 1', 'line 2', 'finished']);
    expect(status.error).toBeUndefined();
  });

  it('cancels active runs cooperatively and allows later starts', async () => {
    let releaseFirst!: () => void;
    let runCount = 0;
    const controller = createRunController({
      run: async (_request, log) => {
        runCount += 1;
        const currentRun = runCount;

        log(`run ${currentRun} started`);

        if (currentRun === 1) {
          await new Promise<void>((resolve) => {
            releaseFirst = resolve;
          });
          log('first run finished after cancellation');
          return;
        }

        log('second run finished');
      },
    });

    const first = controller.start({ kind: 'daily' });
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(controller.cancel()).toBe(true);
    expect(controller.getStatus()).toMatchObject({ state: 'cancelled' });

    const second = controller.start({ kind: 'manual' });
    await second;

    releaseFirst();
    await first;

    const status = controller.getStatus();
    expect(status.state).toBe('succeeded');
    expect(status.logs).toContain('second run finished');
    expect(status.logs).not.toContain('first run finished after cancellation');
  });
});
