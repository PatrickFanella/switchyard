import { fetchJSON } from './api';
import type { Run } from './types';

export type OpenPilotInboxCheck = {
  name: string;
  code: string;
  status: 'pass' | 'warn' | 'fail' | 'skip';
  message: string;
  remediation?: string;
};

export type OpenPilotInboxClaim = {
  state_id: string;
  phase: string;
  run_id?: string;
  run_status?: string;
  active_run: boolean;
  next_action: string;
  last_step_name?: string;
  last_error?: string;
  failing_gate?: string;
  operator_summary?: string;
  recommended_next_action?: string;
  branch?: string;
  pull_request_url?: string;
  pull_request_number?: number;
  merge_commit_sha?: string;
  issue_closed: boolean;
  repair_attempts: number;
  max_repair_attempts: number;
  updated_at: string;
};

export type OpenPilotInboxIssue = {
  key: string;
  repo: string;
  repository_id: string;
  executor_generation: string;
  issue: string;
  issue_id?: number;
  title?: string;
  state?: string;
  labels: string[];
  assignees: string[];
  source: 'upstream' | 'state';
  pickup: 'assigned' | 'labels' | 'both' | 'none' | 'unknown';
  bot_assigned: boolean;
  labels_satisfied: boolean;
  missing_labels: string[];
  spec_required: boolean;
  spec_ready: boolean;
  test_command?: string;
  eligible: boolean;
  action: 'dispatch' | 'resume' | 'none';
  reason_codes: string[];
  checks: OpenPilotInboxCheck[];
  claim?: OpenPilotInboxClaim;
  links: { issue?: string; pull_request?: string; run?: string; state?: string; readiness: string };
};

export type OpenPilotInboxRepository = {
  id: string;
  slug: string;
  executor_generation: string;
  admitted: boolean;
  admission_code: string;
  admission_message?: string;
  polled: boolean;
  upstream: 'listed' | 'unavailable' | 'not_queried';
  upstream_error?: string;
  listed: number;
  claims: number;
};

export type OpenPilotInboxReport = {
  checked_at: string;
  activation_mode?: string;
  upstream: 'available' | 'partial' | 'unavailable' | 'not_queried';
  upstream_error?: string;
  policy: { required_labels: string[]; assignee?: string; require_issue_spec: boolean; poll_repositories: string[] };
  repositories: OpenPilotInboxRepository[];
  issues: OpenPilotInboxIssue[];
  matched: number;
  has_more: boolean;
  next_cursor?: string;
  checks: OpenPilotInboxCheck[];
  blockers: string[];
};

export type InboxEligibility = 'all' | 'eligible' | 'ineligible' | 'claimed';
export type InboxPickup = '' | 'assigned' | 'labels' | 'none';

export type InboxLink = {
  repo: string;
  eligibility: InboxEligibility;
  pickup: InboxPickup;
  phase: string;
  q: string;
  cursor: string;
  issue: string;
};

export const emptyInboxLink: InboxLink = { repo: '', eligibility: 'all', pickup: '', phase: '', q: '', cursor: '', issue: '' };

const eligibilities: InboxEligibility[] = ['all', 'eligible', 'ineligible', 'claimed'];
const pickups: InboxPickup[] = ['', 'assigned', 'labels', 'none'];

