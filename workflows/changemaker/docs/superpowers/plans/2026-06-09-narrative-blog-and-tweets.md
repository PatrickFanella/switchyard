# Narrative Blog and Tweet Drafts Implementation Plan

> **For agentic workers:** Execute this plan task-by-task. Recommended path:
> dispatch a fresh subagent per task, review each result with `review-quality`,
> then continue. For complex multi-agent splits, use
> `parallel-feature-development`, `team-composition-patterns`, and
> `team-communication-protocols`. Steps use checkbox (`- [ ]`) syntax for
> tracking.

**Goal:** Turn the daily commit/PR digest into a richer narrative blog post plus 3-4 social drafts, using the last 7 completed post summaries for continuity.

**Architecture:** Keep Gitea crawling and per-change summaries as-is. Add a focused post-context layer that converts `ChangesOutput` + `NarrativeState` into writer-safe inputs, then extend `writeDailyPost()` to render a conversational intro/body/conclusion, generate tweet drafts, and persist a concise completed-post summary back into narrative memory. Keep raw diffs out of the post writer.

**Tech Stack:** TypeScript ESM, Vitest, Commander CLI, existing Gitea API crawler, existing SQLite cache, existing Markdown artifact writer.

---

## Current Pipeline Map

- CLI entry point: `src/cli.ts:18-179`, especially `generate` command at `src/cli.ts:27-125`.
- Server generation path: `src/server/http.ts:573-689`, especially `createGenerationRunner()`.
- Change collection: `src/gitea/crawler.ts`.
- LLM per-change summaries: `src/llm/summarize.ts`, `src/llm/openai.ts`, `src/llm/llama-line.ts`.
- Theme grouping: `src/changes/cluster.ts`.
- Current Markdown writer: `src/writer/daily-post.ts`.
- Narrative memory: `src/narrative/state.ts`.
- Artifact writing: `src/output.ts`.
- Product docs: `README.md`, `docs/implementation-plan.md`.

## File Structure

- Modify: `src/writer/daily-post.ts`
  - Owns public Markdown rendering, post summary generation, and tweet draft generation.
  - Keep deterministic rendering first so tests are stable. LLM prompt work can layer on later through provider boundaries.
- Create: `src/writer/post-context.ts`
  - Converts `ChangesOutput` + optional `NarrativeState` into a compact `PostWriterContext`.
  - Includes the last 7 recent post summaries, active threads, future work, major clusters, change evidence, and public-safe explanations.
- Modify: `src/narrative/state.ts`
  - Store 7 recent posts instead of 5.
  - Use the writer-produced `post.summary` when updating history.
- Modify: `src/cli.ts`
  - Load narrative state before writing a post so the writer can use recent history.
- Modify: `src/server/http.ts`
  - Mirror the CLI order in `createGenerationRunner()`.
- Modify: `src/writer/daily-post.test.ts`
  - Assert intro/body/conclusion structure and tweet drafts.
- Create: `src/writer/post-context.test.ts`
  - Assert history trimming and context shape.
- Modify: `src/narrative/state.test.ts`
  - Assert 7-summary retention and writer summary persistence.
- Modify: `README.md`
  - Describe richer post output and tweet drafts.
- Modify: `docs/implementation-plan.md`
  - Update post scope, narrative memory, output, and implementation phases.

## Acceptance Criteria

- Generated Markdown has clearly labeled sections:
  - `## Introduction`
  - `## What changed`
  - `## Why it matters`
  - `## What to watch next`
  - `## Conclusion`
  - `## Tweet drafts`
  - `## Receipts`
- The post is conversational and explanatory, not a dry changelog.
- The writer receives up to 7 prior post summaries from narrative state.
- Each completed post stores a concise summary in `NarrativeState.recentPosts[0].summary`.
- `DailyPost` includes `summary` and exactly 3-4 `tweetDrafts` for non-empty change windows.
- Quiet windows still avoid fake momentum and may produce no tweet drafts.
- CLI and server both use the same enriched writer behavior.
- `make verify` passes.

