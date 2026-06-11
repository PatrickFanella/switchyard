import { chmod, mkdir, readFile, writeFile } from 'node:fs/promises';
import { basename, isAbsolute, relative, resolve } from 'node:path';

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

const DEFAULT_RUNTIME_CONFIG: RuntimeConfig = {
  generation: {
    owner: 'subculture-collective',
    summarize: false,
    post: true,
    markCovered: true,
    includeCovered: false,
    includeDiffs: true,
    narrativeUpdate: true,
    socialSection: true,
    summaryRetries: 3,
    lookbackHours: 36,
    cadenceHours: 24,
    maxDiffChars: 12_000,
    reportDateOffsetDays: 1,
    extraPeriods: ['weekly', 'monthly', 'quarterly'],
  },
};

const DEFAULT_PROMPTS: Record<string, string> = {
  'summary-system.md':
    'You summarize code changes for a technical supporter update. Be specific, concise, and avoid talking about Changemaker itself unless the changed repo is Changemaker.\n',
  'summary-user.md':
    'Summarize this change as JSON with headline, userFacingSummary, technicalSummary, impact, notableFiles, and confidence.\n',
  'post-voice.md':
    'Write like a builder journal for technical supporters: concrete, warm, no fake momentum, no process notes in public copy.\n',
  'post-format.md':
    'Format published updates for scanability: short paragraphs, useful section breaks, specific receipts, and no wall-of-text blocks. Keep social copy in the dedicated social section only.\n',
  'social-posts.md':
    'Draft Twitter/X posts as standalone promotional notes with a suggested posting time for each. Keep each post under 280 characters and grounded in public receipts.\n',
};

const PROMPT_FILE_NAME_PATTERN = /^[A-Za-z0-9][A-Za-z0-9-]*\.md$/;

export async function ensureRuntimeFiles(dataDir: string): Promise<void> {
  await mkdir(resolveDataPath(dataDir, 'prompts'), { recursive: true });
  await writeFileIfMissing(resolveDataPath(dataDir, 'admin-config.json'), stringifyRuntimeConfig(DEFAULT_RUNTIME_CONFIG));

  for (const [fileName, content] of Object.entries(DEFAULT_PROMPTS)) {
    await writeFileIfMissing(resolveDataPath(dataDir, 'prompts', fileName), content);
  }
}

export async function readRuntimeConfig(dataDir: string): Promise<RuntimeConfig> {
  await ensureRuntimeFiles(dataDir);
  return normalizeRuntimeConfig(JSON.parse(await readFile(resolveDataPath(dataDir, 'admin-config.json'), 'utf8')));
}

export async function writeRuntimeConfig(dataDir: string, config: unknown): Promise<RuntimeConfig> {
  await ensureRuntimeFiles(dataDir);
  const normalized = normalizeRuntimeConfig(config);
  await writeFileWithMode600(resolveDataPath(dataDir, 'admin-config.json'), stringifyRuntimeConfig(normalized));
  return normalized;
}

export async function readPromptTemplate(dataDir: string, fileName: string): Promise<string> {
  const safeName = safePromptName(fileName);
  await ensureRuntimeFiles(dataDir);
  return readFile(resolveDataPath(dataDir, 'prompts', safeName), 'utf8');
}

export async function writePromptTemplate(dataDir: string, fileName: string, content: string): Promise<void> {
  const safeName = safePromptName(fileName);
  await ensureRuntimeFiles(dataDir);
  await writeFileWithMode600(resolveDataPath(dataDir, 'prompts', safeName), content);
}

