import type { ReactNode } from 'react';
import type {
  DashboardState,
  OpenPilotIssueState,
  OpenPilotStateResponse,
  RunDetail,
  Trigger,
  WorkflowDefinition,
} from '../types';
import { formatTime, statusClasses, HudEmptyState, HudField, HudLabel, HudPanel, HudPanelStrong, HudSection } from './hud';
import { formatOpenPilotGateStatus, workflowName } from '../api';

type OverviewDashboardProps = {
  dashboard: DashboardState;
  workflowDefinitions: WorkflowDefinition[];
  loading: boolean;
  refreshing: boolean;
  selectedRunID: string;
  selectedRunDetail: RunDetail | null;
  openPilotState: OpenPilotStateResponse | null;
  openPilotRepo: string;
  openPilotIssue: string;
  openPilotReady: boolean;
  runActionID: string;
  runningTriggerID: string;
  updatingTriggerID: string;
  workflows: DashboardState['workflows'];
  onRefresh: () => void;
  onSelectRun: (runID: string) => void;
  onRunTrigger: (triggerID: string) => void;
  onToggleTrigger: (trigger: Trigger) => void;
  onRetryRun: (runID: string) => void;
  onCancelRun: (runID: string) => void;
  onResumeOpenPilotState: (state: OpenPilotIssueState) => void;
  onStepAction: (runID: string, stepID: string, action: 'retry' | 'cancel') => void;
  setOpenPilotRepo: (value: string) => void;
  setOpenPilotIssue: (value: string) => void;
};

function runSummary(runs: DashboardState['runs']) {
  const active = runs.filter((run) => run.status === 'queued' || run.status === 'running').length;
  const succeeded = runs.filter((run) => run.status === 'succeeded').length;
  const blocked = runs.filter((run) => run.status === 'failed' || run.status === 'canceled').length;

  return [
    { label: 'Active runs', value: String(active), detail: 'Queued or running now' },
    { label: 'Succeeded', value: String(succeeded), detail: 'Recorded control-plane wins' },
    { label: 'Needs input', value: String(blocked), detail: 'Failed or canceled runs' },
  ];
}

function visibleRuns(runs: DashboardState['runs']) {
  return runs.slice(0, 6);
}

function TriggerCard({
  trigger,
  runningTriggerID,
  updatingTriggerID,
  openPilotReady,
  openPilotRepo,
  openPilotIssue,
  onRunTrigger,
  onToggleTrigger,
  setOpenPilotRepo,
  setOpenPilotIssue,
  workflows,
}: {
  trigger: Trigger;
  runningTriggerID: string;
  updatingTriggerID: string;
  openPilotReady: boolean;
  openPilotRepo: string;
  openPilotIssue: string;
  workflows: DashboardState['workflows'];
  onRunTrigger: (triggerID: string) => void;
  onToggleTrigger: (trigger: Trigger) => void;
  setOpenPilotRepo: (value: string) => void;
  setOpenPilotIssue: (value: string) => void;
}) {
  return (
    <HudPanel className="bg-[hsl(var(--panel-strong))] p-4">
      <div className="flex items-start justify-between gap-3">
        <div>
          <HudLabel className="text-[hsl(var(--faint))]">{trigger.type}</HudLabel>
          <h3 className="mt-2 text-base font-semibold text-[hsl(var(--ink))]">{trigger.name}</h3>
          <p className="mt-2 text-xs uppercase tracking-[0.18em] text-[hsl(var(--muted))]">{workflowName(workflows, trigger.workflow_id)}</p>
        </div>

        <div className="flex flex-wrap justify-end gap-2">
          <button
            className="hud-chip border-[hsl(var(--pulse))] bg-[hsl(var(--pulse-soft))] px-3 py-2 text-[0.62rem] text-[hsl(var(--ink))] transition hover:bg-[hsl(var(--border-faint))] disabled:cursor-not-allowed disabled:opacity-50"
            disabled={!trigger.enabled || runningTriggerID === trigger.id || (trigger.workflow_id === 'open-pilot' && !openPilotReady)}
            title={trigger.workflow_id === 'open-pilot' && !openPilotReady ? 'Fill repo and issue to run Open Pilot' : undefined}
            onClick={() => onRunTrigger(trigger.id)}
            type="button"
          >
            {runningTriggerID === trigger.id ? 'Running…' : 'Run now'}
          </button>
          <button
            className={`hud-chip px-3 py-2 text-[0.62rem] transition disabled:cursor-not-allowed disabled:opacity-50 ${trigger.enabled ? 'border-[hsl(var(--danger))] bg-[hsl(var(--danger-soft))] text-[hsl(var(--danger))]' : 'border-[hsl(var(--confirm))] bg-[hsl(var(--confirm-soft))] text-[hsl(var(--confirm))]'}`}
            disabled={updatingTriggerID === trigger.id}
            onClick={() => onToggleTrigger(trigger)}
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
    </HudPanel>
  );
}

