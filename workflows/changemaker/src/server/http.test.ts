import { mkdtempSync } from 'node:fs';
import { readFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { describe, expect, it, vi } from 'vitest';
import { createGenerationRunner, createHttpApp } from './http.js';
import { ensureRuntimeFiles, writeRuntimeConfig } from './runtime-config.js';
import { createRunController } from './run-controller.js';
import * as dailyPostModule from '../writer/daily-post.js';
import { defaultNarrativeState } from '../narrative/state.js';

describe('http prompt routes', () => {
  it('rejects non-string prompt content without overwriting', async () => {
    const dataDir = mkdtempSync(join(tmpdir(), 'changemaker-http-'));
    await ensureRuntimeFiles(dataDir);
    const app = createHttpApp([], { adminToken: 'test-token', dataDir });
    const promptPath = join(dataDir, 'prompts', 'summary-system.md');
    const original = await readFile(promptPath, 'utf8');

    const baseHeaders = {
      authorization: 'Bearer test-token',
      'content-type': 'application/json',
    };

    await expect(
      app(
        new Request('http://localhost/api/prompts/summary-system.md', {
          method: 'PUT',
          headers: baseHeaders,
          body: '{}',
        }),
      ),
    ).resolves.toMatchObject({ status: 400 });

    await expect(
      app(
        new Request('http://localhost/api/prompts/summary-system.md', {
          method: 'PUT',
          headers: baseHeaders,
          body: '{"content":1}',
        }),
      ),
    ).resolves.toMatchObject({ status: 400 });

    expect(await readFile(promptPath, 'utf8')).toBe(original);
  });
});

describe('http run routes', () => {
  it('exposes run status and starts runs', async () => {
    let release!: () => void;
    const controller = createRunController({
      run: async (_request, log) => {
        log('working');
        await new Promise<void>((resolve) => {
          release = resolve;
        });
        log('done');
      },
    });
    const app = createHttpApp([], { adminToken: 'test-token', runController: controller });

    const authHeaders = { authorization: 'Bearer test-token' };

    await expect(app(new Request('http://localhost/api/runs/status', { headers: authHeaders }))).resolves.toMatchObject({
      status: 200,
    });

    await expect(
      app(
        new Request('http://localhost/api/runs', {
          method: 'POST',
          headers: {
            ...authHeaders,
            'content-type': 'application/json',
          },
          body: JSON.stringify({ kind: 'daily' }),
        }),
      ),
    ).resolves.toMatchObject({ status: 202 });

    await new Promise((resolve) => setTimeout(resolve, 0));

    const runningResponse = await app(new Request('http://localhost/api/runs/status', { headers: authHeaders }));
    const runningStatus = await runningResponse.json();
    expect(runningStatus).toMatchObject({
      state: 'running',
      request: { kind: 'daily' },
    });
    expect(runningStatus.logs).toContain('started');
    expect(runningStatus.logs).toContain('working');

    release();
    await new Promise((resolve) => setTimeout(resolve, 0));

    const finishedResponse = await app(new Request('http://localhost/api/runs/status', { headers: authHeaders }));
    const finishedStatus = await finishedResponse.json();
    expect(finishedStatus).toMatchObject({ state: 'succeeded' });
    expect(finishedStatus.logs).toContain('finished');
  });

  it('cancels active runs and rejects cancellation when idle', async () => {
    let release!: () => void;
    const controller = createRunController({
      run: async (_request, log) => {
        log('working');
        await new Promise<void>((resolve) => {
          release = resolve;
        });
        log('done');
      },
    });
    const app = createHttpApp([], { adminToken: 'test-token', runController: controller });

    const authHeaders = { authorization: 'Bearer test-token' };

    await expect(
      app(
        new Request('http://localhost/api/runs/cancel', {
          method: 'POST',
          headers: authHeaders,
        }),
      ),
    ).resolves.toMatchObject({ status: 409 });

    await expect(
      app(
        new Request('http://localhost/api/runs', {
          method: 'POST',
          headers: {
            ...authHeaders,
            'content-type': 'application/json',
          },
          body: JSON.stringify({ kind: 'daily' }),
        }),
      ),
    ).resolves.toMatchObject({ status: 202 });

    await new Promise((resolve) => setTimeout(resolve, 0));

    const cancelResponse = await app(
      new Request('http://localhost/api/runs/cancel', {
        method: 'POST',
        headers: authHeaders,
      }),
    );

    expect(cancelResponse.status).toBe(200);
    await expect(cancelResponse.json()).resolves.toMatchObject({ state: 'cancelled', request: { kind: 'daily' } });

    await expect(
      app(
        new Request('http://localhost/api/runs', {
          method: 'POST',
          headers: {
            ...authHeaders,
            'content-type': 'application/json',
          },
          body: JSON.stringify({ kind: 'manual' }),
        }),
      ),
    ).resolves.toMatchObject({ status: 202 });

    release();
    await new Promise((resolve) => setTimeout(resolve, 0));

    const statusResponse = await app(new Request('http://localhost/api/runs/status', { headers: authHeaders }));
    await expect(statusResponse.json()).resolves.toMatchObject({ state: 'succeeded' });
  });

  it('uses runtime config for server runs and keeps markCovered disabled for manual runs', async () => {
    const dataDir = mkdtempSync(join(tmpdir(), 'changemaker-run-'));
    await ensureRuntimeFiles(dataDir);
    await writeRuntimeConfig(dataDir, {
      generation: {
        owner: 'octo-org',
        repo: 'docs',
        summarize: true,
        post: false,
        markCovered: false,
        summaryRetries: 7,
      },
    });

    const recordChangeRun = vi.fn();
    const store = {
      close: vi.fn(),
      getLastGeneratedPost: vi.fn(() => undefined),
      listCoveredChangeIds: vi.fn(() => new Set<string>()),
      recordChangeRun,
    };

    const loadGenerateConfig = vi.fn((options: Record<string, unknown>) => ({
      baseUrl: 'https://git.example',
      token: undefined,
      owner: options.owner as string | undefined,
      repo: options.repo as string | undefined,
      outDir: join(dataDir, 'out'),
      dbPath: join(dataDir, 'changemaker.sqlite'),
      includeCovered: false,
      markCovered: Boolean(options.markCovered),
      includeDiffs: true,
      maxDiffChars: 12_000,
      summarize: Boolean(options.summarize),
      post: Boolean(options.post),
      llmProvider: 'openai',
      llmModel: 'gpt-4.1-mini',
      openaiApiKey: undefined,
      llamaLineBaseUrl: 'http://localhost:11434',
      llamaLineApiKey: undefined,
      narrativeStatePath: join(dataDir, 'narrative-state.json'),
      narrativeUpdate: true,
      extraPeriods: [],
      reportDateOffsetDays: 1,
      socialSection: true,
      lookbackHours: 36,
      cadenceHours: 24,
      nowWindow: false,
      manualWindow: true,
      now: new Date('2026-05-27T12:00:00.000Z'),
      since: new Date('2026-05-27T10:00:00.000Z'),
    }));

    const crawlChanges = vi.fn(async (_client, options) => {
      expect(options).toMatchObject({
        owner: 'octo-org',
        repo: 'docs',
        since: new Date('2026-05-27T10:00:00.000Z'),
        now: new Date('2026-05-27T12:00:00.000Z'),
      });
      return {
        generatedAt: '2026-05-27T12:00:00.000Z',
        window: { from: '2026-05-27T10:00:00.000Z', to: '2026-05-27T12:00:00.000Z', lookbackHours: 36 },
        source: { baseUrl: 'https://git.example', owner: 'octo-org', repo: 'docs' },
        totals: { repos: 1, pullRequests: 0, commits: 0, changes: 1 },
        changes: [
          {
            id: 'change-1',
            type: 'commit' as const,
            repo: 'docs',
            title: 'Change 1',
            url: 'https://git.example/docs/commit/change-1',
            createdAt: '2026-05-27T11:00:00.000Z',
            updatedAt: '2026-05-27T11:00:00.000Z',
            covered: false,
          },
        ],
        clusters: [],
      };
    });

    const summarizeChanges = vi.fn(async ({ changes, retry }) => {
      expect(retry.attempts).toBe(7);
      expect(changes).toHaveLength(1);
      return changes;
    });

    const runner = createGenerationRunner(
      { host: '127.0.0.1', port: 3038, postsDir: join(dataDir, 'out'), dataDir, adminToken: 'token', serviceControlEnabled: false },
      {
        loadGenerateConfig,
        storeFactory: () => store,
        crawlChanges,
        summarizeChanges,
        createOpenAIProvider: vi.fn(() => ({ name: 'openai', model: 'gpt-4.1-mini' } as never)),
        writeChangesOutput: vi.fn(async () => join(dataDir, 'out', 'changes.json')),
        writePostOutput: vi.fn(),
        clusterChanges: vi.fn(() => []),
      },
    );

    await runner({ kind: 'manual', since: '2026-05-27T10:00:00.000Z', until: '2026-05-27T12:00:00.000Z' }, vi.fn());

    expect(loadGenerateConfig).toHaveBeenCalledWith(
      expect.objectContaining({
        owner: 'octo-org',
        repo: 'docs',
        summarize: true,
        post: false,
        markCovered: false,
        summaryRetries: 7,
        since: '2026-05-27T10:00:00.000Z',
        until: '2026-05-27T12:00:00.000Z',
      }),
      expect.anything(),
    );
    expect(recordChangeRun).not.toHaveBeenCalled();
    expect(store.close).toHaveBeenCalledOnce();
  });

  it('loads narrative state before writing the post and reuses it for the update', async () => {
    const dataDir = mkdtempSync(join(tmpdir(), 'changemaker-post-'));
    await ensureRuntimeFiles(dataDir);
    await writeRuntimeConfig(dataDir, {
      generation: {
        owner: 'octo-org',
        repo: 'docs',
        summarize: false,
        post: true,
        markCovered: false,
        summaryRetries: 3,
      },
    });

    const narrativeState = defaultNarrativeState();
    const events: string[] = [];
    const loadNarrativeState = vi.fn(async () => {
      events.push('load');
      return narrativeState;
    });
    const saveNarrativeState = vi.fn(async () => undefined);
    const updateNarrativeState = vi.fn((state, post, output) => {
      events.push('update');
      expect(state).toBe(narrativeState);
      expect(post).toMatchObject({ summary: 'Post summary' });
      expect(output).toMatchObject({ generatedAt: '2026-05-27T12:00:00.000Z' });
      return {
        ...state,
        recentPosts: [
          {
            date: '2026-05-27',
            title: post.title,
            summary: post.summary,
            threadsMentioned: [],
          },
          ...state.recentPosts,
        ],
      };
    });
    const writeDailyPostSpy = vi.spyOn(dailyPostModule, 'writeDailyPost').mockImplementation((output, options = {}) => {
      events.push('post');
      expect(options).toEqual({ narrativeState, reportDateOffsetDays: 1, includeSocialSection: true });
      expect(output).toMatchObject({ generatedAt: '2026-05-27T12:00:00.000Z' });
      return {
        title: 'Generated post',
        summary: 'Post summary',
        markdown: '# Generated post\n',
        tweetDrafts: [],
        socialPosts: [],
        coveredChangeIds: [],
        narrativeNotes: {
          whyItMatters: 'Why it matters',
          watchingNext: 'Watch next',
        },
      };
    });

    const runner = createGenerationRunner(
      { host: '127.0.0.1', port: 3038, postsDir: join(dataDir, 'out'), dataDir, adminToken: 'token', serviceControlEnabled: false },
      {
        loadNarrativeState,
        saveNarrativeState,
        updateNarrativeState,
        storeFactory: () => ({
          close: vi.fn(),
          getLastGeneratedPost: vi.fn(() => undefined),
          listCoveredChangeIds: vi.fn(() => new Set<string>()),
          recordChangeRun: vi.fn(),
        }),
        crawlChanges: vi.fn(async () => ({
          generatedAt: '2026-05-27T12:00:00.000Z',
          window: { from: '2026-05-27T10:00:00.000Z', to: '2026-05-27T12:00:00.000Z', lookbackHours: 36 },
          source: { baseUrl: 'https://git.example', owner: 'octo-org', repo: 'docs' },
          totals: { repos: 1, pullRequests: 0, commits: 0, changes: 0 },
          changes: [],
          clusters: [],
        })),
        writeChangesOutput: vi.fn(async () => join(dataDir, 'out', 'changes.json')),
        writePostOutput: vi.fn(async () => join(dataDir, 'out', 'post.md')),
        writeNarrativeStateOutput: vi.fn(async () => join(dataDir, 'out', 'narrative-state.json')),
      },
    );

    await runner({ kind: 'daily' }, vi.fn());

    expect(events).toEqual(['load', 'post', 'update']);
    expect(loadNarrativeState).toHaveBeenCalledTimes(1);
    expect(writeDailyPostSpy).toHaveBeenCalledTimes(1);
    expect(updateNarrativeState).toHaveBeenCalledTimes(1);
    writeDailyPostSpy.mockRestore();
  });
});

describe('http service routes', () => {
  it('rejects service control when disabled', async () => {
    const executor = vi.fn();
    const app = createHttpApp([], {
      adminToken: 'test-token',
      config: { serviceControlEnabled: false },
      serviceExecutor: executor,
    });

    const response = await app(
      new Request('http://localhost/api/service/service-status', {
        method: 'GET',
        headers: { authorization: 'Bearer test-token' },
      }),
    );

    expect(response.status).toBe(403);
    await expect(response.json()).resolves.toEqual({ error: 'service control disabled' });
    expect(executor).not.toHaveBeenCalled();
  });

  it('requires exact confirmation for write actions', async () => {
    const executor = vi.fn();
    const app = createHttpApp([], {
      adminToken: 'test-token',
      config: { serviceControlEnabled: true },
      serviceExecutor: executor,
    });

    const response = await app(
      new Request('http://localhost/api/service/restart-service', {
        method: 'POST',
        headers: {
          authorization: 'Bearer test-token',
          'content-type': 'application/json',
        },
        body: JSON.stringify({ confirm: true, confirmationText: 'wrong' }),
      }),
    );

    expect(response.status).toBe(400);
    await expect(response.json()).resolves.toEqual({ error: 'Confirmation text must be restart-service' });
    expect(executor).not.toHaveBeenCalled();
  });

  it('executes allowed actions and caps output', async () => {
    const executor = vi.fn(async (command: readonly string[]) => ({
      command: [...command],
      exitCode: 0,
      signal: null,
      stdout: 'a'.repeat(8_500),
      stderr: 'b'.repeat(9_000),
    }));

    const app = createHttpApp([], {
      adminToken: 'test-token',
      config: { serviceControlEnabled: true },
      serviceExecutor: executor,
    });

    const response = await app(
      new Request('http://localhost/api/service/service-status', {
        method: 'GET',
        headers: { authorization: 'Bearer test-token' },
      }),
    );

    expect(response.status).toBe(200);
    await expect(response.json()).resolves.toEqual({
      action: 'service-status',
      command: ['systemctl', 'status', 'changemaker-daily.service', '--no-pager'],
      exitCode: 0,
      signal: null,
      stdout: 'a'.repeat(8_000),
      stderr: 'b'.repeat(8_000),
      stdoutTruncated: true,
      stderrTruncated: true,
    });
    expect(executor).toHaveBeenCalledOnce();
  });
});
