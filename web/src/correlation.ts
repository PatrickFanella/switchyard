import { openPilotRunInputs } from './api';
import { sanitizeLinkHref } from './components/artifactPreview';
import { formatEvidenceSearchHash } from './components/evidenceSearch';
import { formatSessionHash } from './components/sessionLinks';
import { formatHash } from './hooks/useHashNavigation';
import type { CorrelationTargets, OpenPilotStateResponse, RunDetail, RunStep } from './types';

// Typed correlation destinations for the run cockpit. Every destination keeps
// its raw value so it can always be copied; `href` is set only when the value
// resolves to an in-app hash route or to an allowlisted external origin.

export type CorrelationKind =
  | 'run'
  | 'step'
  | 'request'
  | 'session'
  | 'executor-request'
  | 'gitea-issue'
  | 'gitea-pull-request'
  | 'gitea-commit'
  | 'gitea-ci'
  | 'trace'
  | 'span'
  | 'link'
  | 'identifier';

// Why a destination has no link. `current` means it points at the run already open.
export type CorrelationUnavailableReason = 'current' | 'not-configured' | 'rejected' | 'invalid-id' | 'no-destination';

export type CorrelationDestination = {
  kind: CorrelationKind;
  key: string;
  label: string;
  value: string;
  sources: string[];
  href: string | null;
  external: boolean;
  reason?: CorrelationUnavailableReason;
};

export type AllowedOrigin = { protocol: 'http:' | 'https:'; host: string; pathPrefix: string };

export const TRACE_ID_PLACEHOLDER = '{trace_id}';

export const correlationKindLabels: Record<CorrelationKind, string> = {
  run: 'Switchyard run',
  step: 'Run step',
  request: 'Model broker request',
  session: 'Executor session',
  'executor-request': 'Executor request',
  'gitea-issue': 'Gitea issue',
  'gitea-pull-request': 'Gitea pull request',
  'gitea-commit': 'Gitea commit',
  'gitea-ci': 'Gitea CI',
  trace: 'Trace',
  span: 'Span',
  link: 'External link',
  identifier: 'Identifier',
};

export const correlationReasonText: Record<CorrelationUnavailableReason, string> = {
  current: 'This is the run currently open.',
  'not-configured': 'No destination is configured for this ID, so it is shown for copying.',
  rejected: 'The URL is outside the approved destinations, so it is not linked.',
  'invalid-id': 'The recorded value is not a valid ID for this destination.',
  'no-destination': 'No Switchyard page exists for this ID; copy it to search elsewhere.',
};

const kindOrder: CorrelationKind[] = ['gitea-ci', 'gitea-pull-request', 'gitea-issue', 'gitea-commit', 'trace', 'span', 'request', 'session', 'executor-request', 'run', 'step', 'link', 'identifier'];

