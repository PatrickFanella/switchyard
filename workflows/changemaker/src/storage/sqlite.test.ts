import { mkdtempSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { afterEach, describe, expect, it } from 'vitest';
import { ChangemakerStore } from './sqlite.js';
import type { ChangeEvent } from '../types.js';

const tempDirs: string[] = [];

afterEach(() => {
  for (const dir of tempDirs.splice(0)) {
    rmSync(dir, { recursive: true, force: true });
  }
});

describe('ChangemakerStore', () => {
  it('records and reads covered change ids', () => {
    const dir = mkdtempSync(join(tmpdir(), 'changemaker-'));
    tempDirs.push(dir);
    const store = new ChangemakerStore(join(dir, 'state.sqlite'));

    store.recordChangeRun({
      id: 'post:1',
      generatedAt: '2026-05-25T12:00:00.000Z',
      path: 'out/post.md',
      changes: [change('pr:owner/repo#1'), change('commit:owner/repo@abc123')],
    });

    expect(store.listCoveredChangeIds()).toEqual(
      new Set(['pr:owner/repo#1', 'commit:owner/repo@abc123']),
    );
    expect(store.getLastGeneratedPost()?.generatedAt).toBe('2026-05-25T12:00:00.000Z');
    store.close();
  });
});

function change(id: string): ChangeEvent {
  return {
    id,
    type: id.startsWith('pr:') ? 'pull_request' : 'commit',
    repo: 'owner/repo',
    title: 'Change',
    url: 'https://example.test/change',
    createdAt: '2026-05-25T12:00:00.000Z',
    updatedAt: '2026-05-25T12:00:00.000Z',
    covered: false,
  };
}
