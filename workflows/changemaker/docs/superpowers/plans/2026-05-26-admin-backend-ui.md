# Admin Backend UI Implementation Plan

> **For agentic workers:** Execute this plan task-by-task. Recommended path:
> dispatch a fresh subagent per task, review each result with `review-quality`,
> then continue. For complex multi-agent splits, use
> `parallel-feature-development`, `team-composition-patterns`, and
> `team-communication-protocols`. Steps use checkbox (`- [ ]`) syntax for
> tracking.

**Goal:** Build a token-protected backend and admin UI for browsing generated posts, editing runtime prompts/config, triggering generation runs, and controlling the deployed service/timer with guardrails.

**Architecture:** Add a small Node HTTP server inside the existing TypeScript package, using existing Changemaker modules instead of shelling out for normal generation. Keep mutable admin state under a runtime data directory so UI edits do not dirty the git checkout. Gate all admin API routes with a single bearer token from environment/config; service/timer operations require explicit confirmation payloads and are only enabled when configured.

**Tech Stack:** TypeScript, Node HTTP/fetch APIs, React, Vite, Tailwind CSS, existing SQLite store, existing Gitea/LLM/writer modules, systemd commands on NUC via guarded backend endpoints.

---

## Scope and Decisions

- V1 scope: posts, prompts, config, run controls, service/timer controls.
- Auth: single admin token.
- Prompt/config persistence: runtime data dir files, not repo files.
- Deployment target: NUC Changemaker service at `/srv/server/projects/changemaker`.
- Backend must never return secret values. Return presence/redacted metadata only for token-like fields.
- Service controls are destructive/ops-sensitive. Require `confirm: true` and an exact `confirmationText` string per operation.

## Complexity Assessment

- Logic depth: Medium — API routing, generation orchestration, and file-backed config.
- Contract sensitivity: High — auth, secrets, filesystem writes, service control endpoints.
- Context span: High — CLI extraction, backend, frontend, deployment updates.
- Discovery need: Medium — current modules are known, but service controls need safe boundaries.
- Failure cost: High — bad endpoint could leak secrets, clear coverage, or restart timer/service unintentionally.
- Concern coupling: High — backend auth, UI controls, file persistence, and ops commands interact.

**Route:** Lightweight written plan with review gate before execution. If implementation expands into multi-host deployment or public exposure, upgrade to a full plan/security review.

## File Structure

### Backend/core files

- Create: `src/server/http.ts` — starts/stops the HTTP server, route dispatch, JSON helpers, static frontend serving.
- Create: `src/server/auth.ts` — bearer token/cookie auth helpers and redaction helpers.
- Create: `src/server/posts.ts` — list/read generated post Markdown/JSON artifacts from configured posts dir.
- Create: `src/server/runtime-config.ts` — read/write runtime admin config and prompt files under data dir.
- Create: `src/server/run-controller.ts` — trigger generation jobs, hold in-memory job status/log tail, prevent concurrent runs.
- Create: `src/server/service-control.ts` — optional guarded systemd/service operations.
- Create: `src/server/types.ts` — shared backend DTOs.
- Create: `src/server.ts` — CLI entrypoint for `changemaker-server`.
- Modify: `src/cli.ts` — extract reusable generation function or import it.
- Create: `src/generate-run.ts` — reusable generation orchestration currently embedded in CLI action.
- Modify: `src/config.ts` — add server config/env parsing.
- Modify: `src/llm/openai.ts`, `src/llm/llama-line.ts`, `src/llm/provider.ts` — load editable prompt templates while preserving defaults.
- Modify: `src/writer/daily-post.ts` — expose editable post voice/preamble config if needed.

### Frontend files

- Modify: `web/src/App.tsx` — replace static shell/posts with admin dashboard sections.
- Delete or repurpose: `web/src/posts.ts` — replace hardcoded sample posts with API client calls.
- Create: `web/src/api.ts` — typed API wrapper with token handling.
- Create: `web/src/types.ts` — UI DTO types matching backend responses.
- Create: `web/src/components/PostReader.tsx` — readable post list and Markdown-ish rendering.
- Create: `web/src/components/PromptEditor.tsx` — prompt list/editor/save flow.
- Create: `web/src/components/ConfigEditor.tsx` — config editor with redacted secrets.
- Create: `web/src/components/RunControl.tsx` — run trigger/status/log tail UI.
- Create: `web/src/components/ServiceControl.tsx` — timer/service status and guarded buttons.
- Modify: `web/src/styles.css` — prose/readability styles.

