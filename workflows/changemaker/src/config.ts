import { z } from 'zod';
import { existsSync, readFileSync } from 'node:fs';

const envSchema = z.object({
  GITEA_BASE_URL: z.string().url().default('https://git.subcult.tv'),
  GITEA_TOKEN: z.string().optional(),
});

export type GenerateOptions = {
  baseUrl?: string;
  token?: string;
  tokenEnv?: string;
  owner?: string;
  repo?: string;
  outDir?: string;
  lookbackHours?: string | number;
  cadenceHours?: string | number;
  since?: string;
  until?: string;
  nowWindow?: boolean;
  dbPath?: string;
  includeCovered?: boolean;
  markCovered?: boolean;
  diffs?: boolean;
  maxDiffChars?: string | number;
  summarize?: boolean;
  post?: boolean;
  llmProvider?: string;
  llmModel?: string;
  openaiApiKey?: string;
  llamaLineBaseUrl?: string;
  llamaLineApiKey?: string;
  summaryRetries?: string | number;
  extraPeriods?: string;
  reportDateOffsetDays?: string | number;
  socialSection?: boolean;
  narrativeStatePath?: string;
  narrativeUpdate?: boolean;
  now?: Date;
};

export type ReportPeriod = 'weekly' | 'monthly' | 'quarterly';

export type GenerateConfig = {
  baseUrl: string;
  token?: string;
  owner?: string;
  repo?: string;
  outDir: string;
  dbPath: string;
  includeCovered: boolean;
  markCovered: boolean;
  includeDiffs: boolean;
  maxDiffChars: number;
  summarize: boolean;
  post: boolean;
  llmProvider: string;
  llmModel: string;
  openaiApiKey?: string;
  llamaLineBaseUrl: string;
  llamaLineApiKey?: string;
  narrativeStatePath: string;
  narrativeUpdate: boolean;
  extraPeriods: ReportPeriod[];
  reportDateOffsetDays: number;
  socialSection: boolean;
  lookbackHours: number;
  cadenceHours: number;
  nowWindow: boolean;
  manualWindow: boolean;
  now: Date;
  since: Date;
};

export type GenerateWindow = {
  since: Date;
  until: Date;
};

export type ServerOptions = {
  host?: string;
  port?: string | number;
  postsDir?: string;
  dataDir?: string;
  adminToken?: string;
  serviceControl?: boolean;
};

export type ServerConfig = {
  host: string;
  port: number;
  postsDir: string;
  dataDir: string;
  adminToken?: string;
  serviceControlEnabled: boolean;
};

export function loadDotEnvFile(path = '.env', env: NodeJS.ProcessEnv = process.env): void {
  if (!existsSync(path)) return;

  for (const rawLine of readFileSync(path, 'utf8').split(/\r?\n/)) {
    const line = rawLine.trim();
    if (!line || line.startsWith('#')) continue;

    const separator = line.indexOf('=');
    if (separator <= 0) continue;

    const key = line.slice(0, separator).trim();
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(key) || env[key] !== undefined) continue;

    env[key] = unquoteEnvValue(line.slice(separator + 1).trim());
  }
}

export function loadGenerateConfig(
  options: GenerateOptions,
  env: NodeJS.ProcessEnv = process.env,
): GenerateConfig {
  const parsedEnv = envSchema.parse(env);
  const now = options.now ?? new Date();
  const manualSince = options.since ? parseDateOption('--since', options.since) : undefined;
  const manualUntil = options.until ? parseDateOption('--until', options.until) : undefined;
  const lookbackHours = parseLookbackHours(options.lookbackHours ?? env.CHANGEMAKER_LOOKBACK_HOURS ?? 36);
  const cadenceHours = parseLookbackHours(options.cadenceHours ?? env.CHANGEMAKER_CADENCE_HOURS ?? 24);
  const tokenFromEnvName = options.tokenEnv ? env[options.tokenEnv] : undefined;
  const token = options.token ?? tokenFromEnvName ?? parsedEnv.GITEA_TOKEN;
  const baseUrl = trimTrailingSlash(options.baseUrl ?? parsedEnv.GITEA_BASE_URL);

  return {
    baseUrl,
    token,
    owner: blankToUndefined(options.owner),
    repo: blankToUndefined(options.repo),
    outDir: options.outDir ?? 'out',
    dbPath: options.dbPath ?? env.CHANGEMAKER_DB_PATH ?? '.cache/changemaker.sqlite',
    includeCovered: options.includeCovered ?? false,
    markCovered: options.markCovered ?? false,
    includeDiffs: options.diffs ?? true,
    maxDiffChars: parsePositiveInteger(options.maxDiffChars ?? env.CHANGEMAKER_MAX_DIFF_CHARS ?? 12_000),
    summarize: options.summarize ?? false,
    post: options.post ?? false,
    llmProvider: options.llmProvider ?? env.CHANGEMAKER_LLM_PROVIDER ?? 'openai',
    llmModel: options.llmModel ?? env.CHANGEMAKER_LLM_MODEL ?? 'gpt-4.1-mini',
    openaiApiKey: options.openaiApiKey ?? env.OPENAI_API_KEY,
    llamaLineBaseUrl: trimTrailingSlash(options.llamaLineBaseUrl ?? env.LLAMA_LINE_BASE_URL ?? 'http://localhost:11434'),
    llamaLineApiKey: options.llamaLineApiKey ?? env.LLAMA_LINE_API_KEY,
    narrativeStatePath: options.narrativeStatePath ?? env.CHANGEMAKER_NARRATIVE_STATE ?? '.cache/narrative-state.json',
    narrativeUpdate: options.narrativeUpdate ?? true,
    extraPeriods: parseReportPeriods(options.extraPeriods ?? env.CHANGEMAKER_EXTRA_PERIODS ?? ''),
    reportDateOffsetDays: parseNonNegativeInteger(
      options.reportDateOffsetDays ?? env.CHANGEMAKER_REPORT_DATE_OFFSET_DAYS ?? 1,
    ),
    socialSection: options.socialSection ?? parseBooleanDefault(env.CHANGEMAKER_SOCIAL_SECTION, true),
    lookbackHours,
    cadenceHours,
    nowWindow: options.nowWindow ?? false,
    manualWindow: Boolean(manualSince || manualUntil),
    now: manualUntil ?? now,
    since:
      manualSince ??
      resolveGenerateWindow({ now: manualUntil ?? now, lookbackHours, cadenceHours, nowWindow: options.nowWindow ?? false }).since,
  };
}

