import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react';

type Workflow = {
  id: string;
  kind: string;
  name: string;
  description: string;
  owns: string[];
  uses: string[];
  status: string;
};

type JSONShapeProperty = {
  type: string;
  description?: string;
};

type JSONShape = {
  type: string;
  description?: string;
  properties?: Record<string, JSONShapeProperty>;
  required?: string[];
};

type PipelineStepDefinition = {
  id: string;
  position: number;
  name: string;
  executor: string;
  profile?: string;
  model?: string;
  inputs?: Record<string, string>;
  output_shape?: JSONShape;
};

type PipelineDefinition = {
  id: string;
  workflow_id?: string;
  name: string;
  description?: string;
  steps: PipelineStepDefinition[];
};

export type WorkflowDefinition = {
  id: string;
  name: string;
  description: string;
  status: string;
  owns?: string[];
  uses?: string[];
  input_shape?: JSONShape;
  pipeline: PipelineDefinition;
  created_at?: string;
  updated_at?: string;
};

type Trigger = {
  id: string;
  workflow_id: string;
  type: string;
  name: string;
  description: string;
  enabled: boolean;
  schedule?: string;
  inputs?: Record<string, string>;
  next_run_at?: string;
  last_run_at?: string;
  updated_at?: string;
  last_error?: string;
};

type Run = {
  id: string;
  workflow_id: string;
  trigger_id?: string;
  status: 'queued' | 'running' | 'succeeded' | 'failed' | 'canceled';
  inputs?: Record<string, string>;
  created_at: string;
  started_at?: string;
  finished_at?: string;
  summary?: string;
};

type RunEvent = {
  id: string;
  run_id: string;
  type: string;
  message: string;
  created_at: string;
};

type RunStep = {
  id: string;
  run_id: string;
  position: number;
  name: string;
  executor: string;
  model?: string;
  status: 'queued' | 'running' | 'succeeded' | 'failed' | 'canceled' | 'timed_out';
  inputs?: Record<string, string>;
  output_summary?: string;
  error?: string;
  attempt_count: number;
  correlation_ids?: Record<string, string>;
  created_at: string;
  started_at?: string;
  finished_at?: string;
};

type Artifact = {
  id: string;
  run_id: string;
  name: string;
  kind: string;
  uri: string;
  created_at: string;
};

type RunDetail = {
  run: Run;
  steps: RunStep[];
  events: RunEvent[];
  artifacts: Artifact[];
};

type OpenPilotIssueState = {
  id: string;
  repo: string;
  issue: string;
  phase: string;
  run_id?: string;
  last_step_name?: string;
  branch?: string;
  base?: string;
  worktree_path?: string;
  pull_request_url?: string;
  pull_request_id?: number;
  pull_request_number?: number;
  merge_commit_sha?: string;
  issue_closed: boolean;
  gate_status?: Record<string, string>;
  resume_inputs?: Record<string, string>;
  repair_attempts: number;
  max_repair_attempts: number;
  last_error?: string;
  updated_at: string;
  created_at: string;
};

type OpenPilotStateResponse = {
  state: OpenPilotIssueState;
  next_action: string;
  run?: Run;
};

type DashboardState = {
  workflows: Workflow[];
  triggers: Trigger[];
  runs: Run[];
  adapters: AdapterPayload;
  scheduler: SchedulerStatus;
  workers: WorkerPoolStatus;
};

type SchedulerStatus = {
  enabled: boolean;
  running: boolean;
  interval?: string;
  last_tick?: string;
  last_error?: string;
};

type WorkerPoolStatus = {
  enabled: boolean;
  running: boolean;
  workers?: number;
  active?: number;
  lease?: string;
  poll_interval?: string;
  last_recover_at?: string;
  recovered_runs?: number;
  failed_leases?: number;
  completed_runs?: number;
  failed_runs?: number;
  last_error?: string;
};

type AdapterPayload = {
  queues: AdapterQueue[];
  sessions: AdapterSession[];
  logs: AdapterLog[];
  prompts: AdapterPrompt[];
  config: AdapterConfig;
  agents: AdapterAgent[];
  skills: AdapterSkill[];
  health: AdapterHealth;
  llama_line: AdapterLlamaLine;
  ocq: AdapterOCQ;
  blacktower: AdapterBlacktower;
  changemaker: AdapterChangemaker;
};

type AdapterQueue = {
  id: string;
  name: string;
  status: string;
  depth: number;
  capacity: number;
  workers: number;
  updated_at: string;
};

type AdapterSession = {
  id: string;
  name: string;
  surface: string;
  status: string;
  workflow_id?: string;
  started_at: string;
  updated_at: string;
};

type AdapterLog = {
  id: string;
  level: string;
  source: string;
  message: string;
  created_at: string;
};

type AdapterPrompt = {
  id: string;
  name: string;
  surface: string;
  template: string;
  variables: string[];
  description: string;
};

type AdapterConfig = {
  environment: string;
  version: string;
  base_url: string;
  features: string[];
  limits: Record<string, number>;
};

type AdapterAgent = {
  id: string;
  name: string;
  role: string;
  status: string;
  model: string;
  capabilities: string[];
};

type AdapterSkill = {
  id: string;
  name: string;
  category: string;
  status: string;
  description: string;
};

type AdapterHealth = {
  status: string;
  services: AdapterService[];
  checked_at: string;
};

type AdapterService = {
  name: string;
  status: string;
  details?: string;
};

type AdapterLlamaLine = {
  requests: { id: string; model: string; route: string; status: string; latency_ms: number; queue_depth: number; created_at: string }[];
  stats: { active_requests: number; queued_requests: number; avg_latency_ms: number; success_rate: number };
  search: { query: string; match: string; source: string; score: number }[];
  upstreams: { name: string; url: string; status: string; pending: number; last_seen_at: string }[];
};

type AdapterOCQ = {
  requests: { id: string; workflow: string; trigger: string; status: string; actor: string; started_at: string; updated_at: string }[];
  logs: { id: string; level: string; channel: string; message: string; created_at: string }[];
  sessions: { id: string; title: string; status: string; user: string; started_at: string; updated_at: string }[];
  settings: { workspace: string; provider: string; retention_days: number; cors_allowed: string[] };
};

type AdapterBlacktower = {
  tasks: { id: string; name: string; status: string; owner: string; progress: number; updated_at: string }[];
  reports: { id: string; title: string; type: string; status: string; findings: string[]; generated_at: string }[];
  scheduler_health: { status: string; next_run_at: string; last_tick_at: string; skew_seconds: number };
};

type AdapterChangemaker = {
  posts: { id: string; title: string; status: string; tags: string[]; published_at: string }[];
  service: { name: string; status: string; version: string; healthy: boolean; updated_at: string };
  run_control: { enabled: boolean; mode: string; active_run_id?: string; queued_runs: number };
};

type ViewID = 'overview' | 'queues' | 'sessions' | 'logs' | 'prompts' | 'config' | 'agents' | 'skills';

type DashboardResponse<T> = {
  [key: string]: T[] | null;
};

const apiBase = (import.meta.env.VITE_SWITCHYARD_API_URL ?? '').replace(/\/$/, '');

export const openPilotStateFields = ['Phase', 'Next action', 'Gate status', 'Pull request URL', 'Merge SHA', 'Issue closed', 'Repair attempts', 'Last error'] as const;

const starterWorkflows: Workflow[] = [
  {
    id: 'open-pilot',
    kind: 'open-pilot',
    name: 'Open Pilot',
    description: 'Guided execution for high-signal Gitea issues with operator visibility.',
    owns: ['issue policy', 'branches', 'pull requests'],
    uses: ['ModelBroker', 'OpenCode executor', 'Blacktower'],
    status: 'seeded',
  },
  {
    id: 'changemaker',
    kind: 'changemaker',
    name: 'Changemaker',
    description: 'Timed narrative and change-report workflows with reviewable artifacts.',
    owns: ['change summaries', 'narrative state'],
    uses: ['ModelBroker'],
    status: 'seeded',
  },
  {
    id: 'devlog',
    kind: 'devlog',
    name: 'Devlog',
    description: 'Capture decisions, progress, and follow-ups as work moves.',
    owns: ['progress logs', 'handoffs'],
    uses: ['Switchyard runs'],
    status: 'seeded',
  },
];

const starterWorkflowDefinitions: WorkflowDefinition[] = [
  {
    id: 'changemaker-daily-digest',
    name: 'Changemaker Daily Digest',
    description: 'Native workflow-creator template for timed narrative digests.',
    status: 'active',
    input_shape: { type: 'object', required: ['topic', 'prompt'] },
    pipeline: {
      id: 'changemaker-daily-digest-pipeline',
      workflow_id: 'changemaker-daily-digest',
      name: 'Changemaker Daily Digest',
      steps: [
        { id: 'prepare', position: 1, name: 'changemaker.prepare', executor: 'internal' },
        { id: 'changemaker-draft', position: 2, name: 'changemaker.generate', executor: 'model', profile: 'content-writer', model: '${model}' },
        { id: 'attach', position: 3, name: 'changemaker.attach-draft', executor: 'internal' },
      ],
    },
  },
];

const routingSteps = ['UI', 'Switchyard', 'ModelBroker', 'OpenCode executor', 'Blacktower'];