### Build/deploy/docs files

- Modify: `package.json` — add `server` script and `changemaker-server` bin; likely add no framework dependency for V1.
- Modify: `README.md` — document server usage and required env vars.
- Modify: `docs/implementation-plan.md` — mention admin UI/server conventions.
- Modify on NUC after commit/push: `/srv/server/projects/changemaker/run-daily.sh` only if run invocation changes.
- Create on NUC after deploy: `/etc/systemd/system/changemaker-admin.service` if user wants persistent UI service.

---

## Task 1: Extract Reusable Generation Orchestration

**Files:**
- Create: `src/generate-run.ts`
- Modify: `src/cli.ts`
- Test: `src/generate-run.test.ts`

- [ ] **Step 1: Write the failing orchestration test**

Create `src/generate-run.test.ts`:

```ts
import { describe, expect, it } from 'vitest';
import { createGeneratePlan } from './generate-run.js';

describe('createGeneratePlan', () => {
  it('marks manual windows as authoritative', () => {
    const plan = createGeneratePlan({
      config: {
        now: new Date('2026-05-22T11:00:00.000Z'),
        since: new Date('2026-05-21T11:00:00.000Z'),
        lookbackHours: 36,
        cadenceHours: 24,
        nowWindow: false,
        manualWindow: true,
      },
      lastPostAt: new Date('2026-05-20T11:00:00.000Z'),
    });

    expect(plan.since.toISOString()).toBe('2026-05-21T11:00:00.000Z');
    expect(plan.until.toISOString()).toBe('2026-05-22T11:00:00.000Z');
  });

  it('uses cadence when no manual window is present', () => {
    const plan = createGeneratePlan({
      config: {
        now: new Date('2026-05-25T12:00:00.000Z'),
        since: new Date('2026-05-24T00:00:00.000Z'),
        lookbackHours: 36,
        cadenceHours: 24,
        nowWindow: false,
        manualWindow: false,
      },
      lastPostAt: new Date('2026-05-24T08:00:00.000Z'),
    });

    expect(plan.until.toISOString()).toBe('2026-05-25T08:00:00.000Z');
    expect(plan.since.toISOString()).toBe('2026-05-23T20:00:00.000Z');
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
pnpm vitest run src/generate-run.test.ts
```

Expected: FAIL because `src/generate-run.ts` does not exist.

- [ ] **Step 3: Create reusable planning helper**

Create `src/generate-run.ts`:

```ts
import { resolveGenerateWindow } from './config.js';

export type GeneratePlanInput = {
  config: {
    now: Date;
    since: Date;
    lookbackHours: number;
    cadenceHours: number;
    nowWindow: boolean;
    manualWindow: boolean;
  };
  lastPostAt?: Date;
};

export type GeneratePlan = {
  since: Date;
  until: Date;
};

export function createGeneratePlan(input: GeneratePlanInput): GeneratePlan {
  if (input.config.manualWindow) {
    return { since: input.config.since, until: input.config.now };
  }

  return resolveGenerateWindow({
    now: input.config.now,
    lookbackHours: input.config.lookbackHours,
    cadenceHours: input.config.cadenceHours,
    lastPostAt: input.lastPostAt,
    nowWindow: input.config.nowWindow,
  });
}
```

- [ ] **Step 4: Replace duplicate CLI window logic**

Modify `src/cli.ts` to import and use `createGeneratePlan`:

```ts
import { createGeneratePlan } from './generate-run.js';
```

Replace the current manual-window `if (!config.manualWindow)` block with:

```ts
const lastPost = store.getLastGeneratedPost();
const plan = createGeneratePlan({
  config,
  lastPostAt: lastPost ? new Date(lastPost.generatedAt) : undefined,
});
config = { ...config, now: plan.until, since: plan.since };
```

- [ ] **Step 5: Verify extraction**

Run:

```bash
pnpm vitest run src/generate-run.test.ts src/config.test.ts
make verify
```

Expected: all tests pass.

---

## Task 2: Add Server Config, Auth, and Safe JSON Helpers

**Files:**
- Modify: `src/config.ts`
- Create: `src/server/auth.ts`
- Create: `src/server/types.ts`
- Create: `src/server/http.ts`
- Test: `src/server/auth.test.ts`

- [ ] **Step 1: Write auth tests**

Create `src/server/auth.test.ts`:

