import { describe, expect, it } from 'vitest';
import { defaultNarrativeState, updateNarrativeState } from './state.js';
import type { ChangesOutput } from '../types.js';
import type { DailyPost } from '../writer/daily-post.js';

describe('updateNarrativeState', () => {
  it('persists the writer summary, active threads, and open PR future work', () => {
    const state = updateNarrativeState(defaultNarrativeState(), post(), output());

    expect(state.recentPosts[0]?.title).toBe('Daily Dev Update: Product progress');
    expect(state.recentPosts[0]?.summary).toBe(post().summary);
    expect(state.recentPosts[0]?.narrativeNotes?.watchingNext).toContain('Watch next');
    expect(state.activeThreads[0]?.id).toBe('product-progress');
    expect(state.futureWork[0]?.source).toBe('open_pr');
  });

  it('retains only seven recent posts', () => {
    const state = updateNarrativeState(stateWithSevenPosts(), post(), output());

    expect(state.recentPosts).toHaveLength(7);
    expect(state.recentPosts.map((recentPost) => recentPost.title)).toEqual([
      'Daily Dev Update: Product progress',
      'Post 1',
      'Post 2',
      'Post 3',
      'Post 4',
      'Post 5',
      'Post 6',
    ]);
  });
});

function post(): DailyPost {
  return {
    title: 'Daily Dev Update: Product progress',
    markdown: '# Daily Dev Update: Product progress\n',
    summary: 'People can get through setup with clearer guidance. — Adds setup copy and validation around the onboarding path.',
    tweetDrafts: [
      'Field report: People can get through setup with clearer guidance. Adds setup copy and validation around the onboarding path.',
      'Receipt: [[owner/repo#1]](https://git.subcult.tv/owner/repo/pulls/1) — People can get through setup with clearer guidance — Adds setup copy and validation around the onboarding path.',
      "Yesterday's thread was Earlier setup work made the first run less brittle. This matters because the update points to concrete changes, not broad claims: People can get through setup with clearer guidance. — Adds setup copy and validation around the onboarding path. Finish setup validation is still queued, so the next slice should be judged against that open work.",
      'Next up: Finish setup validation. Next pass, watch whether those open items close out or stay open. 1 open pull request is still open. The next queued item is finish setup validation. The useful signal is the status of the follow-up work.',
    ],
    socialPosts: [],
    coveredChangeIds: ['pr:owner/repo#1'],
    narrativeNotes: {
      whyItMatters: 'Why this matters',
      watchingNext: 'Watch next',
    },
  };
}

function output(): ChangesOutput {
  return {
    generatedAt: '2026-05-25T12:00:00.000Z',
    window: { from: '2026-05-24T00:00:00.000Z', to: '2026-05-25T12:00:00.000Z', lookbackHours: 36 },
    source: { baseUrl: 'https://git.subcult.tv' },
    totals: { repos: 1, pullRequests: 1, commits: 0, changes: 1 },
    clusters: [
      {
        theme: 'Product progress',
        eventIds: ['pr:owner/repo#1'],
        summary: 'Product progress: Add post writer',
        narrativeImportance: 'major',
      },
    ],
    changes: [
      {
        id: 'pr:owner/repo#1',
        type: 'pull_request',
        repo: 'owner/repo',
        title: 'Add post writer',
        state: 'open',
        prIndex: 1,
        url: 'https://git.subcult.tv/owner/repo/pulls/1',
        createdAt: '2026-05-25T10:00:00.000Z',
        updatedAt: '2026-05-25T10:00:00.000Z',
        covered: false,
      },
    ],
  };
}

function stateWithSevenPosts(): Parameters<typeof updateNarrativeState>[0] {
  return {
    projectContext: {
      name: 'changemaker',
      audience: 'builders and supporters who want understandable progress updates',
      tone: 'conversational, concrete, and candid',
    },
    activeThreads: [],
    futureWork: [],
    recentPosts: Array.from({ length: 7 }, (_, index) => ({
      date: `2026-06-0${index + 1}`,
      title: `Post ${index + 1}`,
      summary: `Summary ${index + 1}`,
      threadsMentioned: ['setup-polish'],
    })),
  };
}