const views: { id: ViewID; label: string; source: string }[] = [
  { id: 'overview', label: 'Overview', source: 'Switchyard' },
  { id: 'queues', label: 'Queues', source: 'ModelBroker' },
  { id: 'sessions', label: 'Sessions', source: 'OpenCode executor / Blacktower' },
  { id: 'logs', label: 'Logs / Search', source: 'ModelBroker / OpenCode executor' },
  { id: 'prompts', label: 'Prompts', source: 'Workflow policy' },
  { id: 'config', label: 'Config', source: 'Workflow policy / broker' },
  { id: 'agents', label: 'Agents', source: 'Blacktower' },
  { id: 'skills', label: 'Skills', source: 'Blacktower' },
];

const emptyAdapters: AdapterPayload = {
  queues: [],
  sessions: [],
  logs: [],
  prompts: [],
  config: { environment: 'unknown', version: 'unknown', base_url: '', features: [], limits: {} },
  agents: [],
  skills: [],
  health: { status: 'unknown', services: [], checked_at: '' },
  llama_line: { requests: [], stats: { active_requests: 0, queued_requests: 0, avg_latency_ms: 0, success_rate: 0 }, search: [], upstreams: [] },
  ocq: { requests: [], logs: [], sessions: [], settings: { workspace: '', provider: '', retention_days: 0, cors_allowed: [] } },
  blacktower: { tasks: [], reports: [], scheduler_health: { status: 'unknown', next_run_at: '', last_tick_at: '', skew_seconds: 0 } },
  changemaker: { posts: [], service: { name: '', status: 'unknown', version: '', healthy: false, updated_at: '' }, run_control: { enabled: false, mode: 'manual', queued_runs: 0 } },
};

const emptyScheduler: SchedulerStatus = { enabled: false, running: false, interval: 'disabled' };
const emptyWorkers: WorkerPoolStatus = { enabled: false, running: false, workers: 0, active: 0 };

function apiPath(path: string) {
  return `${apiBase}${path}`;
}

class APIError extends Error {
  status: number;

  constructor(status: number) {
    super(`Switchyard API returned ${status}`);
    this.status = status;
  }
}

async function fetchJSON<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(apiPath(path), {
    headers: { 'content-type': 'application/json', ...(init?.headers ?? {}) },
    ...init,
  });

  if (!response.ok) {
    throw new APIError(response.status);
  }

  return (await response.json()) as T;
}

function formatTime(value?: string) {
  if (!value) {
    return 'Not scheduled';
  }

  return new Intl.DateTimeFormat(undefined, {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  }).format(new Date(value));
}

function statusClasses(status: string) {
  switch (status) {
    case 'succeeded':
      return 'border-[hsl(var(--confirm))] bg-[hsl(var(--confirm-soft))] text-[hsl(var(--confirm))]';
    case 'failed':
      return 'border-[hsl(var(--danger))] bg-[hsl(var(--danger-soft))] text-[hsl(var(--danger))]';
    case 'running':
      return 'border-[hsl(var(--pulse))] bg-[hsl(var(--pulse-soft))] text-[hsl(var(--pulse))]';
    case 'queued':
      return 'border-[hsl(var(--warn))] bg-[hsl(var(--warn-soft))] text-[hsl(var(--warn))]';
    case 'canceled':
      return 'border-[hsl(var(--faint))] bg-[hsl(var(--panel-soft))] text-[hsl(var(--muted))]';
    case 'seeded':
      return 'border-[hsl(var(--signal))] bg-[hsl(var(--signal-soft))] text-[hsl(var(--signal))]';
    default:
      return 'border-[hsl(var(--border))] bg-[hsl(var(--panel-soft))] text-[hsl(var(--ink))]';
  }
}

function workflowName(workflows: Workflow[], workflowID: string) {
  return workflows.find((workflow) => workflow.id === workflowID)?.name ?? workflowID;
}

function workflowDefinitionExecutors(definition: WorkflowDefinition) {
  return arrayOrEmpty(definition.pipeline?.steps)
    .slice()
    .sort((left, right) => left.position - right.position)
    .map((step) => step.executor)
    .join(' → ') || 'No steps';
}

function messageFrom(error: unknown) {
  return error instanceof Error ? error.message : 'Unexpected Switchyard dashboard error';
}

function arrayOrEmpty<T>(value: T[] | null | undefined): T[] {
  return Array.isArray(value) ? value : [];
}

function normalizeRunDetail(detail: RunDetail): RunDetail {
  return {
    ...detail,
    steps: arrayOrEmpty(detail.steps),
    events: arrayOrEmpty(detail.events),
    artifacts: arrayOrEmpty(detail.artifacts),
  };
}

function normalizeOpenPilotState(response: OpenPilotStateResponse): OpenPilotStateResponse {
  return {
    ...response,
    state: {
      ...response.state,
      gate_status: response.state.gate_status ?? {},
      resume_inputs: response.state.resume_inputs ?? {},
    },
  };
}

function openPilotRunInputs(detail: RunDetail) {
  const inputs = detail.run.inputs ?? {};
  const repo = inputs.repo?.trim();
  const issue = inputs.issue?.trim();

  if (!repo || !issue) {
    return null;
  }

  return { repo, issue };
}

function formatOpenPilotGateStatus(gateStatus?: Record<string, string>) {
  const entries = Object.entries(gateStatus ?? {});
  return entries.length > 0 ? entries.map(([key, value]) => `${key}: ${value}`).join(' · ') : 'None';
}

function normalizeAdapters(adapters: AdapterPayload): AdapterPayload {
  return {
    ...emptyAdapters,
    ...adapters,
    queues: arrayOrEmpty(adapters.queues),
    sessions: arrayOrEmpty(adapters.sessions),
    logs: arrayOrEmpty(adapters.logs),
    prompts: arrayOrEmpty(adapters.prompts),
    agents: arrayOrEmpty(adapters.agents),
    skills: arrayOrEmpty(adapters.skills),
    health: adapters.health ?? emptyAdapters.health,
    config: adapters.config ?? emptyAdapters.config,
    llama_line: {
      ...emptyAdapters.llama_line,
      ...(adapters.llama_line ?? {}),
      requests: arrayOrEmpty(adapters.llama_line?.requests),
      search: arrayOrEmpty(adapters.llama_line?.search),
      upstreams: arrayOrEmpty(adapters.llama_line?.upstreams),
    },
    ocq: {
      ...emptyAdapters.ocq,
      ...(adapters.ocq ?? {}),
      requests: arrayOrEmpty(adapters.ocq?.requests),
      logs: arrayOrEmpty(adapters.ocq?.logs),
      sessions: arrayOrEmpty(adapters.ocq?.sessions),
    },
    blacktower: {
      ...emptyAdapters.blacktower,
      ...(adapters.blacktower ?? {}),
      tasks: arrayOrEmpty(adapters.blacktower?.tasks),
      reports: arrayOrEmpty(adapters.blacktower?.reports),
    },
    changemaker: {
      ...emptyAdapters.changemaker,
      ...(adapters.changemaker ?? {}),
      posts: arrayOrEmpty(adapters.changemaker?.posts),
    },
  };
}

function Field({ label, value }: { label: string; value: string | number }) {
  return (
    <div className="hud-panel bg-[hsl(var(--panel-soft))] px-3 py-2">
      <div className="hud-label text-[hsl(var(--signal))]">{label}</div>
      <div className="mt-2 break-words text-sm text-[hsl(var(--ink))]">{value}</div>
    </div>
  );
}

function EmptyState({ children }: { children: string }) {
  return <p className="hud-panel bg-[hsl(var(--panel-soft))] px-4 py-3 text-sm text-[hsl(var(--muted))]">{children}</p>;
}