const requestKeys = new Set(['model_broker_request_id', 'llama_line_request_id', 'request_id']);
const sessionKeys = new Set(['opencode_session_id', 'ocq_session_id']);
const executorRequestKeys = new Set(['opencode_request_id', 'ocq_request_id']);
const repoPattern = /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/;
const issuePattern = /^[1-9][0-9]{0,9}$/;
const shaPattern = /^[0-9a-f]{7,64}$/i;
const traceIDPattern = /^[A-Za-z0-9._:-]{1,256}$/;
const urlPattern = /https?:\/\/[^\s<>"'`)\]]+/g;
const failedStatuses = new Set(['failed', 'timed_out', 'canceled']);
const failedGateValues = new Set(['failed', 'failure', 'blocked', 'rejected', 'changes_requested', 'timed_out', 'error']);

function parseBase(raw: string | undefined): AllowedOrigin | null {
  const trimmed = raw?.trim() ?? '';
  if (!trimmed) return null;
  const placeholderAt = trimmed.indexOf(TRACE_ID_PLACEHOLDER);
  // A trace base allows paths on its origin below the segment holding the placeholder.
  const candidate = placeholderAt >= 0 ? trimmed.slice(0, placeholderAt).replace(/\/[^/]*$/, '') || trimmed.slice(0, placeholderAt) : trimmed;
  try {
    const parsed = new URL(candidate);
    if ((parsed.protocol !== 'http:' && parsed.protocol !== 'https:') || !parsed.hostname || parsed.username || parsed.password) return null;
    return { protocol: parsed.protocol, host: parsed.host, pathPrefix: parsed.pathname.replace(/\/+$/, '') };
  } catch {
    return null;
  }
}

// correlationAllowlist derives the approved external origins from the targets
// the control plane publishes. Nothing else is linkable.
export function correlationAllowlist(targets: CorrelationTargets | null | undefined): AllowedOrigin[] {
  return [parseBase(targets?.gitea_base_url), parseBase(targets?.trace_ui_base_url)].filter((origin): origin is AllowedOrigin => origin !== null);
}

// allowedExternalHref returns a normalized http(s) URL only when it passes the
// shared link sanitizer, carries no credentials, and sits on an allowlisted
// origin under its path prefix. Everything else yields null.
export function allowedExternalHref(raw: string, allowlist: AllowedOrigin[]): string | null {
  const sanitized = sanitizeLinkHref(raw);
  if (!sanitized) return null;
  let parsed: URL;
  try {
    parsed = new URL(sanitized);
  } catch {
    return null;
  }
  if ((parsed.protocol !== 'http:' && parsed.protocol !== 'https:') || parsed.username || parsed.password) return null;
  const allowed = allowlist.some(
    (origin) =>
      origin.protocol === parsed.protocol &&
      origin.host === parsed.host &&
      (origin.pathPrefix === '' || parsed.pathname === origin.pathPrefix || parsed.pathname.startsWith(`${origin.pathPrefix}/`)),
  );
  return allowed ? parsed.toString() : null;
}

function giteaBase(targets: CorrelationTargets | null | undefined) {
  const raw = targets?.gitea_base_url?.trim() ?? '';
  return parseBase(raw) ? raw.replace(/\/+$/, '') : '';
}

export function giteaIssueHref(targets: CorrelationTargets | null | undefined, repo: string, issue: string) {
  const base = giteaBase(targets);
  if (!base || !repoPattern.test(repo) || !issuePattern.test(issue)) return null;
  return allowedExternalHref(`${base}/${repo}/issues/${issue}`, correlationAllowlist(targets));
}

export function giteaPullRequestHref(targets: CorrelationTargets | null | undefined, repo: string, number: string) {
  const base = giteaBase(targets);
  if (!base || !repoPattern.test(repo) || !issuePattern.test(number)) return null;
  return allowedExternalHref(`${base}/${repo}/pulls/${number}`, correlationAllowlist(targets));
}

export function giteaCommitHref(targets: CorrelationTargets | null | undefined, repo: string, sha: string) {
  const base = giteaBase(targets);
  if (!base || !repoPattern.test(repo) || !shaPattern.test(sha)) return null;
  return allowedExternalHref(`${base}/${repo}/commit/${sha.toLowerCase()}`, correlationAllowlist(targets));
}

export function giteaActionsHref(targets: CorrelationTargets | null | undefined, repo: string) {
  const base = giteaBase(targets);
  if (!base || !repoPattern.test(repo)) return null;
  return allowedExternalHref(`${base}/${repo}/actions`, correlationAllowlist(targets));
}

export function traceHref(targets: CorrelationTargets | null | undefined, traceID: string): { href: string | null; reason?: CorrelationUnavailableReason } {
  const base = targets?.trace_ui_base_url?.trim() ?? '';
  if (!base || !parseBase(base)) return { href: null, reason: 'not-configured' };
  if (!traceIDPattern.test(traceID)) return { href: null, reason: 'invalid-id' };
  const encoded = encodeURIComponent(traceID);
  const url = base.includes(TRACE_ID_PLACEHOLDER) ? base.split(TRACE_ID_PLACEHOLDER).join(encoded) : `${base.replace(/\/+$/, '')}/${encoded}`;
  const href = allowedExternalHref(url, correlationAllowlist(targets));
  return href ? { href } : { href: null, reason: 'rejected' };
}

// In-app evidence deep links extend the run cockpit route with an evidence
// segment: `#/runs/<run>/<steps|artifacts|events>/<id>`. The shell router
// resolves the run from the first segment; the cockpit focuses the evidence.
export type EvidenceKind = 'steps' | 'artifacts' | 'events';
export type EvidenceFocus = { runID: string; kind: EvidenceKind; id: string };

const evidenceKinds = new Set<EvidenceKind>(['steps', 'artifacts', 'events']);

export function formatEvidenceHash(runID: string, kind: EvidenceKind, id: string) {
  return `${formatHash('runs', runID)}/${kind}/${encodeURIComponent(id)}`;
}

export function parseEvidenceHash(hash: string | undefined): EvidenceFocus | null {
  const normalized = hash?.trim() ?? '';
  if (!normalized.startsWith('#/runs/')) return null;
  const [runPart, kindPart, idPart] = normalized.slice('#/runs/'.length).split('/');
  if (!runPart || !kindPart || !idPart || !evidenceKinds.has(kindPart as EvidenceKind)) return null;
  try {
    return { runID: decodeURIComponent(runPart), kind: kindPart as EvidenceKind, id: decodeURIComponent(idPart) };
  } catch {
    return null;
  }
}

export function evidenceAnchorID(kind: EvidenceKind, id: string) {
  return `evidence-${kind}-${id}`;
}

export function classifyCorrelationURL(url: string): CorrelationKind {
  let path = '';
  try {
    path = new URL(url).pathname;
  } catch {
    return 'link';
  }
  if (/\/actions(\/|$)/.test(path)) return 'gitea-ci';
  if (/\/pulls\/\d+/.test(path)) return 'gitea-pull-request';
  if (/\/issues\/\d+/.test(path)) return 'gitea-issue';
  if (/\/commit\/[0-9a-f]+/i.test(path)) return 'gitea-commit';
  return 'link';
}

function externalDestination(key: string, label: string, value: string, source: string, allowlist: AllowedOrigin[], kind?: CorrelationKind): CorrelationDestination {
  const href = allowedExternalHref(value, allowlist);
  return { kind: kind ?? classifyCorrelationURL(value), key, label, value, sources: [source], href, external: href !== null, reason: href ? undefined : allowlist.length === 0 ? 'not-configured' : 'rejected' };
}

function isAbsoluteURL(value: string) {
  return /^[a-z][a-z0-9+.-]*:/i.test(value.trim());
}

// resolveCorrelation maps one recorded correlation key/value to its destination.
export function resolveCorrelation(key: string, rawValue: string, context: { runID: string; source: string; targets: CorrelationTargets | null | undefined }): CorrelationDestination {
  const value = rawValue.trim();
  const base = { key, value, sources: [context.source], external: false };
  const allowlist = correlationAllowlist(context.targets);
  if (isAbsoluteURL(value)) {
    return externalDestination(key, key, value, context.source, allowlist);
  }
  if (key === 'trace_id') {
    const trace = traceHref(context.targets, value);
    return { ...base, kind: 'trace', label: key, href: trace.href, external: trace.href !== null, reason: trace.reason };
  }
  if (key === 'span_id') {
    return { ...base, kind: 'span', label: key, href: null, reason: 'no-destination' };
  }
  if (requestKeys.has(key)) {
    return { ...base, kind: 'request', label: key, href: value ? formatHash('queues', '', value) : null, reason: value ? undefined : 'invalid-id' };
  }
  if (sessionKeys.has(key)) {
    return { ...base, kind: 'session', label: key, href: value ? formatSessionHash(value) : null, reason: value ? undefined : 'invalid-id' };
  }
  if (executorRequestKeys.has(key)) {
    const href = value ? formatEvidenceSearchHash({ scope: 'correlations', query: value, cursor: '' }) : null;
    return { ...base, kind: 'executor-request', label: key, href, reason: href ? undefined : 'invalid-id' };
  }
  if (/(^|_)run_id$/.test(key)) {
    if (value === context.runID) return { ...base, kind: 'run', label: key, href: null, reason: 'current' };
    return { ...base, kind: 'run', label: key, href: value ? formatHash('runs', value) : null, reason: value ? undefined : 'invalid-id' };
  }
  if (/(^|_)step_id$/.test(key)) {
    return { ...base, kind: 'step', label: key, href: value ? formatEvidenceHash(context.runID, 'steps', value) : null, reason: value ? undefined : 'invalid-id' };
  }
  return { ...base, kind: 'identifier', label: key, href: null, reason: 'no-destination' };
}

export function extractURLs(text: string | undefined) {
  return Array.from(new Set(text?.match(urlPattern) ?? [])).map((url) => url.replace(/[.,;:]+$/, ''));
}

function stepSource(step: RunStep) {
  return `Step ${step.position} · ${step.name}`;
}

// collectCorrelations gathers every correlation for a run: step correlation
// IDs, URLs recorded in failed step output, and Open Pilot issue/PR/CI state.
// Identical destinations are merged and their sources concatenated.
export function collectCorrelations(detail: RunDetail, openPilotState: OpenPilotStateResponse | null, targets: CorrelationTargets | null | undefined): CorrelationDestination[] {
  const runID = detail.run.id;
  const allowlist = correlationAllowlist(targets);
  const items: CorrelationDestination[] = [];

  for (const step of detail.steps) {
    for (const [key, value] of Object.entries(step.correlation_ids ?? {})) {
      if (value?.trim()) items.push(resolveCorrelation(key, value, { runID, source: stepSource(step), targets }));
    }
    if (failedStatuses.has(step.status)) {
      for (const url of [...extractURLs(step.error), ...extractURLs(step.output_summary)]) {
        items.push(externalDestination('step output', 'URL in step output', url, stepSource(step), allowlist));
      }
    }
  }

  const state = openPilotState?.state;
  const inputs = openPilotRunInputs(detail);
  const repo = state?.repo ?? inputs?.repo ?? '';
  const issue = state?.issue ?? inputs?.issue ?? '';
  const stateSource = state ? 'Open Pilot state' : 'Run inputs';
  if (repo && issue) {
    const href = giteaIssueHref(targets, repo, issue);
    items.push({ kind: 'gitea-issue', key: 'issue', label: 'Issue', value: `${repo}#${issue}`, sources: [stateSource], href, external: href !== null, reason: href ? undefined : giteaBase(targets) ? 'invalid-id' : 'not-configured' });
  }
  if (state?.pull_request_url) {
    items.push(externalDestination('pull_request_url', 'Pull request', state.pull_request_url, 'Open Pilot state', allowlist, 'gitea-pull-request'));
  } else if (state?.pull_request_number && repo) {
    const href = giteaPullRequestHref(targets, repo, String(state.pull_request_number));
    items.push({ kind: 'gitea-pull-request', key: 'pull_request_number', label: 'Pull request', value: `${repo}!${state.pull_request_number}`, sources: ['Open Pilot state'], href, external: href !== null, reason: href ? undefined : 'not-configured' });
  }
  if (state?.merge_commit_sha && repo) {
    const href = giteaCommitHref(targets, repo, state.merge_commit_sha);
    items.push({ kind: 'gitea-commit', key: 'merge_commit_sha', label: 'Merge commit', value: state.merge_commit_sha, sources: ['Open Pilot state'], href, external: href !== null, reason: href ? undefined : giteaBase(targets) ? 'invalid-id' : 'not-configured' });
  }
  if (state?.gate_status?.ci_checks && repo) {
    const href = giteaActionsHref(targets, repo);
    items.push({ kind: 'gitea-ci', key: 'ci_checks', label: `CI checks (${state.gate_status.ci_checks})`, value: `${repo} actions`, sources: ['Open Pilot state'], href, external: href !== null, reason: href ? undefined : 'not-configured' });
  }
  if (state?.run_id) {
    items.push(resolveCorrelation('open_pilot_run_id', state.run_id, { runID, source: 'Open Pilot state', targets }));
  }

  const merged = new Map<string, CorrelationDestination>();
  for (const item of items) {
    const id = `${item.kind}\u0000${item.key}\u0000${item.value}`;
    const existing = merged.get(id);
    if (existing) {
      for (const source of item.sources) if (!existing.sources.includes(source)) existing.sources.push(source);
    } else {
      merged.set(id, { ...item, sources: [...item.sources] });
    }
  }
  return Array.from(merged.values()).sort((left, right) => kindOrder.indexOf(left.kind) - kindOrder.indexOf(right.kind) || left.key.localeCompare(right.key) || left.value.localeCompare(right.value));
}

export type FailureEvidence = { kind: EvidenceKind; id: string; label: string; href: string };

export type RunFailureSummary = {
  status: RunDetail['run']['status'];
  headline: string;
  detail: string;
  failedSteps: RunStep[];
  gateFailures: { gate: string; status: string }[];
  evidence: FailureEvidence[];
  links: CorrelationDestination[];
};

function firstLine(text: string | undefined) {
  return (text ?? '').split('\n').map((line) => line.trim()).find(Boolean) ?? '';
}

// summarizeRunFailure explains why a run failed and points at the evidence:
// failed steps, failure events, likely verification artifacts, and the CI, PR,
// and trace destinations that belong to the failing context.
export function summarizeRunFailure(detail: RunDetail, openPilotState: OpenPilotStateResponse | null, destinations: CorrelationDestination[]): RunFailureSummary | null {
  const runID = detail.run.id;
  const failedSteps = detail.steps.filter((step) => failedStatuses.has(step.status)).sort((left, right) => left.position - right.position);
  const gateFailures = Object.entries(openPilotState?.state.gate_status ?? {})
    .filter(([, status]) => failedGateValues.has(status.trim().toLowerCase()))
    .map(([gate, status]) => ({ gate, status }));
  if (!failedStatuses.has(detail.run.status) && failedSteps.length === 0 && gateFailures.length === 0) {
    return null;
  }

  const primary = failedSteps[0];
  const headline = primary ? `${primary.name} ${primary.status.replace('_', ' ')}` : gateFailures[0] ? `Gate ${gateFailures[0].gate} ${gateFailures[0].status}` : `Run ${detail.run.status.replace('_', ' ')}`;
  const detailText = firstLine(primary?.error) || firstLine(openPilotState?.state.last_error) || firstLine(primary?.output_summary) || detail.run.summary || 'No failure message was recorded.';

  const evidence: FailureEvidence[] = [
    ...failedSteps.map((step) => ({ kind: 'steps' as const, id: step.id, label: `Step ${step.position} · ${step.name}`, href: formatEvidenceHash(runID, 'steps', step.id) })),
    ...detail.events
      .filter((event) => /fail|error|timed?_?out|cancel|reject/i.test(`${event.type} ${event.message}`))
      .slice(0, 5)
      .map((event) => ({ kind: 'events' as const, id: event.id, label: `Event ${event.type}`, href: formatEvidenceHash(runID, 'events', event.id) })),
    ...detail.artifacts
      .filter((artifact) => /verif|ci|check|test|log|evidence|review|feedback|diff/i.test(`${artifact.name} ${artifact.kind}`))
      .slice(0, 5)
      .map((artifact) => ({ kind: 'artifacts' as const, id: artifact.id, label: `Artifact ${artifact.name}`, href: formatEvidenceHash(runID, 'artifacts', artifact.id) })),
  ];

  const failingSources = new Set(failedSteps.map(stepSource));
  const links = destinations.filter(
    (destination) =>
      destination.kind === 'gitea-ci' ||
      destination.kind === 'gitea-pull-request' ||
      (destination.sources.some((source) => failingSources.has(source)) && ['trace', 'request', 'session', 'link'].includes(destination.kind)),
  );

  return { status: detail.run.status, headline, detail: detailText, failedSteps, gateFailures, evidence, links };
}
