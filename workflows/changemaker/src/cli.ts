#!/usr/bin/env node
import { Command } from 'commander';
import { loadDotEnvFile, loadGenerateConfig } from './config.js';
import { GiteaClient } from './gitea/client.js';
import { crawlChanges } from './gitea/crawler.js';
import { writeChangesOutput, writeNarrativeStateOutput, writePostOutput } from './output.js';
import { ChangemakerStore } from './storage/sqlite.js';
import { createOpenAIProvider } from './llm/openai.js';
import { createLlamaLineProvider } from './llm/llama-line.js';
import { summarizeChanges } from './llm/summarize.js';
import { clusterChanges } from './changes/cluster.js';
import { writeDailyPost } from './writer/daily-post.js';
import { loadNarrativeState, saveNarrativeState, updateNarrativeState } from './narrative/state.js';
import { inspectChangesFile, inspectNarrativeFile } from './inspect.js';
import { createBackfillWindows, type BackfillCadence } from './backfill.js';
import { createGeneratePlan } from './generate-run.js';
import type { GenerateConfig, ReportPeriod } from './config.js';

export function createProgram(): Command {
  const program = new Command();

  program
    .name('changemaker')
    .description('Generate daily narrative updates from Gitea activity.')
    .version('0.1.0');

  program
    .command('generate')
    .description('Collect recent Gitea PRs and commits and write normalized changes JSON.')
    .option('--base-url <url>', 'Gitea base URL')
    .option('--token <token>', 'Gitea API token')
    .option('--token-env <name>', 'environment variable containing the Gitea token')
    .option('--owner <owner>', 'limit crawl to one owner/org')
    .option('--repo <repo>', 'limit crawl to one repository; requires --owner')
    .option('--out-dir <dir>', 'output directory', 'out')
    .option('--db-path <path>', 'SQLite cache path')
    .option('--include-covered', 'include changes already linked to prior generated posts')
    .option('--mark-covered', 'mark emitted changes as covered after writing output')
    .option('--no-diffs', 'skip fetching PR and commit diffs')
    .option('--max-diff-chars <chars>', 'maximum filtered diff characters per change')
    .option('--summarize', 'summarize each normalized change with the configured LLM provider')
    .option('--post', 'write a Markdown daily post from clustered changes')
    .option('--narrative-state-path <path>', 'narrative state JSON path')
    .option('--no-narrative-update', 'skip narrative state autoupdate when writing a post')
    .option('--llm-provider <provider>', 'LLM provider name')
    .option('--llm-model <model>', 'LLM model name')
    .option('--llama-line-base-url <url>', 'llama-line base URL')
    .option('--llama-line-api-key <key>', 'llama-line bearer token')
    .option('--summary-retries <count>', 'LLM summary attempts per change', '3')
    .option('--extra-periods <periods>', 'comma-separated extra period posts to generate when due: weekly,monthly,quarterly')
    .option('--report-date-offset-days <days>', 'days to subtract from publish date in the post title/header', '1')
    .option('--no-social-section', 'omit the separate Twitter/X section from generated posts')
    .option('--since <date>', 'explicit activity window start date/time')
    .option('--until <date>', 'explicit activity window end date/time')
    .option('--lookback-hours <hours>', 'activity fetch window in hours', '36')
    .option('--cadence-hours <hours>', 'nominal publishing cadence in hours', '24')
    .option('--now-window', 'force window to end at current run time instead of last post + cadence')
    .action(async (options: Parameters<typeof loadGenerateConfig>[0]) => {
      let config = loadGenerateConfig(options);
      if (config.repo && !config.owner) {
        throw new Error('--repo requires --owner');
      }

      const store = new ChangemakerStore(config.dbPath);
      const client = new GiteaClient({ baseUrl: config.baseUrl, token: config.token });
      try {
        if (!config.manualWindow) {
          const lastPost = store.getLastGeneratedPost();
          const plan = createGeneratePlan({
            config,
            lastPostAt: lastPost ? new Date(lastPost.generatedAt) : undefined,
          });
          config = { ...config, now: plan.until, since: plan.since };
        }
        const coveredChangeIds = store.listCoveredChangeIds();
        const output = await crawlChanges(client, { ...config, coveredChangeIds });
        if (config.summarize) {
          const provider =
            config.llmProvider === 'openai'
              ? createOpenAIProvider({ apiKey: config.openaiApiKey, model: config.llmModel })
              : config.llmProvider === 'llama-line'
                ? createLlamaLineProvider({
                    baseUrl: config.llamaLineBaseUrl,
                    apiKey: config.llamaLineApiKey,
                    model: config.llmModel,
                  })
                : undefined;
          if (!provider) throw new Error(`Unsupported LLM provider for summaries: ${config.llmProvider}`);
          output.changes = await summarizeChanges({
            changes: output.changes,
            provider,
            store,
            retry: {
              attempts: Number.parseInt(String(options.summaryRetries ?? '3'), 10),
              onRetry: ({ changeId, attempt, error }) => {
                process.stderr.write(`retrying summary for ${changeId} after attempt ${attempt}: ${error.message}\n`);
              },
            },
          });
          output.clusters = clusterChanges(output.changes);
        }
        const changesPath = await writeChangesOutput(output, config.outDir);
        const narrativeState = config.post && config.narrativeUpdate ? await loadNarrativeState(config.narrativeStatePath) : undefined;
        const post = config.post
          ? writeDailyPost(output, {
              ...(narrativeState ? { narrativeState } : {}),
              reportDateOffsetDays: config.reportDateOffsetDays,
              includeSocialSection: config.socialSection,
            })
          : undefined;
        const postPath = post ? await writePostOutput(post, output.generatedAt, config.outDir) : undefined;
        let narrativePath: string | undefined;
        if (post && config.narrativeUpdate) {
          const nextState = updateNarrativeState(narrativeState ?? (await loadNarrativeState(config.narrativeStatePath)), post, output);
          await saveNarrativeState(config.narrativeStatePath, nextState);
          narrativePath = await writeNarrativeStateOutput(nextState, output.generatedAt, config.outDir);
        }

        const periodPostPaths: string[] = [];
        if (config.post && !config.manualWindow) {
          for (const period of dueReportPeriods(config.extraPeriods, config.now)) {
            const window = periodWindow(period, config.now);
            const periodOutput = await crawlChanges(client, {
              ...config,
              since: window.since,
              now: window.until,
              lookbackHours: window.lookbackHours,
              coveredChangeIds,
              includeCovered: true,
            });

            if (config.summarize) {
              const provider =
                config.llmProvider === 'openai'
                  ? createOpenAIProvider({ apiKey: config.openaiApiKey, model: config.llmModel })
                  : config.llmProvider === 'llama-line'
                    ? createLlamaLineProvider({
                        baseUrl: config.llamaLineBaseUrl,
                        apiKey: config.llamaLineApiKey,
                        model: config.llmModel,
                      })
                    : undefined;
              if (!provider) throw new Error(`Unsupported LLM provider for summaries: ${config.llmProvider}`);
              periodOutput.changes = await summarizeChanges({
                changes: periodOutput.changes,
                provider,
                store,
                retry: {
                  attempts: Number.parseInt(String(options.summaryRetries ?? '3'), 10),
                  onRetry: ({ changeId, attempt, error }) => {
                    process.stderr.write(`retrying summary for ${changeId} after attempt ${attempt}: ${error.message}\n`);
                  },
                },
              });
              periodOutput.clusters = clusterChanges(periodOutput.changes);
            }

            const periodPost = writeDailyPost(periodOutput, {
              ...(narrativeState ? { narrativeState } : {}),
              reportKind: period,
              reportDateOffsetDays: config.reportDateOffsetDays,
              includeSocialSection: config.socialSection,
            });
            periodPostPaths.push(await writePostOutput(periodPost, periodOutput.generatedAt, config.outDir, period));
          }
        }

        if (config.markCovered) {
          store.recordChangeRun({
            id: `changes:${output.generatedAt}`,
            generatedAt: output.generatedAt,
            path: postPath ?? changesPath,
            changes: output.changes,
            kind: postPath ? 'post' : 'changes',
          });
        }

        process.stderr.write(
          `wrote ${[changesPath, postPath, ...periodPostPaths, narrativePath].filter(Boolean).join(', ')} (${output.totals.repos} repos, ${output.totals.changes} changes)\n`,
        );
      } finally {
        store.close();
      }
    });

  const changes = program.command('changes').description('Inspect generated change artifacts.');
  changes
    .command('inspect')
    .description('Print a summary of a generated changes JSON file.')
    .requiredOption('--file <path>', 'changes JSON file')
    .action(async (options: { file: string }) => {
      process.stdout.write(await inspectChangesFile(options.file));
    });

  const narrative = program.command('narrative').description('Inspect narrative memory.');
  narrative
    .command('inspect')
    .description('Print a summary of a narrative state JSON file.')
    .option('--file <path>', 'narrative state JSON file', '.cache/narrative-state.json')
    .action(async (options: { file: string }) => {
      process.stdout.write(await inspectNarrativeFile(options.file));
    });

  program
    .command('backfill')
    .description('Plan historical dev-blog windows without generating posts yet.')
    .requiredOption('--start <date>', 'start date')
    .requiredOption('--end <date>', 'end date')
    .option('--cadence <cadence>', 'daily, weekly, monthly, or quarterly', 'daily')
    .option('--time-travel-strict', 'avoid future knowledge leaking into simulated old posts')
    .action((options: { start: string; end: string; cadence: BackfillCadence; timeTravelStrict?: boolean }) => {
      if (!['daily', 'weekly', 'monthly', 'quarterly'].includes(options.cadence)) {
        throw new Error('--cadence must be daily, weekly, monthly, or quarterly');
      }
      const windows = createBackfillWindows({
        start: options.start,
        end: options.end,
        cadence: options.cadence,
        timeTravelStrict: options.timeTravelStrict ?? false,
      });
      process.stdout.write(`${JSON.stringify({ windows }, null, 2)}\n`);
    });

  return program;
}

