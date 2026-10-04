import type {
  DashboardState,
  OpenPilotIssueState,
  OpenPilotStateResponse,
  OperatorSummary,
  RunDetail,
  Trigger,
  WorkflowDefinition,
} from '../types';
import { formatTime, statusClasses, HudEmptyState, HudField, HudLabel, HudPanel, HudPanelStrong, HudSection } from './hud';
import { formatOpenPilotGateStatus, workflowName } from '../api';
import { canResumeOpenPilot } from './runCockpit';
import { RunModelOverrideDispatch } from './RunModelOverrideDispatch';

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
  onOpenRunCockpit: (runID: string) => void;
  onOpenInbox?: () => void;
  setOpenPilotRepo: (value: string) => void;
  setOpenPilotIssue: (value: string) => void;
};

function runSummary(summary: OperatorSummary | null, runs: DashboardState['runs']) {
  if (summary) {
    return [
      { label: 'Active runs', value: String(summary.data.runs.queued + summary.data.runs.running), detail: `${summary.data.runs.queued} queued · ${summary.data.runs.running} running` },
      { label: 'Succeeded (24h)', value: String(summary.data.runs.succeeded_24h), detail: 'Completed in the last 24 hours' },
      { label: 'Needs attention (24h)', value: String(summary.data.runs.failed_24h), detail: 'Failed or timed out in the last 24 hours' },
    ];
  }

  const active = runs.filter((run) => run.status === 'queued' || run.status === 'running').length;
  const succeeded = runs.filter((run) => run.status === 'succeeded').length;
  const blocked = runs.filter((run) => ['failed', 'canceled', 'paused', 'timed_out'].includes(run.status)).length;

  return [
    { label: 'Active runs', value: String(active), detail: 'Queued or running now' },
    { label: 'Succeeded', value: String(succeeded), detail: 'Recorded control-plane wins' },
    { label: 'Needs input', value: String(blocked), detail: 'Failed, canceled, paused, or timed out' },
  ];
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
          <p className="mt-2 text-xs text-[hsl(var(--muted))]">{workflowName(workflows, trigger.workflow_id)}</p>
        </div>

        <div className="flex flex-wrap justify-end gap-2">
          <button
            className="hud-chip signal-primary px-3 py-2 disabled:opacity-50"
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
          <label className="hud-panel bg-[hsl(var(--panel-soft))] px-3 py-2 text-xs text-[hsl(var(--faint))]">
            <span className="text-[hsl(var(--signal))]">Repo</span>
            <input
              className="mt-2 w-full border border-[hsl(var(--border-faint))] bg-[hsl(var(--void))] px-2 py-2 text-sm normal-case tracking-normal text-[hsl(var(--ink))]"
              onChange={(event) => setOpenPilotRepo(event.target.value)}
              placeholder="OWNER/REPO"
              value={openPilotRepo}
            />
          </label>
          <label className="hud-panel bg-[hsl(var(--panel-soft))] px-3 py-2 text-xs text-[hsl(var(--faint))]">
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

      <div className="mt-4 grid gap-3 text-xs text-[hsl(var(--faint))] sm:grid-cols-2">
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
      {trigger.last_error ? <p className="mt-3 text-xs text-[hsl(var(--danger))]">Last error: {trigger.last_error}</p> : null}
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
      <div className="border-t border-[hsl(var(--border-faint))] px-4 py-3 text-xs text-[hsl(var(--faint))]">
        <div>{run.id}</div>
        <div className="mt-1 text-[hsl(var(--muted))]">{formatTime(run.created_at)}</div>
      </div>
    </button>
  );
}

