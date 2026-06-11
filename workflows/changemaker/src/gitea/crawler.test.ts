import { describe, expect, it } from 'vitest';
import { crawlChanges } from './crawler.js';

describe('crawlChanges', () => {
  it('collects recent PRs and branch commits into normalized changes', async () => {
    const client = {
      searchRepos: async () => [
        {
          name: 'tool',
          full_name: 'subculture-collective/tool',
          private: false,
          html_url: 'https://git.subcult.tv/subculture-collective/tool',
          default_branch: 'main',
          owner: { login: 'subculture-collective' },
        },
      ],
      listPullRequests: async () => [
        {
          id: 1,
          index: 42,
          title: 'Add daily post generator',
          body: 'Creates the first pipeline.',
          state: 'open' as const,
          html_url: 'https://git.subcult.tv/subculture-collective/tool/pulls/42',
          created_at: '2026-05-25T09:00:00.000Z',
          updated_at: '2026-05-25T10:00:00.000Z',
          user: { login: 'onnwee' },
          head: { ref: 'feature/daily' },
        },
      ],
      listBranches: async () => [{ name: 'main' }, { name: 'feature/daily' }],
      listCommits: async (_owner: string, _repo: string, options: { branch: string }) => [
        {
          sha: options.branch === 'main' ? 'abc123' : 'def456',
          html_url: `https://git.subcult.tv/subculture-collective/tool/commit/${options.branch}`,
          created: '2026-05-25T11:00:00.000Z',
          author: { login: 'onnwee' },
          commit: {
            message: options.branch === 'main' ? 'Update README' : 'Prototype branch collector',
          },
        },
      ],
      getPullRequestDiff: async () => 'diff --git a/src/pr.ts b/src/pr.ts\n+pr',
      getCommitDiff: async (_owner: string, _repo: string, sha: string) =>
        `diff --git a/src/${sha}.ts b/src/${sha}.ts\n+commit`,
    };

    const output = await crawlChanges(client as never, {
      baseUrl: 'https://git.subcult.tv',
      since: new Date('2026-05-24T00:00:00.000Z'),
      now: new Date('2026-05-25T12:00:00.000Z'),
      lookbackHours: 36,
    });

    expect(output.totals).toEqual({ repos: 1, pullRequests: 1, commits: 2, changes: 3 });
    expect(output.changes.map((change) => change.id)).toContain('pr:subculture-collective/tool#42');
    expect(output.changes.map((change) => change.id)).toContain('commit:subculture-collective/tool@def456');
    expect(output.changes.every((change) => change.diff !== undefined)).toBe(true);
  });

  it('filters covered changes by default', async () => {
    const client = {
      searchRepos: async () => [
        {
          name: 'tool',
          full_name: 'subculture-collective/tool',
          private: false,
          html_url: 'https://git.subcult.tv/subculture-collective/tool',
          default_branch: 'main',
          owner: { login: 'subculture-collective' },
        },
      ],
      listPullRequests: async () => [],
      listBranches: async () => [{ name: 'main' }],
      listCommits: async () => [
        {
          sha: 'abc123',
          html_url: 'https://git.subcult.tv/subculture-collective/tool/commit/abc123',
          created: '2026-05-25T11:00:00.000Z',
          commit: { message: 'Already covered' },
        },
      ],
    };

    const output = await crawlChanges(client as never, {
      baseUrl: 'https://git.subcult.tv',
      since: new Date('2026-05-24T00:00:00.000Z'),
      now: new Date('2026-05-25T12:00:00.000Z'),
      lookbackHours: 36,
      coveredChangeIds: new Set(['commit:subculture-collective/tool@abc123']),
    });

    expect(output.totals.changes).toBe(0);
  });

  it('excludes PRs and commits after the requested window end', async () => {
    const client = {
      searchRepos: async () => [
        {
          name: 'tool',
          full_name: 'subculture-collective/tool',
          private: false,
          html_url: 'https://git.subcult.tv/subculture-collective/tool',
          default_branch: 'main',
          owner: { login: 'subculture-collective' },
        },
      ],
      listPullRequests: async () => [
        {
          id: 1,
          index: 1,
          title: 'Inside window',
          state: 'open' as const,
          html_url: 'https://git.subcult.tv/subculture-collective/tool/pulls/1',
          created_at: '2026-05-25T09:00:00.000Z',
          updated_at: '2026-05-25T10:00:00.000Z',
        },
        {
          id: 2,
          index: 2,
          title: 'Future PR',
          state: 'open' as const,
          html_url: 'https://git.subcult.tv/subculture-collective/tool/pulls/2',
          created_at: '2026-05-25T13:00:00.000Z',
          updated_at: '2026-05-25T13:00:00.000Z',
        },
      ],
      listBranches: async () => [{ name: 'main' }],
      listCommits: async () => [
        {
          sha: 'inside',
          html_url: 'https://git.subcult.tv/subculture-collective/tool/commit/inside',
          created: '2026-05-25T11:00:00.000Z',
          commit: { message: 'Inside commit' },
        },
        {
          sha: 'future',
          html_url: 'https://git.subcult.tv/subculture-collective/tool/commit/future',
          created: '2026-05-25T13:00:00.000Z',
          commit: { message: 'Future commit' },
        },
      ],
    };

    const output = await crawlChanges(client as never, {
      baseUrl: 'https://git.subcult.tv',
      since: new Date('2026-05-24T00:00:00.000Z'),
      now: new Date('2026-05-25T12:00:00.000Z'),
      lookbackHours: 36,
      includeDiffs: false,
    });

    expect(output.changes.map((change) => change.id)).toEqual([
      'commit:subculture-collective/tool@inside',
      'pr:subculture-collective/tool#1',
    ]);
  });

  it('skips repositories last updated before the requested window', async () => {
    let listedBranches = false;
    const client = {
      searchRepos: async () => [
        {
          name: 'old-tool',
          full_name: 'subculture-collective/old-tool',
          private: false,
          html_url: 'https://git.subcult.tv/subculture-collective/old-tool',
          default_branch: 'main',
          updated_at: '2026-05-23T00:00:00.000Z',
          owner: { login: 'subculture-collective' },
        },
      ],
      listPullRequests: async () => [],
      listBranches: async () => {
        listedBranches = true;
        return [];
      },
      listCommits: async () => [],
    };

    const output = await crawlChanges(client as never, {
      baseUrl: 'https://git.subcult.tv',
      since: new Date('2026-05-24T00:00:00.000Z'),
      now: new Date('2026-05-25T12:00:00.000Z'),
      lookbackHours: 36,
    });

    expect(output.totals.repos).toBe(0);
    expect(listedBranches).toBe(false);
  });
});
