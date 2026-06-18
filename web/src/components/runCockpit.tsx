import type { Artifact, OpenPilotStateResponse, RunDetail, RunStep } from '../types';

export const terminalRunStatuses = new Set(['succeeded', 'failed', 'canceled']);
export const activeRunStatuses = new Set(['queued', 'running']);
export const retryableStepStatuses = new Set(['failed', 'canceled', 'timed_out']);
export const cancelableStepStatuses = new Set(['queued', 'running']);

export function runDurationMS(detail: RunDetail | null) {
  if (!detail?.run.started_at || !detail.run.finished_at) {
    return 0;
  }

  const started = new Date(detail.run.started_at).getTime();
  const finished = new Date(detail.run.finished_at).getTime();

  return Number.isFinite(started) && Number.isFinite(finished) && finished >= started ? finished - started : 0;
}

export function stepDurationMS(step: RunStep) {
  if (!step.started_at || !step.finished_at) {
    return 0;
  }

  const started = new Date(step.started_at).getTime();
  const finished = new Date(step.finished_at).getTime();

  return Number.isFinite(started) && Number.isFinite(finished) && finished >= started ? finished - started : 0;
}

export function artifactLabel(artifact: Artifact) {
  return `${artifact.kind} · ${artifact.name}`;
}

export function canResumeOpenPilot(state: OpenPilotStateResponse | null) {
  return Boolean(state && ['failed', 'paused', 'agent_completed', 'merged'].includes(state.state.phase));
}
