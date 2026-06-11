import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { dirname } from 'node:path';
import type { ChangesOutput } from '../types.js';
import type { DailyPost } from '../writer/daily-post.js';

export type NarrativeState = {
  projectContext: {
    name: string;
    audience: string;
    tone: string;
  };
  activeThreads: NarrativeThread[];
  futureWork: FutureWork[];
  recentPosts: RecentPost[];
};

export type NarrativeThread = {
  id: string;
  title: string;
  status: 'active' | 'paused' | 'resolved';
  summary: string;
  lastSeen: string;
  evidenceLinks: string[];
  expectedNext?: string;
};

export type FutureWork = {
  id: string;
  title: string;
  source: 'open_pr' | 'branch' | 'issue' | 'manual';
  summary: string;
  confidence: 'low' | 'medium' | 'high';
};

export type RecentPost = {
  date: string;
  title: string;
  summary: string;
  threadsMentioned: string[];
  narrativeNotes?: {
    whyItMatters: string;
    watchingNext: string;
  };
};

export function defaultNarrativeState(): NarrativeState {
  return {
    projectContext: {
      name: 'changemaker',
      audience: 'technical supporters following product progress',
      tone: 'technical, candid, product-progress, Patreon-friendly',
    },
    activeThreads: [],
    futureWork: [],
    recentPosts: [],
  };
}

export async function loadNarrativeState(path: string): Promise<NarrativeState> {
  try {
    return JSON.parse(await readFile(path, 'utf8')) as NarrativeState;
  } catch (error) {
    if (isNodeError(error) && error.code === 'ENOENT') {
      return defaultNarrativeState();
    }
    throw error;
  }
}

function isNodeError(error: unknown): error is NodeJS.ErrnoException {
  return error instanceof Error && 'code' in error;
}

export async function saveNarrativeState(path: string, state: NarrativeState): Promise<void> {
  await mkdir(dirname(path), { recursive: true });
  await writeFile(path, `${JSON.stringify(state, null, 2)}\n`, 'utf8');
}

export function updateNarrativeState(state: NarrativeState, post: DailyPost, output: ChangesOutput): NarrativeState {
  const date = output.generatedAt.slice(0, 10);
  const threads = mergeThreads(state.activeThreads, output, date);
  const threadIds = output.clusters.map((cluster) => threadId(cluster.theme));

  return {
    ...state,
    activeThreads: threads,
    futureWork: deriveFutureWork(state.futureWork, output),
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
  };
}

function mergeThreads(existing: NarrativeThread[], output: ChangesOutput, date: string): NarrativeThread[] {
  const byId = new Map(existing.map((thread) => [thread.id, thread]));
  for (const cluster of output.clusters) {
    const id = threadId(cluster.theme);
    const links = cluster.eventIds
      .map((eventId) => output.changes.find((change) => change.id === eventId)?.url)
      .filter((url): url is string => Boolean(url));
    byId.set(id, {
      id,
      title: cluster.theme,
      status: 'active',
      summary: cluster.summary,
      lastSeen: date,
      evidenceLinks: [...new Set([...(byId.get(id)?.evidenceLinks ?? []), ...links])].slice(-10),
      expectedNext: 'Follow uncovered PRs, branch commits, and related changes in the next update.',
    });
  }
  return [...byId.values()];
}

function deriveFutureWork(existing: FutureWork[], output: ChangesOutput): FutureWork[] {
  const openWork = output.changes
    .filter((change) => change.type === 'pull_request' && change.state === 'open')
    .map((change) => ({
      id: `open-pr:${change.id}`,
      title: change.title,
      source: 'open_pr' as const,
      summary: `Open pull request still in motion: ${change.title}`,
      confidence: 'high' as const,
    }));
  const merged = new Map([...existing, ...openWork].map((work) => [work.id, work]));
  return [...merged.values()].slice(0, 20);
}

function threadId(theme: string): string {
  return theme.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '');
}
