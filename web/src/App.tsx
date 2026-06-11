import { useCallback, useEffect, useMemo, useState } from 'react';

type Workflow = {
  id: string;
  kind: string;
  name: string;
  description: string;
  owns: string[];
  uses: string[];
  status: string;
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
  events: RunEvent[];
  artifacts: Artifact[];
};

type DashboardState = {
  workflows: Workflow[];
  triggers: Trigger[];
  runs: Run[];
};

type DashboardResponse<T> = {
  [key: string]: T[] | null;
};

const apiBase = (import.meta.env.VITE_SWITCHYARD_API_URL ?? '').replace(/\/$/, '');

const starterWorkflows: Workflow[] = [
  {
    id: 'open-pilot',
    kind: 'open-pilot',
    name: 'Open Pilot',
    description: 'Guided execution for high-signal Gitea issues with operator visibility.',
    owns: ['issue policy', 'branches', 'pull requests'],
    uses: ['llama-line', 'OCQ', 'Blacktower'],
    status: 'seeded',
  },
  {
    id: 'changemaker',
    kind: 'changemaker',
    name: 'Changemaker',
    description: 'Timed narrative and change-report workflows with reviewable artifacts.',
    owns: ['change summaries', 'narrative state'],
    uses: ['llama-line'],
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

function apiPath(path: string) {
  return `${apiBase}${path}`;
}

async function fetchJSON<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(apiPath(path), {
    headers: { 'content-type': 'application/json', ...(init?.headers ?? {}) },
    ...init,
  });

  if (!response.ok) {
    throw new Error(`Switchyard API returned ${response.status}`);
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
      return 'border-emerald-400/20 bg-emerald-400/10 text-emerald-200';
    case 'failed':
      return 'border-rose-400/20 bg-rose-400/10 text-rose-200';
    case 'running':
      return 'border-cyan-400/20 bg-cyan-400/10 text-cyan-100';
    case 'queued':
      return 'border-amber-400/20 bg-amber-400/10 text-amber-100';
    default:
      return 'border-white/10 bg-white/5 text-zinc-300';
  }
}

function workflowName(workflows: Workflow[], workflowID: string) {
  return workflows.find((workflow) => workflow.id === workflowID)?.name ?? workflowID;
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
    events: arrayOrEmpty(detail.events),
    artifacts: arrayOrEmpty(detail.artifacts),
  };
}

