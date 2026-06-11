import { describe, expect, it } from 'vitest';
import { writeDailyPost } from './daily-post.js';
import type { NarrativeState } from '../narrative/state.js';
import type { ChangesOutput } from '../types.js';

describe('writeDailyPost', () => {
  it('writes a conversational narrative post with tweet drafts and wiki-style links', () => {
    const post = writeDailyPost(output(), { narrativeState: narrativeState() });

    expect(post.markdown).toContain('# Jun 8 field report: Product progress');
    expect(post.markdown).toContain('## Introduction');
    expect(post.markdown).toContain("Yesterday's thread was: Earlier setup work made the first run less brittle.");
    expect(post.markdown).toContain('The queue still has finish setup validation waiting, so the tone stays measured.');
    expect(post.markdown).toContain('## What changed');
    expect(post.markdown).toContain('People can get through setup with clearer guidance');
    expect(post.markdown).toContain('[[owner/repo#1]](https://git.subcult.tv/owner/repo/pulls/1)');
    expect(post.markdown).toContain('## Why it matters');
    expect(post.markdown).toContain('This matters because the update points to concrete changes, not broad claims');
    expect(post.markdown).toContain('## What to watch next');
    expect(post.markdown).toContain('## Conclusion');
    expect(post.markdown).toContain('## Twitter / X posts');
    expect(post.markdown).toContain('### Post 1 — 09:00');
    expect(post.markdown).toContain('## Receipts');
    expect(post.summary).toContain('People can get through setup with clearer guidance');
    expect(post.summary).toContain('Adds setup copy and validation around the onboarding path');
    expect(post.tweetDrafts).toHaveLength(4);
    expect(post.socialPosts[0]).toMatchObject({ channel: 'twitter', suggestedTime: '09:00' });
    expect(post.tweetDrafts[0]).toContain('Field report: People can get through setup with clearer guidance.');
    expect(post.tweetDrafts.every((tweet) => tweet.length <= 280)).toBe(true);
    expect(post.coveredChangeIds).toEqual(['pr:owner/repo#1', 'commit:owner/repo@abc123']);
  });

  it('does not inflate quiet windows', () => {
    const post = writeDailyPost(quietOutput());

    expect(post.markdown).toContain('Quiet window');
    expect(post.markdown).toContain('No uncovered public changes landed.');
    expect(post.markdown).toContain('## Twitter / X posts');
    expect(post.markdown).toContain('_No social posts for a quiet window._');
    expect(post.summary).toBe('Quiet window: no uncovered public changes landed.');
    expect(post.tweetDrafts).toEqual([]);
    expect(post.narrativeNotes.whyItMatters).toContain('do not inflate public narrative');
  });

  it('can write extra period headers without the social section', () => {
    const post = writeDailyPost(output(), {
      reportKind: 'weekly',
      reportDateOffsetDays: 1,
      includeSocialSection: false,
    });

    expect(post.markdown).toContain('# Jun 8 weekly field report: Product progress');
    expect(post.markdown).not.toContain('## Twitter / X posts');
    expect(post.socialPosts).toHaveLength(4);
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

function quietOutput(): ChangesOutput {
  return {
    generatedAt: '2026-06-09T12:00:00.000Z',
    window: { from: '2026-06-08T00:00:00.000Z', to: '2026-06-09T12:00:00.000Z', lookbackHours: 36 },
    source: { baseUrl: 'https://git.subcult.tv' },
    totals: { repos: 0, pullRequests: 0, commits: 0, changes: 0 },
    clusters: [],
    changes: [],
  };
}

function narrativeState(): NarrativeState {
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
    recentPosts: [
      {
        date: '2026-06-08',
        title: 'Jun 8 field report',
        summary: 'Earlier setup work made the first run less brittle.',
        threadsMentioned: ['setup-polish'],
      },
    ],
  };
}
