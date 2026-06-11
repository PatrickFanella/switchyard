import { describe, expect, it } from 'vitest';
import { buildPostWriterContext } from './post-context.js';
import type { NarrativeState } from '../narrative/state.js';
import type { ChangesOutput } from '../types.js';

describe('buildPostWriterContext', () => {
  it('keeps the last 7 post summaries and public-safe change evidence', () => {
    const context = buildPostWriterContext(output(), stateWithEightPosts());

    expect(context.history.recentPosts).toHaveLength(7);
    expect(context.history.recentPosts.map((post) => post.title)).toEqual([
      'Post 1',
      'Post 2',
      'Post 3',
      'Post 4',
      'Post 5',
      'Post 6',
      'Post 7',
    ]);
    expect(context.history.futureWork).toEqual([
      {
        id: 'open-pr:owner/repo#2',
        title: 'Finish setup validation',
        source: 'open_pr',
        summary: 'Follow-up work is still open for setup validation.',
        confidence: 'high',
      },
    ]);
    expect(context.changeEvidence[0]).toMatchObject({
      id: 'pr:owner/repo#1',
      repo: 'owner/repo',
      title: 'Add guided setup',
      publicSummary: 'People can get through setup with clearer guidance.',
      technicalSummary: 'Adds setup copy and validation around the onboarding path.',
      linkText: '[[owner/repo#1]](https://git.subcult.tv/owner/repo/pulls/1)',
    });
    expect(context.changeEvidence[0]).not.toHaveProperty('diff');
  });
});

function output(): ChangesOutput {
  return {
    generatedAt: '2026-06-09T12:00:00.000Z',
    window: { from: '2026-06-08T00:00:00.000Z', to: '2026-06-09T12:00:00.000Z', lookbackHours: 36 },
    source: { baseUrl: 'https://git.subcult.tv' },
    totals: { repos: 1, pullRequests: 1, commits: 1, changes: 2 },
    clusters: [
      {
        theme: 'Product progress',
        eventIds: ['pr:owner/repo#1', 'commit:owner/repo@abc123'],
        summary: 'Product progress: Guided setup and validation cleanup',
        narrativeImportance: 'major',
      },
    ],
    changes: [
      {
        id: 'pr:owner/repo#1',
        type: 'pull_request',
        repo: 'owner/repo',
        title: 'Add guided setup',
        url: 'https://git.subcult.tv/owner/repo/pulls/1',
        createdAt: '2026-06-09T09:00:00.000Z',
        updatedAt: '2026-06-09T10:00:00.000Z',
        prIndex: 1,
        covered: false,
        summary: {
          headline: 'Guided setup arrives',
          userFacingSummary: 'People can get through setup with clearer guidance.',
          technicalSummary: 'Adds setup copy and validation around the onboarding path.',
          impact: 'feature',
          notableFiles: ['src/setup.ts'],
          confidence: 0.9,
        },
      },
      {
        id: 'commit:owner/repo@abc123',
        type: 'commit',
        repo: 'owner/repo',
        title: 'Tighten validation copy',
        url: 'https://git.subcult.tv/owner/repo/commit/abc123',
        createdAt: '2026-06-09T11:00:00.000Z',
        updatedAt: '2026-06-09T11:00:00.000Z',
        sha: 'abc123',
        covered: false,
      },
    ],
  };
}

function stateWithEightPosts(): NarrativeState {
  return {
    projectContext: {
      name: 'changemaker',
      audience: 'builders and supporters who want understandable progress updates',
      tone: 'conversational, concrete, and candid',
    },
    activeThreads: [
      {
        id: 'setup-polish',
        title: 'Setup polish',
        status: 'active',
        summary: 'Recent work has made first-run setup less confusing.',
        lastSeen: '2026-06-08',
        evidenceLinks: ['https://git.subcult.tv/owner/repo/pulls/0'],
        expectedNext: 'Watch whether setup errors become easier to resolve.',
      },
    ],
    futureWork: [
      {
        id: 'open-pr:owner/repo#2',
        title: 'Finish setup validation',
        source: 'open_pr',
        summary: 'Follow-up work is still open for setup validation.',
        confidence: 'high',
      },
    ],
    recentPosts: Array.from({ length: 8 }, (_, index) => ({
      date: `2026-06-0${Math.min(index + 1, 9)}`,
      title: `Post ${index + 1}`,
      summary: `Summary ${index + 1}`,
      threadsMentioned: ['setup-polish'],
    })),
  };
}
