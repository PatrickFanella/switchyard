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

const routingSteps = ['UI', 'Switchyard', 'llama-line', 'OCQ', 'Blacktower'];

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

  const visibleRuns = dashboard.runs.slice(0, 6);

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
              UI → Switchyard → llama-line → OCQ → Blacktower
            </div>
          </div>
        </header>

        {error ? (
          <div className="hud-panel border-[hsl(var(--danger))] bg-[hsl(var(--danger-soft))] px-4 py-3 text-sm text-[hsl(var(--ink))]" role="alert">
            <span className="hud-label text-[hsl(var(--danger))]">Error</span>
            <div className="mt-2">{error}</div>
          </div>
        ) : null}

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

                        <button
                          className="hud-chip border-[hsl(var(--pulse))] bg-[hsl(var(--pulse-soft))] px-3 py-2 text-[0.62rem] text-[hsl(var(--ink))] transition hover:bg-[hsl(var(--border-faint))] disabled:cursor-not-allowed disabled:opacity-50"
                          disabled={!trigger.enabled || runningTriggerID === trigger.id}
                          onClick={() => void runTrigger(trigger.id)}
                          type="button"
                        >
                          {runningTriggerID === trigger.id ? 'Running…' : 'Run now'}
                        </button>
                      </div>

                      <p className="mt-3 text-sm leading-6 text-[hsl(var(--muted))]">{trigger.description}</p>

                      <div className="mt-4 grid gap-3 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))] sm:grid-cols-2">
                        <div className="hud-panel bg-[hsl(var(--panel-soft))] px-3 py-2">
                          <div className="text-[hsl(var(--signal))]">Schedule</div>
                          <div className="mt-1 text-[hsl(var(--ink))]">{trigger.schedule || 'Manual'}</div>
                        </div>
                        <div className="hud-panel bg-[hsl(var(--panel-soft))] px-3 py-2">
                          <div className="text-[hsl(var(--signal))]">Next run</div>
                          <div className="mt-1 text-[hsl(var(--ink))]">{formatTime(trigger.next_run_at)}</div>
                        </div>
                      </div>
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
      </section>
    </main>
  );
}
