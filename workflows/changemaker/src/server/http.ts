import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http';
import { readFile, stat } from 'node:fs/promises';
import { extname, join, resolve } from 'node:path';
import { loadGenerateConfig, type GenerateConfig, type ReportPeriod, type ServerConfig } from '../config.js';
import { ChangemakerStore } from '../storage/sqlite.js';
import { GiteaClient } from '../gitea/client.js';
import { crawlChanges } from '../gitea/crawler.js';
import { createGeneratePlan } from '../generate-run.js';
import { createOpenAIProvider } from '../llm/openai.js';
import { createLlamaLineProvider } from '../llm/llama-line.js';
import { summarizeChanges } from '../llm/summarize.js';
import { clusterChanges } from '../changes/cluster.js';
import { writeChangesOutput, writeNarrativeStateOutput, writePostOutput } from '../output.js';
import { loadNarrativeState, saveNarrativeState, updateNarrativeState } from '../narrative/state.js';
import { writeDailyPost } from '../writer/daily-post.js';
import { authorizeRequest } from './auth.js';
import { listGeneratedPosts, readGeneratedPost } from './posts.js';
import { readRuntimeConfig } from './runtime-config.js';
import { createRunController, type RunController, type RunRequest } from './run-controller.js';
import {
  assertServiceConfirmation,
  executeServiceAction,
  type ServiceAction,
  type ServiceConfirmation,
  type ServiceExecutor,
} from './service-control.js';
import { readPromptTemplate, writePromptTemplate, writeRuntimeConfig } from './runtime-config.js';
import type { ApiError } from './types.js';

export type JsonPrimitive = string | number | boolean | null;
export type JsonValue = JsonPrimitive | JsonValue[] | { [key: string]: JsonValue };

export type RouteHandler = (request: Request, url: URL) => Response | Promise<Response>;

export type RouteDefinition = {
  method: string;
  path: string;
  handler: RouteHandler;
};

export type HttpApp = (request: Request) => Promise<Response>;

export type StartServerResult = Server;

type RunStore = Pick<ChangemakerStore, 'close' | 'getLastGeneratedPost' | 'listCoveredChangeIds' | 'recordChangeRun'>;

export type ServerRunDependencies = {
  readRuntimeConfig?: typeof readRuntimeConfig;
  loadGenerateConfig?: typeof loadGenerateConfig;
  storeFactory?: (dbPath: string) => RunStore;
  crawlChanges?: typeof crawlChanges;
  createGeneratePlan?: typeof createGeneratePlan;
  summarizeChanges?: typeof summarizeChanges;
  createOpenAIProvider?: typeof createOpenAIProvider;
  createLlamaLineProvider?: typeof createLlamaLineProvider;
  clusterChanges?: typeof clusterChanges;
  writeChangesOutput?: typeof writeChangesOutput;
  writePostOutput?: typeof writePostOutput;
  loadNarrativeState?: typeof loadNarrativeState;
  saveNarrativeState?: typeof saveNarrativeState;
  updateNarrativeState?: typeof updateNarrativeState;
  writeNarrativeStateOutput?: typeof writeNarrativeStateOutput;
};

export type HttpAppOptions = {
  adminToken?: string;
  dataDir?: string;
  postsDir?: string;
  runController?: RunController;
  config?: {
    serviceControlEnabled: boolean;
  };
  serviceExecutor?: ServiceExecutor;
};

const MAX_SERVICE_OUTPUT_CHARS = 8_000;

export function jsonResponse(body: JsonValue | ApiError, init: ResponseInit = {}): Response {
  const headers = new Headers(init.headers);
  headers.set('content-type', 'application/json; charset=utf-8');

  return new Response(JSON.stringify(body), {
    ...init,
    headers,
  });
}

export function textResponse(body: string, init: ResponseInit = {}): Response {
  const headers = new Headers(init.headers);
  headers.set('content-type', 'text/plain; charset=utf-8');

  return new Response(body, {
    ...init,
    headers,
  });
}

export function notFoundResponse(path = 'not found'): Response {
  return jsonResponse({ error: path }, { status: 404 });
}

export function methodNotAllowedResponse(methods: string[]): Response {
  return textResponse('method not allowed', {
    status: 405,
    headers: { allow: methods.join(', ') },
  });
}

