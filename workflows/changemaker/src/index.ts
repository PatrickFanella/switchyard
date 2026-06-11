export const projectName = 'changemaker';

export function describeProject(): string {
  return 'Daily dev-update generator for public Gitea activity.';
}

export { loadGenerateConfig, resolveGenerateWindow } from './config.js';
export { clusterChanges } from './changes/cluster.js';
export { GiteaClient } from './gitea/client.js';
export { crawlChanges } from './gitea/crawler.js';
export { ChangemakerStore } from './storage/sqlite.js';
export { summarizeChanges } from './llm/summarize.js';
export { createLlamaLineProvider, LlamaLineProvider } from './llm/llama-line.js';
export { writeDailyPost } from './writer/daily-post.js';
export { loadNarrativeState, saveNarrativeState, updateNarrativeState } from './narrative/state.js';
export { inspectChangesFile, inspectNarrativeFile } from './inspect.js';
export { createBackfillWindows } from './backfill.js';
export type { ChangeEvent, ChangesOutput } from './types.js';
