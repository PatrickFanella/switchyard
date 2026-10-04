import { useMemo, useState } from 'react';
import { canResumeOpenPilot, artifactLabel, runDurationMS, stepDurationMS, retryableStepStatuses, cancelableStepStatuses } from './runCockpit';
import type { OpenPilotStateResponse, RunDetail, RunStep } from '../types';
import { formatTime, HudEmptyState, HudField, HudLabel, HudPanel, HudPanelStrong, HudSection, statusClasses } from './hud';
import { LiveRefreshIndicator } from './LiveRefreshIndicator';
import type { LiveRefreshState } from '../hooks/useLiveRefresh';
import { ArtifactPreview } from './ArtifactPreview';
import { ChangemakerReviewPanel } from './ChangemakerReviewPanel';
import { DevlogReviewPanel } from './DevlogReviewPanel';
import { RunPolicyEvidence } from './RunPolicyEvidence';
import { collectCorrelations, summarizeRunFailure } from '../correlation';
import { CorrelationLinks, EvidenceAnchor, EvidenceFocusNotice, RunFailureSummaryPanel, RunUnavailableNotice, useCorrelationTargets, useEvidenceFocus } from './CorrelationLinks';

type RunCockpitPageProps = {
  selectedRunID: string;
  selectedRunDetail: RunDetail | null;
  selectedRunMissing?: boolean;
  openPilotState: OpenPilotStateResponse | null;
  runActionID: string;
  onRetryRun: (runID: string) => void;
  onCancelRun: (runID: string) => void;
  onResumeOpenPilotState: (state: OpenPilotStateResponse['state']) => void;
  onStepAction: (runID: string, stepID: string, action: 'retry' | 'cancel') => void;
  onBackToOverview: () => void;
  onRefresh: () => void;
  liveRefresh?: LiveRefreshState;
};

function runActionLabel(detail: RunDetail, state: OpenPilotStateResponse | null) {
  return detail.run.workflow_id === 'open-pilot' && canResumeOpenPilot(state) ? 'Resume Open Pilot' : 'Retry run';
}

function inputClassName() {
  return 'mt-2 w-full border border-[hsl(var(--border-faint))] bg-[hsl(var(--void))] px-3 py-2 text-sm text-[hsl(var(--ink))] outline-none transition placeholder:text-[hsl(var(--faint))] focus:border-[hsl(var(--signal))]';
}

function filterEvidence<T extends object>(items: T[], query: string, fields: Array<keyof T>) {
  const normalized = query.trim().toLowerCase();
  if (!normalized) {
    return items;
  }

  return items.filter((item) => fields.some((field) => String(item[field] ?? '').toLowerCase().includes(normalized)));
}

function durationText(ms: number) {
  return ms > 0 ? `${ms}ms` : '—';
}