export async function readJsonBody<T>(request: Request, maxBytes = 1_000_000): Promise<T> {
  const contentLength = request.headers.get('content-length');
  if (contentLength) {
    const declared = Number.parseInt(contentLength, 10);
    if (Number.isInteger(declared) && declared > maxBytes) {
      throw new Error(`Request body too large: ${declared} bytes`);
    }
  }

  const reader = request.body?.getReader();
  if (!reader) {
    throw new Error('Request body is required');
  }

  const decoder = new TextDecoder();
  let text = '';
  let size = 0;

  while (true) {
    const { done, value } = await reader.read();
    if (done) break;

    size += value.byteLength;
    if (size > maxBytes) {
      throw new Error(`Request body too large: ${size} bytes`);
    }

    text += decoder.decode(value, { stream: true });
  }

  text += decoder.decode();
  if (!text.trim()) {
    throw new Error('Request body is required');
  }

  try {
    return JSON.parse(text) as T;
  } catch {
    throw new Error('Request body must be valid JSON');
  }
}

export function createHttpApp(routes: RouteDefinition[] = [], options: HttpAppOptions = {}): HttpApp {
  const runController = options.runController ?? createRunController();

  return async (request: Request) => {
    const url = new URL(request.url);

    if (request.method === 'GET' && url.pathname === '/health') {
      return jsonResponse({ ok: true });
    }

    if (request.method === 'GET' && url.pathname === '/api/posts') {
      return handleListPosts(request, options);
    }

    if (request.method === 'GET' && url.pathname.startsWith('/api/posts/')) {
      return handleReadPost(request, url, options);
    }

    if (request.method === 'GET' && url.pathname === '/api/runtime-config') {
      return handleReadRuntimeConfig(request, options);
    }

    if (request.method === 'GET' && url.pathname === '/api/runs/status') {
      return handleReadRunStatus(request, options, runController);
    }

    if (request.method === 'POST' && url.pathname === '/api/runs') {
      return handleStartRun(request, options, runController);
    }

    if (request.method === 'POST' && url.pathname === '/api/runs/cancel') {
      return handleCancelRun(request, options, runController);
    }

    if (url.pathname.startsWith('/api/service/')) {
      return handleServiceAction(request, url, options);
    }

    if (request.method === 'PUT' && url.pathname === '/api/runtime-config') {
      return handleWriteRuntimeConfig(request, options);
    }

    if (url.pathname.startsWith('/api/prompts/')) {
      if (request.method === 'GET') {
        return handleReadPrompt(request, url, options);
      }

      if (request.method === 'PUT') {
        return handleWritePrompt(request, url, options);
      }
    }

    if (request.method === 'GET' && !url.pathname.startsWith('/api/')) {
      return serveStaticAsset(url.pathname);
    }

    const route = routes.find((candidate) => candidate.method === request.method && candidate.path === url.pathname);
    if (!route) return notFoundResponse();

    return route.handler(request, url);
  };
}

async function serveStaticAsset(pathname: string): Promise<Response> {
  const root = resolve(process.cwd(), 'dist-web');
  const requestedPath = pathname === '/' ? '/index.html' : pathname;
  const candidate = resolve(root, `.${decodeURIComponent(requestedPath)}`);

  if (!candidate.startsWith(`${root}/`) && candidate !== root) {
    return notFoundResponse();
  }

  try {
    const info = await stat(candidate);
    const filePath = info.isDirectory() ? join(candidate, 'index.html') : candidate;
    const bytes = await readFile(filePath);
    return new Response(bytes, {
      headers: { 'content-type': contentTypeFor(filePath) },
    });
  } catch {
    return notFoundResponse();
  }
}

function contentTypeFor(filePath: string): string {
  switch (extname(filePath)) {
    case '.html':
      return 'text/html; charset=utf-8';
    case '.css':
      return 'text/css; charset=utf-8';
    case '.js':
      return 'text/javascript; charset=utf-8';
    case '.svg':
      return 'image/svg+xml';
    default:
      return 'application/octet-stream';
  }
}