function dueReportPeriods(enabled: ReportPeriod[], publishDate: Date): ReportPeriod[] {
  return enabled.filter((period) => isPeriodDue(period, publishDate));
}

function isPeriodDue(period: ReportPeriod, publishDate: Date): boolean {
  const dayOfWeek = publishDate.getUTCDay();
  const dayOfMonth = publishDate.getUTCDate();
  const month = publishDate.getUTCMonth();

  if (period === 'weekly') return dayOfWeek === 1;
  if (period === 'monthly') return dayOfMonth === 1;
  return dayOfMonth === 1 && month % 3 === 0;
}

function periodWindow(period: ReportPeriod, publishDate: Date): Pick<GenerateConfig, 'since'> & {
  until: Date;
  lookbackHours: number;
} {
  const until = startOfUtcDay(publishDate);
  const days = period === 'weekly' ? 7 : period === 'monthly' ? daysInPreviousUtcMonth(publishDate) : daysInPreviousUtcQuarter(publishDate);
  const since = new Date(until.getTime() - days * 24 * 60 * 60 * 1000);
  return { since, until, lookbackHours: days * 24 };
}

function startOfUtcDay(date: Date): Date {
  return new Date(Date.UTC(date.getUTCFullYear(), date.getUTCMonth(), date.getUTCDate()));
}