function FeaturePage({ view, adapters }: { view: ViewID; adapters: AdapterPayload }) {
  if (view === 'queues') {
    return (
      <FeatureFrame title="Queue cockpit" kicker="ModelBroker" note="Queue, upstream health, inflight work, and cutover ingress from the native model broker.">
        <div className="mb-4 grid gap-3 md:grid-cols-4">
          <Field label="Active" value={adapters.llama_line.stats.active_requests} />
          <Field label="Queued" value={adapters.llama_line.stats.queued_requests} />
          <Field label="Avg latency" value={`${adapters.llama_line.stats.avg_latency_ms}ms`} />
          <Field label="Success" value={`${Math.round(adapters.llama_line.stats.success_rate * 100)}%`} />
        </div>
        {adapters.queues.length === 0 ? <EmptyState>No queue adapters have reported yet.</EmptyState> : null}
        <div className="grid gap-4 lg:grid-cols-2">
          {adapters.queues.map((queue) => (
            <article key={queue.id} className="hud-panel-strong p-4">
              <div className="flex items-start justify-between gap-3">
                <div>
                  <p className="hud-label text-[hsl(var(--faint))]">{queue.id}</p>
                  <h3 className="mt-2 text-xl font-semibold text-[hsl(var(--ink))]">{queue.name}</h3>
                </div>
                <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(queue.status)}`}>{queue.status}</span>
              </div>
              <div className="mt-4 grid gap-3 sm:grid-cols-3">
                <Field label="Depth" value={queue.depth} />
                <Field label="Capacity" value={queue.capacity} />
                <Field label="Workers" value={queue.workers} />
              </div>
              <p className="mt-4 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">Updated {formatTime(queue.updated_at)}</p>
            </article>
          ))}
        </div>
        <div className="mt-4 grid gap-4 lg:grid-cols-2">
          <article className="hud-panel-strong p-4">
            <h3 className="hud-label text-[hsl(var(--signal))]">Upstreams</h3>
            <div className="mt-4 grid gap-3">
              {adapters.llama_line.upstreams.map((upstream) => (
                <div key={upstream.name} className="hud-panel bg-[hsl(var(--panel-soft))] p-3">
                  <div className="flex items-center justify-between gap-3">
                    <span className="text-sm font-semibold text-[hsl(var(--ink))]">{upstream.name}</span>
                    <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(upstream.status)}`}>{upstream.status}</span>
                  </div>
                  <p className="mt-2 break-all text-xs text-[hsl(var(--muted))]">{upstream.url}</p>
                  <p className="mt-2 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">Pending {upstream.pending} · seen {formatTime(upstream.last_seen_at)}</p>
                </div>
              ))}
            </div>
          </article>
          <article className="hud-panel-strong p-4">
            <h3 className="hud-label text-[hsl(var(--signal))]">Recent requests</h3>
            <div className="mt-4 grid gap-3">
              {adapters.llama_line.requests.map((request) => (
                <div key={request.id} className="hud-panel bg-[hsl(var(--panel-soft))] p-3">
                  <div className="flex items-center justify-between gap-3">
                    <span className="text-sm font-semibold text-[hsl(var(--ink))]">{request.model}</span>
                    <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(request.status)}`}>{request.status}</span>
                  </div>
                  <p className="mt-2 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">{request.route} · {request.latency_ms}ms · depth {request.queue_depth}</p>
                </div>
              ))}
            </div>
          </article>
        </div>
      </FeatureFrame>
    );
  }

  if (view === 'sessions') {
    return (
      <FeatureFrame title="Session console" kicker="OpenCode executor" note="OpenCode executor/Blacktower session list, transcript targets, and future send-prompt controls.">
        <div className="mb-4 grid gap-3 md:grid-cols-4">
          <Field label="Workspace" value={adapters.ocq.settings.workspace || 'unknown'} />
          <Field label="Provider" value={adapters.ocq.settings.provider || 'unknown'} />
          <Field label="Retention" value={`${adapters.ocq.settings.retention_days}d`} />
          <Field label="CORS" value={adapters.ocq.settings.cors_allowed.join(', ') || 'none'} />
        </div>
        {adapters.sessions.length === 0 ? <EmptyState>No sessions have been reported yet.</EmptyState> : null}
        <div className="grid gap-3">
          {adapters.sessions.map((session) => (
            <article key={session.id} className="hud-panel bg-[hsl(var(--panel-strong))] p-4">
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div>
                  <p className="hud-label text-[hsl(var(--faint))]">{session.surface}</p>
                  <h3 className="mt-2 text-lg font-semibold text-[hsl(var(--ink))]">{session.name}</h3>
                  <p className="mt-2 text-xs uppercase tracking-[0.18em] text-[hsl(var(--muted))]">{session.id}</p>
                </div>
                <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(session.status)}`}>{session.status}</span>
              </div>
              <div className="mt-4 grid gap-3 sm:grid-cols-3">
                <Field label="Workflow" value={session.workflow_id || 'unbound'} />
                <Field label="Started" value={formatTime(session.started_at)} />
                <Field label="Updated" value={formatTime(session.updated_at)} />
              </div>
            </article>
          ))}
        </div>
        <article className="hud-panel-strong mt-4 p-4">
          <h3 className="hud-label text-[hsl(var(--signal))]">OpenCode executor requests</h3>
          <div className="mt-4 grid gap-3">
            {adapters.ocq.requests.map((request) => (
              <div key={request.id} className="hud-panel bg-[hsl(var(--panel-soft))] p-3">
                <div className="flex items-center justify-between gap-3">
                  <span className="text-sm font-semibold text-[hsl(var(--ink))]">{request.workflow}</span>
                  <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(request.status)}`}>{request.status}</span>
                </div>
                <p className="mt-2 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">{request.trigger} · {request.actor} · updated {formatTime(request.updated_at)}</p>
              </div>
            ))}
          </div>
        </article>
      </FeatureFrame>
    );
  }

  if (view === 'logs') {
    return (
      <FeatureFrame title="Logs and search" kicker="ModelBroker / OpenCode executor" note="Unified request, log, audit, and semantic-search surface for native modules.">
        <article className="hud-panel-strong mb-4 p-4">
          <h3 className="hud-label text-[hsl(var(--signal))]">Search hits</h3>
          <div className="mt-4 grid gap-3">
            {adapters.llama_line.search.map((hit) => (
              <div key={`${hit.source}-${hit.query}`} className="hud-panel bg-[hsl(var(--panel-soft))] p-3">
                <div className="flex items-center justify-between gap-3">
                  <span className="text-sm font-semibold text-[hsl(var(--ink))]">{hit.match}</span>
                  <span className="hud-chip bg-[hsl(var(--pulse-soft))] px-2.5 py-1 text-[0.62rem] text-[hsl(var(--pulse))]">{Math.round(hit.score * 100)}%</span>
                </div>
                <p className="mt-2 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">{hit.query} · {hit.source}</p>
              </div>
            ))}
          </div>
        </article>
        {adapters.logs.length === 0 ? <EmptyState>No logs are available yet.</EmptyState> : null}
        <div className="grid gap-3" role="log" aria-live="polite">
          {[...adapters.logs, ...adapters.ocq.logs.map((log) => ({ ...log, source: `ocq:${log.channel}` }))].map((log) => (
            <article key={log.id} className="hud-panel bg-[hsl(var(--panel-strong))] px-4 py-3">
              <div className="flex flex-wrap items-center justify-between gap-3">
                <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(log.level)}`}>{log.level}</span>
                <span className="text-[0.65rem] uppercase tracking-[0.18em] text-[hsl(var(--faint))]">{log.source} · {formatTime(log.created_at)}</span>
              </div>
              <p className="mt-3 text-sm leading-6 text-[hsl(var(--muted))]">{log.message}</p>
            </article>
          ))}
        </div>
      </FeatureFrame>
    );
  }

  if (view === 'prompts') {
    return (
      <FeatureFrame title="Prompt workbench" kicker="Workflow policy" note="Editable workflow prompt inventory, variables, and future review controls.">
        <div className="mb-4 grid gap-3 md:grid-cols-4">
          <Field label="Run mode" value={adapters.changemaker.run_control.mode} />
          <Field label="Run control" value={adapters.changemaker.run_control.enabled ? 'enabled' : 'disabled'} />
          <Field label="Queued runs" value={adapters.changemaker.run_control.queued_runs} />
          <Field label="Active run" value={adapters.changemaker.run_control.active_run_id || 'none'} />
        </div>
        {adapters.prompts.length === 0 ? <EmptyState>No prompt templates have been reported yet.</EmptyState> : null}
        <div className="grid gap-4 lg:grid-cols-2">
          {adapters.prompts.map((prompt) => (
            <article key={prompt.id} className="hud-panel-strong p-4">
              <p className="hud-label text-[hsl(var(--faint))]">{prompt.surface}</p>
              <h3 className="mt-2 text-lg font-semibold text-[hsl(var(--ink))]">{prompt.name}</h3>
              <p className="mt-3 text-sm leading-6 text-[hsl(var(--muted))]">{prompt.description}</p>
              <pre className="mt-4 overflow-auto border border-[hsl(var(--border-faint))] bg-[hsl(var(--void))] p-3 text-xs text-[hsl(var(--ink))]">{prompt.template}</pre>
              <p className="mt-3 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">Variables: {prompt.variables.join(', ') || 'none'}</p>
            </article>
          ))}
        </div>
        <article className="hud-panel-strong mt-4 p-4">
          <h3 className="hud-label text-[hsl(var(--signal))]">Posts</h3>
          <div className="mt-4 grid gap-3">
            {adapters.changemaker.posts.map((post) => (
              <div key={post.id} className="hud-panel bg-[hsl(var(--panel-soft))] p-3">
                <div className="flex items-center justify-between gap-3">
                  <span className="text-sm font-semibold text-[hsl(var(--ink))]">{post.title}</span>
                  <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(post.status)}`}>{post.status}</span>
                </div>
                <p className="mt-2 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">{post.tags.join(', ') || 'untagged'} · {formatTime(post.published_at)}</p>
              </div>
            ))}
          </div>
        </article>
      </FeatureFrame>
    );
  }

  if (view === 'config') {
    return (
      <FeatureFrame title="Configuration matrix" kicker="Workflow policy / broker" note="Runtime config, feature flags, limits, and future adapter edit surfaces.">
        <div className="grid gap-3 md:grid-cols-3">
          <Field label="Environment" value={adapters.config.environment} />
          <Field label="Version" value={adapters.config.version} />
          <Field label="Base URL" value={adapters.config.base_url || 'not configured'} />
          <Field label="Workflow policy" value={`${adapters.changemaker.service.status} ${adapters.changemaker.service.version}`} />
          <Field label="Health" value={adapters.health.status} />
          <Field label="Checked" value={formatTime(adapters.health.checked_at)} />
        </div>
        <div className="mt-4 grid gap-4 lg:grid-cols-2">
          <article className="hud-panel-strong p-4">
            <h3 className="hud-label text-[hsl(var(--signal))]">Features</h3>
            <div className="mt-4 flex flex-wrap gap-2">
              {adapters.config.features.map((feature) => <span key={feature} className="hud-chip bg-[hsl(var(--panel-soft))] px-2.5 py-1 text-[0.62rem] text-[hsl(var(--ink))]">{feature}</span>)}
            </div>
          </article>
          <article className="hud-panel-strong p-4">
            <h3 className="hud-label text-[hsl(var(--signal))]">Limits</h3>
            <div className="mt-4 grid gap-2">
              {Object.entries(adapters.config.limits).map(([key, value]) => <Field key={key} label={key} value={value} />)}
            </div>
          </article>
        </div>
        <article className="hud-panel-strong mt-4 p-4">
          <h3 className="hud-label text-[hsl(var(--signal))]">Module health</h3>
          <div className="mt-4 grid gap-3 md:grid-cols-2">
            {adapters.health.services.map((service) => (
              <div key={service.name} className="hud-panel bg-[hsl(var(--panel-soft))] p-3">
                <div className="flex items-center justify-between gap-3">
                  <span className="text-sm font-semibold text-[hsl(var(--ink))]">{service.name}</span>
                  <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(service.status)}`}>{service.status}</span>
                </div>
                <p className="mt-2 text-xs text-[hsl(var(--muted))]">{service.details || 'No details'}</p>
              </div>
            ))}
          </div>
        </article>
      </FeatureFrame>
    );
  }

  if (view === 'agents') {
    return (
      <FeatureFrame title="Agent board" kicker="Blacktower" note="Agent roles, model bindings, board controls, and capability inventory.">
        {adapters.agents.length === 0 ? <EmptyState>No agents have been reported yet.</EmptyState> : null}
        <div className="grid gap-4 lg:grid-cols-2">
          {adapters.agents.map((agent) => (
            <article key={agent.id} className="hud-panel-strong p-4">
              <div className="flex items-start justify-between gap-3">
                <div>
                  <p className="hud-label text-[hsl(var(--faint))]">{agent.role}</p>
                  <h3 className="mt-2 text-lg font-semibold text-[hsl(var(--ink))]">{agent.name}</h3>
                </div>
                <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(agent.status)}`}>{agent.status}</span>
              </div>
              <Field label="Model" value={agent.model} />
              <p className="mt-4 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">Capabilities: {agent.capabilities.join(', ') || 'none'}</p>
            </article>
          ))}
        </div>
        <article className="hud-panel-strong mt-4 p-4">
          <h3 className="hud-label text-[hsl(var(--signal))]">Control-center tasks</h3>
          <div className="mt-4 grid gap-3">
            {adapters.blacktower.tasks.map((task) => (
              <div key={task.id} className="hud-panel bg-[hsl(var(--panel-soft))] p-3">
                <div className="flex items-center justify-between gap-3">
                  <span className="text-sm font-semibold text-[hsl(var(--ink))]">{task.name}</span>
                  <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(task.status)}`}>{task.progress}%</span>
                </div>
                <p className="mt-2 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">{task.owner} · {formatTime(task.updated_at)}</p>
              </div>
            ))}
          </div>
        </article>
      </FeatureFrame>
    );
  }

  return (
    <FeatureFrame title="Skill registry" kicker="Blacktower" note="Installed skills, categories, status, and future editing/sync controls.">
      <div className="mb-4 grid gap-3 md:grid-cols-4">
        <Field label="Scheduler" value={adapters.blacktower.scheduler_health.status} />
        <Field label="Next run" value={formatTime(adapters.blacktower.scheduler_health.next_run_at)} />
        <Field label="Last tick" value={formatTime(adapters.blacktower.scheduler_health.last_tick_at)} />
        <Field label="Skew" value={`${adapters.blacktower.scheduler_health.skew_seconds}s`} />
      </div>
      {adapters.skills.length === 0 ? <EmptyState>No skills have been reported yet.</EmptyState> : null}
      <div className="grid gap-4 lg:grid-cols-2">
        {adapters.skills.map((skill) => (
          <article key={skill.id} className="hud-panel-strong p-4">
            <div className="flex items-start justify-between gap-3">
              <div>
                <p className="hud-label text-[hsl(var(--faint))]">{skill.category}</p>
                <h3 className="mt-2 text-lg font-semibold text-[hsl(var(--ink))]">{skill.name}</h3>
              </div>
              <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(skill.status)}`}>{skill.status}</span>
            </div>
            <p className="mt-3 text-sm leading-6 text-[hsl(var(--muted))]">{skill.description}</p>
          </article>
        ))}
      </div>
      <article className="hud-panel-strong mt-4 p-4">
        <h3 className="hud-label text-[hsl(var(--signal))]">Reports</h3>
        <div className="mt-4 grid gap-3">
          {adapters.blacktower.reports.map((report) => (
            <div key={report.id} className="hud-panel bg-[hsl(var(--panel-soft))] p-3">
              <div className="flex items-center justify-between gap-3">
                <span className="text-sm font-semibold text-[hsl(var(--ink))]">{report.title}</span>
                <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(report.status)}`}>{report.type}</span>
              </div>
              <ul className="mt-3 list-inside list-disc text-sm leading-6 text-[hsl(var(--muted))]">
                {report.findings.map((finding) => <li key={finding}>{finding}</li>)}
              </ul>
            </div>
          ))}
        </div>
      </article>
    </FeatureFrame>
  );
}