export async function startServer(config: ServerConfig): Promise<StartServerResult> {
  if (!config.adminToken) {
    throw new Error('CHANGEMAKER_ADMIN_TOKEN is required');
  }

  const runController = createRunController({ run: createGenerationRunner(config) });
  const app = createHttpApp([], {
    adminToken: config.adminToken,
    dataDir: config.dataDir,
    postsDir: config.postsDir,
    runController,
    config: { serviceControlEnabled: config.serviceControlEnabled },
  });

  const server = createServer((request, response) => {
    void handleNodeRequest(request, response, app).catch((error: unknown) => {
      const message = error instanceof Error ? error.message : 'Internal server error';
      if (!response.headersSent) {
        response.statusCode = 500;
        response.setHeader('content-type', 'application/json; charset=utf-8');
      }
      response.end(JSON.stringify({ error: message }));
    });
  });

  await new Promise<void>((resolve, reject) => {
    server.once('error', reject);
    server.listen(config.port, config.host, () => {
      server.off('error', reject);
      resolve();
    });
  });

  return server;
}

async function handleReadRunStatus(
  request: Request,
  options: HttpAppOptions,
  runController: RunController,
): Promise<Response> {
  if (!authorizeRequest(request, options.adminToken)) {
    return jsonResponse({ error: 'unauthorized' }, { status: 401 });
  }

  return jsonResponse(runController.getStatus());
}

async function handleStartRun(
  request: Request,
  options: HttpAppOptions,
  runController: RunController,
): Promise<Response> {
  if (!authorizeRequest(request, options.adminToken)) {
    return jsonResponse({ error: 'unauthorized' }, { status: 401 });
  }

  try {
    const body = parseRunRequest(await readJsonBody<unknown>(request));
    if (runController.getStatus().state === 'running') {
      return jsonResponse({ error: 'A run is already in progress' }, { status: 409 });
    }

    void runController.start(body).catch(() => undefined);
    return jsonResponse(runController.getStatus(), { status: 202 });
  } catch (error) {
    const message = toErrorMessage(error);
    if (message === 'Request body is required' || message === 'Request body must be valid JSON' || message === 'Run kind must be daily or manual') {
      return jsonResponse({ error: message }, { status: 400 });
    }

    return jsonResponse({ error: message }, { status: 500 });
  }
}

async function handleCancelRun(
  request: Request,
  options: HttpAppOptions,
  runController: RunController,
): Promise<Response> {
  if (!authorizeRequest(request, options.adminToken)) {
    return jsonResponse({ error: 'unauthorized' }, { status: 401 });
  }

  if (runController.getStatus().state !== 'running' || !runController.cancel()) {
    return jsonResponse({ error: 'No run is active' }, { status: 409 });
  }

  return jsonResponse(runController.getStatus());
}

async function handleListPosts(request: Request, options: HttpAppOptions): Promise<Response> {
  if (!authorizeRequest(request, options.adminToken)) {
    return jsonResponse({ error: 'unauthorized' }, { status: 401 });
  }

  if (!options.postsDir) {
    return jsonResponse([]);
  }

  try {
    return jsonResponse(await listGeneratedPosts(options.postsDir));
  } catch (error) {
    return jsonResponse({ error: toErrorMessage(error) }, { status: 500 });
  }
}

async function handleReadPost(request: Request, url: URL, options: HttpAppOptions): Promise<Response> {
  if (!authorizeRequest(request, options.adminToken)) {
    return jsonResponse({ error: 'unauthorized' }, { status: 401 });
  }

  if (!options.postsDir) {
    return jsonResponse({ error: 'post not found' }, { status: 404 });
  }

  let fileName: string;
  try {
    fileName = decodeURIComponent(url.pathname.slice('/api/posts/'.length));
  } catch {
    return jsonResponse({ error: 'Invalid post file name' }, { status: 400 });
  }

  try {
    return jsonResponse(await readGeneratedPost(options.postsDir, fileName));
  } catch (error) {
    const message = toErrorMessage(error);
    if (message === 'Invalid post file name') {
      return jsonResponse({ error: message }, { status: 400 });
    }

    if (isMissingFileError(error)) {
      return jsonResponse({ error: 'post not found' }, { status: 404 });
    }

    return jsonResponse({ error: message }, { status: 500 });
  }
}

async function handleReadRuntimeConfig(request: Request, options: HttpAppOptions): Promise<Response> {
  if (!authorizeRequest(request, options.adminToken)) {
    return jsonResponse({ error: 'unauthorized' }, { status: 401 });
  }

  if (!options.dataDir) {
    return jsonResponse({ error: 'runtime config unavailable' }, { status: 500 });
  }

  try {
    return jsonResponse(await readRuntimeConfig(options.dataDir));
  } catch (error) {
    return jsonResponse({ error: toErrorMessage(error) }, { status: 500 });
  }
}