```ts
import { describe, expect, it } from 'vitest';
import { authorizeRequest, redactConfigValue } from './auth.js';

describe('server auth', () => {
  it('accepts a matching bearer token', () => {
    const request = new Request('http://localhost/api/posts', {
      headers: { authorization: 'Bearer test-token' },
    });

    expect(authorizeRequest(request, 'test-token')).toBe(true);
  });

  it('rejects missing and wrong bearer tokens', () => {
    expect(authorizeRequest(new Request('http://localhost/api/posts'), 'test-token')).toBe(false);
    expect(
      authorizeRequest(
        new Request('http://localhost/api/posts', { headers: { authorization: 'Bearer wrong' } }),
        'test-token',
      ),
    ).toBe(false);
  });

  it('redacts secret-like config values', () => {
    expect(redactConfigValue('LLAMA_LINE_API_KEY', 'abc123')).toEqual({ present: true, redacted: '••••••' });
    expect(redactConfigValue('CHANGEMAKER_LLM_MODEL', 'gpt-5.4-mini-fast')).toBe('gpt-5.4-mini-fast');
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
pnpm vitest run src/server/auth.test.ts
```

Expected: FAIL because server auth files do not exist.

- [ ] **Step 3: Add server DTO types**

Create `src/server/types.ts`:

```ts
export type RedactedSecret = {
  present: boolean;
  redacted: '••••••';
};

export type PublicServerConfig = {
  host: string;
  port: number;
  postsDir: string;
  dataDir: string;
  serviceControlEnabled: boolean;
};

export type ApiError = {
  error: string;
};
```

- [ ] **Step 4: Add auth helpers**

Create `src/server/auth.ts`:

```ts
import type { RedactedSecret } from './types.js';

const SECRET_NAME_PATTERN = /(TOKEN|KEY|SECRET|PASSWORD)/i;

export function authorizeRequest(request: Request, adminToken: string | undefined): boolean {
  if (!adminToken) return false;
  const header = request.headers.get('authorization') ?? '';
  return header === `Bearer ${adminToken}`;
}

export function redactConfigValue(key: string, value: string | undefined): string | RedactedSecret | undefined {
  if (!SECRET_NAME_PATTERN.test(key)) return value;
  return { present: Boolean(value), redacted: '••••••' };
}
```

- [ ] **Step 5: Add server config parsing**

Modify `src/config.ts` by adding:

```ts
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

export function loadServerConfig(options: ServerOptions = {}, env: NodeJS.ProcessEnv = process.env): ServerConfig {
  return {
    host: options.host ?? env.CHANGEMAKER_SERVER_HOST ?? '127.0.0.1',
    port: parsePositiveInteger(options.port ?? env.CHANGEMAKER_SERVER_PORT ?? 3038),
    postsDir: options.postsDir ?? env.CHANGEMAKER_POSTS_DIR ?? env.CHANGEMAKER_OUT_DIR ?? 'out',
    dataDir: options.dataDir ?? env.CHANGEMAKER_DATA_DIR ?? '.cache',
    adminToken: options.adminToken ?? env.CHANGEMAKER_ADMIN_TOKEN,
    serviceControlEnabled: options.serviceControl ?? env.CHANGEMAKER_SERVICE_CONTROL === 'true',
  };
}
```

- [ ] **Step 6: Verify auth/config**

Run:

```bash
pnpm vitest run src/server/auth.test.ts src/config.test.ts
```

Expected: PASS.

---

## Task 3: Serve Generated Posts Over API

**Files:**
- Create: `src/server/posts.ts`
- Modify: `src/server/http.ts`
- Test: `src/server/posts.test.ts`

- [ ] **Step 1: Write post API tests**

Create `src/server/posts.test.ts`:

```ts
import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { listGeneratedPosts, readGeneratedPost } from './posts.js';

describe('generated posts', () => {
  it('lists markdown posts newest first', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'changemaker-posts-'));
    writeFileSync(join(dir, '2026-05-22-post.md'), '# May 22 field report: Code progress\n\nBody');
    writeFileSync(join(dir, '2026-05-23-post.md'), '# May 23 field report: Infrastructure\n\nBody');

    const posts = await listGeneratedPosts(dir);

    expect(posts.map((post) => post.fileName)).toEqual(['2026-05-23-post.md', '2026-05-22-post.md']);
    expect(posts[0]).toMatchObject({ date: '2026-05-23', title: 'May 23 field report: Infrastructure' });
  });

  it('reads one markdown post by file name', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'changemaker-posts-'));
    writeFileSync(join(dir, '2026-05-22-post.md'), '# Title\n\nReadable body');

    const post = await readGeneratedPost(dir, '2026-05-22-post.md');

    expect(post.markdown).toContain('Readable body');
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
pnpm vitest run src/server/posts.test.ts
```