function FeatureFrame({ title, kicker, note, children }: { title: string; kicker: string; note: string; children: ReactNode }) {
  return (
    <section className="hud-panel-strong p-4 sm:p-5">
      <div className="border-b-2 border-[hsl(var(--border-faint))] pb-4">
        <p className="hud-label text-[hsl(var(--faint))]">{kicker}</p>
        <h2 className="mt-2 text-2xl font-semibold uppercase tracking-tight text-[hsl(var(--ink))]">{title}</h2>
        <p className="mt-3 max-w-3xl text-sm leading-6 text-[hsl(var(--muted))]">{note}</p>
      </div>
      <div className="mt-4">{children}</div>
    </section>
  );
}

export default function App() {
  const [dashboard, setDashboard] = useState<DashboardState>({ workflows: starterWorkflows, triggers: [], runs: [], adapters: emptyAdapters, scheduler: emptyScheduler, workers: emptyWorkers });
  const [workflowDefinitions, setWorkflowDefinitions] = useState<WorkflowDefinition[]>(starterWorkflowDefinitions);
  const [selectedRunID, setSelectedRunID] = useState<string>('');
  const [runDetail, setRunDetail] = useState<RunDetail | null>(null);
  const [openPilotState, setOpenPilotState] = useState<OpenPilotStateResponse | null>(null);
  const [activeView, setActiveView] = useState<ViewID>('overview');
  const [loading, setLoading] = useState(true);
  const [runningTriggerID, setRunningTriggerID] = useState<string>('');
  const [updatingTriggerID, setUpdatingTriggerID] = useState<string>('');
  const [runActionID, setRunActionID] = useState<string>('');
  const [openPilotRepo, setOpenPilotRepo] = useState('');
  const [openPilotIssue, setOpenPilotIssue] = useState('');
  const [error, setError] = useState<string>('');
  const openPilotRepoInput = openPilotRepo.trim();
  const openPilotIssueInput = openPilotIssue.trim();
  const openPilotInputsReady = openPilotRepoInput !== '' && openPilotIssueInput !== '';

  const loadDashboard = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const [workflowData, workflowDefinitionData, triggerData, runData, adapterData, schedulerData, workerData] = await Promise.all([
        fetchJSON<DashboardResponse<Workflow>>('/api/workflows'),
        fetchJSON<{ workflow_definitions: WorkflowDefinition[] }>('/api/workflow-definitions').catch(() => ({ workflow_definitions: starterWorkflowDefinitions })),
        fetchJSON<DashboardResponse<Trigger>>('/api/triggers'),
        fetchJSON<DashboardResponse<Run>>('/api/runs'),
        fetchJSON<AdapterPayload>('/api/adapters'),
        fetchJSON<SchedulerStatus>('/api/scheduler'),
        fetchJSON<WorkerPoolStatus>('/api/workers'),
      ]);

      setDashboard({
        workflows: arrayOrEmpty(workflowData.workflows as Workflow[] | null | undefined),
        triggers: arrayOrEmpty(triggerData.triggers as Trigger[] | null | undefined),
        runs: arrayOrEmpty(runData.runs as Run[] | null | undefined),
        adapters: normalizeAdapters(adapterData),
        scheduler: schedulerData ?? emptyScheduler,
        workers: workerData ?? emptyWorkers,
      });
      setWorkflowDefinitions(arrayOrEmpty(workflowDefinitionData.workflow_definitions).length > 0 ? arrayOrEmpty(workflowDefinitionData.workflow_definitions) : starterWorkflowDefinitions);
    } catch (caught) {
      setError(messageFrom(caught));
    } finally {
      setLoading(false);
    }
  }, []);

  const loadRunDetail = useCallback(async (runID: string) => {
    if (!runID) {
      setRunDetail(null);
      setOpenPilotState(null);
      return;
    }

    setOpenPilotState(null);
    setError('');
    try {
      const detail = await fetchJSON<RunDetail>(`/api/runs/${runID}`);
      const normalized = normalizeRunDetail(detail);
      setRunDetail(normalized);
      const openPilotInputs = normalized.run.workflow_id === 'open-pilot' ? openPilotRunInputs(normalized) : null;
      if (!openPilotInputs) {
        setOpenPilotState(null);
        return;
      }
      try {
        const state = await fetchJSON<OpenPilotStateResponse>(`/api/open-pilot/state?repo=${encodeURIComponent(openPilotInputs.repo)}&issue=${encodeURIComponent(openPilotInputs.issue)}`);
        setOpenPilotState(normalizeOpenPilotState(state));
      } catch (caught) {
        if (caught instanceof APIError && caught.status === 404) {
          setOpenPilotState(null);
          return;
        }
        throw caught;
      }
    } catch (caught) {
      setError(messageFrom(caught));
    }
  }, []);

  useEffect(() => {
    void loadDashboard();
  }, [loadDashboard]);

  useEffect(() => {
    if (!selectedRunID && dashboard.runs.length > 0) {
      setSelectedRunID(dashboard.runs[0].id);
    }
  }, [dashboard.runs, selectedRunID]);

  useEffect(() => {
    void loadRunDetail(selectedRunID);
  }, [loadRunDetail, selectedRunID]);

  const runSummary = useMemo(() => {
    const active = dashboard.runs.filter((run) => run.status === 'queued' || run.status === 'running').length;
    const succeeded = dashboard.runs.filter((run) => run.status === 'succeeded').length;
    const blocked = dashboard.runs.filter((run) => run.status === 'failed' || run.status === 'canceled').length;

    return [
      { label: 'Active runs', value: String(active), detail: 'Queued or running now' },
      { label: 'Succeeded', value: String(succeeded), detail: 'Recorded control-plane wins' },
      { label: 'Needs input', value: String(blocked), detail: 'Failed or canceled runs' },
    ];
  }, [dashboard.runs]);

  const visibleRuns = dashboard.runs.slice(0, 6);

  async function runTrigger(triggerID: string) {
    setRunningTriggerID(triggerID);
    setError('');

    try {
      const trigger = dashboard.triggers.find((item) => item.id === triggerID);
      const isOpenPilot = trigger?.workflow_id === 'open-pilot';
      const data = isOpenPilot && openPilotInputsReady
        ? await fetchJSON<{ run: Run }>('/api/runs', {
            method: 'POST',
            body: JSON.stringify({ workflow_id: 'open-pilot', trigger_id: triggerID, inputs: { repo: openPilotRepoInput, issue: openPilotIssueInput } }),
          })
        : await fetchJSON<{ run: Run }>(`/api/triggers/${triggerID}/run`, { method: 'POST' });
      setSelectedRunID(data.run.id);
      await loadDashboard();
      await loadRunDetail(data.run.id);
    } catch (caught) {
      setError(messageFrom(caught));
    } finally {
      setRunningTriggerID('');
    }
  }

  async function toggleTrigger(trigger: Trigger) {
    setUpdatingTriggerID(trigger.id);
    setError('');
    try {
      await fetchJSON<{ trigger: Trigger }>(`/api/triggers/${trigger.id}`, {
        method: 'PATCH',
        body: JSON.stringify({ enabled: !trigger.enabled }),
      });
      await loadDashboard();
    } catch (caught) {
      setError(messageFrom(caught));
    } finally {
      setUpdatingTriggerID('');
    }
  }

  async function runAction(runID: string, action: 'retry' | 'cancel') {
    setRunActionID(`${runID}:${action}`);
    setError('');
    try {
      const data = await fetchJSON<{ run: Run }>(`/api/runs/${runID}/${action}`, { method: 'POST' });
      setSelectedRunID(data.run.id);
      await loadDashboard();
      await loadRunDetail(data.run.id);
    } catch (caught) {
      setError(messageFrom(caught));
    } finally {
      setRunActionID('');
    }
  }

  async function resumeOpenPilotState(state: OpenPilotIssueState) {
    setRunActionID(`${state.id}:open-pilot:resume`);
    setError('');
    try {
      const data = await fetchJSON<{ run: Run }>(`/api/open-pilot/state/resume?repo=${encodeURIComponent(state.repo)}&issue=${encodeURIComponent(state.issue)}`, { method: 'POST' });
      setSelectedRunID(data.run.id);
      await loadDashboard();
      await loadRunDetail(data.run.id);
    } catch (caught) {
      setError(messageFrom(caught));
    } finally {
      setRunActionID('');
    }
  }

  async function stepAction(runID: string, stepID: string, action: 'retry' | 'cancel') {
    setRunActionID(`${stepID}:step:${action}`);
    setError('');
    try {
      await fetchJSON<{ step: RunStep } | { run: Run; step: RunStep }>(`/api/runs/${runID}/steps/${stepID}/${action}`, { method: 'POST' });
      await loadRunDetail(runID);
      await loadDashboard();
    } catch (caught) {
      setError(messageFrom(caught));
    } finally {
      setRunActionID('');
    }
  }

  return (
    <main className="min-h-screen bg-[hsl(var(--void))] px-4 py-4 text-[hsl(var(--ink))] sm:px-6 sm:py-6 lg:px-8">
      <section className="mx-auto flex min-h-[calc(100vh-2rem)] max-w-7xl flex-col gap-4 border-[3px] border-[hsl(var(--border))] bg-[hsl(var(--panel))] p-4 shadow-[8px_8px_0_hsl(var(--shadow))] sm:min-h-[calc(100vh-3rem)] sm:p-6 lg:p-8">
        <header className="flex flex-col gap-4 border-b-2 border-[hsl(var(--border-faint))] pb-4 lg:flex-row lg:items-end lg:justify-between">
          <div className="max-w-3xl space-y-4">
            <div className="flex flex-wrap gap-2 text-[hsl(var(--muted))]">
              <span className="hud-chip bg-[hsl(var(--panel-strong))] px-3 py-2 text-[0.62rem] text-[hsl(var(--signal))]">
                <span aria-hidden="true" className="hud-led bg-[hsl(var(--signal))]" />
                Switchyard control-plane suite
              </span>
              <span className="hud-chip bg-[hsl(var(--panel-strong))] px-3 py-2 text-[0.62rem] text-[hsl(var(--ink))]">
                API {apiBase || 'local'}
              </span>
              <span className={`hud-chip px-3 py-2 text-[0.62rem] ${loading ? 'border-[hsl(var(--warn))] bg-[hsl(var(--warn-soft))] text-[hsl(var(--warn))]' : 'border-[hsl(var(--confirm))] bg-[hsl(var(--confirm-soft))] text-[hsl(var(--confirm))]'}`}>
                <span aria-hidden="true" className={`hud-led ${loading ? 'bg-[hsl(var(--warn))]' : 'bg-[hsl(var(--confirm))]'}`} />
                {loading ? 'Syncing' : 'Live'}
              </span>
            </div>

            <div className="space-y-3">
              <p className="hud-label text-[hsl(var(--faint))]">Operator dashboard</p>
              <h1 className="max-w-4xl text-4xl font-semibold uppercase tracking-tight text-[hsl(var(--ink))] sm:text-5xl">
                Orchestrate workflows, triggers, and runs from one hard-edged command surface.
              </h1>
              <p className="max-w-2xl text-sm leading-6 text-[hsl(var(--muted))] sm:text-base">
                Switchyard routes work across operator-facing workflows, scheduled entry points, and live execution
                state so teams can see what is moving, what is blocked, and what is next.
              </p>
            </div>
          </div>

          <div className="hud-panel-strong max-w-sm px-4 py-3">
            <p className="hud-label text-[hsl(var(--faint))]">Architecture</p>
            <div className="mt-3 text-sm font-medium uppercase tracking-[0.24em] text-[hsl(var(--ink))]">
              UI → Switchyard → ModelBroker → OpenCode executor → Blacktower
            </div>
          </div>
        </header>

        {error ? (
          <div className="hud-panel border-[hsl(var(--danger))] bg-[hsl(var(--danger-soft))] px-4 py-3 text-sm text-[hsl(var(--ink))]" role="alert">
            <span className="hud-label text-[hsl(var(--danger))]">Error</span>
            <div className="mt-2">{error}</div>
          </div>
        ) : null}

        <nav className="hud-panel-strong p-3" aria-label="Switchyard UI modules">
          <div className="flex gap-2 overflow-x-auto pb-1">
            {views.map((view) => (
              <button
                key={view.id}
                className={`hud-chip shrink-0 px-3 py-2 text-[0.62rem] transition hover:border-[hsl(var(--signal))] hover:text-[hsl(var(--signal))] ${
                  activeView === view.id
                    ? 'border-[hsl(var(--signal))] bg-[hsl(var(--signal-soft))] text-[hsl(var(--signal))]'
                    : 'bg-[hsl(var(--panel-soft))] text-[hsl(var(--muted))]'
                }`}
                onClick={() => setActiveView(view.id)}
                type="button"
              >
                <span>{view.label}</span>
                <span className="hidden text-[hsl(var(--faint))] sm:inline">/{view.source}</span>
              </button>
            ))}
          </div>
        </nav>

        {activeView === 'overview' ? (
        <div className="grid gap-4 lg:grid-cols-[1.45fr_0.95fr]">
          <section className="grid gap-4">
            <article className="hud-panel-strong p-4 sm:p-5">
              <div className="flex flex-wrap items-end justify-between gap-4 border-b border-[hsl(var(--border-faint))] pb-4">
                <div>
                  <p className="hud-label text-[hsl(var(--faint))]">Workflows</p>
                  <h2 className="mt-2 text-xl font-semibold uppercase tracking-tight text-[hsl(var(--ink))]">Operator lanes</h2>
                </div>
                <span
                  className={`hud-chip px-3 py-2 text-[0.62rem] ${loading ? 'border-[hsl(var(--warn))] bg-[hsl(var(--warn-soft))] text-[hsl(var(--warn))]' : 'border-[hsl(var(--confirm))] bg-[hsl(var(--confirm-soft))] text-[hsl(var(--confirm))]'}`}
                  role="status"
                >
                  <span aria-hidden="true" className={`hud-led ${loading ? 'bg-[hsl(var(--warn))]' : 'bg-[hsl(var(--confirm))]'}`} />
                  {loading ? 'Syncing lanes' : `${dashboard.workflows.length} active lanes`}
                </span>
              </div>

              <div className="mt-4 grid gap-4 md:grid-cols-3">
                {dashboard.workflows.map((workflow) => (
                  <article key={workflow.id} className="hud-panel bg-[hsl(var(--panel-strong))] p-4">
                    <div className="flex items-start justify-between gap-3 border-b border-[hsl(var(--border-faint))] pb-3">
                      <div>
                        <p className="hud-label text-[hsl(var(--faint))]">{workflow.kind}</p>
                        <h3 className="mt-2 text-lg font-semibold text-[hsl(var(--ink))]">{workflow.name}</h3>
                      </div>
                      <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(workflow.status)}`}>{workflow.status}</span>
                    </div>
                    <p className="mt-3 text-sm leading-6 text-[hsl(var(--muted))]">{workflow.description}</p>
                    <dl className="mt-4 space-y-3 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">
                      <div>
                        <dt className="text-[hsl(var(--signal))]">Owns</dt>
                        <dd className="mt-1 leading-5 text-[hsl(var(--ink))]">{workflow.owns.join(', ') || '—'}</dd>
                      </div>
                      <div>
                        <dt className="text-[hsl(var(--signal))]">Uses</dt>
                        <dd className="mt-1 leading-5 text-[hsl(var(--ink))]">{workflow.uses.join(', ') || 'Switchyard'}</dd>
                      </div>
                    </dl>
                  </article>
                ))}
              </div>
            </article>

            <article className="hud-panel-strong p-4 sm:p-5">
              <div className="flex flex-wrap items-end justify-between gap-4 border-b border-[hsl(var(--border-faint))] pb-4">
                <div>
                  <p className="hud-label text-[hsl(var(--faint))]">Workflow Creator</p>
                  <h2 className="mt-2 text-xl font-semibold uppercase tracking-tight text-[hsl(var(--ink))]">Custom pipelines</h2>
                </div>
                <span className="hud-chip border-[hsl(var(--signal))] bg-[hsl(var(--signal-soft))] px-3 py-2 text-[0.62rem] text-[hsl(var(--signal))]">
                  {workflowDefinitions.length} definitions
                </span>
              </div>

              <div className="mt-4 grid gap-3">
                {workflowDefinitions.length === 0 ? (
                  <p className="hud-panel bg-[hsl(var(--panel-soft))] px-4 py-3 text-sm text-[hsl(var(--muted))]">
                    No workflow definitions returned by the API yet.
                  </p>
                ) : null}

                {workflowDefinitions.map((definition) => (
                  <article key={definition.id} className="hud-panel bg-[hsl(var(--panel-strong))] p-4">
                    <div className="flex items-start justify-between gap-3 border-b border-[hsl(var(--border-faint))] pb-3">
                      <div>
                        <p className="hud-label text-[hsl(var(--faint))]">{definition.id}</p>
                        <h3 className="mt-2 text-base font-semibold text-[hsl(var(--ink))]">{definition.name}</h3>
                      </div>
                      <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(definition.status)}`}>{definition.status}</span>
                    </div>
                    <p className="mt-3 text-sm leading-6 text-[hsl(var(--muted))]">{definition.description}</p>
                    <dl className="mt-4 grid gap-3 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))] sm:grid-cols-3">
                      <div>
                        <dt className="text-[hsl(var(--signal))]">Steps</dt>
                        <dd className="mt-1 text-[hsl(var(--ink))]">{arrayOrEmpty(definition.pipeline?.steps).length}</dd>
                      </div>
                      <div className="sm:col-span-2">
                        <dt className="text-[hsl(var(--signal))]">Executor chain</dt>
                        <dd className="mt-1 leading-5 text-[hsl(var(--ink))]">{workflowDefinitionExecutors(definition)}</dd>
                      </div>
                      <div className="sm:col-span-3">
                        <dt className="text-[hsl(var(--signal))]">Required input</dt>
                        <dd className="mt-1 leading-5 text-[hsl(var(--ink))]">{definition.input_shape?.required?.join(', ') || 'none declared'}</dd>
                      </div>
                    </dl>
                  </article>
                ))}
              </div>
            </article>

            <article className="grid gap-4 md:grid-cols-[1.05fr_0.95fr]">
              <div className="hud-panel-strong p-4 sm:p-5">
                <p className="hud-label text-[hsl(var(--faint))]">Triggers</p>
                <h2 className="mt-2 text-xl font-semibold uppercase tracking-tight text-[hsl(var(--ink))]">Entry points</h2>
                <div className="mt-4 grid gap-3">
                  {dashboard.triggers.length === 0 ? (
                    <p className="hud-panel bg-[hsl(var(--panel-soft))] px-4 py-3 text-sm text-[hsl(var(--muted))]">
                      {loading ? 'Loading triggers…' : 'No triggers returned by the API.'}
                    </p>
                  ) : null}

                  {dashboard.triggers.map((trigger) => (
                    <article key={trigger.id} className="hud-panel bg-[hsl(var(--panel-strong))] p-4">
                      <div className="flex items-start justify-between gap-3">
                        <div>
                          <p className="hud-label text-[hsl(var(--faint))]">{trigger.type}</p>
                          <h3 className="mt-2 text-base font-semibold text-[hsl(var(--ink))]">{trigger.name}</h3>
                          <p className="mt-2 text-xs uppercase tracking-[0.18em] text-[hsl(var(--muted))]">
                            {workflowName(dashboard.workflows, trigger.workflow_id)}
                          </p>
                        </div>

                        <div className="flex flex-wrap justify-end gap-2">
                          <button
                            className="hud-chip border-[hsl(var(--pulse))] bg-[hsl(var(--pulse-soft))] px-3 py-2 text-[0.62rem] text-[hsl(var(--ink))] transition hover:bg-[hsl(var(--border-faint))] disabled:cursor-not-allowed disabled:opacity-50"
                            disabled={!trigger.enabled || runningTriggerID === trigger.id || (trigger.workflow_id === 'open-pilot' && !openPilotInputsReady)}
                            title={trigger.workflow_id === 'open-pilot' && !openPilotInputsReady ? 'Fill repo and issue to run Open Pilot' : undefined}
                            onClick={() => void runTrigger(trigger.id)}
                            type="button"
                          >
                            {runningTriggerID === trigger.id ? 'Running…' : 'Run now'}
                          </button>
                          <button
                            className={`hud-chip px-3 py-2 text-[0.62rem] transition disabled:cursor-not-allowed disabled:opacity-50 ${trigger.enabled ? 'border-[hsl(var(--danger))] bg-[hsl(var(--danger-soft))] text-[hsl(var(--danger))]' : 'border-[hsl(var(--confirm))] bg-[hsl(var(--confirm-soft))] text-[hsl(var(--confirm))]'}`}
                            disabled={updatingTriggerID === trigger.id}
                            onClick={() => void toggleTrigger(trigger)}
                            type="button"
                          >
                            {updatingTriggerID === trigger.id ? 'Saving…' : trigger.enabled ? 'Disable' : 'Enable'}
                          </button>
                        </div>
                      </div>

                      <p className="mt-3 text-sm leading-6 text-[hsl(var(--muted))]">{trigger.description}</p>

                      {trigger.workflow_id === 'open-pilot' ? (
                        <div className="mt-4 grid gap-3 sm:grid-cols-2">
                          <label className="hud-panel bg-[hsl(var(--panel-soft))] px-3 py-2 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">
                            <span className="text-[hsl(var(--signal))]">Repo</span>
                            <input
                              className="mt-2 w-full border border-[hsl(var(--border-faint))] bg-[hsl(var(--void))] px-2 py-2 text-sm normal-case tracking-normal text-[hsl(var(--ink))]"
                              onChange={(event) => setOpenPilotRepo(event.target.value)}
                              placeholder="OWNER/REPO"
                              value={openPilotRepo}
                            />
                          </label>
                          <label className="hud-panel bg-[hsl(var(--panel-soft))] px-3 py-2 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">
                            <span className="text-[hsl(var(--signal))]">Issue</span>
                            <input
                              className="mt-2 w-full border border-[hsl(var(--border-faint))] bg-[hsl(var(--void))] px-2 py-2 text-sm normal-case tracking-normal text-[hsl(var(--ink))]"
                              onChange={(event) => setOpenPilotIssue(event.target.value)}
                              placeholder="123"
                              value={openPilotIssue}
                            />
                          </label>
                        </div>
                      ) : null}

                      <div className="mt-4 grid gap-3 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))] sm:grid-cols-2">
                        <div className="hud-panel bg-[hsl(var(--panel-soft))] px-3 py-2">
                          <div className="text-[hsl(var(--signal))]">Schedule</div>
                          <div className="mt-1 text-[hsl(var(--ink))]">{trigger.schedule || 'Manual'}</div>
                        </div>
                        <div className="hud-panel bg-[hsl(var(--panel-soft))] px-3 py-2">
                          <div className="text-[hsl(var(--signal))]">Next run</div>
                          <div className="mt-1 text-[hsl(var(--ink))]">{formatTime(trigger.next_run_at)}</div>
                        </div>
                        <div className="hud-panel bg-[hsl(var(--panel-soft))] px-3 py-2">
                          <div className="text-[hsl(var(--signal))]">Last run</div>
                          <div className="mt-1 text-[hsl(var(--ink))]">{formatTime(trigger.last_run_at)}</div>
                        </div>
                        <div className="hud-panel bg-[hsl(var(--panel-soft))] px-3 py-2">
                          <div className="text-[hsl(var(--signal))]">State</div>
                          <div className="mt-1 text-[hsl(var(--ink))]">{trigger.enabled ? 'enabled' : 'disabled'}</div>
                        </div>
                      </div>
                      {trigger.last_error ? <p className="mt-3 text-xs uppercase tracking-[0.18em] text-[hsl(var(--danger))]">Last error: {trigger.last_error}</p> : null}
                    </article>
                  ))}
                </div>
              </div>

              <div className="hud-panel-strong p-4 sm:p-5">
                <p className="hud-label text-[hsl(var(--faint))]">Runs / status</p>
                <h2 className="mt-2 text-xl font-semibold uppercase tracking-tight text-[hsl(var(--ink))]">Live posture</h2>
                <div className="mt-4 space-y-3">
                  {runSummary.map((run) => (
                    <div key={run.label} className="hud-panel flex items-center justify-between gap-4 bg-[hsl(var(--panel-strong))] px-4 py-3">
                      <div>
                        <div className="text-sm font-medium text-[hsl(var(--ink))]">{run.label}</div>
                        <div className="mt-1 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">{run.detail}</div>
                      </div>
                      <div className="text-3xl font-semibold tracking-tight text-[hsl(var(--signal))]">{run.value}</div>
                    </div>
                  ))}
                </div>
                <div className="mt-4 grid gap-3 sm:grid-cols-2">
                  <Field label="Scheduler" value={dashboard.scheduler.enabled ? (dashboard.scheduler.running ? 'running' : 'enabled') : 'disabled'} />
                  <Field label="Interval" value={dashboard.scheduler.interval || 'disabled'} />
                  <Field label="Last tick" value={formatTime(dashboard.scheduler.last_tick)} />
                  <Field label="Last error" value={dashboard.scheduler.last_error || 'none'} />
                  <Field label="Workers" value={dashboard.workers.enabled ? `${dashboard.workers.active ?? 0}/${dashboard.workers.workers ?? 0} active` : 'disabled'} />
                  <Field label="Worker state" value={dashboard.workers.enabled ? (dashboard.workers.running ? 'running' : 'enabled') : 'disabled'} />
                  <Field label="Completed" value={dashboard.workers.completed_runs ?? 0} />
                  <Field label="Lease failures" value={dashboard.workers.failed_leases ?? 0} />
                </div>
              </div>
            </article>
          </section>

          <aside className="grid gap-4">
            <article className="hud-panel-strong p-4 sm:p-5">
              <p className="hud-label text-[hsl(var(--faint))]">Run detail</p>
              <h2 className="mt-2 text-xl font-semibold uppercase tracking-tight text-[hsl(var(--ink))]">Events and artifacts</h2>

              <div className="mt-4 grid gap-3">
                {dashboard.runs.length === 0 ? (
                  <p className="hud-panel bg-[hsl(var(--panel-soft))] px-4 py-3 text-sm text-[hsl(var(--muted))]">
                    Dispatch a trigger to create the first run envelope.
                  </p>
                ) : (
                  visibleRuns.map((run) => (
                    <button
                      key={run.id}
                      className={`hud-panel text-left transition hover:bg-[hsl(var(--panel-soft))] focus-visible:ring-2 focus-visible:ring-[hsl(var(--pulse))] focus-visible:ring-offset-2 focus-visible:ring-offset-[hsl(var(--void))] ${
                        selectedRunID === run.id ? 'border-[hsl(var(--signal))] bg-[hsl(var(--signal-soft))]' : 'bg-[hsl(var(--panel-soft))]'
                      }`}
                      onClick={() => setSelectedRunID(run.id)}
                      type="button"
                    >
                      <div className="flex items-center justify-between gap-3 px-4 py-3">
                        <span className="text-sm font-medium text-[hsl(var(--ink))]">{workflowName(dashboard.workflows, run.workflow_id)}</span>
                        <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(run.status)}`}>{run.status}</span>
                      </div>
                      <div className="border-t border-[hsl(var(--border-faint))] px-4 py-3 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">
                        <div>{run.id}</div>
                        <div className="mt-1 text-[hsl(var(--muted))]">{formatTime(run.created_at)}</div>
                      </div>
                    </button>
                  ))
                )}
              </div>

              {runDetail ? (
                <div className="mt-4 space-y-4 border-t-2 border-[hsl(var(--border-faint))] pt-4" aria-live="polite">
                  <div className="hud-panel bg-[hsl(var(--panel-strong))] p-4">
                    <h3 className="text-sm font-semibold uppercase tracking-[0.2em] text-[hsl(var(--ink))]">
                      {runDetail.run.summary || 'Run events'}
                    </h3>
                    <p className="mt-2 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">
                      {workflowName(dashboard.workflows, runDetail.run.workflow_id)} · {runDetail.run.status}
                    </p>
                    <dl className="mt-4 grid gap-3 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))] sm:grid-cols-2">
                      <div className="hud-panel bg-[hsl(var(--panel-soft))] px-3 py-2">
                        <dt className="text-[hsl(var(--signal))]">Created</dt>
                        <dd className="mt-1 text-[hsl(var(--ink))]">{formatTime(runDetail.run.created_at)}</dd>
                      </div>
                      <div className="hud-panel bg-[hsl(var(--panel-soft))] px-3 py-2">
                        <dt className="text-[hsl(var(--signal))]">Artifacts</dt>
                        <dd className="mt-1 text-[hsl(var(--ink))]">{runDetail.artifacts.length}</dd>
                      </div>
                    </dl>
                    <div className="mt-4 flex flex-wrap gap-2">
                      <button
                        className="hud-chip border-[hsl(var(--pulse))] bg-[hsl(var(--pulse-soft))] px-3 py-2 text-[0.62rem] text-[hsl(var(--ink))] transition hover:bg-[hsl(var(--border-faint))] disabled:cursor-not-allowed disabled:opacity-50"
                        disabled={runActionID === `${runDetail.run.id}:retry`}
                        onClick={() => openPilotState && runDetail.run.workflow_id === 'open-pilot' && ['failed', 'paused', 'agent_completed', 'merged'].includes(openPilotState.state.phase) ? void resumeOpenPilotState(openPilotState.state) : void runAction(runDetail.run.id, 'retry')}
                        type="button"
                      >
                        {runActionID === `${runDetail.run.id}:retry` || (openPilotState && runActionID === `${openPilotState.state.id}:open-pilot:resume`) ? 'Retrying…' : openPilotState && runDetail.run.workflow_id === 'open-pilot' && ['failed', 'paused', 'agent_completed', 'merged'].includes(openPilotState.state.phase) ? 'Resume Open Pilot' : 'Retry run'}
                      </button>
                      <button
                        className="hud-chip border-[hsl(var(--danger))] bg-[hsl(var(--danger-soft))] px-3 py-2 text-[0.62rem] text-[hsl(var(--danger))] transition hover:bg-[hsl(var(--border-faint))] disabled:cursor-not-allowed disabled:opacity-50"
                        disabled={!['queued', 'running'].includes(runDetail.run.status) || runActionID === `${runDetail.run.id}:cancel`}
                        onClick={() => void runAction(runDetail.run.id, 'cancel')}
                        type="button"
                      >
                        {runActionID === `${runDetail.run.id}:cancel` ? 'Canceling…' : 'Cancel run'}
                      </button>
                    </div>
                  </div>

                  {openPilotState && runDetail.run.workflow_id === 'open-pilot' ? (
                    <div className="hud-panel bg-[hsl(var(--panel-strong))] p-4">
                      <div className="flex items-center justify-between gap-3">
                        <div>
                          <p className="hud-label text-[hsl(var(--faint))]">Open Pilot lifecycle state</p>
                          <h3 className="mt-2 text-sm font-semibold uppercase tracking-[0.2em] text-[hsl(var(--ink))]">
                            {openPilotState.state.repo}#{openPilotState.state.issue}
                          </h3>
                        </div>
                        <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(openPilotState.state.phase)}`}>
                          {openPilotState.state.phase}
                        </span>
                      </div>

                      <dl className="mt-4 grid gap-3 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))] sm:grid-cols-2">
                        <div className="hud-panel bg-[hsl(var(--panel-soft))] px-3 py-2">
                          <dt className="text-[hsl(var(--signal))]">Next action</dt>
                          <dd className="mt-1 break-words text-[hsl(var(--ink))]">{openPilotState.next_action}</dd>
                        </div>
                        <div className="hud-panel bg-[hsl(var(--panel-soft))] px-3 py-2">
                          <dt className="text-[hsl(var(--signal))]">Gate status</dt>
                          <dd className="mt-1 break-words text-[hsl(var(--ink))]">{formatOpenPilotGateStatus(openPilotState.state.gate_status)}</dd>
                        </div>
                        <div className="hud-panel bg-[hsl(var(--panel-soft))] px-3 py-2">
                          <dt className="text-[hsl(var(--signal))]">Pull request URL</dt>
                          <dd className="mt-1 break-words text-[hsl(var(--ink))]">{openPilotState.state.pull_request_url || '—'}</dd>
                        </div>
                        <div className="hud-panel bg-[hsl(var(--panel-soft))] px-3 py-2">
                          <dt className="text-[hsl(var(--signal))]">Merge SHA</dt>
                          <dd className="mt-1 break-words text-[hsl(var(--ink))]">{openPilotState.state.merge_commit_sha || '—'}</dd>
                        </div>
                        <div className="hud-panel bg-[hsl(var(--panel-soft))] px-3 py-2">
                          <dt className="text-[hsl(var(--signal))]">Issue closed</dt>
                          <dd className="mt-1 text-[hsl(var(--ink))]">{openPilotState.state.issue_closed ? 'yes' : 'no'}</dd>
                        </div>
                        <div className="hud-panel bg-[hsl(var(--panel-soft))] px-3 py-2">
                          <dt className="text-[hsl(var(--signal))]">Repair attempts</dt>
                          <dd className="mt-1 text-[hsl(var(--ink))]">{openPilotState.state.repair_attempts} / {openPilotState.state.max_repair_attempts}</dd>
                        </div>
                        <div className="hud-panel bg-[hsl(var(--panel-soft))] px-3 py-2 sm:col-span-2">
                          <dt className="text-[hsl(var(--signal))]">Last error</dt>
                          <dd className="mt-1 break-words text-[hsl(var(--ink))]">{openPilotState.state.last_error || '—'}</dd>
                        </div>
                      </dl>
                    </div>
                  ) : null}

                  <div className="hud-panel bg-[hsl(var(--panel-strong))] p-4">
                    <div className="flex items-center justify-between gap-3">
                      <div>
                        <p className="hud-label text-[hsl(var(--faint))]">Step timeline</p>
                        <h3 className="mt-2 text-sm font-semibold uppercase tracking-[0.2em] text-[hsl(var(--ink))]">
                          {runDetail.steps.length} recorded step{runDetail.steps.length === 1 ? '' : 's'}
                        </h3>
                      </div>
                    </div>

                    <div className="mt-4 grid gap-3">
                      {runDetail.steps.length === 0 ? (
                        <p className="hud-panel bg-[hsl(var(--panel-soft))] px-4 py-3 text-sm text-[hsl(var(--muted))]">
                          No durable steps have been recorded for this run yet.
                        </p>
                      ) : (
                        runDetail.steps.map((step) => (
                          <article key={step.id} className="hud-panel bg-[hsl(var(--panel-soft))] p-4">
                            <div className="flex flex-wrap items-start justify-between gap-3">
                              <div>
                                <p className="hud-label text-[hsl(var(--faint))]">Step {step.position} · {step.executor}</p>
                                <h4 className="mt-2 text-sm font-semibold text-[hsl(var(--ink))]">{step.name}</h4>
                                <p className="mt-2 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">
                                  Attempts: {step.attempt_count} · {formatTime(step.started_at)} → {formatTime(step.finished_at)}
                                </p>
                              </div>
                              <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(step.status)}`}>{step.status}</span>
                            </div>
                            {step.model ? <p className="mt-3 text-xs uppercase tracking-[0.18em] text-[hsl(var(--signal))]">Model: {step.model}</p> : null}
                            {step.output_summary ? <p className="mt-3 text-sm leading-6 text-[hsl(var(--muted))]">{step.output_summary}</p> : null}
                            {step.error ? <p className="mt-3 text-sm leading-6 text-[hsl(var(--danger))]">{step.error}</p> : null}
                            {step.correlation_ids && Object.keys(step.correlation_ids).length > 0 ? (
                              <dl className="mt-3 grid gap-2 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">
                                {Object.entries(step.correlation_ids).map(([key, value]) => (
                                  <div key={key} className="flex justify-between gap-3 border-t border-[hsl(var(--border-faint))] pt-2">
                                    <dt className="text-[hsl(var(--signal))]">{key}</dt>
                                    <dd className="break-all text-right text-[hsl(var(--ink))]">{value}</dd>
                                  </div>
                                ))}
                              </dl>
                            ) : null}
                            <div className="mt-4 flex flex-wrap gap-2">
                              <button
                                className="hud-chip border-[hsl(var(--pulse))] bg-[hsl(var(--pulse-soft))] px-3 py-2 text-[0.62rem] text-[hsl(var(--ink))] transition hover:bg-[hsl(var(--border-faint))] disabled:cursor-not-allowed disabled:opacity-50"
                                disabled={!['failed', 'canceled', 'timed_out'].includes(step.status) || runActionID === `${step.id}:step:retry`}
                                onClick={() => void stepAction(runDetail.run.id, step.id, 'retry')}
                                type="button"
                              >
                                {runActionID === `${step.id}:step:retry` ? 'Retrying…' : 'Retry step'}
                              </button>
                              <button
                                className="hud-chip border-[hsl(var(--danger))] bg-[hsl(var(--danger-soft))] px-3 py-2 text-[0.62rem] text-[hsl(var(--danger))] transition hover:bg-[hsl(var(--border-faint))] disabled:cursor-not-allowed disabled:opacity-50"
                                disabled={!['queued', 'running'].includes(step.status) || runActionID === `${step.id}:step:cancel`}
                                onClick={() => void stepAction(runDetail.run.id, step.id, 'cancel')}
                                type="button"
                              >
                                {runActionID === `${step.id}:step:cancel` ? 'Canceling…' : 'Cancel step'}
                              </button>
                            </div>
                          </article>
                        ))
                      )}
                    </div>
                  </div>

                  <div className="space-y-3">
                    {runDetail.events.length === 0 ? (
                      <p className="hud-panel bg-[hsl(var(--panel-soft))] px-4 py-3 text-sm text-[hsl(var(--muted))]">
                        No events have been recorded for this run yet.
                      </p>
                    ) : (
                      runDetail.events.map((event) => (
                        <article key={event.id} className="hud-panel border-[hsl(var(--pulse))] bg-[hsl(var(--panel-strong))] px-4 py-3">
                          <div className="flex items-center justify-between gap-3">
                            <p className="hud-label text-[hsl(var(--pulse))]">{event.type}</p>
                            <span className="text-[0.65rem] uppercase tracking-[0.18em] text-[hsl(var(--faint))]">
                              {formatTime(event.created_at)}
                            </span>
                          </div>
                          <p className="mt-2 text-sm leading-6 text-[hsl(var(--muted))]">{event.message}</p>
                        </article>
                      ))
                    )}
                  </div>

                  <div className="hud-panel bg-[hsl(var(--panel-strong))] p-4">
                    <h3 className="text-sm font-semibold uppercase tracking-[0.2em] text-[hsl(var(--ink))]">Artifacts</h3>
                    <p className="mt-2 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">
                      {runDetail.artifacts.length ? `${runDetail.artifacts.length} attached artifact${runDetail.artifacts.length === 1 ? '' : 's'}` : 'No artifacts attached'}
                    </p>

                    {runDetail.artifacts.length > 0 ? (
                      <div className="mt-4 grid gap-3">
                        {runDetail.artifacts.map((artifact) => (
                          <div key={artifact.id} className="hud-panel bg-[hsl(var(--panel-soft))] px-3 py-2">
                            <div className="flex items-start justify-between gap-3">
                              <div>
                                <p className="text-sm font-medium text-[hsl(var(--ink))]">{artifact.name}</p>
                                <p className="mt-1 text-[0.65rem] uppercase tracking-[0.18em] text-[hsl(var(--faint))]">
                                  {artifact.kind}
                                </p>
                              </div>
                              <span className="text-[0.65rem] uppercase tracking-[0.18em] text-[hsl(var(--signal))]">
                                {formatTime(artifact.created_at)}
                              </span>
                            </div>
                            <p className="mt-2 break-all text-xs text-[hsl(var(--muted))]">{artifact.uri}</p>
                            <a className="mt-3 inline-block text-xs uppercase tracking-[0.18em] text-[hsl(var(--signal))] underline" href={artifact.uri} rel="noreferrer" target="_blank">
                              Open artifact
                            </a>
                          </div>
                        ))}
                      </div>
                    ) : null}
                  </div>
                </div>
              ) : null}
            </article>

            <article className="hud-panel-strong p-4 sm:p-5">
              <p className="hud-label text-[hsl(var(--faint))]">Routing model</p>
              <div className="mt-4 flex flex-col gap-3 text-sm text-[hsl(var(--ink))]">
                {routingSteps.map((item, index) => (
                  <div key={item}>
                    <div className="hud-panel bg-[hsl(var(--panel-soft))] px-4 py-3 uppercase tracking-[0.2em]">{item}</div>
                    {index < routingSteps.length - 1 ? <div className="px-4 pt-2 text-[hsl(var(--faint))]">↓</div> : null}
                  </div>
                ))}
              </div>
            </article>
          </aside>
        </div>
        ) : (
          <FeaturePage view={activeView} adapters={dashboard.adapters} />
        )}
      </section>
    </main>
  );
}