function StepCard({ step, onStepAction, runID, runActionID }: { step: RunStep; onStepAction: RunCockpitPageProps['onStepAction']; runID: string; runActionID: string }) {
  const retrying = runActionID === `${step.id}:step:retry`;
  const canceling = runActionID === `${step.id}:step:cancel`;

  return (
    <HudPanel className="bg-[hsl(var(--panel-soft))] p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <HudLabel className="text-[hsl(var(--faint))]">Step {step.position} · {step.executor}</HudLabel>
          <h4 className="mt-2 text-base font-semibold text-[hsl(var(--ink))]">{step.name}</h4>
          <p className="mt-2 text-xs text-[hsl(var(--faint))]">
            Attempts {step.attempt_count} · {formatTime(step.started_at)} → {formatTime(step.finished_at)} · {durationText(stepDurationMS(step))}
          </p>
        </div>
        <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(step.status)}`}>{step.status}</span>
      </div>

      <div className="mt-4 grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <HudField label="Recorded profile" value={step.profile || 'not recorded'} />
        <HudField label="Model" value={step.model || '—'} />
        <HudField label="Created" value={formatTime(step.created_at)} />
        <HudField label="Started" value={formatTime(step.started_at)} />
        <HudField label="Finished" value={formatTime(step.finished_at)} />
      </div>

      {step.output_summary ? <p className="mt-4 text-sm leading-6 text-[hsl(var(--muted))]">{step.output_summary}</p> : null}
      {step.error ? <p className="mt-3 text-sm leading-6 text-[hsl(var(--danger))]">{step.error}</p> : null}

      {Object.entries(step.correlation_ids ?? {}).length > 0 ? (
        <div className="mt-4 grid gap-2">
          {Object.entries(step.correlation_ids ?? {}).map(([key, value]) => (
            <div key={key} className="flex items-start justify-between gap-3 border-t border-[hsl(var(--border-faint))] pt-2 text-xs text-[hsl(var(--faint))]">
              <span className="text-[hsl(var(--signal))]">{key}</span>
              <span className="break-all text-right text-[hsl(var(--ink))]">{value}</span>
            </div>
          ))}
        </div>
      ) : null}

      <div className="mt-4 flex flex-wrap gap-2">
        <button
          className="hud-chip border-[hsl(var(--pulse))] bg-[hsl(var(--pulse-soft))] px-3 py-2 text-[0.62rem] text-[hsl(var(--ink))] transition hover:bg-[hsl(var(--border-faint))] disabled:cursor-not-allowed disabled:opacity-50"
          disabled={!retryableStepStatuses.has(step.status) || retrying}
          onClick={() => onStepAction(runID, step.id, 'retry')}
          type="button"
        >
          {retrying ? 'Retrying…' : 'Retry step'}
        </button>
        <button
          className="hud-chip border-[hsl(var(--danger))] bg-[hsl(var(--danger-soft))] px-3 py-2 text-[0.62rem] text-[hsl(var(--danger))] transition hover:bg-[hsl(var(--border-faint))] disabled:cursor-not-allowed disabled:opacity-50"
          disabled={!cancelableStepStatuses.has(step.status) || canceling}
          onClick={() => onStepAction(runID, step.id, 'cancel')}
          type="button"
        >
          {canceling ? 'Canceling…' : 'Cancel step'}
        </button>
      </div>
    </HudPanel>
  );
}

export function RunCockpitPage({
  selectedRunID,
  selectedRunDetail,
  selectedRunMissing = false,
  openPilotState,
  runActionID,
  onRetryRun,
  onCancelRun,
  onResumeOpenPilotState,
  onStepAction,
  onBackToOverview,
  onRefresh,
  liveRefresh,
}: RunCockpitPageProps) {
  const [stepStatus, setStepStatus] = useState('all');
  const [stepExecutor, setStepExecutor] = useState('all');
  const [evidenceQuery, setEvidenceQuery] = useState('');

  const steps = selectedRunDetail?.steps ?? [];
  const artifacts = selectedRunDetail?.artifacts ?? [];
  const events = selectedRunDetail?.events ?? [];
  const executorOptions = useMemo(() => ['all', ...Array.from(new Set(steps.map((step) => step.executor))).sort((left, right) => left.localeCompare(right))], [steps]);
  const filteredSteps = useMemo(
    () => steps.filter((step) => (stepStatus === 'all' || step.status === stepStatus) && (stepExecutor === 'all' || step.executor === stepExecutor)),
    [stepExecutor, stepStatus, steps],
  );
  const filteredArtifacts = useMemo(() => filterEvidence(artifacts, evidenceQuery, ['name', 'kind', 'uri']), [artifacts, evidenceQuery]);
  const filteredEvents = useMemo(() => filterEvidence(events, evidenceQuery, ['type', 'message']), [evidenceQuery, events]);
  const correlationTargets = useCorrelationTargets();
  const correlations = useMemo(
    () => (selectedRunDetail ? collectCorrelations(selectedRunDetail, openPilotState, correlationTargets.targets) : []),
    [correlationTargets.targets, openPilotState, selectedRunDetail],
  );
  const failureSummary = useMemo(
    () => (selectedRunDetail ? summarizeRunFailure(selectedRunDetail, openPilotState, correlations) : null),
    [correlations, openPilotState, selectedRunDetail],
  );
  const evidenceRenderKey = useMemo(() => ({}), [filteredSteps, filteredArtifacts, filteredEvents]);
  const evidenceFocus = useEvidenceFocus(selectedRunDetail?.run.id ?? '', evidenceRenderKey);
  const recordedProfiles = useMemo(
    () => Array.from(new Set(steps.map((step) => step.profile).filter((profile): profile is string => Boolean(profile)))).sort(),
    [steps],
  );
  const resolvedModels = useMemo(
    () => Array.from(new Set(steps.map((step) => step.model).filter((model): model is string => Boolean(model)))).sort(),
    [steps],
  );

  if (!selectedRunID) {
    return (
      <HudSection kicker="Run cockpit" title="Run cockpit" note="Deep run inspection, Open Pilot lifecycle gates, evidence, and operator actions.">
        <HudEmptyState>Select a run from Overview to open the run cockpit.</HudEmptyState>
      </HudSection>
    );
  }

  if (!selectedRunDetail) {
    return (
      <HudSection kicker="Run cockpit" title="Run cockpit" note="Deep run inspection, Open Pilot lifecycle gates, evidence, and operator actions.">
        <RunUnavailableNotice missing={selectedRunMissing} runID={selectedRunID} />
      </HudSection>
    );
  }

  const openPilot = selectedRunDetail.run.workflow_id === 'open-pilot';
  const resumeable = openPilot && canResumeOpenPilot(openPilotState);
  const retryBusy = runActionID === `${selectedRunDetail.run.id}:retry`;
  const cancelBusy = runActionID === `${selectedRunDetail.run.id}:cancel`;
  const resumeBusy = openPilotState ? runActionID === `${openPilotState.state.id}:open-pilot:resume` : false;

  return (
    <section className="grid gap-4">
      <HudSection
        kicker="Run cockpit"
        title="Run cockpit"
        note="Deep run inspection, Open Pilot lifecycle gates, evidence, and operator actions."
        actions={[
          liveRefresh ? <LiveRefreshIndicator key="live-refresh" live={liveRefresh} /> : null,
          <button
            key="overview"
            className="hud-chip border-[hsl(var(--border))] bg-[hsl(var(--panel-soft))] px-3 py-2 text-[0.62rem] text-[hsl(var(--muted))] transition hover:border-[hsl(var(--signal))] hover:text-[hsl(var(--signal))]"
            onClick={onBackToOverview}
            type="button"
          >
            Back to overview
          </button>,
          <button
            key="refresh"
            className="hud-chip border-[hsl(var(--signal))] bg-[hsl(var(--signal-soft))] px-3 py-2 text-[0.62rem] text-[hsl(var(--signal))] transition hover:bg-[hsl(var(--panel-soft))] disabled:cursor-not-allowed disabled:opacity-50"
            disabled={retryBusy || cancelBusy || resumeBusy}
            onClick={onRefresh}
            type="button"
          >
            Refresh
          </button>,
        ]}
      >
        <div className="grid gap-4 xl:grid-cols-[1.1fr_0.9fr]">
          <div className="space-y-4">
            <div className="flex flex-wrap items-start justify-between gap-3">
              <div>
                <HudLabel className="text-[hsl(var(--faint))]">{selectedRunDetail.run.workflow_id}</HudLabel>
                <h3 className="mt-2 text-2xl font-semibold text-[hsl(var(--ink))]">{selectedRunDetail.run.summary || selectedRunDetail.run.id}</h3>
                <p className="mt-2 text-xs text-[hsl(var(--muted))]">{selectedRunDetail.run.id}</p>
              </div>
              <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(selectedRunDetail.run.status)}`}>{selectedRunDetail.run.status}</span>
            </div>

            <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
              <HudField label="Created" value={formatTime(selectedRunDetail.run.created_at)} />
              <HudField label="Started" value={formatTime(selectedRunDetail.run.started_at)} />
              <HudField label="Finished" value={formatTime(selectedRunDetail.run.finished_at)} />
              <HudField label="Duration" value={durationText(runDurationMS(selectedRunDetail))} />
              <HudField label="Steps" value={selectedRunDetail.steps.length} />
              <HudField label="Artifacts" value={selectedRunDetail.artifacts.length} />
              <HudField label="Events" value={selectedRunDetail.events.length} />
              <HudField label="Selected run" value={selectedRunID} />
              <HudField label="Recorded profiles" value={recordedProfiles.join(', ') || 'not recorded'} />
              <HudField label="Resolved models" value={resolvedModels.join(', ') || 'not recorded'} />
            </div>

            <div className="flex flex-wrap gap-2">
              <button
                className="hud-chip border-[hsl(var(--pulse))] bg-[hsl(var(--pulse-soft))] px-3 py-2 text-[0.62rem] text-[hsl(var(--ink))] transition hover:bg-[hsl(var(--border-faint))] disabled:cursor-not-allowed disabled:opacity-50"
                disabled={retryBusy || resumeBusy}
                onClick={() => (resumeable && openPilotState ? onResumeOpenPilotState(openPilotState.state) : onRetryRun(selectedRunDetail.run.id))}
                type="button"
              >
                {retryBusy || resumeBusy ? 'Working…' : runActionLabel(selectedRunDetail, openPilotState)}
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
          </div>

          <HudPanelStrong className="p-4">
            <HudLabel className="text-[hsl(var(--faint))]">Command strip</HudLabel>
            <div className="mt-4 grid gap-3 sm:grid-cols-2">
              <HudField label="Workflow" value={selectedRunDetail.run.workflow_id} />
              <HudField label="State" value={selectedRunDetail.run.status} />
              <HudField label="Run ID" value={selectedRunDetail.run.id} />
              <HudField label="Action" value={runActionLabel(selectedRunDetail, openPilotState)} />
            </div>
            <p className="mt-4 text-sm leading-6 text-[hsl(var(--muted))]">
              Use the cockpit to inspect execution timing, rerun the whole envelope, or jump back to the overview scan bands.
            </p>
          </HudPanelStrong>
        </div>
      </HudSection>

      <EvidenceFocusNotice state={evidenceFocus} />
      {failureSummary ? <RunFailureSummaryPanel summary={failureSummary} /> : null}
      <RunPolicyEvidence policy={selectedRunDetail.policy} />

      <HudSection kicker="Open Pilot lifecycle" title="Open Pilot lifecycle" note="Repository issue state, lifecycle gates, and repair loop details live here for Open Pilot runs.">
        {openPilot ? (
          openPilotState ? (
            <div className="grid gap-4 xl:grid-cols-[1.1fr_0.9fr]">
              <HudPanelStrong className="p-4">
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <div>
                    <HudLabel className="text-[hsl(var(--faint))]">Repo # issue</HudLabel>
                    <h3 className="mt-2 text-xl font-semibold text-[hsl(var(--ink))]">
                      {openPilotState.state.repo}#{openPilotState.state.issue}
                    </h3>
                  </div>
                  <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(openPilotState.state.phase)}`}>{openPilotState.state.phase}</span>
                </div>

                <div className="mt-4 grid gap-3 sm:grid-cols-2">
                  <HudField label="Next action" value={openPilotState.next_action} />
                  <HudField label="Branch" value={openPilotState.state.branch || '—'} />
                  <HudField label="Base" value={openPilotState.state.base || '—'} />
                  <HudField label="Worktree" value={openPilotState.state.worktree_path || '—'} />
                  <HudField label="Pull request URL" value={openPilotState.state.pull_request_url || '—'} />
                  <HudField label="Merge SHA" value={openPilotState.state.merge_commit_sha || '—'} />
                  <HudField label="Issue closed" value={openPilotState.state.issue_closed ? 'yes' : 'no'} />
                  <HudField label="Repair attempts" value={`${openPilotState.state.repair_attempts} / ${openPilotState.state.max_repair_attempts}`} />
                  <HudField className="sm:col-span-2" label="Last error" value={openPilotState.state.last_error || '—'} />
                </div>

                {resumeable ? (
                  <div className="mt-4 flex flex-wrap gap-2">
                    <button
                      className="hud-chip border-[hsl(var(--pulse))] bg-[hsl(var(--pulse-soft))] px-3 py-2 text-[0.62rem] text-[hsl(var(--ink))] transition hover:bg-[hsl(var(--border-faint))] disabled:cursor-not-allowed disabled:opacity-50"
                      disabled={resumeBusy}
                      onClick={() => onResumeOpenPilotState(openPilotState.state)}
                      type="button"
                    >
                      {resumeBusy ? 'Resuming…' : 'Resume Open Pilot'}
                    </button>
                  </div>
                ) : null}
              </HudPanelStrong>

              <HudPanelStrong className="p-4">
                <HudLabel className="text-[hsl(var(--faint))]">Gate status</HudLabel>
                <div className="mt-4 grid gap-3">
                  {Object.entries(openPilotState.state.gate_status ?? {}).length > 0 ? (
                    Object.entries(openPilotState.state.gate_status ?? {}).map(([key, value]) => (
                      <HudPanel key={key} className="bg-[hsl(var(--panel-soft))] px-3 py-2">
                        <div className="text-xs text-[hsl(var(--signal))]">{key}</div>
                        <p className="mt-2 text-sm text-[hsl(var(--ink))]">{value}</p>
                      </HudPanel>
                    ))
                  ) : (
                    <HudEmptyState>No gate status recorded yet.</HudEmptyState>
                  )}
                </div>
              </HudPanelStrong>
            </div>
          ) : (
            <HudEmptyState>Open Pilot state is not available for this run yet.</HudEmptyState>
          )
        ) : (
          <HudEmptyState>This run is not managed by Open Pilot lifecycle state.</HudEmptyState>
        )}
      </HudSection>

      <HudSection kicker="Step timeline" title="Step timeline" note="Filter step execution by status and executor, then retry or cancel the exact hop that needs attention.">
        <div className="grid gap-3 lg:grid-cols-[minmax(180px,0.7fr)_minmax(220px,0.8fr)_1fr]">
          <label className="hud-panel bg-[hsl(var(--panel-soft))] px-3 py-2">
            <span className="hud-label text-[hsl(var(--signal))]">Status</span>
            <select className={inputClassName()} onChange={(event) => setStepStatus(event.target.value)} value={stepStatus}>
              {['all', 'queued', 'running', 'succeeded', 'failed', 'canceled', 'timed_out', 'paused'].map((item) => (
                <option key={item} value={item}>
                  {item}
                </option>
              ))}
            </select>
          </label>

          <label className="hud-panel bg-[hsl(var(--panel-soft))] px-3 py-2">
            <span className="hud-label text-[hsl(var(--signal))]">Executor</span>
            <select className={inputClassName()} onChange={(event) => setStepExecutor(event.target.value)} value={stepExecutor}>
              {executorOptions.map((item) => (
                <option key={item} value={item}>
                  {item}
                </option>
              ))}
            </select>
          </label>

          <HudField label="Visible steps" value={filteredSteps.length} />
        </div>

        <div className="mt-4 grid gap-3">
          {filteredSteps.length === 0 ? (
            <HudEmptyState>No steps match the current filters.</HudEmptyState>
          ) : (
            filteredSteps.map((step) => (
              <EvidenceAnchor focus={evidenceFocus.focus} id={step.id} key={step.id} kind="steps">
                <StepCard onStepAction={onStepAction} runActionID={runActionID} runID={selectedRunDetail.run.id} step={step} />
              </EvidenceAnchor>
            ))
          )}
        </div>
      </HudSection>

      {selectedRunDetail.run.workflow_id === 'devlog' ? <DevlogReviewPanel runID={selectedRunDetail.run.id} runStatus={selectedRunDetail.run.status} /> : null}
      {selectedRunDetail.run.workflow_id === 'changemaker-daily-digest' ? (
        <ChangemakerReviewPanel onRevisionCreated={() => onRefresh()} runID={selectedRunDetail.run.id} runStatus={selectedRunDetail.run.status} />
      ) : null}

      <HudSection kicker="Evidence" title="Evidence" note="Search artifacts and events together when you need the full paper trail.">
        <label className="hud-panel bg-[hsl(var(--panel-soft))] px-3 py-2">
          <span className="hud-label text-[hsl(var(--signal))]">Search evidence</span>
          <input className={inputClassName()} onChange={(event) => setEvidenceQuery(event.target.value)} placeholder="artifact, event type, message, URI" value={evidenceQuery} />
        </label>

        <div className="mt-4 grid gap-4 xl:grid-cols-2">
          <HudPanelStrong className="p-4">
            <div className="flex items-center justify-between gap-3">
              <HudLabel className="text-[hsl(var(--faint))]">Artifacts</HudLabel>
              <span className="text-xs text-[hsl(var(--muted))]">{filteredArtifacts.length} visible</span>
            </div>
            <div className="mt-4 grid gap-3">
              {filteredArtifacts.length === 0 ? (
                <HudEmptyState>No artifacts match the current search.</HudEmptyState>
              ) : (
                filteredArtifacts.map((artifact) => (
                  <EvidenceAnchor focus={evidenceFocus.focus} id={artifact.id} key={artifact.id} kind="artifacts">
                    <HudPanel className="bg-[hsl(var(--panel-soft))] p-4">
                      <div className="flex items-start justify-between gap-3">
                        <div>
                          <p className="text-sm font-semibold text-[hsl(var(--ink))]">{artifactLabel(artifact)}</p>
                          <p className="mt-1 text-xs text-[hsl(var(--faint))]">{artifact.kind}</p>
                        </div>
                        <span className="text-xs text-[hsl(var(--signal))]">{formatTime(artifact.created_at)}</span>
                      </div>
                      <ArtifactPreview artifact={artifact} />
                    </HudPanel>
                  </EvidenceAnchor>
                ))
              )}
            </div>
          </HudPanelStrong>

          <HudPanelStrong className="p-4">
            <div className="flex items-center justify-between gap-3">
              <HudLabel className="text-[hsl(var(--faint))]">Events</HudLabel>
              <span className="text-xs text-[hsl(var(--muted))]">{filteredEvents.length} visible</span>
            </div>
            <div className="mt-4 grid gap-3">
              {filteredEvents.length === 0 ? (
                <HudEmptyState>No events match the current search.</HudEmptyState>
              ) : (
                filteredEvents.map((event) => (
                  <EvidenceAnchor focus={evidenceFocus.focus} id={event.id} key={event.id} kind="events">
                    <HudPanel className="border-[hsl(var(--pulse))] bg-[hsl(var(--panel-soft))] p-4">
                      <div className="flex items-center justify-between gap-3">
                        <p className="hud-label text-[hsl(var(--pulse))]">{event.type}</p>
                        <span className="text-xs text-[hsl(var(--faint))]">{formatTime(event.created_at)}</span>
                      </div>
                      <p className="mt-3 text-sm leading-6 text-[hsl(var(--muted))]">{event.message}</p>
                    </HudPanel>
                  </EvidenceAnchor>
                ))
              )}
            </div>
          </HudPanelStrong>
        </div>
      </HudSection>

      <HudSection kicker="Correlation" title="Correlation + traces" note="Runs, steps, requests, sessions, Gitea issue/PR/CI, and traces. Only approved destinations are linked; every ID can be copied.">
        <CorrelationLinks destinations={correlations} targets={correlationTargets} />
      </HudSection>
    </section>
  );
}
