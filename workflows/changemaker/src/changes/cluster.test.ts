import { describe, expect, it } from 'vitest';
import { clusterChanges } from './cluster.js';
import type { ChangeEvent } from '../types.js';

describe('clusterChanges', () => {
  it('groups changes by narrative theme', () => {
    const clusters = clusterChanges([
      change('pr:owner/repo#1', 'pull_request', 'Add post writer'),
      change('commit:owner/repo@abc', 'commit', 'Fix branch collector bug'),
      change('commit:owner/repo@def', 'commit', 'Update README docs'),
    ]);

    expect(clusters.map((cluster) => cluster.theme)).toContain('Product progress');
    expect(clusters.map((cluster) => cluster.theme)).toContain('Fixes and reliability');
    expect(clusters.map((cluster) => cluster.theme)).toContain('Documentation and communication');
  });
});

function change(id: string, type: ChangeEvent['type'], title: string): ChangeEvent {
  return {
    id,
    type,
    repo: 'owner/repo',
    title,
    url: 'https://example.test/change',
    createdAt: '2026-05-25T12:00:00.000Z',
    updatedAt: '2026-05-25T12:00:00.000Z',
    covered: false,
  };
}