// Deep links live under `#/inbox` as one URL-encoded parameter segment so the
// shell router keeps resolving the inbox view: `#/inbox/repo=owner%2Frepo&eligibility=eligible&issue=owner%2Frepo%2342`.
export function parseInboxHash(hash: string | undefined): InboxLink {
  const normalized = hash?.trim() ?? '';
  if (!normalized.startsWith('#/inbox/')) {
    return { ...emptyInboxLink };
  }
  let params: URLSearchParams;
  try {
    params = new URLSearchParams(normalized.slice('#/inbox/'.length));
  } catch {
    return { ...emptyInboxLink };
  }
  const eligibility = params.get('eligibility') ?? 'all';
  const pickup = params.get('pickup') ?? '';
  const cursor = params.get('cursor') ?? '';
  return {
    repo: (params.get('repo') ?? '').slice(0, 256),
    eligibility: (eligibilities as string[]).includes(eligibility) ? (eligibility as InboxEligibility) : 'all',
    pickup: (pickups as string[]).includes(pickup) ? (pickup as InboxPickup) : '',
    phase: /^[a-z_]{0,32}$/.test(params.get('phase') ?? '') ? (params.get('phase') ?? '') : '',
    q: (params.get('q') ?? '').slice(0, 128),
    cursor: /^[A-Za-z0-9_-]{0,512}$/.test(cursor) ? cursor : '',
    issue: (params.get('issue') ?? '').slice(0, 300),
  };
}

export function formatInboxHash(link: InboxLink) {
  const params = new URLSearchParams();
  if (link.repo.trim()) params.set('repo', link.repo.trim());
  if (link.eligibility !== 'all') params.set('eligibility', link.eligibility);
  if (link.pickup) params.set('pickup', link.pickup);
  if (link.phase) params.set('phase', link.phase);
  if (link.q.trim()) params.set('q', link.q.trim());
  if (link.cursor) params.set('cursor', link.cursor);
  if (link.issue) params.set('issue', link.issue);
  const encoded = params.toString();
  return encoded ? `#/inbox/${encoded}` : '#/inbox';
}

export function inboxQuery(link: InboxLink, limit = 25) {
  const params = new URLSearchParams();
  if (link.repo.trim()) params.set('repo', link.repo.trim());
  if (link.eligibility !== 'all') params.set('eligibility', link.eligibility);
  if (link.pickup) params.set('pickup', link.pickup);
  if (link.phase) params.set('phase', link.phase);
  if (link.q.trim()) params.set('q', link.q.trim());
  if (link.cursor) params.set('cursor', link.cursor);
  params.set('limit', String(limit));
  return params.toString();
}

function arrayOr<T>(value: T[] | null | undefined): T[] {
  return Array.isArray(value) ? value : [];
}

export function normalizeInboxReport(report: OpenPilotInboxReport): OpenPilotInboxReport {
  return {
    ...report,
    repositories: arrayOr(report.repositories),
    checks: arrayOr(report.checks),
    blockers: arrayOr(report.blockers),
    policy: { ...report.policy, required_labels: arrayOr(report.policy?.required_labels), poll_repositories: arrayOr(report.policy?.poll_repositories) },
    issues: arrayOr(report.issues).map((issue) => ({
      ...issue,
      labels: arrayOr(issue.labels),
      assignees: arrayOr(issue.assignees),
      missing_labels: arrayOr(issue.missing_labels),
      reason_codes: arrayOr(issue.reason_codes),
      checks: arrayOr(issue.checks),
      links: issue.links ?? { readiness: '' },
    })),
  };
}

export async function loadOpenPilotInbox(link: InboxLink) {
  return normalizeInboxReport(await fetchJSON<OpenPilotInboxReport>(`/api/open-pilot/inbox?${inboxQuery(link)}`));
}

// Dispatch uses the existing manual Open Pilot route, which applies the
// admission gate, the durable (repo, issue) claim, and admin authorization.
export async function dispatchOpenPilotIssue(issue: OpenPilotInboxIssue) {
  const body: Record<string, string> = { repo: issue.repo, issue: issue.issue };
  if (issue.test_command) body.test_command = issue.test_command;
  return fetchJSON<{ run: Run }>('/api/workflows/open-pilot/run', { method: 'POST', body: JSON.stringify(body) });
}

// Only http(s) URLs from upstream are rendered as external links.
export function safeExternalURL(value?: string) {
  if (!value) return '';
  try {
    const parsed = new URL(value);
    return parsed.protocol === 'https:' || parsed.protocol === 'http:' ? parsed.toString() : '';
  } catch {
    return '';
  }
}
