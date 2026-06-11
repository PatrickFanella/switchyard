import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import { inspectChangesFile, inspectNarrativeFile } from './inspect.js';

const tempDirs: string[] = [];

afterEach(() => {
  for (const dir of tempDirs.splice(0)) {
    rmSync(dir, { recursive: true, force: true });
  }
});

describe('inspect helpers', () => {
  it('summarizes changes JSON', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'changemaker-'));
    tempDirs.push(dir);
    const file = join(dir, 'changes.json');
    writeFileSync(
      file,
      JSON.stringify({
        totals: { changes: 1, repos: 1, pullRequests: 1, commits: 0 },
        window: { from: 'a', to: 'b' },
        clusters: [{ theme: 'Product progress', eventIds: ['pr:1'], narrativeImportance: 'major' }],
      }),
    );

    await expect(inspectChangesFile(file)).resolves.toContain('Changes: 1');
  });

  it('summarizes narrative JSON', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'changemaker-'));
    tempDirs.push(dir);
    const file = join(dir, 'state.json');
    writeFileSync(
      file,
      JSON.stringify({
        projectContext: { name: 'changemaker' },
        activeThreads: [],
        futureWork: [],
        recentPosts: [],
      }),
    );

    await expect(inspectNarrativeFile(file)).resolves.toContain('Project: changemaker');
  });
});