function normalizeRuntimeConfig(config: unknown): RuntimeConfig {
  const input = isRecord(config) ? config : {};
  const generation = isRecord(input.generation) ? input.generation : {};

  return {
    generation: {
      owner: normalizeOptionalString(generation.owner) ?? DEFAULT_RUNTIME_CONFIG.generation.owner,
      repo: normalizeOptionalString(generation.repo),
      llmProvider: normalizeOptionalString(generation.llmProvider),
      llmModel: normalizeOptionalString(generation.llmModel),
      summarize: normalizeBoolean(generation.summarize, DEFAULT_RUNTIME_CONFIG.generation.summarize),
      post: normalizeBoolean(generation.post, DEFAULT_RUNTIME_CONFIG.generation.post),
      markCovered: normalizeBoolean(generation.markCovered, DEFAULT_RUNTIME_CONFIG.generation.markCovered),
      includeCovered: normalizeBoolean(generation.includeCovered, DEFAULT_RUNTIME_CONFIG.generation.includeCovered),
      includeDiffs: normalizeBoolean(generation.includeDiffs, DEFAULT_RUNTIME_CONFIG.generation.includeDiffs),
      narrativeUpdate: normalizeBoolean(generation.narrativeUpdate, DEFAULT_RUNTIME_CONFIG.generation.narrativeUpdate),
      socialSection: normalizeBoolean(generation.socialSection, DEFAULT_RUNTIME_CONFIG.generation.socialSection),
      summaryRetries: normalizePositiveInteger(
        generation.summaryRetries,
        DEFAULT_RUNTIME_CONFIG.generation.summaryRetries,
      ),
      lookbackHours: normalizePositiveNumber(generation.lookbackHours, DEFAULT_RUNTIME_CONFIG.generation.lookbackHours),
      cadenceHours: normalizePositiveNumber(generation.cadenceHours, DEFAULT_RUNTIME_CONFIG.generation.cadenceHours),
      maxDiffChars: normalizePositiveInteger(generation.maxDiffChars, DEFAULT_RUNTIME_CONFIG.generation.maxDiffChars),
      reportDateOffsetDays: normalizeNonNegativeInteger(
        generation.reportDateOffsetDays,
        DEFAULT_RUNTIME_CONFIG.generation.reportDateOffsetDays,
      ),
      extraPeriods: normalizeExtraPeriods(generation.extraPeriods),
    },
  };
}

function safePromptName(fileName: string): string {
  const safe = basename(fileName);
  if (safe !== fileName || !PROMPT_FILE_NAME_PATTERN.test(safe)) {
    throw new Error('Invalid prompt file name');
  }

  return safe;
}

function resolveDataPath(dataDir: string, ...segments: string[]): string {
  const root = resolve(dataDir);
  const target = resolve(root, ...segments);
  const relativePath = relative(root, target);

  if (relativePath.startsWith('..') || isAbsolute(relativePath)) {
    throw new Error('Invalid runtime file path');
  }

  return target;
}

async function writeFileIfMissing(path: string, content: string): Promise<void> {
  try {
    await readFile(path, 'utf8');
  } catch (error) {
    if (!isMissingFileError(error)) throw error;
    await writeFileWithMode600(path, content);
  }
}

async function writeFileWithMode600(path: string, content: string): Promise<void> {
  await writeFile(path, content, { mode: 0o600 });
  await chmod(path, 0o600);
}

function stringifyRuntimeConfig(config: RuntimeConfig): string {
  return `${JSON.stringify(config, null, 2)}\n`;
}

function normalizeOptionalString(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined;
}

function normalizeBoolean(value: unknown, fallback: boolean): boolean {
  return typeof value === 'boolean' ? value : fallback;
}

function normalizePositiveInteger(value: unknown, fallback: number): number {
  return typeof value === 'number' && Number.isInteger(value) && value > 0 ? value : fallback;
}

function normalizeNonNegativeInteger(value: unknown, fallback: number): number {
  return typeof value === 'number' && Number.isInteger(value) && value >= 0 ? value : fallback;
}

function normalizePositiveNumber(value: unknown, fallback: number): number {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : fallback;
}

function normalizeExtraPeriods(value: unknown): Array<'weekly' | 'monthly' | 'quarterly'> {
  const raw = Array.isArray(value) ? value : DEFAULT_RUNTIME_CONFIG.generation.extraPeriods;
  const unique = new Set<'weekly' | 'monthly' | 'quarterly'>();
  for (const period of raw) {
    if (period === 'weekly' || period === 'monthly' || period === 'quarterly') unique.add(period);
  }
  return [...unique];
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null;
}

function isMissingFileError(error: unknown): boolean {
  return typeof error === 'object' && error !== null && 'code' in error && (error as { code?: string }).code === 'ENOENT';
}