function RunRow({ run, selected, onSelect }: { run: DashboardState['runs'][number]; selected: boolean; onSelect: (runID: string) => void }) {
  return (
    <button
      className={`hud-panel text-left transition hover:bg-[hsl(var(--panel-soft))] focus-visible:ring-2 focus-visible:ring-[hsl(var(--pulse))] focus-visible:ring-offset-2 focus-visible:ring-offset-[hsl(var(--void))] ${
        selected ? 'border-[hsl(var(--signal))] bg-[hsl(var(--signal-soft))]' : 'bg-[hsl(var(--panel-soft))]'
      }`}
      onClick={() => onSelect(run.id)}
      type="button"
    >
      <div className="flex items-center justify-between gap-3 px-4 py-3">
        <span className="text-sm font-medium text-[hsl(var(--ink))]">{run.workflow_id}</span>
        <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(run.status)}`}>{run.status}</span>
      </div>
      <div className="border-t border-[hsl(var(--border-faint))] px-4 py-3 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">
        <div>{run.id}</div>
        <div className="mt-1 text-[hsl(var(--muted))]">{formatTime(run.created_at)}</div>
      </div>
    </button>
  );
}

function EvidenceList({ title, children }: { title: string; children: ReactNode }) {
  return (
    <HudPanel className="bg-[hsl(var(--panel-strong))] p-4">
      <h3 className="hud-label text-[hsl(var(--signal))]">{title}</h3>
      <div className="mt-4 max-h-80 space-y-3 overflow-y-auto pr-1">{children}</div>
    </HudPanel>
  );
}

function RunDetailPanel({
  selectedRunDetail,
  dashboard,
  openPilotState,
  runActionID,
  onRetryRun,
  onCancelRun,
  onResumeOpenPilotState,
  onStepAction,
}: {
  selectedRunDetail: RunDetail | null;
  dashboard: DashboardState;
  openPilotState: OpenPilotStateResponse | null;
  runActionID: string;
  onRetryRun: (runID: string) => void;
  onCancelRun: (runID: string) => void;
  onResumeOpenPilotState: (state: OpenPilotIssueState) => void;
  onStepAction: (runID: string, stepID: string, action: 'retry' | 'cancel') => void;
}) {
  if (!selectedRunDetail) {
    return <HudEmptyState>Select a run to inspect its timeline, evidence, and operator actions.</HudEmptyState>;
  }

  return (
    <div className="space-y-4">
      <HudPanel className="bg-[hsl(var(--panel-strong))] p-4">
        <div className="flex items-center justify-between gap-3">
          <div>
            <h3 className="text-sm font-semibold uppercase tracking-[0.2em] text-[hsl(var(--ink))]">{selectedRunDetail.run.summary || 'Run events'}</h3>
            <p className="mt-2 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">{workflowName(dashboard.workflows, selectedRunDetail.run.workflow_id)} · {selectedRunDetail.run.status}</p>
          </div>
          <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(selectedRunDetail.run.status)}`}>{selectedRunDetail.run.status}</span>
        </div>
        <dl className="mt-4 grid gap-3 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))] sm:grid-cols-2">
          <HudField label="Created" value={formatTime(selectedRunDetail.run.created_at)} />
          <HudField label="Artifacts" value={selectedRunDetail.artifacts.length} />
        </dl>
        <div className="mt-4 flex flex-wrap gap-2">
          <button
            className="hud-chip border-[hsl(var(--pulse))] bg-[hsl(var(--pulse-soft))] px-3 py-2 text-[0.62rem] text-[hsl(var(--ink))] transition hover:bg-[hsl(var(--border-faint))] disabled:cursor-not-allowed disabled:opacity-50"
            disabled={runActionID === `${selectedRunDetail.run.id}:retry`}
            onClick={() => openPilotState && selectedRunDetail.run.workflow_id === 'open-pilot' && ['failed', 'paused', 'agent_completed', 'merged'].includes(openPilotState.state.phase) ? onResumeOpenPilotState(openPilotState.state) : onRetryRun(selectedRunDetail.run.id)}
            type="button"
          >
            {runActionID === `${selectedRunDetail.run.id}:retry` || (openPilotState && runActionID === `${openPilotState.state.id}:open-pilot:resume`) ? 'Retrying…' : openPilotState && selectedRunDetail.run.workflow_id === 'open-pilot' && ['failed', 'paused', 'agent_completed', 'merged'].includes(openPilotState.state.phase) ? 'Resume Open Pilot' : 'Retry run'}
          </button>
          <button
            className="hud-chip border-[hsl(var(--danger))] bg-[hsl(var(--danger-soft))] px-3 py-2 text-[0.62rem] text-[hsl(var(--danger))] transition hover:bg-[hsl(var(--border-faint))] disabled:cursor-not-allowed disabled:opacity-50"
            disabled={!['queued', 'running'].includes(selectedRunDetail.run.status) || runActionID === `${selectedRunDetail.run.id}:cancel`}
            onClick={() => onCancelRun(selectedRunDetail.run.id)}
            type="button"
          >
            {runActionID === `${selectedRunDetail.run.id}:cancel` ? 'Canceling…' : 'Cancel run'}
          </button>
        </div>
      </HudPanel>

      {openPilotState && selectedRunDetail.run.workflow_id === 'open-pilot' ? (
        <HudPanel className="bg-[hsl(var(--panel-strong))] p-4">
          <div className="flex items-center justify-between gap-3">
            <div>
              <HudLabel className="text-[hsl(var(--faint))]">Open Pilot lifecycle state</HudLabel>
              <h3 className="mt-2 text-sm font-semibold uppercase tracking-[0.2em] text-[hsl(var(--ink))]">{openPilotState.state.repo}#{openPilotState.state.issue}</h3>
            </div>
            <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(openPilotState.state.phase)}`}>{openPilotState.state.phase}</span>
          </div>

          <dl className="mt-4 grid gap-3 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))] sm:grid-cols-2">
            <HudField label="Next action" value={openPilotState.next_action} />
            <HudField label="Gate status" value={formatOpenPilotGateStatus(openPilotState.state.gate_status)} />
            <HudField label="Pull request URL" value={openPilotState.state.pull_request_url || '—'} />
            <HudField label="Merge SHA" value={openPilotState.state.merge_commit_sha || '—'} />
            <HudField label="Issue closed" value={openPilotState.state.issue_closed ? 'yes' : 'no'} />
            <HudField label="Repair attempts" value={`${openPilotState.state.repair_attempts} / ${openPilotState.state.max_repair_attempts}`} />
            <HudField className="sm:col-span-2" label="Last error" value={openPilotState.state.last_error || '—'} />
          </dl>
        </HudPanel>
      ) : null}

      <HudPanel className="bg-[hsl(var(--panel-strong))] p-4">
        <div className="flex items-center justify-between gap-3">
          <div>
            <HudLabel className="text-[hsl(var(--faint))]">Step timeline</HudLabel>
            <h3 className="mt-2 text-sm font-semibold uppercase tracking-[0.2em] text-[hsl(var(--ink))]">{selectedRunDetail.steps.length} recorded step{selectedRunDetail.steps.length === 1 ? '' : 's'}</h3>
          </div>
        </div>

        <div className="mt-4 grid gap-3">
          {selectedRunDetail.steps.length === 0 ? (
            <HudEmptyState>No durable steps have been recorded for this run yet.</HudEmptyState>
          ) : (
            selectedRunDetail.steps.map((step) => (
              <HudPanel key={step.id} className="bg-[hsl(var(--panel-soft))] p-4">
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <div>
                    <HudLabel className="text-[hsl(var(--faint))]">Step {step.position} · {step.executor}</HudLabel>
                    <h4 className="mt-2 text-sm font-semibold text-[hsl(var(--ink))]">{step.name}</h4>
                    <p className="mt-2 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">Attempts: {step.attempt_count} · {formatTime(step.started_at)} → {formatTime(step.finished_at)}</p>
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
                    onClick={() => onStepAction(selectedRunDetail.run.id, step.id, 'retry')}
                    type="button"
                  >
                    {runActionID === `${step.id}:step:retry` ? 'Retrying…' : 'Retry step'}
                  </button>
                  <button
                    className="hud-chip border-[hsl(var(--danger))] bg-[hsl(var(--danger-soft))] px-3 py-2 text-[0.62rem] text-[hsl(var(--danger))] transition hover:bg-[hsl(var(--border-faint))] disabled:cursor-not-allowed disabled:opacity-50"
                    disabled={!['queued', 'running'].includes(step.status) || runActionID === `${step.id}:step:cancel`}
                    onClick={() => onStepAction(selectedRunDetail.run.id, step.id, 'cancel')}
                    type="button"
                  >
                    {runActionID === `${step.id}:step:cancel` ? 'Canceling…' : 'Cancel step'}
                  </button>
                </div>
              </HudPanel>
            ))
          )}
        </div>
      </HudPanel>
    </div>
  );
}

export function OverviewDashboard({
  dashboard,
  workflowDefinitions,
  loading,
  refreshing,
  selectedRunID,
  selectedRunDetail,
  openPilotState,
  openPilotRepo,
  openPilotIssue,
  openPilotReady,
  runActionID,
  runningTriggerID,
  updatingTriggerID,
  onRefresh,
  onSelectRun,
  onRunTrigger,
  onToggleTrigger,
  onRetryRun,
  onCancelRun,
  onResumeOpenPilotState,
  onStepAction,
  setOpenPilotRepo,
  setOpenPilotIssue,
  workflows,
}: OverviewDashboardProps) {
  const summaries = runSummary(dashboard.runs);
  const recentRuns = visibleRuns(dashboard.runs);
  const activeRuns = dashboard.runs.filter((run) => run.status === 'queued' || run.status === 'running').length;
  const workQueue = dashboard.triggers.filter((trigger) => trigger.enabled).length;

  return (
    <section className="grid gap-4">
      <HudSection
        kicker="Command strip"
        title="Command strip"
        note="Live API health, scheduler posture, worker pool, and the quickest refresh and routing controls."
        actions={[
          <button
            key="refresh"
            className="hud-chip border-[hsl(var(--signal))] bg-[hsl(var(--signal-soft))] px-3 py-2 text-[0.62rem] text-[hsl(var(--signal))] transition hover:bg-[hsl(var(--panel-soft))] disabled:cursor-not-allowed disabled:opacity-50"
            disabled={loading || refreshing}
            onClick={onRefresh}
            type="button"
          >
            {refreshing ? 'Refreshing…' : loading ? 'Syncing…' : 'Refresh'}
          </button>,
        ]}
      >
        <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-4">
          <HudField label="API health" value={dashboard.adapters.health.status} />
          <HudField label="Scheduler" value={dashboard.scheduler.enabled ? (dashboard.scheduler.running ? 'running' : 'enabled') : 'disabled'} />
          <HudField label="Workers" value={dashboard.workers.enabled ? `${dashboard.workers.active ?? 0}/${dashboard.workers.workers ?? 0} active` : 'disabled'} />
          <HudField label="Queues / runs" value={`${dashboard.adapters.queues.length} queues · ${dashboard.runs.length} runs`} />
          <HudField label="Active runs" value={activeRuns} />
          <HudField label="Enabled triggers" value={workQueue} />
          <HudField label="Workflow definitions" value={workflowDefinitions.length} />
          <HudField label="Selected run" value={selectedRunID || 'none'} />
        </div>
      </HudSection>

      <HudSection kicker="Active work" title="Active work" note="Recent runs, trigger controls, and operator inputs grouped for quick scanning.">
        <div className="grid gap-4 xl:grid-cols-[1.15fr_0.85fr]">
          <div className="space-y-3">
            {dashboard.triggers.length === 0 ? <HudEmptyState>{loading ? 'Loading triggers…' : 'No triggers returned by the API.'}</HudEmptyState> : null}
            {dashboard.triggers.map((trigger) => (
              <TriggerCard
                key={trigger.id}
                trigger={trigger}
                runningTriggerID={runningTriggerID}
                updatingTriggerID={updatingTriggerID}
                openPilotReady={openPilotReady}
                openPilotRepo={openPilotRepo}
                openPilotIssue={openPilotIssue}
                onRunTrigger={onRunTrigger}
                onToggleTrigger={onToggleTrigger}
                workflows={workflows}
                setOpenPilotRepo={setOpenPilotRepo}
                setOpenPilotIssue={setOpenPilotIssue}
              />
            ))}
          </div>

          <div className="space-y-4">
            <HudPanelStrong className="p-4">
              <HudLabel className="text-[hsl(var(--faint))]">Run posture</HudLabel>
              <div className="mt-4 space-y-3">
                {summaries.map((run) => (
                  <div key={run.label} className="hud-panel flex items-center justify-between gap-4 bg-[hsl(var(--panel-strong))] px-4 py-3">
                    <div>
                      <div className="text-sm font-medium text-[hsl(var(--ink))]">{run.label}</div>
                      <div className="mt-1 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">{run.detail}</div>
                    </div>
                    <div className="text-3xl font-semibold tracking-tight text-[hsl(var(--signal))]">{run.value}</div>
                  </div>
                ))}
              </div>
            </HudPanelStrong>

            <HudPanelStrong className="p-4">
              <HudLabel className="text-[hsl(var(--faint))]">Recent runs</HudLabel>
              <div className="mt-4 grid gap-3">
                {dashboard.runs.length === 0 ? (
                  <HudEmptyState>Dispatch a trigger to create the first run envelope.</HudEmptyState>
                ) : (
                  recentRuns.map((run) => <RunRow key={run.id} run={run} selected={selectedRunID === run.id} onSelect={onSelectRun} />)
                )}
              </div>
            </HudPanelStrong>
          </div>
        </div>
      </HudSection>

      <HudSection kicker="Selected run" title="Selected run" note="Timeline, lifecycle state, retry/cancel actions, and summary for the currently selected run.">
        <RunDetailPanel
          dashboard={dashboard}
          openPilotState={openPilotState}
          runActionID={runActionID}
          selectedRunDetail={selectedRunDetail}
          onCancelRun={onCancelRun}
          onRetryRun={onRetryRun}
          onResumeOpenPilotState={onResumeOpenPilotState}
          onStepAction={onStepAction}
        />
      </HudSection>

      <HudSection kicker="Evidence" title="Evidence" note="Events and artifacts laid out for fast operator review with compact scrollable panels.">
        <div className="grid gap-4 lg:grid-cols-2">
          <EvidenceList title="Events">
            {selectedRunDetail?.events.length ? (
              selectedRunDetail.events.map((event) => (
                <HudPanel key={event.id} className="border-[hsl(var(--pulse))] bg-[hsl(var(--panel-strong))] px-4 py-3">
                  <div className="flex items-center justify-between gap-3">
                    <p className="hud-label text-[hsl(var(--pulse))]">{event.type}</p>
                    <span className="text-[0.65rem] uppercase tracking-[0.18em] text-[hsl(var(--faint))]">{formatTime(event.created_at)}</span>
                  </div>
                  <p className="mt-2 text-sm leading-6 text-[hsl(var(--muted))]">{event.message}</p>
                </HudPanel>
              ))
            ) : (
              <HudEmptyState>No events have been recorded for this run yet.</HudEmptyState>
            )}
          </EvidenceList>

          <EvidenceList title="Artifacts">
            {selectedRunDetail?.artifacts.length ? (
              selectedRunDetail.artifacts.map((artifact) => (
                <HudPanel key={artifact.id} className="bg-[hsl(var(--panel-strong))] px-3 py-2">
                  <div className="flex items-start justify-between gap-3">
                    <div>
                      <p className="text-sm font-medium text-[hsl(var(--ink))]">{artifact.name}</p>
                      <p className="mt-1 text-[0.65rem] uppercase tracking-[0.18em] text-[hsl(var(--faint))]">{artifact.kind}</p>
                    </div>
                    <span className="text-[0.65rem] uppercase tracking-[0.18em] text-[hsl(var(--signal))]">{formatTime(artifact.created_at)}</span>
                  </div>
                  <p className="mt-2 break-all text-xs text-[hsl(var(--muted))]">{artifact.uri}</p>
                  <a className="mt-3 inline-block text-xs uppercase tracking-[0.18em] text-[hsl(var(--signal))] underline" href={artifact.uri} rel="noreferrer" target="_blank">
                    Open artifact
                  </a>
                </HudPanel>
              ))
            ) : (
              <HudEmptyState>No artifacts attached</HudEmptyState>
            )}
          </EvidenceList>
        </div>
      </HudSection>
    </section>
  );
}
