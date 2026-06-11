import { describe, expect, it } from 'vitest';
import { filterDiff } from './diff-filter.js';

describe('filterDiff', () => {
  it('keeps source diffs and drops lockfiles', () => {
    const diff = [
      'diff --git a/src/index.ts b/src/index.ts',
      'index 111..222 100644',
      '--- a/src/index.ts',
      '+++ b/src/index.ts',
      '@@ -1 +1 @@',
      '-old',
      '+new',
      'diff --git a/pnpm-lock.yaml b/pnpm-lock.yaml',
      'index 333..444 100644',
      '--- a/pnpm-lock.yaml',
      '+++ b/pnpm-lock.yaml',
      '@@ -1 +1 @@',
      '-lock old',
      '+lock new',
    ].join('\n');

    const filtered = filterDiff(diff);

    expect(filtered.files).toEqual(['src/index.ts', 'pnpm-lock.yaml']);
    expect(filtered.text).toContain('src/index.ts');
    expect(filtered.text).not.toContain('lock new');
  });

  it('truncates long diffs', () => {
    const filtered = filterDiff(`diff --git a/src/a.ts b/src/a.ts\n${'x'.repeat(100)}`, 20);

    expect(filtered.truncated).toBe(true);
    expect(filtered.text).toContain('[diff truncated]');
  });
});