async function handleWriteRuntimeConfig(request: Request, options: HttpAppOptions): Promise<Response> {
  if (!authorizeRequest(request, options.adminToken)) {
    return jsonResponse({ error: 'unauthorized' }, { status: 401 });
  }

  if (!options.dataDir) {
    return jsonResponse({ error: 'runtime config unavailable' }, { status: 500 });
  }

  try {
    const body = await readJsonBody<unknown>(request);
    return jsonResponse(await writeRuntimeConfig(options.dataDir, body));
  } catch (error) {
    const message = toErrorMessage(error);
    if (message === 'Request body is required' || message === 'Request body must be valid JSON') {
      return jsonResponse({ error: message }, { status: 400 });
    }

    return jsonResponse({ error: message }, { status: 500 });
  }
}

async function handleServiceAction(request: Request, url: URL, options: HttpAppOptions): Promise<Response> {
  if (!authorizeRequest(request, options.adminToken)) {
    return jsonResponse({ error: 'unauthorized' }, { status: 401 });
  }

  if (!options.config?.serviceControlEnabled) {
    return jsonResponse({ error: 'service control disabled' }, { status: 403 });
  }

  let action: ServiceAction;
  try {
    action = parseServiceAction(url.pathname);
  } catch (error) {
    return jsonResponse({ error: toErrorMessage(error) }, { status: 400 });
  }

  const isStatusAction = action === 'service-status' || action === 'timer-status';
  if (!isStatusAction && request.method !== 'POST') {
    return methodNotAllowedResponse(['POST']);
  }

  if (isStatusAction && request.method !== 'GET' && request.method !== 'POST') {
    return methodNotAllowedResponse(['GET', 'POST']);
  }

  try {
    if (!isStatusAction) {
      const body = await readJsonBody<ServiceConfirmation>(request);
      assertServiceConfirmation(action, body);
    }

    const result = await executeServiceAction(action, options.serviceExecutor);
    const stdout = result.stdout.slice(0, MAX_SERVICE_OUTPUT_CHARS);
    const stderr = result.stderr.slice(0, MAX_SERVICE_OUTPUT_CHARS);

    return jsonResponse({
      action,
      command: result.command,
      exitCode: result.exitCode,
      signal: result.signal,
      stdout,
      stderr,
      stdoutTruncated: result.stdout.length > MAX_SERVICE_OUTPUT_CHARS,
      stderrTruncated: result.stderr.length > MAX_SERVICE_OUTPUT_CHARS,
    });
  } catch (error) {
    const message = toErrorMessage(error);
    if (message === 'Request body is required' || message === 'Request body must be valid JSON' || message.startsWith('Confirmation text must be ')) {
      return jsonResponse({ error: message }, { status: 400 });
    }

    return jsonResponse({ error: message }, { status: 500 });
  }
}

async function handleReadPrompt(request: Request, url: URL, options: HttpAppOptions): Promise<Response> {
  if (!authorizeRequest(request, options.adminToken)) {
    return jsonResponse({ error: 'unauthorized' }, { status: 401 });
  }

  if (!options.dataDir) {
    return jsonResponse({ error: 'runtime config unavailable' }, { status: 500 });
  }

  let fileName: string;
  try {
    fileName = decodeURIComponent(url.pathname.slice('/api/prompts/'.length));
  } catch {
    return jsonResponse({ error: 'Invalid prompt file name' }, { status: 400 });
  }

  try {
    return textResponse(await readPromptTemplate(options.dataDir, fileName));
  } catch (error) {
    const message = toErrorMessage(error);
    if (message === 'Invalid prompt file name') {
      return jsonResponse({ error: message }, { status: 400 });
    }

    if (isMissingFileError(error)) {
      return jsonResponse({ error: 'prompt not found' }, { status: 404 });
    }

    return jsonResponse({ error: message }, { status: 500 });
  }
}