Expected: FAIL because `posts.ts` does not exist.

- [ ] **Step 3: Implement post file helpers**

Create `src/server/posts.ts`:

```ts
import { readdir, readFile } from 'node:fs/promises';
import { basename, join } from 'node:path';

export type PostListItem = {
  fileName: string;
  date: string;
  title: string;
};

export type PostDetail = PostListItem & {
  markdown: string;
};

export async function listGeneratedPosts(postsDir: string): Promise<PostListItem[]> {
  const files = (await readdir(postsDir, { withFileTypes: true }).catch(() => []))
    .filter((entry) => entry.isFile() && /^\d{4}-\d{2}-\d{2}-post\.md$/.test(entry.name))
    .map((entry) => entry.name)
    .sort()
    .reverse();

  return Promise.all(files.map((fileName) => readPostSummary(postsDir, fileName)));
}

export async function readGeneratedPost(postsDir: string, fileName: string): Promise<PostDetail> {
  const safeName = basename(fileName);
  if (!/^\d{4}-\d{2}-\d{2}-post\.md$/.test(safeName)) {
    throw new Error('Invalid post file name');
  }
  const markdown = await readFile(join(postsDir, safeName), 'utf8');
  return { ...(await readPostSummary(postsDir, safeName)), markdown };
}

async function readPostSummary(postsDir: string, fileName: string): Promise<PostListItem> {
  const markdown = await readFile(join(postsDir, fileName), 'utf8');
  const heading = markdown.match(/^#\s+(.+)$/m)?.[1] ?? fileName;
  return { fileName, date: fileName.slice(0, 10), title: heading };
}
```

- [ ] **Step 4: Wire HTTP post routes**

In `src/server/http.ts`, add route behavior:

```ts
if (url.pathname === '/api/posts' && request.method === 'GET') {
  return jsonResponse(await listGeneratedPosts(config.postsDir));
}

if (url.pathname.startsWith('/api/posts/') && request.method === 'GET') {
  const fileName = decodeURIComponent(url.pathname.slice('/api/posts/'.length));
  return jsonResponse(await readGeneratedPost(config.postsDir, fileName));
}
```

- [ ] **Step 5: Verify post helpers**

Run:

```bash
pnpm vitest run src/server/posts.test.ts
make verify
```

Expected: PASS.

---

## Task 4: Add Runtime Prompt and Config Editing

**Files:**
- Create: `src/server/runtime-config.ts`
- Modify: `src/llm/openai.ts`
- Modify: `src/llm/llama-line.ts`
- Modify: `src/llm/provider.ts`
- Test: `src/server/runtime-config.test.ts`

- [ ] **Step 1: Write runtime config tests**

Create `src/server/runtime-config.test.ts`:

```ts
import { mkdtempSync } from 'node:fs';
import { readFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { ensureRuntimeFiles, readRuntimeConfig, writePromptTemplate } from './runtime-config.js';

describe('runtime config', () => {
  it('creates editable prompt and config files under data dir', async () => {
    const dataDir = mkdtempSync(join(tmpdir(), 'changemaker-runtime-'));

    await ensureRuntimeFiles(dataDir);

    expect(await readFile(join(dataDir, 'prompts', 'summary-system.md'), 'utf8')).toContain('summarize');
    expect(await readRuntimeConfig(dataDir)).toMatchObject({ generation: { summarize: false, post: true } });
  });

  it('writes prompt templates without touching repo files', async () => {
    const dataDir = mkdtempSync(join(tmpdir(), 'changemaker-runtime-'));
    await ensureRuntimeFiles(dataDir);

    await writePromptTemplate(dataDir, 'summary-system.md', 'New prompt');

    expect(await readFile(join(dataDir, 'prompts', 'summary-system.md'), 'utf8')).toBe('New prompt');
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
pnpm vitest run src/server/runtime-config.test.ts
```

Expected: FAIL because runtime-config module does not exist.

- [ ] **Step 3: Implement runtime files**

Create `src/server/runtime-config.ts` with these defaults:

