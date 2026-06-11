import type { FutureWork, NarrativeState, NarrativeThread, RecentPost } from '../narrative/state.js';
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
    futureWork: FutureWork[];
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
      futureWork: (state?.futureWork ?? []).slice(0, 7),
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