export function resolveGenerateWindow(input: {
  now: Date;
  lookbackHours: number;
  cadenceHours: number;
  lastPostAt?: Date;
  nowWindow?: boolean;
}): GenerateWindow {
  const until =
    input.lastPostAt && !input.nowWindow
      ? minDate(input.now, addHours(input.lastPostAt, input.cadenceHours))
      : input.now;
  return {
    since: addHours(until, -input.lookbackHours),
    until,
  };
}

function parseReportPeriods(value: string): ReportPeriod[] {
  const periods = value
    .split(',')
    .map((part) => part.trim().toLowerCase())
    .filter(Boolean);
  const unique = new Set<ReportPeriod>();

  for (const period of periods) {
    if (period !== 'weekly' && period !== 'monthly' && period !== 'quarterly') {
      throw new Error(`Invalid report period: ${period}`);
    }
    unique.add(period);
  }

  return [...unique];
}

export function loadServerConfig(options: ServerOptions = {}, env: NodeJS.ProcessEnv = process.env): ServerConfig {
  const host = normalizeHost(options.host ?? env.CHANGEMAKER_SERVER_HOST);
  const adminToken = normalizeOptionalTrimmedString(options.adminToken ?? env.CHANGEMAKER_ADMIN_TOKEN);

  return {
    host: host ?? '127.0.0.1',
    port: parsePortNumber(options.port ?? env.CHANGEMAKER_SERVER_PORT ?? 3038),
    postsDir: options.postsDir ?? env.CHANGEMAKER_POSTS_DIR ?? env.CHANGEMAKER_OUT_DIR ?? 'out',
    dataDir: options.dataDir ?? env.CHANGEMAKER_DATA_DIR ?? '.cache',
    adminToken,
    serviceControlEnabled: options.serviceControl ?? parseBoolean(env.CHANGEMAKER_SERVICE_CONTROL),
  };
}

function addHours(date: Date, hours: number): Date {
  return new Date(date.getTime() + hours * 60 * 60 * 1000);
}

function minDate(a: Date, b: Date): Date {
  return a.getTime() <= b.getTime() ? a : b;
}

function parsePositiveInteger(value: string | number): number {
  const parsed = typeof value === 'number' ? value : Number.parseInt(value, 10);
  if (!Number.isInteger(parsed) || parsed <= 0) {
    throw new Error(`Invalid positive integer: ${value}`);
  }
  return parsed;
}

function parseNonNegativeInteger(value: string | number): number {
  const parsed = typeof value === 'number' ? value : Number.parseInt(value, 10);
  if (!Number.isInteger(parsed) || parsed < 0) {
    throw new Error(`Invalid non-negative integer: ${value}`);
  }
  return parsed;
}

function parsePortNumber(value: string | number): number {
  const parsed = typeof value === 'number' ? value : Number(value.trim());
  if (!Number.isInteger(parsed) || parsed < 1 || parsed > 65535) {
    throw new Error(`Invalid port: ${value}`);
  }
  return parsed;
}

function parseLookbackHours(value: string | number): number {
  const parsed = typeof value === 'number' ? value : Number.parseFloat(value);
  if (!Number.isFinite(parsed) || parsed <= 0) {
    throw new Error(`Invalid lookback hours: ${value}`);
  }
  return parsed;
}

function parseBoolean(value: string | undefined): boolean {
  return value === 'true';
}

function parseBooleanDefault(value: string | undefined, fallback: boolean): boolean {
  if (value === undefined) return fallback;
  return value === 'true';
}

function normalizeHost(value: string | undefined): string | undefined {
  const trimmed = value?.trim();
  return trimmed ? trimmed : undefined;
}

function normalizeOptionalTrimmedString(value: string | undefined): string | undefined {
  const trimmed = value?.trim();
  return trimmed ? trimmed : undefined;
}

function parseDateOption(name: string, value: string): Date {
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) {
    throw new Error(`Invalid ${name} date: ${value}`);
  }
  return parsed;
}

function trimTrailingSlash(value: string): string {
  return value.replace(/\/+$/, '');
}

function blankToUndefined(value: string | undefined): string | undefined {
  return value && value.trim() ? value.trim() : undefined;
}

function unquoteEnvValue(value: string): string {
  if (
    (value.startsWith('"') && value.endsWith('"')) ||
    (value.startsWith("'") && value.endsWith("'"))
  ) {
    return value.slice(1, -1);
  }
  return value;
}
