import type { ChangeEvent, ChangeSummary } from '../types.js';

export const CHANGE_SUMMARY_PROMPT_VERSION = 'change-summary-v1';

export type LlmProvider = {
  name: string;
  model: string;
  summarizeChange(change: ChangeEvent): Promise<ChangeSummary>;
};