export default function App() {
  const [dashboard, setDashboard] = useState<DashboardState>({ workflows: starterWorkflows, triggers: [], runs: [] });
  const [selectedRunID, setSelectedRunID] = useState<string>('');
  const [runDetail, setRunDetail] = useState<RunDetail | null>(null);
  const [loading, setLoading] = useState(true);
  const [runningTriggerID, setRunningTriggerID] = useState<string>('');
  const [error, setError] = useState<string>('');

  const loadDashboard = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const [workflowData, triggerData, runData] = await Promise.all([
        fetchJSON<DashboardResponse<Workflow>>('/api/workflows'),
        fetchJSON<DashboardResponse<Trigger>>('/api/triggers'),
        fetchJSON<DashboardResponse<Run>>('/api/runs'),
      ]);

      setDashboard({
        workflows: arrayOrEmpty(workflowData.workflows as Workflow[] | null | undefined),
        triggers: arrayOrEmpty(triggerData.triggers as Trigger[] | null | undefined),
        runs: arrayOrEmpty(runData.runs as Run[] | null | undefined),
      });
    } catch (caught) {
      setError(messageFrom(caught));
    } finally {
      setLoading(false);
    }
  }, []);

  const loadRunDetail = useCallback(async (runID: string) => {
    if (!runID) {
      setRunDetail(null);
      return;
    }

    setError('');
    try {
      const detail = await fetchJSON<RunDetail>(`/api/runs/${runID}`);
      setRunDetail(normalizeRunDetail(detail));
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

  async function runTrigger(triggerID: string) {
    setRunningTriggerID(triggerID);
    setError('');
    try {
      const data = await fetchJSON<{ run: Run }>(`/api/triggers/${triggerID}/run`, { method: 'POST' });
      setSelectedRunID(data.run.id);
      await loadDashboard();
      await loadRunDetail(data.run.id);
    } catch (caught) {
      setError(messageFrom(caught));
    } finally {
      setRunningTriggerID('');
    }
  }

  return (
    <main className="min-h-screen bg-[radial-gradient(circle_at_top,_rgba(217,70,239,0.18),_transparent_36%),linear-gradient(180deg,#09090b_0%,#111113_52%,#09090b_100%)] px-6 py-8 text-zinc-100 sm:px-8 lg:px-12">
      <section className="mx-auto flex min-h-[calc(100vh-4rem)] max-w-7xl flex-col gap-6 overflow-hidden rounded-[2rem] border border-white/10 bg-zinc-950/80 p-6 shadow-[0_20px_80px_rgba(0,0,0,0.45)] backdrop-blur md:p-8">
        <header className="flex flex-col gap-6 border-b border-white/10 pb-6 lg:flex-row lg:items-end lg:justify-between">
          <div className="max-w-3xl">
            <p className="text-xs uppercase tracking-[0.38em] text-fuchsia-300/90">Switchyard control-plane suite</p>
            <h1 className="mt-3 text-4xl font-semibold tracking-tight text-white sm:text-5xl">
              Orchestrate workflows, triggers, and runs from one polished command surface.
            </h1>
            <p className="mt-4 max-w-2xl text-sm leading-6 text-zinc-300 sm:text-base">
              Switchyard routes work across operator-facing workflows, scheduled entry points, and live execution
              state so teams can see what is moving, what is blocked, and what is next.
            </p>
          </div>

          <div className="rounded-2xl border border-fuchsia-400/20 bg-fuchsia-400/10 px-4 py-3 text-sm text-fuchsia-100 shadow-lg shadow-fuchsia-950/20">
            <div className="text-[11px] uppercase tracking-[0.28em] text-fuchsia-200/75">Architecture</div>
            <div className="mt-2 font-medium text-white">UI → Switchyard → llama-line → OCQ → Blacktower</div>
          </div>
        </header>

        {error ? (
          <div className="rounded-2xl border border-rose-400/20 bg-rose-400/10 px-4 py-3 text-sm text-rose-100" role="alert">
            {error}
          </div>
        ) : null}

        <div className="grid gap-6 lg:grid-cols-[1.45fr_0.95fr]">
          <section className="grid gap-6">
            <article className="rounded-3xl border border-white/10 bg-white/5 p-5 shadow-[inset_0_1px_0_rgba(255,255,255,0.04)]">
              <div className="flex items-center justify-between gap-4">
                <div>
                  <p className="text-xs uppercase tracking-[0.3em] text-zinc-400">Workflows</p>
                  <h2 className="mt-2 text-xl font-semibold text-white">Operator lanes</h2>
                </div>
                <span className="rounded-full border border-emerald-400/20 bg-emerald-400/10 px-3 py-1 text-xs font-medium text-emerald-200">
                  {loading ? 'Syncing lanes' : `${dashboard.workflows.length} active lanes`}
                </span>
              </div>

              <div className="mt-5 grid gap-4 md:grid-cols-3">
                {dashboard.workflows.map((workflow) => (
                  <div key={workflow.id} className="rounded-2xl border border-white/10 bg-zinc-900/70 p-4">
                    <div className="flex items-center justify-between gap-3">
                      <h3 className="font-semibold text-white">{workflow.name}</h3>
                      <span className={`rounded-full border px-2.5 py-1 text-[11px] uppercase tracking-[0.24em] ${statusClasses(workflow.status)}`}>
                        {workflow.status}
                      </span>
                    </div>
                    <p className="mt-3 text-sm leading-6 text-zinc-300">{workflow.description}</p>
                    <p className="mt-3 text-xs text-zinc-500">Uses: {workflow.uses.join(', ') || 'Switchyard'}</p>
                  </div>
                ))}
              </div>
            </article>

            <article className="grid gap-6 md:grid-cols-[1.05fr_0.95fr]">
              <div className="rounded-3xl border border-white/10 bg-zinc-900/70 p-5">
                <p className="text-xs uppercase tracking-[0.3em] text-zinc-400">Triggers</p>
                <h2 className="mt-2 text-xl font-semibold text-white">Entry points</h2>
                <div className="mt-5 grid gap-3">
                  {dashboard.triggers.length === 0 ? (
                    <p className="rounded-2xl border border-white/10 bg-black/20 px-4 py-3 text-sm text-zinc-400">
                      {loading ? 'Loading triggers…' : 'No triggers returned by the API.'}
                    </p>
                  ) : null}
                  {dashboard.triggers.map((trigger) => (
                    <div key={trigger.id} className="rounded-2xl border border-cyan-400/20 bg-cyan-400/10 p-4 text-cyan-100">
                      <div className="flex items-start justify-between gap-3">
                        <div>
                          <div className="text-sm font-semibold text-white">{trigger.name}</div>
                          <div className="mt-1 text-xs uppercase tracking-[0.22em] text-cyan-200/70">
                            {trigger.type} · {workflowName(dashboard.workflows, trigger.workflow_id)}
                          </div>
                        </div>
                        <button
                          className="rounded-full border border-white/10 bg-white/10 px-3 py-1.5 text-xs font-medium text-white transition hover:bg-white/20 disabled:cursor-not-allowed disabled:opacity-50"
                          disabled={!trigger.enabled || runningTriggerID === trigger.id}
                          onClick={() => void runTrigger(trigger.id)}
                          type="button"
                        >
                          {runningTriggerID === trigger.id ? 'Running…' : 'Run now'}
                        </button>
                      </div>
                      <p className="mt-3 text-sm leading-6 text-cyan-50/80">{trigger.description}</p>
                      <p className="mt-2 text-xs text-cyan-100/60">Next: {trigger.schedule || formatTime(trigger.next_run_at)}</p>
                    </div>
                  ))}
                </div>
              </div>

              <div className="rounded-3xl border border-white/10 bg-zinc-900/70 p-5">
                <p className="text-xs uppercase tracking-[0.3em] text-zinc-400">Runs / status</p>
                <h2 className="mt-2 text-xl font-semibold text-white">Live posture</h2>
                <div className="mt-5 space-y-3">
                  {runSummary.map((run) => (
                    <div key={run.label} className="flex items-center justify-between gap-4 rounded-2xl border border-white/10 bg-black/20 px-4 py-3">
                      <div>
                        <div className="text-sm font-medium text-white">{run.label}</div>
                        <div className="text-xs text-zinc-400">{run.detail}</div>
                      </div>
                      <div className="text-2xl font-semibold tracking-tight text-fuchsia-200">{run.value}</div>
                    </div>
                  ))}
                </div>
              </div>
            </article>
          </section>

          <aside className="grid gap-6">
            <article className="rounded-3xl border border-white/10 bg-zinc-900/70 p-5">
              <p className="text-xs uppercase tracking-[0.3em] text-zinc-400">Run detail</p>
              <h2 className="mt-2 text-xl font-semibold text-white">Events and artifacts</h2>
              <div className="mt-5 grid gap-3">
                {dashboard.runs.length === 0 ? (
                  <p className="rounded-2xl border border-white/10 bg-white/5 p-4 text-sm text-zinc-400">
                    Dispatch a trigger to create the first run envelope.
                  </p>
                ) : (
                  dashboard.runs.slice(0, 6).map((run) => (
                    <button
                      key={run.id}
                      className={`rounded-2xl border p-4 text-left transition hover:bg-white/10 ${
                        selectedRunID === run.id ? 'border-fuchsia-300/40 bg-fuchsia-300/10' : 'border-white/10 bg-white/5'
                      }`}
                      onClick={() => setSelectedRunID(run.id)}
                      type="button"
                    >
                      <div className="flex items-center justify-between gap-3">
                        <span className="text-sm font-medium text-white">{workflowName(dashboard.workflows, run.workflow_id)}</span>
                        <span className={`rounded-full border px-2.5 py-1 text-[11px] uppercase tracking-[0.18em] ${statusClasses(run.status)}`}>
                          {run.status}
                        </span>
                      </div>
                      <div className="mt-2 text-xs text-zinc-500">{run.id}</div>
                      <div className="mt-1 text-xs text-zinc-400">{formatTime(run.created_at)}</div>
                    </button>
                  ))
                )}
              </div>

              {runDetail ? (
                <div className="mt-5 rounded-2xl border border-white/10 bg-black/20 p-4">
                  <h3 className="text-sm font-semibold text-white">{runDetail.run.summary || 'Run events'}</h3>
                  <div className="mt-4 space-y-3">
                    {runDetail.events.map((event) => (
                      <div key={event.id} className="border-l border-fuchsia-300/30 pl-3">
                        <div className="text-xs uppercase tracking-[0.2em] text-fuchsia-200">{event.type}</div>
                        <div className="mt-1 text-sm leading-6 text-zinc-300">{event.message}</div>
                      </div>
                    ))}
                  </div>
                  <p className="mt-4 text-xs text-zinc-500">Artifacts: {runDetail.artifacts.length}</p>
                </div>
              ) : null}
            </article>

            <article className="rounded-3xl border border-fuchsia-400/15 bg-gradient-to-b from-fuchsia-400/10 to-transparent p-5">
              <p className="text-xs uppercase tracking-[0.3em] text-fuchsia-200/80">Routing model</p>
              <div className="mt-4 flex flex-col gap-3 text-sm text-zinc-200">
                {['UI', 'Switchyard', 'llama-line', 'OCQ', 'Blacktower'].map((item, index) => (
                  <div key={item}>
                    <div className="rounded-2xl border border-white/10 bg-black/20 px-4 py-3">{item}</div>
                    {index < 4 ? <div className="pl-4 pt-3 text-zinc-500">↓</div> : null}
                  </div>
                ))}
              </div>
            </article>
          </aside>
        </div>
      </section>
    </main>
  );
}