function SummaryRunRow({ run, selected, onSelect }: { run: NonNullable<OperatorSummary>['data']['runs']['recent'][number]; selected: boolean; onSelect: (runID: string) => void }) {
  return (
    <button
      className={`hud-panel text-left transition hover:bg-[hsl(var(--panel-soft))] focus-visible:ring-2 focus-visible:ring-[hsl(var(--pulse))] focus-visible:ring-offset-2 focus-visible:ring-offset-[hsl(var(--void))] ${
        selected ? 'border-[hsl(var(--signal))] bg-[hsl(var(--signal-soft))]' : 'bg-[hsl(var(--panel-soft))]'
      }`}
      onClick={() => onSelect(run.id)}
      type="button"
    >
      <div className="flex items-center justify-between gap-3 px-4 py-3">
        <span className="text-sm font-medium text-[hsl(var(--ink))]">{run.workflow_name}</span>
        <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(run.state)}`}>{run.state}</span>
      </div>
      <div className="border-t border-[hsl(var(--border-faint))] px-4 py-3 text-xs text-[hsl(var(--faint))]">
        <div>{run.summary || run.id}</div>
        <div className="mt-1 text-[hsl(var(--muted))]">{formatTime(run.finished_at || run.started_at)}</div>
      </div>
    </button>
  );
}

function SelectedRunSummaryCard({
  selectedRunDetail,
  openPilotState,
  runActionID,
  onRetryRun,
  onCancelRun,
  onResumeOpenPilotState,
  onOpenRunCockpit,
}: {
  selectedRunDetail: RunDetail | null;
  openPilotState: OpenPilotStateResponse | null;
  runActionID: string;
  onRetryRun: (runID: string) => void;
  onCancelRun: (runID: string) => void;
  onResumeOpenPilotState: (state: OpenPilotIssueState) => void;
  onOpenRunCockpit: (runID: string) => void;
}) {
  if (!selectedRunDetail) {
    return <HudEmptyState>Select a run to inspect the compact summary and open the run cockpit.</HudEmptyState>;
  }

  const resumeable = selectedRunDetail.run.workflow_id === 'open-pilot' && canResumeOpenPilot(openPilotState);
  const retryBusy = runActionID === `${selectedRunDetail.run.id}:retry`;
  const cancelBusy = runActionID === `${selectedRunDetail.run.id}:cancel`;
  const resumeBusy = openPilotState ? runActionID === `${openPilotState.state.id}:open-pilot:resume` : false;

  return (
    <HudPanelStrong className="p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <HudLabel className="text-[hsl(var(--faint))]">Selected run</HudLabel>
          <h3 className="mt-2 text-xl font-semibold text-[hsl(var(--ink))]">{selectedRunDetail.run.summary || selectedRunDetail.run.id}</h3>
          <p className="mt-2 text-xs text-[hsl(var(--faint))]">{selectedRunDetail.run.workflow_id} · {selectedRunDetail.run.status}</p>
        </div>
        <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(selectedRunDetail.run.status)}`}>{selectedRunDetail.run.status}</span>
      </div>

      <div className="mt-4 grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <HudField label="Run ID" value={selectedRunDetail.run.id} />
        <HudField label="Workflow" value={selectedRunDetail.run.workflow_id} />
        <HudField label="Steps" value={selectedRunDetail.steps.length} />
        <HudField label="Artifacts" value={selectedRunDetail.artifacts.length} />
        <HudField label="Events" value={selectedRunDetail.events.length} />
        <HudField label="Created" value={formatTime(selectedRunDetail.run.created_at)} />
        {openPilotState && selectedRunDetail.run.workflow_id === 'open-pilot' ? <HudField label="Open Pilot phase" value={openPilotState.state.phase} /> : null}
        {openPilotState && selectedRunDetail.run.workflow_id === 'open-pilot' ? <HudField label="Next action" value={openPilotState.next_action} /> : null}
      </div>

      {openPilotState && selectedRunDetail.run.workflow_id === 'open-pilot' ? (
        <div className="mt-4 rounded-none border border-[hsl(var(--border-faint))] bg-[hsl(var(--panel-soft))] p-3 text-sm text-[hsl(var(--muted))]">
          {formatOpenPilotGateStatus(openPilotState.state.gate_status)}
        </div>
      ) : null}

      <div className="mt-4 flex flex-wrap gap-2">
        <button
          className="hud-chip signal-primary px-3 py-2"
          onClick={() => onOpenRunCockpit(selectedRunDetail.run.id)}
          type="button"
        >
          Open cockpit
        </button>
        <button
          className="hud-chip border-[hsl(var(--pulse))] bg-[hsl(var(--pulse-soft))] px-3 py-2 text-[0.62rem] text-[hsl(var(--ink))] transition hover:bg-[hsl(var(--border-faint))] disabled:cursor-not-allowed disabled:opacity-50"
          disabled={retryBusy || resumeBusy}
          onClick={() => (resumeable && openPilotState ? onResumeOpenPilotState(openPilotState.state) : onRetryRun(selectedRunDetail.run.id))}
          type="button"
        >
          {retryBusy || resumeBusy ? 'Working…' : resumeable ? 'Resume Open Pilot' : 'Retry run'}
        </button>
        <button
          className="hud-chip border-[hsl(var(--danger))] bg-[hsl(var(--danger-soft))] px-3 py-2 text-[0.62rem] text-[hsl(var(--danger))] transition hover:bg-[hsl(var(--border-faint))] disabled:cursor-not-allowed disabled:opacity-50"
          disabled={!['queued', 'running'].includes(selectedRunDetail.run.status) || cancelBusy}
          onClick={() => onCancelRun(selectedRunDetail.run.id)}
          type="button"
        >
          {cancelBusy ? 'Canceling…' : 'Cancel run'}
        </button>
      </div>
    </HudPanelStrong>
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
  onOpenRunCockpit,
  onOpenInbox,
  setOpenPilotRepo,
  setOpenPilotIssue,
  workflows,
}: OverviewDashboardProps) {
  const operatorSummary = dashboard.operatorSummary;
  const summaries = runSummary(operatorSummary, dashboard.runs);
  const summaryRecentRuns = operatorSummary?.data.runs.recent ?? [];
  const fallbackRecentRuns = dashboard.runs.slice(0, 6);
  const activeRuns = operatorSummary ? operatorSummary.data.runs.queued + operatorSummary.data.runs.running : dashboard.runs.filter((run) => run.status === 'queued' || run.status === 'running').length;
  const workQueue = dashboard.triggers.filter((trigger) => trigger.enabled).length;

  return (
    <section className="grid gap-4">
      <HudSection
        kicker="Live system"
        title="System posture"
        note="Authoritative operator summary for runtime health, scheduling, worker capacity, and current workload."
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
          <HudField label="Runtime status" value={operatorSummary?.status.level || dashboard.adapters.health.status} />
          <HudField label="Scheduler" value={operatorSummary?.data.scheduler.state || (dashboard.scheduler.enabled ? (dashboard.scheduler.running ? 'running' : 'enabled') : 'disabled')} />
          <HudField label="Workers" value={operatorSummary ? `${operatorSummary.data.workers.busy}/${operatorSummary.data.workers.configured} busy` : dashboard.workers.enabled ? `${dashboard.workers.active ?? 0}/${dashboard.workers.workers ?? 0} active` : 'disabled'} />
          <HudField label="Queues / runs" value={`${dashboard.adapters.queues.length} queues · ${operatorSummary ? activeRuns : dashboard.runs.length} active`} />
          <HudField label="Active runs" value={activeRuns} />
          <HudField label="Enabled triggers" value={workQueue} />
          <HudField label="Workflow definitions" value={workflowDefinitions.length} />
          <HudField label="Selected run" value={selectedRunID || 'none'} />
        </div>
        {operatorSummary ? (
          <div className="mt-4 grid gap-3 lg:grid-cols-2">
            <HudPanel className="bg-[hsl(var(--panel-soft))] p-3">
              <HudLabel className="text-[hsl(var(--faint))]">Operator summary</HudLabel>
              <p className="mt-2 text-sm text-[hsl(var(--ink))]">{operatorSummary.status.summary}</p>
              <p className="mt-2 text-xs text-[hsl(var(--faint))]">Observed {formatTime(operatorSummary.observed_at)} · schema {operatorSummary.schema_version}</p>
            </HudPanel>
            <HudPanel className="bg-[hsl(var(--panel-soft))] p-3">
              <HudLabel className="text-[hsl(var(--faint))]">Component posture</HudLabel>
              <div className="mt-2 flex flex-wrap gap-2">
                {operatorSummary.data.components.map((component) => (
                  <span key={component.id} className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(component.status)}`} title={component.summary}>{component.name}: {component.status}</span>
                ))}
              </div>
              {operatorSummary.alerts.length > 0 ? <p className="mt-2 text-xs text-[hsl(var(--warn))]">{operatorSummary.alerts.length} active alert{operatorSummary.alerts.length === 1 ? '' : 's'}</p> : null}
            </HudPanel>
          </div>
        ) : null}
      </HudSection>

      <HudSection
        kicker="Dispatch"
        title="Workflows and triggers"
        note="Start manual work, manage schedules, and watch recent execution state."
        actions={onOpenInbox ? [
          <button key="inbox" className="hud-chip border-[hsl(var(--signal))] bg-[hsl(var(--signal-soft))] px-3 py-2 text-[0.62rem] text-[hsl(var(--signal))] transition hover:bg-[hsl(var(--panel-soft))]" onClick={onOpenInbox} type="button">
            Open Pilot inbox
          </button>,
        ] : undefined}
      >
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
            <RunModelOverrideDispatch
              triggers={dashboard.triggers}
              onDispatched={(runID) => {
                onSelectRun(runID);
                onRefresh();
              }}
            />
            <HudPanelStrong className="p-4">
              <HudLabel className="text-[hsl(var(--faint))]">Run posture</HudLabel>
              <div className="mt-4 space-y-3">
                {summaries.map((run) => (
                  <div key={run.label} className="hud-panel flex items-center justify-between gap-4 bg-[hsl(var(--panel-strong))] px-4 py-3">
                    <div>
                      <div className="text-sm font-medium text-[hsl(var(--ink))]">{run.label}</div>
                      <div className="mt-1 text-xs text-[hsl(var(--faint))]">{run.detail}</div>
                    </div>
                    <div className="text-3xl font-semibold tracking-tight text-[hsl(var(--signal))]">{run.value}</div>
                  </div>
                ))}
              </div>
            </HudPanelStrong>

            <HudPanelStrong className="p-4">
              <HudLabel className="text-[hsl(var(--faint))]">Recent runs</HudLabel>
              <div className="mt-4 grid gap-3">
                {(operatorSummary ? summaryRecentRuns : fallbackRecentRuns).length === 0 ? (
                  <HudEmptyState>Dispatch a trigger to create the first run envelope.</HudEmptyState>
                ) : (
                  operatorSummary
                    ? summaryRecentRuns.map((run) => <SummaryRunRow key={run.id} run={run} selected={selectedRunID === run.id} onSelect={onSelectRun} />)
                    : fallbackRecentRuns.map((run) => <RunRow key={run.id} run={run} selected={selectedRunID === run.id} onSelect={onSelectRun} />)
                )}
              </div>
            </HudPanelStrong>
          </div>
        </div>
      </HudSection>

      <HudSection kicker="Inspection" title="Selected run" note="Review the current run and take the next safe operator action.">
        <SelectedRunSummaryCard
          openPilotState={openPilotState}
          onCancelRun={onCancelRun}
          onOpenRunCockpit={onOpenRunCockpit}
          onRetryRun={onRetryRun}
          onResumeOpenPilotState={onResumeOpenPilotState}
          runActionID={runActionID}
          selectedRunDetail={selectedRunDetail}
        />
      </HudSection>
    </section>
  );
}