function daysInPreviousUtcMonth(date: Date): number {
  const startThisMonth = new Date(Date.UTC(date.getUTCFullYear(), date.getUTCMonth(), 1));
  const startPreviousMonth = new Date(Date.UTC(date.getUTCFullYear(), date.getUTCMonth() - 1, 1));
  return Math.round((startThisMonth.getTime() - startPreviousMonth.getTime()) / (24 * 60 * 60 * 1000));
}

function daysInPreviousUtcQuarter(date: Date): number {
  const quarterStartMonth = Math.floor(date.getUTCMonth() / 3) * 3;
  const startThisQuarter = new Date(Date.UTC(date.getUTCFullYear(), quarterStartMonth, 1));
  const startPreviousQuarter = new Date(Date.UTC(date.getUTCFullYear(), quarterStartMonth - 3, 1));
  return Math.round((startThisQuarter.getTime() - startPreviousQuarter.getTime()) / (24 * 60 * 60 * 1000));
}

export async function main(argv: string[] = process.argv): Promise<void> {
  loadDotEnvFile();
  await createProgram().parseAsync(argv);
}

if (import.meta.url === `file://${process.argv[1]}`) {
  main().catch((error: unknown) => {
    const message = error instanceof Error ? error.message : String(error);
    process.stderr.write(`changemaker: ${message}\n`);
    process.exitCode = 1;
  });
}
