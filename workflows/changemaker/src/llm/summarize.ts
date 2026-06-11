import { createHash } from 'node:crypto';
import type { ChangemakerStore } from '../storage/sqlite.js';
import type { ChangeEvent, ChangeSummary } from '../types.js';
import { CHANGE_SUMMARY_PROMPT_VERSION, type LlmProvider } from './provider.js';

export async function summarizeChanges(input: {
  changes: ChangeEvent[];
  provider: LlmProvider;
  store: ChangemakerStore;
  retry?: RetryOptions;
}): Promise<ChangeEvent[]> {
  const enriched: ChangeEvent[] = [];
  for (const change of input.changes) {
    const cacheKey = changeSummaryCacheKey(change, input.provider);
    const cached = input.store.getProviderCache<ChangeSummary>(cacheKey);
    if (cached) {
      enriched.push({ ...change, summary: cached });
      continue;
    }

    const summary = await summarizeChangeWithRetry(change, input.provider, input.retry);
    input.store.setProviderCache({
      cacheKey,
      provider: input.provider.name,
      model: input.provider.model,
      promptVersion: CHANGE_SUMMARY_PROMPT_VERSION,
      value: summary,
    });
    enriched.push({ ...change, summary });
  }
  return enriched;
}

export type RetryOptions = {
  attempts?: number;
  delayMs?: number;
  onRetry?: (event: { changeId: string; attempt: number; error: Error }) => void;
};

export async function summarizeChangeWithRetry(
  change: ChangeEvent,
  provider: LlmProvider,
  options: RetryOptions = {},
): Promise<ChangeSummary> {
  const attempts = normalizeAttempts(options.attempts ?? 3);
  const delayMs = options.delayMs ?? 500;
  let lastError: Error | undefined;

  for (let attempt = 1; attempt <= attempts; attempt += 1) {
    try {
      return await provider.summarizeChange(change);
    } catch (error) {
      lastError = error instanceof Error ? error : new Error(String(error));
      if (attempt >= attempts) break;
      options.onRetry?.({ changeId: change.id, attempt, error: lastError });
      await sleep(delayMs * attempt);
    }
  }

  throw new Error(
    `LLM summary failed after ${attempts} attempts for ${change.id}: ${lastError?.message ?? 'unknown error'}`,
  );
}

export function changeSummaryCacheKey(change: ChangeEvent, provider: LlmProvider): string {
  const hash = createHash('sha256')
    .update(JSON.stringify({
      id: change.id,
      title: change.title,
      body: change.body,
      diff: change.diff,
      promptVersion: CHANGE_SUMMARY_PROMPT_VERSION,
    }))
    .digest('hex');
  return `${provider.name}:${provider.model}:${CHANGE_SUMMARY_PROMPT_VERSION}:${hash}`;
}

function sleep(ms: number): Promise<void> {
  return ms > 0 ? new Promise((resolve) => setTimeout(resolve, ms)) : Promise.resolve();
}

function normalizeAttempts(value: number): number {
  return Number.isInteger(value) && value > 0 ? value : 3;
}