```ts
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { basename, join } from 'node:path';

export type RuntimeConfig = {
  generation: {
    owner?: string;
    repo?: string;
    summarize: boolean;
    post: boolean;
    markCovered: boolean;
    summaryRetries: number;
  };
};

const DEFAULT_RUNTIME_CONFIG: RuntimeConfig = {
  generation: {
    owner: 'subculture-collective',
    summarize: false,
    post: true,
    markCovered: true,
    summaryRetries: 3,
  },
};

const DEFAULT_PROMPTS: Record<string, string> = {
  'summary-system.md': 'You summarize code changes for a technical supporter update. Be specific, concise, and avoid talking about Changemaker itself unless the changed repo is Changemaker.\n',
  'summary-user.md': 'Summarize this change as JSON with headline, userFacingSummary, technicalSummary, impact, notableFiles, and confidence.\n',
  'post-voice.md': 'Write like a builder journal for technical supporters: concrete, warm, no fake momentum, no process notes in public copy.\n',
};

export async function ensureRuntimeFiles(dataDir: string): Promise<void> {
  await mkdir(join(dataDir, 'prompts'), { recursive: true });
  await writeFileIfMissing(join(dataDir, 'admin-config.json'), `${JSON.stringify(DEFAULT_RUNTIME_CONFIG, null, 2)}\n`);
  for (const [fileName, content] of Object.entries(DEFAULT_PROMPTS)) {
    await writeFileIfMissing(join(dataDir, 'prompts', fileName), content);
  }
}

export async function readRuntimeConfig(dataDir: string): Promise<RuntimeConfig> {
  await ensureRuntimeFiles(dataDir);
  return JSON.parse(await readFile(join(dataDir, 'admin-config.json'), 'utf8')) as RuntimeConfig;
}

export async function writeRuntimeConfig(dataDir: string, config: RuntimeConfig): Promise<RuntimeConfig> {
  await ensureRuntimeFiles(dataDir);
  await writeFile(join(dataDir, 'admin-config.json'), `${JSON.stringify(config, null, 2)}\n`, { mode: 0o600 });
  return config;
}

export async function readPromptTemplate(dataDir: string, fileName: string): Promise<string> {
  await ensureRuntimeFiles(dataDir);
  return readFile(join(dataDir, 'prompts', safePromptName(fileName)), 'utf8');
}

export async function writePromptTemplate(dataDir: string, fileName: string, content: string): Promise<void> {
  await ensureRuntimeFiles(dataDir);
  await writeFile(join(dataDir, 'prompts', safePromptName(fileName)), content, { mode: 0o600 });
}

function safePromptName(fileName: string): string {
  const safe = basename(fileName);
  if (!/^[a-z0-9-]+\.md$/.test(safe)) throw new Error('Invalid prompt file name');
  return safe;
}

async function writeFileIfMissing(path: string, content: string): Promise<void> {
  try {
    await readFile(path, 'utf8');
  } catch {
    await writeFile(path, content, { mode: 0o600 });
  }
}
```

- [ ] **Step 4: Expose prompt API routes**

In `src/server/http.ts`, add routes:

```ts
if (url.pathname === '/api/runtime-config' && request.method === 'GET') {
  return jsonResponse(await readRuntimeConfig(config.dataDir));
}

if (url.pathname === '/api/runtime-config' && request.method === 'PUT') {
  return jsonResponse(await writeRuntimeConfig(config.dataDir, await request.json()));
}

if (url.pathname.startsWith('/api/prompts/') && request.method === 'GET') {
  return textResponse(await readPromptTemplate(config.dataDir, decodeURIComponent(url.pathname.slice('/api/prompts/'.length))));
}

if (url.pathname.startsWith('/api/prompts/') && request.method === 'PUT') {
  const body = (await request.json()) as { content?: string };
  await writePromptTemplate(config.dataDir, decodeURIComponent(url.pathname.slice('/api/prompts/'.length)), body.content ?? '');
  return jsonResponse({ ok: true });
}
```

- [ ] **Step 5: Verify runtime config**

Run:

```bash
pnpm vitest run src/server/runtime-config.test.ts
make verify
```

Expected: PASS.

---

## Task 5: Add Run Controller API

**Files:**
- Create: `src/server/run-controller.ts`
- Modify: `src/server/http.ts`
- Test: `src/server/run-controller.test.ts`

- [ ] **Step 1: Write run controller tests**

Create `src/server/run-controller.test.ts`:

