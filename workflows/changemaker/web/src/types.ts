export type PromptFileName = 'summary-system.md' | 'summary-user.md' | 'post-voice.md' | 'post-format.md' | 'social-posts.md';

export type PromptDefinition = {
  fileName: PromptFileName;
  title: string;
  description: string;
};

export const PROMPT_DEFINITIONS: PromptDefinition[] = [
  {
    fileName: 'summary-system.md',
    title: 'Summary system prompt',
    description: 'Core summarization rules for the model that reads raw change sets.',
  },
  {
    fileName: 'summary-user.md',
    title: 'Summary user prompt',
    description: 'Template that shapes each run into structured JSON output.',
  },
  {
    fileName: 'post-voice.md',
    title: 'Post voice prompt',
    description: 'Tone guide for turning the summary into the published field report.',
  },
  {
    fileName: 'post-format.md',
    title: 'Post formatting prompt',
    description: 'Formatting guidance for section breaks, paragraph length, receipts, and social separation.',
  },
  {
    fileName: 'social-posts.md',
    title: 'Social posts prompt',
    description: 'Guidance for Twitter/X drafts and suggested posting times.',
  },
];

export type PostSummary = {
  fileName: string;
  date: string;
  title: string;
};

export type PostDetail = PostSummary & {
  markdown: string;
};

export type RuntimeGenerationConfig = {
  owner?: string;
  repo?: string;
  llmProvider?: string;
  llmModel?: string;
  summarize: boolean;
  post: boolean;
  markCovered: boolean;
  includeCovered: boolean;
  includeDiffs: boolean;
  narrativeUpdate: boolean;
  socialSection: boolean;
  summaryRetries: number;
  lookbackHours: number;
  cadenceHours: number;
  maxDiffChars: number;
  reportDateOffsetDays: number;
  extraPeriods: Array<'weekly' | 'monthly' | 'quarterly'>;
};

export type RuntimeConfig = {
  generation: RuntimeGenerationConfig;
};

export type RunRequest = {
  kind: 'daily' | 'manual';
  since?: string;
  until?: string;
};

export type RunStatus = {
  state: 'idle' | 'running' | 'succeeded' | 'failed';
  startedAt?: string;
  finishedAt?: string;
  error?: string;
  request?: RunRequest;
  logs: string[];
};

export type ServiceAction =
  | 'service-status'
  | 'timer-status'
  | 'restart-service'
  | 'restart-timer'
  | 'stop-timer'
  | 'start-timer';

export type ServiceExecutionResult = {
  action: ServiceAction;
  command: string[];
  exitCode: number | null;
  signal: string | null;
  stdout: string;
  stderr: string;
  stdoutTruncated: boolean;
  stderrTruncated: boolean;
};

export type ApiErrorPayload = {
  error: string;
};
