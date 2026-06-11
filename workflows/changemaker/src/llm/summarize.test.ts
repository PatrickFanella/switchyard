import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import { ChangemakerStore } from '../storage/sqlite.js';
import type { ChangeEvent, ChangeSummary } from '../types.js';
import { summarizeChangeWithRetry, summarizeChanges } from './summarize.js';

const tempDirs: string[] = [];

afterEach(() => {
  for (const dir of tempDirs.splice(0)) {
    rmSync(dir, { recursive: true, force: true });
  }
});

describe('summarizeChanges', () => {
  it('caches per-change summaries', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'changemaker-'));
    tempDirs.push(dir);
    const store = new ChangemakerStore(join(dir, 'state.sqlite'));
    let calls = 0;
    const provider = {
      name: 'fake',
      model: 'test-model',
      summarizeChange: async (): Promise<ChangeSummary> => {
        calls += 1;
        return {
          headline: 'Change headline',
          userFacingSummary: 'Visible progress.',
          technicalSummary: 'Technical detail.',
          impact: 'feature',
          notableFiles: ['src/index.ts'],
          confidence: 0.9,
        };
      },
    };

    const changes = [change('commit:owner/repo@abc123')];
    const first = await summarizeChanges({ changes, provider, store });
    const second = await summarizeChanges({ changes, provider, store });

    expect(first[0]?.summary?.headline).toBe('Change headline');
    expect(second[0]?.summary?.headline).toBe('Change headline');
    expect(calls).toBe(1);
    store.close();
  });

  it('retries failed summary returns before caching success', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'changemaker-'));
    tempDirs.push(dir);
    const store = new ChangemakerStore(join(dir, 'state.sqlite'));
    let calls = 0;
    const retries: string[] = [];
    const provider = {
      name: 'fake',
      model: 'test-model',
      summarizeChange: async (): Promise<ChangeSummary> => {
        calls += 1;
        if (calls === 1) throw new Error('bad JSON return');
        return summary();
      },
    };

    const result = await summarizeChanges({
      changes: [change('commit:owner/repo@retry')],
      provider,
      store,
      retry: { attempts: 2, delayMs: 0, onRetry: ({ changeId }) => retries.push(changeId) },
    });

    expect(result[0]?.summary?.headline).toBe('Change headline');
    expect(calls).toBe(2);
    expect(retries).toEqual(['commit:owner/repo@retry']);
    store.close();
  });

  it('throws with change id after retry exhaustion', async () => {
    const provider = {
      name: 'fake',
      model: 'test-model',
      summarizeChange: async (): Promise<ChangeSummary> => {
        throw new Error('still bad');
      },
    };

    await expect(
      summarizeChangeWithRetry(change('commit:owner/repo@fail'), provider, { attempts: 2, delayMs: 0 }),
    ).rejects.toThrow('LLM summary failed after 2 attempts for commit:owner/repo@fail');
  });
});

function summary(): ChangeSummary {
  return {
    headline: 'Change headline',
    userFacingSummary: 'Visible progress.',
    technicalSummary: 'Technical detail.',
    impact: 'feature',
    notableFiles: ['src/index.ts'],
    confidence: 0.9,
  };
}

function change(id: string): ChangeEvent {
  return {
    id,
    type: 'commit',
    repo: 'owner/repo',
    title: 'Add thing',
    url: 'https://example.test/change',
    createdAt: '2026-05-25T12:00:00.000Z',
    updatedAt: '2026-05-25T12:00:00.000Z',
    diff: 'diff --git a/src/index.ts b/src/index.ts',
    covered: false,
  };
}
