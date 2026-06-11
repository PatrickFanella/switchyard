import { readFile } from 'node:fs/promises';
import type { ChangesOutput } from './types.js';
import type { NarrativeState } from './narrative/state.js';

export async function inspectChangesFile(path: string): Promise<string> {
  const output = JSON.parse(await readFile(path, 'utf8')) as ChangesOutput;
  const lines = [
    `Changes: ${output.totals.changes}`,
    `Repos: ${output.totals.repos}`,
    `PRs: ${output.totals.pullRequests}`,
    `Commits: ${output.totals.commits}`,
    `Window: ${output.window.from} -> ${output.window.to}`,
    'Clusters:',
    ...output.clusters.map((cluster) => `- ${cluster.theme} (${cluster.eventIds.length}) ${cluster.narrativeImportance}`),
  ];
  return `${lines.join('\n')}\n`;
}

export async function inspectNarrativeFile(path: string): Promise<string> {
  const state = JSON.parse(await readFile(path, 'utf8')) as NarrativeState;
  const lines = [
    `Project: ${state.projectContext.name}`,
    `Active threads: ${state.activeThreads.length}`,
    ...state.activeThreads.map((thread) => `- ${thread.title}: ${thread.status}, last seen ${thread.lastSeen}`),
    `Future work: ${state.futureWork.length}`,
    `Recent posts: ${state.recentPosts.length}`,
  ];
  return `${lines.join('\n')}\n`;
}