```ts
import { describe, expect, it } from 'vitest';
import { createRunController } from './run-controller.js';

describe('run controller', () => {
  it('prevents concurrent runs', async () => {
    let release!: () => void;
    const controller = createRunController({
      run: () => new Promise<void>((resolve) => { release = resolve; }),
    });

    const first = controller.start({ kind: 'daily' });
    expect(controller.getStatus().state).toBe('running');

    await expect(controller.start({ kind: 'daily' })).rejects.toThrow('A run is already in progress');

    release();
    await first;
    expect(controller.getStatus().state).toBe('succeeded');
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
pnpm vitest run src/server/run-controller.test.ts
```

Expected: FAIL because module does not exist.

- [ ] **Step 3: Implement in-memory run controller**

Create `src/server/run-controller.ts`:

```ts
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
  logs: string[];
};

export function createRunController(deps: { run: (request: RunRequest, log: (line: string) => void) => Promise<void> }) {
  let status: RunStatus = { state: 'idle', logs: [] };
  let active: Promise<void> | undefined;

  return {
    getStatus: () => status,
    async start(request: RunRequest): Promise<void> {
      if (active) throw new Error('A run is already in progress');
      status = { state: 'running', startedAt: new Date().toISOString(), logs: [] };
      const log = (line: string) => {
        status.logs = [...status.logs.slice(-199), line];
      };
      active = deps
        .run(request, log)
        .then(() => {
          status = { ...status, state: 'succeeded', finishedAt: new Date().toISOString() };
        })
        .catch((error: unknown) => {
          status = {
            ...status,
            state: 'failed',
            finishedAt: new Date().toISOString(),
            error: error instanceof Error ? error.message : String(error),
          };
          throw error;
        })
        .finally(() => {
          active = undefined;
        });
      return active;
    },
  };
}
```

- [ ] **Step 4: Wire run routes**

Add routes in `src/server/http.ts`:

```ts
if (url.pathname === '/api/runs/status' && request.method === 'GET') {
  return jsonResponse(runController.getStatus());
}

if (url.pathname === '/api/runs' && request.method === 'POST') {
  const body = (await request.json()) as RunRequest;
  runController.start(body).catch(() => undefined);
  return jsonResponse(runController.getStatus(), 202);
}
```

The `run` dependency must call the reusable generation flow with runtime config defaults and append log lines such as `started`, `wrote <path>`, and `finished`.

- [ ] **Step 5: Verify run controller**

Run:

```bash
pnpm vitest run src/server/run-controller.test.ts
make verify
```

Expected: PASS.

---

## Task 6: Add Guarded Service Controls

**Files:**
- Create: `src/server/service-control.ts`
- Modify: `src/server/http.ts`
- Test: `src/server/service-control.test.ts`

- [ ] **Step 1: Write service control tests**

Create `src/server/service-control.test.ts`:

```ts
import { describe, expect, it } from 'vitest';
import { assertServiceConfirmation, createServiceCommand } from './service-control.js';

describe('service control', () => {
  it('requires exact confirmation text', () => {
    expect(() => assertServiceConfirmation('restart-timer', { confirm: true, confirmationText: 'restart-timer' })).not.toThrow();
    expect(() => assertServiceConfirmation('restart-timer', { confirm: true, confirmationText: 'wrong' })).toThrow('Confirmation text must be restart-timer');
  });

  it('maps allowed actions to systemctl commands', () => {
    expect(createServiceCommand('timer-status')).toEqual(['systemctl', 'status', 'changemaker-daily.timer', '--no-pager']);
    expect(createServiceCommand('restart-timer')).toEqual(['sudo', '-n', 'systemctl', 'restart', 'changemaker-daily.timer']);
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
pnpm vitest run src/server/service-control.test.ts
```

Expected: FAIL because module does not exist.

- [ ] **Step 3: Implement command allowlist**

Create `src/server/service-control.ts`:

```ts
export type ServiceAction = 'service-status' | 'timer-status' | 'restart-service' | 'restart-timer' | 'stop-timer' | 'start-timer';

export type ServiceConfirmation = {
  confirm?: boolean;
  confirmationText?: string;
};

const COMMANDS: Record<ServiceAction, string[]> = {
  'service-status': ['systemctl', 'status', 'changemaker-daily.service', '--no-pager'],
  'timer-status': ['systemctl', 'status', 'changemaker-daily.timer', '--no-pager'],
  'restart-service': ['sudo', '-n', 'systemctl', 'restart', 'changemaker-daily.service'],
  'restart-timer': ['sudo', '-n', 'systemctl', 'restart', 'changemaker-daily.timer'],
  'stop-timer': ['sudo', '-n', 'systemctl', 'stop', 'changemaker-daily.timer'],
  'start-timer': ['sudo', '-n', 'systemctl', 'start', 'changemaker-daily.timer'],
};

export function createServiceCommand(action: ServiceAction): string[] {
  return COMMANDS[action];
}

export function assertServiceConfirmation(action: ServiceAction, body: ServiceConfirmation): void {
  if (action === 'service-status' || action === 'timer-status') return;
  if (!body.confirm || body.confirmationText !== action) {
    throw new Error(`Confirmation text must be ${action}`);
  }
}
```