---

### Task 1: Add Post Writer Context

**Files:**
- Create: `src/writer/post-context.ts`
- Create: `src/writer/post-context.test.ts`

- [ ] **Step 1: Write the failing context tests**

Create `src/writer/post-context.test.ts`:

```ts
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
    futureWork: [],
    recentPosts: Array.from({ length: 8 }, (_, index) => ({
      date: `2026-06-0${Math.min(index + 1, 9)}`,
      title: `Post ${index + 1}`,
      summary: `Summary ${index + 1}`,
      threadsMentioned: ['setup-polish'],
    })),
  };
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
pnpm vitest run src/writer/post-context.test.ts
```

Expected: fail because `src/writer/post-context.ts` does not exist.

- [ ] **Step 3: Implement the context builder**

Create `src/writer/post-context.ts`:

```ts
import type { NarrativeState, NarrativeThread, RecentPost } from '../narrative/state.js';
import type { ChangeCluster, ChangeEvent, ChangesOutput } from '../types.js';

export type PostWriterContext = {
  project: {
    name: string;
    audience: string;
    tone: string;
  };
  window: ChangesOutput['window'];
  totals: ChangesOutput['totals'];
  history: {
    recentPosts: RecentPost[];
    activeThreads: NarrativeThread[];
  };
  majorClusters: ChangeCluster[];
  backgroundClusters: ChangeCluster[];
  changeEvidence: PublicChangeEvidence[];
};

export type PublicChangeEvidence = {
  id: string;
  repo: string;
  title: string;
  type: ChangeEvent['type'];
  state?: ChangeEvent['state'];
  publicSummary: string;
  technicalSummary: string;
  impact: string;
  linkText: string;
};

export function buildPostWriterContext(output: ChangesOutput, state?: NarrativeState): PostWriterContext {
  return {
    project: state?.projectContext ?? {
      name: 'changemaker',
      audience: 'technical supporters following product progress',
      tone: 'conversational, concrete, and candid',
    },
    window: output.window,
    totals: output.totals,
    history: {
      recentPosts: (state?.recentPosts ?? []).slice(0, 7),
      activeThreads: (state?.activeThreads ?? []).filter((thread) => thread.status === 'active').slice(0, 7),
    },
    majorClusters: output.clusters.filter((cluster) => cluster.narrativeImportance === 'major'),
    backgroundClusters: output.clusters.filter((cluster) => cluster.narrativeImportance !== 'major'),
    changeEvidence: output.changes.map(toPublicChangeEvidence),
  };
}

function toPublicChangeEvidence(change: ChangeEvent): PublicChangeEvidence {
  return {
    id: change.id,
    repo: change.repo,
    title: change.title,
    type: change.type,
    state: change.state,
    publicSummary: change.summary?.userFacingSummary ?? change.title,
    technicalSummary: change.summary?.technicalSummary ?? change.body ?? change.title,
    impact: change.summary?.impact ?? 'other',
    linkText: inlineChange(change),
  };
}

function inlineChange(change: ChangeEvent): string {
  if (change.type === 'pull_request' && change.prIndex !== undefined) {
    return `[[${change.repo}#${change.prIndex}]](${change.url})`;
  }
  if (change.sha) {
    return `[[${change.repo}@${change.sha.slice(0, 7)}]](${change.url})`;
  }
  return `[[${change.repo}]](${change.url})`;
}
```

- [ ] **Step 4: Run test to verify it passes**

Run:

```bash
pnpm vitest run src/writer/post-context.test.ts
```

Expected: pass.

---

### Task 2: Extend DailyPost With Summary and Tweet Drafts

**Files:**
- Modify: `src/writer/daily-post.ts`
- Modify: `src/writer/daily-post.test.ts`

- [ ] **Step 1: Write failing writer tests**

Modify `src/writer/daily-post.test.ts`:

```ts
import { describe, expect, it } from 'vitest';
import { writeDailyPost } from './daily-post.js';
import type { NarrativeState } from '../narrative/state.js';
import type { ChangesOutput } from '../types.js';