async function handleWritePrompt(request: Request, url: URL, options: HttpAppOptions): Promise<Response> {
  if (!authorizeRequest(request, options.adminToken)) {
    return jsonResponse({ error: 'unauthorized' }, { status: 401 });
  }

  if (!options.dataDir) {
    return jsonResponse({ error: 'runtime config unavailable' }, { status: 500 });
  }

  let fileName: string;
  try {
    fileName = decodeURIComponent(url.pathname.slice('/api/prompts/'.length));
  } catch {
    return jsonResponse({ error: 'Invalid prompt file name' }, { status: 400 });
  }

  try {
    const body = await readJsonBody<{ content?: string }>(request);
    if (typeof body !== 'object' || body === null || typeof body.content !== 'string') {
      return jsonResponse({ error: 'Prompt content must be a string' }, { status: 400 });
    }

    await writePromptTemplate(options.dataDir, fileName, body.content);
    return jsonResponse({ ok: true });
  } catch (error) {
    const message = toErrorMessage(error);
    if (message === 'Invalid prompt file name') {
      return jsonResponse({ error: message }, { status: 400 });
    }

    if (message === 'Request body is required' || message === 'Request body must be valid JSON') {
      return jsonResponse({ error: message }, { status: 400 });
    }

    return jsonResponse({ error: message }, { status: 500 });
  }
}

function isMissingFileError(error: unknown): boolean {
  return typeof error === 'object' && error !== null && 'code' in error && (error as { code?: string }).code === 'ENOENT';
}

function parseServiceAction(pathname: string): ServiceAction {
  const action = decodeURIComponent(pathname.slice('/api/service/'.length));
  if (
    action !== 'service-status' &&
    action !== 'timer-status' &&
    action !== 'restart-service' &&
    action !== 'restart-timer' &&
    action !== 'stop-timer' &&
    action !== 'start-timer'
  ) {
    throw new Error('Invalid service action');
  }

  return action;
}

function parseRunRequest(body: unknown): RunRequest {
  if (!isRecord(body) || (body.kind !== 'daily' && body.kind !== 'manual')) {
    throw new Error('Run kind must be daily or manual');
  }

  const request: RunRequest = { kind: body.kind };

  if (typeof body.since === 'string') request.since = body.since;
  if (typeof body.until === 'string') request.until = body.until;

  return request;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null;
}

function toErrorMessage(error: unknown): string {
  return error instanceof Error ? error.message : 'Internal server error';
}