- [ ] **Step 4: Wire disabled-by-default route**

In `src/server/http.ts`, route `/api/service/:action` must:

1. Return `403 {"error":"service control disabled"}` unless `config.serviceControlEnabled` is true.
2. Validate action is in the allowlist.
3. Require confirmation for write actions.
4. Execute only the fixed command array from `createServiceCommand`.
5. Return stdout/stderr with a max length of 8,000 chars.

- [ ] **Step 5: Verify service controls**

Run:

```bash
pnpm vitest run src/server/service-control.test.ts
make verify
```

Expected: PASS.

---

## Task 7: Build the Admin UI Against the API

**Files:**
- Create: `web/src/api.ts`
- Create: `web/src/types.ts`
- Create: `web/src/components/PostReader.tsx`
- Create: `web/src/components/PromptEditor.tsx`
- Create: `web/src/components/ConfigEditor.tsx`
- Create: `web/src/components/RunControl.tsx`
- Create: `web/src/components/ServiceControl.tsx`
- Modify: `web/src/App.tsx`
- Modify: `web/src/styles.css`

- [ ] **Step 1: Add UI DTO types**

Create `web/src/types.ts`:

```ts
export type PostListItem = { fileName: string; date: string; title: string };
export type PostDetail = PostListItem & { markdown: string };
export type RunStatus = { state: 'idle' | 'running' | 'succeeded' | 'failed'; logs: string[]; error?: string };
export type RuntimeConfig = { generation: { owner?: string; repo?: string; summarize: boolean; post: boolean; markCovered: boolean; summaryRetries: number } };
```

- [ ] **Step 2: Add API wrapper**

Create `web/src/api.ts`:

```ts
import type { PostDetail, PostListItem, RunStatus, RuntimeConfig } from './types';

export class ApiClient {
  constructor(private readonly token: string) {}

  async posts(): Promise<PostListItem[]> {
    return this.json('/api/posts');
  }

  async post(fileName: string): Promise<PostDetail> {
    return this.json(`/api/posts/${encodeURIComponent(fileName)}`);
  }

  async runtimeConfig(): Promise<RuntimeConfig> {
    return this.json('/api/runtime-config');
  }

  async saveRuntimeConfig(config: RuntimeConfig): Promise<RuntimeConfig> {
    return this.json('/api/runtime-config', { method: 'PUT', body: JSON.stringify(config) });
  }

  async runStatus(): Promise<RunStatus> {
    return this.json('/api/runs/status');
  }

  async startRun(body: { kind: 'daily' | 'manual'; since?: string; until?: string }): Promise<RunStatus> {
    return this.json('/api/runs', { method: 'POST', body: JSON.stringify(body) });
  }

  private async json<T>(path: string, init: RequestInit = {}): Promise<T> {
    const response = await fetch(path, {
      ...init,
      headers: {
        'content-type': 'application/json',
        authorization: `Bearer ${this.token}`,
        ...init.headers,
      },
    });
    if (!response.ok) throw new Error(await response.text());
    return response.json() as Promise<T>;
  }
}
```

- [ ] **Step 3: Replace static app shell**

Modify `web/src/App.tsx` to:

1. Store token in `localStorage.changemakerAdminToken`.
2. Show a token input screen until a token is set.
3. Instantiate `new ApiClient(token)`.
4. Render tabs: Posts, Prompts, Config, Runs, Service.
5. Avoid showing secret values anywhere.

- [ ] **Step 4: Implement PostReader**

Create `web/src/components/PostReader.tsx` to load `/api/posts`, default to newest, render Markdown headings/paragraphs/lists with readable CSS classes. For V1, do not add a Markdown dependency; split lines and map `#`, `##`, `- `, and paragraphs.

- [ ] **Step 5: Implement editors/controls**

Create prompt/config/run/service components with explicit save/trigger buttons. Service write buttons must require typing exact action text before enabling.