describe('writeDailyPost', () => {
  it('writes a conversational narrative post with tweet drafts and wiki-style links', () => {
    const post = writeDailyPost(output(), { narrativeState: narrativeState() });

    expect(post.markdown).toContain('# Jun 9 field report: Product progress');
    expect(post.markdown).toContain('## Introduction');
    expect(post.markdown).toContain('## What changed');
    expect(post.markdown).toContain('## Why it matters');
    expect(post.markdown).toContain('## What to watch next');
    expect(post.markdown).toContain('## Conclusion');
    expect(post.markdown).toContain('## Tweet drafts');
    expect(post.markdown).toContain('## Receipts');
    expect(post.markdown).toContain('Yesterday\'s thread was: Earlier setup work made the first run less brittle.');
    expect(post.markdown).toContain('Guided setup arrives');
    expect(post.markdown).toContain('[[owner/repo#1]](https://git.subcult.tv/owner/repo/pulls/1)');
    expect(post.summary).toContain('Guided setup arrives');
    expect(post.tweetDrafts).toHaveLength(4);
    expect(post.tweetDrafts[0]).toContain('Guided setup arrives');
    expect(post.tweetDrafts.every((tweet) => tweet.length <= 280)).toBe(true);
    expect(post.coveredChangeIds).toEqual(['pr:owner/repo#1', 'commit:owner/repo@abc123']);
  });

  it('does not inflate quiet windows', () => {
    const post = writeDailyPost(quietOutput());

    expect(post.markdown).toContain('Quiet window');
    expect(post.markdown).toContain('No public commits or pull requests landed in this window.');
    expect(post.summary).toBe('Quiet window: no uncovered public changes landed.');
    expect(post.tweetDrafts).toEqual([]);
    expect(post.narrativeNotes.whyItMatters).toContain('do not inflate public narrative');
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
      audience: 'technical supporters and curious non-developers',
      tone: 'conversational and approachable',
    },
    activeThreads: [],
    futureWork: [],
    recentPosts: [
      {
        date: '2026-06-08',
        title: 'Jun 8 field report: Setup polish',
        summary: 'Earlier setup work made the first run less brittle.',
        threadsMentioned: ['setup-polish'],
      },
    ],
  };
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
pnpm vitest run src/writer/daily-post.test.ts
```

Expected: fail because `writeDailyPost()` does not accept narrative state and `DailyPost` does not expose `summary` or `tweetDrafts`.

- [ ] **Step 3: Implement enriched writer**

Modify `src/writer/daily-post.ts` with these interface changes and helper calls. Keep existing helpers that still apply, but update duplicate `inlineChange()` to import context evidence instead.

```ts
import type { NarrativeState } from '../narrative/state.js';
import type { ChangeCluster, ChangeEvent, ChangesOutput } from '../types.js';
import { buildPostWriterContext, type PostWriterContext } from './post-context.js';

export type DailyPost = {
  title: string;
  markdown: string;
  coveredChangeIds: string[];
  narrativeNotes: DailyPostNarrativeNotes;
  summary: string;
  tweetDrafts: string[];
};

export type DailyPostOptions = {
  narrativeState?: NarrativeState;
};

export function writeDailyPost(output: ChangesOutput, options: DailyPostOptions = {}): DailyPost {
  const context = buildPostWriterContext(output, options.narrativeState);
  const title = titleForOutput(output);
  const tweetDrafts = tweetDraftsForContext(context);
  const summary = summaryForContext(context);
  const markdown = [`# ${title}`, '', ...bodySectionsForOutput(output, context, tweetDrafts)].join('\n');

  return {
    title,
    markdown: `${markdown.trim()}\n`,
    coveredChangeIds: output.changes.map((change) => change.id),
    narrativeNotes: narrativeNotesForOutput(output),
    summary,
    tweetDrafts,
  };
}
```

Replace `bodySectionsForOutput(output: ChangesOutput)` with:

```ts
function bodySectionsForOutput(output: ChangesOutput, context: PostWriterContext, tweetDrafts: string[]): string[] {
  if (output.changes.length === 0) {
    return [
      '## Introduction',
      '',
      'Quiet window. No public commits or pull requests landed in this window.',
      '',
      '## Conclusion',
      '',
      'No fake motion today; the next update should pick up when public work starts moving again.',
      '',
      '## Receipts',
      '',
      '- No uncovered changes',
    ];
  }

  const leadClusters = context.majorClusters.length ? context.majorClusters : output.clusters.slice(0, 2);
  const projectGroups = topProjectGroups(output.changes, 5);
  const receiptLines = linkLines(output.changes, 12);

  return [
    '## Introduction',
    '',
    introductionForContext(context, leadClusters),
    '',
    '## What changed',
    '',
    clusterSection(leadClusters, output.changes),
    '',
    projectSection(projectGroups),
    '',
    '## Why it matters',
    '',
    whyItMattersForContext(context),
    '',
    '## What to watch next',
    '',
    watchingNextForContext(output, context),
    '',
    '## Conclusion',
    '',
    conclusionForContext(context),
    '',
    '## Tweet drafts',
    '',
    ...tweetDrafts.map((tweet, index) => `${index + 1}. ${tweet}`),
    '',
    '## Receipts',
    '',
    ...receiptLines,
  ].filter((line) => line !== undefined);
}
```

Add these helpers below the existing functions:

```ts
function introductionForContext(context: PostWriterContext, leadClusters: ChangeCluster[]): string {
  const history = context.history.recentPosts[0]?.summary
    ? ` Yesterday's thread was: ${context.history.recentPosts[0].summary}`
    : '';
  return `This update covers ${humanCount(context.totals.changes, 'public change')} across ${humanCount(context.totals.repos, 'repo')}. The headline is ${inlineClusterList(leadClusters)}: practical progress that makes the work easier to understand, safer to use, or steadier to build on.${history}`;
}

function whyItMattersForContext(context: PostWriterContext): string {
  const first = context.changeEvidence[0];
  if (!first) return 'There is no new public work to interpret in this window.';
  return `${first.publicSummary} That matters because progress updates are easier to follow when they explain the human effect of the code, not just the commit titles. This window moved the project from raw implementation details toward clearer behavior and a more legible path for people watching from the outside.`;
}

function watchingNextForContext(output: ChangesOutput, context: PostWriterContext): string {
  const open = output.changes.filter((change) => change.type === 'pull_request' && change.state === 'open');
  const expected = context.history.activeThreads[0]?.expectedNext;
  if (open.length > 0) {
    return `${humanCount(open.length, 'open pull request')} still needs follow-up, so treat that work as in motion rather than shipped.${expected ? ` ${expected}` : ''}`;
  }
  return expected ?? 'Next, watch whether these changes turn into fewer rough edges for the people using or maintaining the project.';
}

function conclusionForContext(context: PostWriterContext): string {
  const first = context.changeEvidence[0];
  return first
    ? `The short version: ${first.title} is not just another line in the log. It is part of a steadier story about turning scattered code work into visible product progress.`
    : 'The short version: nothing public landed, and the honest update is to wait for real movement.';
}

function summaryForContext(context: PostWriterContext): string {
  if (context.changeEvidence.length === 0) return 'Quiet window: no uncovered public changes landed.';
  const headlines = context.changeEvidence.slice(0, 3).map((change) => change.title);
  return `${headlines.join('; ')}. ${context.totals.changes} public changes moved across ${context.totals.repos} repos.`;
}

function tweetDraftsForContext(context: PostWriterContext): string[] {
  if (context.changeEvidence.length === 0) return [];
  const first = context.changeEvidence[0];
  const theme = context.majorClusters[0]?.theme ?? 'Code progress';
  return [
    `${first.title}: today's update turns commit-log detail into a clearer progress story. What part of product work do you wish more projects explained better?`,
    `New dev update: ${theme.toLowerCase()} across ${humanCount(context.totals.repos, 'repo')}, with the focus on impact rather than raw commit noise.`,
    `${first.publicSummary} Small changes can still shift the experience when they remove confusion or sharpen the path forward.`,
    `Behind the scenes today: ${humanCount(context.totals.changes, 'public change')}, ${humanCount(context.totals.pullRequests, 'pull request')}, and a clearer story about why the work matters.`,
  ].map(trimTweet);
}

function trimTweet(value: string): string {
  return value.length <= 280 ? value : `${value.slice(0, 277).trimEnd()}…`;
}
```

Adjust `clusterSection()` so it no longer emits its own header label:

```ts
function clusterSection(clusters: ChangeCluster[], changes: ChangeEvent[]): string {
  if (clusters.length === 0) return '- No major thread identified.';
  return clusters
    .map((cluster) => {
      const events = cluster.eventIds.map((id) => changes.find((change) => change.id === id)).filter(Boolean);
      const examples = events
        .slice(0, 2)
        .map((event) => (event ? inlineChange(event) : undefined))
        .filter(Boolean)
        .join(' and ');
      return `- **${sentenceCase(cluster.theme)}.** ${stripThemePrefix(cluster.summary)}${examples ? ` (${examples})` : ''}`;
    })
    .join('\n');
}
```

- [ ] **Step 4: Run writer tests**

Run:

```bash
pnpm vitest run src/writer/daily-post.test.ts src/writer/post-context.test.ts
```

Expected: pass.

---

### Task 3: Persist 7 Recent Summaries in Narrative State

**Files:**
- Modify: `src/narrative/state.ts`
- Modify: `src/narrative/state.test.ts`

- [ ] **Step 1: Write failing narrative-state test**

Replace `src/narrative/state.test.ts` with:

```ts
import { describe, expect, it } from 'vitest';
import { defaultNarrativeState, updateNarrativeState } from './state.js';
import type { ChangesOutput } from '../types.js';
import type { DailyPost } from '../writer/daily-post.js';

describe('updateNarrativeState', () => {
  it('adds recent posts, active threads, open PR future work, and writer summary', () => {
    const state = updateNarrativeState(defaultNarrativeState(), post('A useful public summary'), output());

    expect(state.recentPosts[0]?.title).toBe('Daily Dev Update: Product progress');
    expect(state.recentPosts[0]?.summary).toBe('A useful public summary');
    expect(state.recentPosts[0]?.narrativeNotes?.watchingNext).toContain('Watch next');
    expect(state.activeThreads[0]?.id).toBe('product-progress');
    expect(state.futureWork[0]?.source).toBe('open_pr');
  });

  it('keeps 7 recent post summaries', () => {
    let state = defaultNarrativeState();

    for (let index = 0; index < 8; index += 1) {
      state = updateNarrativeState(state, post(`Summary ${index}`), {
        ...output(),
        generatedAt: `2026-06-${String(index + 1).padStart(2, '0')}T12:00:00.000Z`,
      });
    }

    expect(state.recentPosts).toHaveLength(7);
    expect(state.recentPosts[0]?.summary).toBe('Summary 7');
    expect(state.recentPosts.at(-1)?.summary).toBe('Summary 1');
  });
});

function post(summary: string): DailyPost {
  return {
    title: 'Daily Dev Update: Product progress',
    markdown: '# Daily Dev Update: Product progress\n',
    coveredChangeIds: ['pr:owner/repo#1'],
    summary,
    tweetDrafts: ['Draft one', 'Draft two', 'Draft three'],
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
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
pnpm vitest run src/narrative/state.test.ts
```

Expected: fail because `DailyPost.summary` is not persisted and recent posts are capped at 5.

- [ ] **Step 3: Update narrative state**

Modify `src/narrative/state.ts` inside `updateNarrativeState()`:

```ts
    recentPosts: [
      {
        date,
        title: post.title,
        summary: post.summary,
        threadsMentioned: threadIds,
        narrativeNotes: post.narrativeNotes,
      },
      ...state.recentPosts,
    ].slice(0, 7),
```

Keep `summarizePost(output)` in the file only if another caller needs it. If TypeScript reports it as unused, delete the function.

- [ ] **Step 4: Run narrative tests**

Run:

```bash
pnpm vitest run src/narrative/state.test.ts src/writer/daily-post.test.ts
```

Expected: pass.

---

### Task 4: Wire Narrative State Into CLI and Server Writers

**Files:**
- Modify: `src/cli.ts`
- Modify: `src/server/http.ts`
- Modify: `src/server/http.test.ts`

- [ ] **Step 1: Write failing server/CLI-oriented test**

In `src/server/http.test.ts`, add an assertion to the run API flow test that the generated post receives narrative state. If the current test uses dependency injection for `writePostOutput`, add this shape to the fake post returned by the injected writer:

```ts
const enrichedPost = {
  title: 'Jun 9 field report: Product progress',
  markdown: '# Jun 9 field report: Product progress\n\n## Tweet drafts\n\n1. Draft\n',
  coveredChangeIds: ['pr:owner/repo#1'],
  summary: 'Guided setup moved forward with clearer first-run behavior.',
  tweetDrafts: ['Draft one', 'Draft two', 'Draft three'],
  narrativeNotes: {
    whyItMatters: 'The public impact is clearer setup.',
    watchingNext: 'Watch whether the setup path gets smoother.',
  },
};
```

Expected assertion:

```ts
expect(writtenPost?.summary).toBe('Guided setup moved forward with clearer first-run behavior.');
```

- [ ] **Step 2: Run targeted server test**

Run:

```bash
pnpm vitest run src/server/http.test.ts
```

Expected: fail if the test still assumes the old `DailyPost` shape or if narrative state is loaded after writing.

- [ ] **Step 3: Update CLI generation order**

Modify `src/cli.ts:98-107` so narrative state is loaded before `writeDailyPost()`:

```ts
        const changesPath = await writeChangesOutput(output, config.outDir);
        const state = config.post && config.narrativeUpdate ? await loadNarrativeState(config.narrativeStatePath) : undefined;
        const post = config.post ? writeDailyPost(output, { narrativeState: state }) : undefined;
        const postPath = post ? await writePostOutput(post, output.generatedAt, config.outDir) : undefined;
        let narrativePath: string | undefined;
        if (post && config.narrativeUpdate && state) {
          const nextState = updateNarrativeState(state, post, output);
          await saveNarrativeState(config.narrativeStatePath, nextState);
          narrativePath = await writeNarrativeStateOutput(nextState, output.generatedAt, config.outDir);
        }
```

- [ ] **Step 4: Update server generation order**

Modify `src/server/http.ts:665-678` similarly:

```ts
      const changesPath = await writeServerChangesOutput(output, generateConfig.outDir);
      log(`wrote ${changesPath}`);

      const state =
        generateConfig.post && generateConfig.narrativeUpdate
          ? await loadServerNarrativeState(generateConfig.narrativeStatePath)
          : undefined;
      const post = generateConfig.post ? writeDailyPost(output, { narrativeState: state }) : undefined;
      const postPath = post ? await writeServerPostOutput(post, output.generatedAt, generateConfig.outDir) : undefined;
      if (postPath) log(`wrote ${postPath}`);

      if (post && generateConfig.narrativeUpdate && state) {
        const nextState = updateServerNarrativeState(state, post, output);
        await saveServerNarrativeState(generateConfig.narrativeStatePath, nextState);
        const narrativePath = await writeServerNarrativeStateOutput(nextState, output.generatedAt, generateConfig.outDir);
        log(`wrote ${narrativePath}`);
      }
```

- [ ] **Step 5: Run targeted tests**

Run:

```bash
pnpm vitest run src/server/http.test.ts src/writer/daily-post.test.ts src/narrative/state.test.ts
```

Expected: pass.

---

### Task 5: Update Docs and Product Spec

**Files:**
- Modify: `README.md`
- Modify: `docs/implementation-plan.md`

- [ ] **Step 1: Update `docs/implementation-plan.md` behavior**

Modify the opening paragraph and narrative memory sections to say:

```md
Changemaker is a daily dev-update generator for public Gitea activity. It crawls repositories, reads pull requests and commits, summarizes code changes, maintains narrative continuity, and writes a conversational Markdown blog post with 3-4 tweet drafts for supporters who want understandable product progress, not raw commit logs.
```

Change writer rules to:

```md
Writer rules:

- transform commit and PR facts into an introduction/body/conclusion narrative
- connect today's changes to active threads and the last 7 recent post summaries
- explain technical work in accessible language without hiding source links
- mention likely next steps only when supported by open PRs, branches, issues, or narrative state
- do not claim open or unmerged work has shipped
- include 3-4 concise tweet drafts for non-empty updates
- include inline wiki-style source links
```

Change output section to remove the stale `out/YYYY-MM-DD-post.json` line unless a JSON post artifact is implemented in the same branch:

```text
out/YYYY-MM-DD-post.md
out/YYYY-MM-DD-changes.json
out/YYYY-MM-DD-narrative-state.json
```

- [ ] **Step 2: Update `README.md` output description**

Add a concise paragraph near the CLI examples:

```md
When `--post` is enabled, the Markdown post is written as a narrative update rather than a raw changelog. It includes an introduction, a clear explanation of what changed, why it matters, what to watch next, a conclusion, 3-4 tweet drafts, and source receipts. Narrative state keeps the last 7 post summaries so each daily run can connect to the recent story without rereading old Markdown.
```

- [ ] **Step 3: Run docs-adjacent checks**

Run:

```bash
pnpm vitest run src/writer/daily-post.test.ts src/narrative/state.test.ts
```

Expected: pass.

---

### Task 6: Full Verification

**Files:**
- No new files.

- [ ] **Step 1: Run canonical verification**

Run:

```bash
make verify
```

Expected: all formatting, type-checking, linting, and tests pass.

- [ ] **Step 2: If `make verify` fails on tweet length or brittle copy assertions**

Update assertions to test structure and essential facts rather than exact full paragraphs. Keep exact checks for headings, link format, history inclusion, `tweetDrafts.length`, and `tweet.length <= 280`.

- [ ] **Step 3: Manual smoke run with fixture-like output**

Run a local generation command using a narrow repo/window available to the developer environment:

```bash
pnpm build
node dist/cli.js generate --owner subculture-collective --out-dir out-smoke --post --no-diffs --include-covered --now-window
```

Expected:

- `out-smoke/YYYY-MM-DD-post.md` exists.
- The Markdown contains `## Tweet drafts`.
- The Markdown contains `## Receipts`.
- The command does not commit files under `out-smoke/`.

---

## Follow-up Option: LLM Composed Final Drafts

This plan intentionally improves the existing deterministic writer first because it is testable and keeps the daily service reliable. If the deterministic prose is still not expressive enough, add a second plan for an optional `PostComposer` provider after this lands:

- Extend `LlmProvider` with `writePost(input: PostWriterContext): Promise<DailyPostDraft>`.
- Wire runtime prompt `post-voice.md` into OpenAI and llama-line providers.
- Validate LLM output with a strict schema containing `title`, `markdown`, `summary`, and `tweetDrafts`.
- Fall back to deterministic `writeDailyPost()` on provider failure so the 24-hour agent still publishes.

## Self-Review

- Spec coverage: The plan covers richer blog structure, broader accessibility, last 7 summaries, completed-post summary persistence, 3-4 tweet drafts, CLI/server paths, docs, and canonical verification.
- Placeholder scan: No red-flag placeholder language or open-ended implementation steps remain. The only conditional language is explicit fallback guidance for failures.
- Type consistency: `DailyPost.summary`, `DailyPost.tweetDrafts`, `DailyPostOptions`, `PostWriterContext`, and `buildPostWriterContext()` are introduced before later tasks reference them.