export function createGenerationRunner(config: ServerConfig, deps: ServerRunDependencies = {}) {
  const readServerRuntimeConfig = deps.readRuntimeConfig ?? readRuntimeConfig;
  const loadServerGenerateConfig = deps.loadGenerateConfig ?? loadGenerateConfig;
  const createStore = deps.storeFactory ?? ((dbPath: string) => new ChangemakerStore(dbPath));
  const crawlServerChanges = deps.crawlChanges ?? crawlChanges;
  const createServerGeneratePlan = deps.createGeneratePlan ?? createGeneratePlan;
  const summarizeServerChanges = deps.summarizeChanges ?? summarizeChanges;
  const createServerOpenAIProvider = deps.createOpenAIProvider ?? createOpenAIProvider;
  const createServerLlamaLineProvider = deps.createLlamaLineProvider ?? createLlamaLineProvider;
  const clusterServerChanges = deps.clusterChanges ?? clusterChanges;
  const writeServerChangesOutput = deps.writeChangesOutput ?? writeChangesOutput;
  const writeServerPostOutput = deps.writePostOutput ?? writePostOutput;
  const loadServerNarrativeState = deps.loadNarrativeState ?? loadNarrativeState;
  const saveServerNarrativeState = deps.saveNarrativeState ?? saveNarrativeState;
  const updateServerNarrativeState = deps.updateNarrativeState ?? updateNarrativeState;
  const writeServerNarrativeStateOutput = deps.writeNarrativeStateOutput ?? writeNarrativeStateOutput;

  return async (request: RunRequest, log: (line: string) => void): Promise<void> => {
    const runtimeConfig = await readServerRuntimeConfig(config.dataDir);
    const dbPath = process.env.CHANGEMAKER_DB_PATH ?? join(config.dataDir, 'changemaker.sqlite');
    const narrativeStatePath = process.env.CHANGEMAKER_NARRATIVE_STATE ?? join(config.dataDir, 'narrative-state.json');
    const store = createStore(dbPath);
    try {
      const baseConfig = loadServerGenerateConfig(
        {
          outDir: config.postsDir,
          dbPath,
          narrativeStatePath,
          owner: runtimeConfig.generation.owner,
          repo: runtimeConfig.generation.repo,
          llmProvider: runtimeConfig.generation.llmProvider,
          llmModel: runtimeConfig.generation.llmModel,
          since: request.kind === 'manual' ? request.since : undefined,
          until: request.kind === 'manual' ? request.until : undefined,
          includeCovered: runtimeConfig.generation.includeCovered,
          diffs: runtimeConfig.generation.includeDiffs,
          maxDiffChars: runtimeConfig.generation.maxDiffChars,
          summarize: runtimeConfig.generation.summarize,
          post: runtimeConfig.generation.post,
          markCovered: runtimeConfig.generation.markCovered,
          narrativeUpdate: runtimeConfig.generation.narrativeUpdate,
          summaryRetries: runtimeConfig.generation.summaryRetries,
          lookbackHours: runtimeConfig.generation.lookbackHours,
          cadenceHours: runtimeConfig.generation.cadenceHours,
          extraPeriods: runtimeConfig.generation.extraPeriods.join(','),
          reportDateOffsetDays: runtimeConfig.generation.reportDateOffsetDays,
          socialSection: runtimeConfig.generation.socialSection,
        },
        process.env,
      );

      let generateConfig = baseConfig;
      if (request.kind !== 'manual') {
        const lastPost = store.getLastGeneratedPost();
        const plan = createServerGeneratePlan({
          config: generateConfig,
          lastPostAt: lastPost ? new Date(lastPost.generatedAt) : undefined,
        });
        generateConfig = { ...generateConfig, now: plan.until, since: plan.since };
      }

      const client = new GiteaClient({ baseUrl: generateConfig.baseUrl, token: generateConfig.token });
      const coveredChangeIds = store.listCoveredChangeIds();
      const output = await crawlServerChanges(client, {
        baseUrl: generateConfig.baseUrl,
        owner: generateConfig.owner,
        repo: generateConfig.repo,
        since: generateConfig.since,
        now: generateConfig.now,
        lookbackHours: generateConfig.lookbackHours,
        coveredChangeIds,
        includeCovered: generateConfig.includeCovered,
        includeDiffs: generateConfig.includeDiffs,
        maxDiffChars: generateConfig.maxDiffChars,
      });

      if (generateConfig.summarize) {
        const provider =
          generateConfig.llmProvider === 'openai'
            ? createServerOpenAIProvider({ apiKey: generateConfig.openaiApiKey, model: generateConfig.llmModel })
            : generateConfig.llmProvider === 'llama-line'
              ? createServerLlamaLineProvider({
                  baseUrl: generateConfig.llamaLineBaseUrl,
                  apiKey: generateConfig.llamaLineApiKey,
                  model: generateConfig.llmModel,
                })
              : undefined;
        if (!provider) throw new Error(`Unsupported LLM provider for summaries: ${generateConfig.llmProvider}`);

        output.changes = await summarizeServerChanges({
          changes: output.changes,
          provider,
          store: store as ChangemakerStore,
          retry: {
            attempts: runtimeConfig.generation.summaryRetries,
            onRetry: ({ changeId, attempt, error }) => {
              process.stderr.write(`retrying summary for ${changeId} after attempt ${attempt}: ${error.message}\n`);
            },
          },
        });
        output.clusters = clusterServerChanges(output.changes);
      }

      const changesPath = await writeServerChangesOutput(output, generateConfig.outDir);
      log(`wrote ${changesPath}`);

      const narrativeState =
        generateConfig.post && generateConfig.narrativeUpdate
          ? await loadServerNarrativeState(generateConfig.narrativeStatePath)
          : undefined;
      const post = generateConfig.post
        ? writeDailyPost(output, {
            ...(narrativeState ? { narrativeState } : {}),
            reportDateOffsetDays: generateConfig.reportDateOffsetDays,
            includeSocialSection: generateConfig.socialSection,
          })
        : undefined;
      const postPath = post ? await writeServerPostOutput(post, output.generatedAt, generateConfig.outDir) : undefined;
      if (postPath) log(`wrote ${postPath}`);

      if (post && generateConfig.narrativeUpdate) {
        const nextState = updateServerNarrativeState(
          narrativeState ?? (await loadServerNarrativeState(generateConfig.narrativeStatePath)),
          post,
          output,
        );
        await saveServerNarrativeState(generateConfig.narrativeStatePath, nextState);
        const narrativePath = await writeServerNarrativeStateOutput(nextState, output.generatedAt, generateConfig.outDir);
        log(`wrote ${narrativePath}`);
      }

      const periodPostPaths: string[] = [];
      if (generateConfig.post && request.kind !== 'manual') {
        for (const period of dueReportPeriods(generateConfig.extraPeriods, generateConfig.now)) {
          const window = periodWindow(period, generateConfig.now);
          const periodOutput = await crawlServerChanges(client, {
            baseUrl: generateConfig.baseUrl,
            owner: generateConfig.owner,
            repo: generateConfig.repo,
            since: window.since,
            now: window.until,
            lookbackHours: window.lookbackHours,
            coveredChangeIds,
            includeCovered: true,
            includeDiffs: generateConfig.includeDiffs,
            maxDiffChars: generateConfig.maxDiffChars,
          });

          if (generateConfig.summarize) {
            const provider =
              generateConfig.llmProvider === 'openai'
                ? createServerOpenAIProvider({ apiKey: generateConfig.openaiApiKey, model: generateConfig.llmModel })
                : generateConfig.llmProvider === 'llama-line'
                  ? createServerLlamaLineProvider({
                      baseUrl: generateConfig.llamaLineBaseUrl,
                      apiKey: generateConfig.llamaLineApiKey,
                      model: generateConfig.llmModel,
                    })
                  : undefined;
            if (!provider) throw new Error(`Unsupported LLM provider for summaries: ${generateConfig.llmProvider}`);
            periodOutput.changes = await summarizeServerChanges({
              changes: periodOutput.changes,
              provider,
              store: store as ChangemakerStore,
              retry: {
                attempts: runtimeConfig.generation.summaryRetries,
                onRetry: ({ changeId, attempt, error }) => {
                  process.stderr.write(`retrying summary for ${changeId} after attempt ${attempt}: ${error.message}\n`);
                },
              },
            });
            periodOutput.clusters = clusterServerChanges(periodOutput.changes);
          }

          const periodPost = writeDailyPost(periodOutput, {
            ...(narrativeState ? { narrativeState } : {}),
            reportKind: period,
            reportDateOffsetDays: generateConfig.reportDateOffsetDays,
            includeSocialSection: generateConfig.socialSection,
          });
          const periodPostPath = await writeServerPostOutput(periodPost, periodOutput.generatedAt, generateConfig.outDir, period);
          periodPostPaths.push(periodPostPath);
          log(`wrote ${periodPostPath}`);
        }
      }

      if (generateConfig.markCovered) {
        store.recordChangeRun({
          id: `changes:${output.generatedAt}`,
          generatedAt: output.generatedAt,
          path: postPath ?? changesPath,
          changes: output.changes,
          kind: postPath ? 'post' : 'changes',
        });
        for (const path of periodPostPaths) {
          store.recordChangeRun({
            id: `period:${path}`,
            generatedAt: output.generatedAt,
            path,
            changes: [],
            kind: 'post',
          });
        }
      }
    } finally {
      store.close();
    }
  };
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

async function handleNodeRequest(request: IncomingMessage, response: ServerResponse, app: HttpApp): Promise<void> {
  const webRequest = await toWebRequest(request);
  const webResponse = await app(webRequest);

  response.statusCode = webResponse.status;
  response.statusMessage = webResponse.statusText;
  webResponse.headers.forEach((value, key) => {
    response.setHeader(key, value);
  });

  if (request.method === 'HEAD' || !webResponse.body) {
    response.end();
    return;
  }

  const body = Buffer.from(await webResponse.arrayBuffer());
  if (body.length > 0) {
    response.write(body);
  }
  response.end();
}

async function toWebRequest(request: IncomingMessage): Promise<Request> {
  const url = new URL(request.url ?? '/', `http://${request.headers.host ?? '127.0.0.1'}`);
  const headers = new Headers();
  for (const [key, value] of Object.entries(request.headers)) {
    if (typeof value === 'undefined') continue;
    if (Array.isArray(value)) {
      for (const item of value) headers.append(key, item);
      continue;
    }
    headers.set(key, value);
  }

  const method = request.method ?? 'GET';
  if (method === 'GET' || method === 'HEAD') {
    return new Request(url, { method, headers });
  }

  const chunks: Buffer[] = [];
  for await (const chunk of request) {
    chunks.push(typeof chunk === 'string' ? Buffer.from(chunk) : Buffer.from(chunk));
  }

  const body = chunks.length > 0 ? Buffer.concat(chunks) : undefined;
  return new Request(url, { method, headers, body });
}