- [ ] **Step 6: Verify UI build**

Run:

```bash
pnpm run build:web
make verify
```

Expected: PASS.

---

## Task 8: Add Server Entrypoint, Scripts, Docs, and Deployment Notes

**Files:**
- Create: `src/server.ts`
- Modify: `package.json`
- Modify: `README.md`
- Modify: `docs/implementation-plan.md`
- Optional deploy: `/etc/systemd/system/changemaker-admin.service` on NUC

- [ ] **Step 1: Create server entrypoint**

Create `src/server.ts`:

```ts
#!/usr/bin/env node
import { loadDotEnvFile, loadServerConfig } from './config.js';
import { startServer } from './server/http.js';

loadDotEnvFile();

const config = loadServerConfig();

if (!config.adminToken) {
  process.stderr.write('changemaker-server: CHANGEMAKER_ADMIN_TOKEN is required\n');
  process.exit(1);
}

const server = await startServer(config);
process.stderr.write(`changemaker-server: listening on http://${config.host}:${config.port}\n`);

process.on('SIGINT', () => server.close());
process.on('SIGTERM', () => server.close());
```

- [ ] **Step 2: Add package scripts/bin**

Modify `package.json`:

```json
{
  "bin": {
    "changemaker": "./dist/cli.js",
    "changemaker-server": "./dist/server.js"
  },
  "scripts": {
    "server": "node dist/server.js"
  }
}
```

Keep existing scripts intact.

- [ ] **Step 3: Document env vars**

Update README with:

```md
## Admin server

```bash
CHANGEMAKER_ADMIN_TOKEN='set-a-long-random-token' pnpm run build && pnpm run server
```

Important env:
- `CHANGEMAKER_SERVER_HOST` default `127.0.0.1`
- `CHANGEMAKER_SERVER_PORT` default `3038`
- `CHANGEMAKER_ADMIN_TOKEN` required
- `CHANGEMAKER_POSTS_DIR` defaults to `out`
- `CHANGEMAKER_DATA_DIR` defaults to `.cache`
- `CHANGEMAKER_SERVICE_CONTROL=true` enables systemd actions
```

- [ ] **Step 4: Verify final build**

Run:

```bash
make verify
node dist/server.js --help || true
```

Expected: `make verify` passes. `node dist/server.js` without token exits with the required-token message.

- [ ] **Step 5: Commit**

Run:

```bash
git status --short
git add src web package.json pnpm-lock.yaml README.md docs/implementation-plan.md docs/superpowers/plans/2026-05-26-admin-backend-ui.md
git diff --cached --check
git diff --cached | rg -n '90pDQ|LLAMA_LINE_API_KEY=|OPENAI_API_KEY=|GITEA_TOKEN=|CHANGEMAKER_ADMIN_TOKEN=' || true
git commit -m "Add admin backend UI plan and server"
```

Expected: no real secrets in staged diff.

---

## Validation Checklist

- `make verify` passes.
- `pnpm run build:web` passes.
- `CHANGEMAKER_ADMIN_TOKEN=test node dist/server.js` starts locally.
- `curl -H 'Authorization: Bearer test' http://127.0.0.1:3038/api/posts` returns post list when pointed at a posts dir.
- `curl http://127.0.0.1:3038/api/posts` returns 401.
- Runtime prompt edits write under `CHANGEMAKER_DATA_DIR/prompts/` only.
- Runtime config edits write under `CHANGEMAKER_DATA_DIR/admin-config.json` only.
- API responses never contain `OPENAI_API_KEY`, `LLAMA_LINE_API_KEY`, `GITEA_TOKEN`, or the admin token value.
- Service-control write endpoints return 403 unless `CHANGEMAKER_SERVICE_CONTROL=true`.
- Service-control write endpoints reject missing/wrong confirmation text.
- Run controller rejects concurrent runs.
- Frontend does not include static sample posts after API wiring.

## Self-Review

- Spec coverage: posts are served/readable (Task 3 + Task 7); prompts are visible/editable (Task 4 + Task 7); config editable (Task 4 + Task 7); control via UI including runs/service (Task 5, Task 6, Task 7); auth (Task 2); data-dir persistence (Task 4).
- Placeholder scan: no placeholder markers and no vague “handle errors” steps without concrete behavior.
- Type consistency: backend `RunStatus`, `RuntimeConfig`, `PostListItem`, and `PostDetail` names are mirrored in frontend types.
