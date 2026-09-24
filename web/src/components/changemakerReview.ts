import { apiPath, APIError, arrayOrEmpty } from '../api';
import type { Artifact, Run } from '../types';

export type ChangemakerVersionState =
  | 'not-ready'
  | 'no-draft'
  | 'awaiting-review'
  | 'approved'
  | 'rejected'
  | 'revision-requested'
  | 'approval-expired'
  | 'destination-changed'
  | 'publishing'
  | 'publication-failed'
  | 'published'
  | 'superseded';

export type ChangemakerDecisionKind = 'approved' | 'rejected' | 'revision-requested';

export type ChangemakerReviewDecision = {
  id: string;
  thread_id: string;
  version: number;
  run_id: string;
  artifact_id: string;
  draft_sha256: string;
  evidence_sha256: string;
  decision: ChangemakerDecisionKind;
  notes: string;
  actor: string;
  adapter: string;
  destination: string;
  revision_run_id?: string;
  created_at: string;
  expires_at: string;
};

export type ChangemakerPublicationReceipt = {
  id: string;
  idempotency_key: string;
  run_id: string;
  artifact_id: string;
  artifact_sha256: string;
  adapter: string;
  destination: string;
  approval_id: string;
  status: 'sending' | 'delivered' | 'failed';
  attempts: number;
  reconciled: boolean;
  external_id?: string;
  external_url?: string;
  last_error?: string;
  requested_by: string;
  created_at: string;
  updated_at: string;
  delivered_at?: string;
};

export type ChangemakerDraftVersionView = {
  id: string;
  thread_id: string;
  version: number;
  run_id: string;
  parent_version?: number;
  sources_run_id: string;
  source_mode: 'original' | 'same' | 'recollected';
  revision_notes?: string;
  requested_by?: string;
  created_at: string;
  run_status: string;
  run_summary?: string;
  trigger_id?: string;
  draft: Artifact | null;
  draft_sha256?: string;
  evidence_sha256?: string;
  evidence_artifact_id?: string;
  grounding_status?: string;
  draft_generated: boolean;
  cited_sources?: string;
  unknown_citations?: string;
  ready: boolean;
  not_ready_reason?: string;
  state: ChangemakerVersionState;
  idempotency_key?: string;
  current_decision?: ChangemakerReviewDecision;
  current_receipt?: ChangemakerPublicationReceipt;
};

export type ChangemakerPublicationStatus = {
  enabled: boolean;
  adapter: string;
  destination?: string;
  reason?: string;
  approval_ttl_seconds: number;
  max_draft_versions: number;
  policy: string;
};

export type ChangemakerDraftReview = {
  thread_id: string;
  selected_version: number;
  latest_version: number;
  current_version: number;
  published_version?: number;
  state: ChangemakerVersionState;
  versions: ChangemakerDraftVersionView[];
  decisions: ChangemakerReviewDecision[];
  receipts: ChangemakerPublicationReceipt[];
  publication: ChangemakerPublicationStatus;
  access?: { role: string; can_write: boolean };
};

export type ChangemakerActionResult<T> =
  | { ok: true; status: number; body: T }
  | { ok: false; status: number; code?: string; message: string; body?: T };

export const changemakerStateLabels: Record<ChangemakerVersionState, string> = {
  'not-ready': 'Draft not ready',
  'no-draft': 'No draft generated',
  'awaiting-review': 'Awaiting review',
  approved: 'Approved, not published',
  rejected: 'Rejected',
  'revision-requested': 'Revision requested',
  'approval-expired': 'Approval expired',
  'destination-changed': 'Approved for a different destination',
  publishing: 'Publication in progress',
  'publication-failed': 'Publication failed; retry is safe',
  published: 'Published',
  superseded: 'Superseded by a newer version',
};

export const changemakerSourceModeLabels: Record<ChangemakerDraftVersionView['source_mode'], string> = {
  original: 'original collection',
  same: 'same sources as the revised version',
  recollected: 'sources re-collected over the same window',
};

function changemakerDraftPath(runID: string, suffix = '') {
  return `/api/changemaker/drafts/${encodeURIComponent(runID)}${suffix}`;
}

export function normalizeChangemakerReview(review: ChangemakerDraftReview): ChangemakerDraftReview {
  return {
    ...review,
    versions: arrayOrEmpty(review.versions).map((version) => ({ ...version, draft: version.draft ?? null })),
    decisions: arrayOrEmpty(review.decisions),
    receipts: arrayOrEmpty(review.receipts),
  };
}

export async function loadChangemakerReview(runID: string): Promise<ChangemakerDraftReview> {
  const response = await fetch(apiPath(changemakerDraftPath(runID)), { headers: { accept: 'application/json' } });
  if (!response.ok) {
    throw new APIError(response.status);
  }
  return normalizeChangemakerReview((await response.json()) as ChangemakerDraftReview);
}

async function postChangemakerAction<T>(path: string, payload: unknown): Promise<ChangemakerActionResult<T>> {
  const response = await fetch(apiPath(path), {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(payload),
  });
  const body = (await response.json().catch(() => ({}))) as T & { error?: { code?: string; message?: string } };
  if (response.ok) {
    return { ok: true, status: response.status, body };
  }
  return {
    ok: false,
    status: response.status,
    code: body.error?.code,
    message: body.error?.message ?? `Switchyard API returned ${response.status}`,
    body: body.error ? undefined : body,
  };
}

type DigestBinding = { version: number; draft_sha256: string; evidence_sha256: string };

export function bindingFor(view: ChangemakerDraftVersionView): DigestBinding {
  return { version: view.version, draft_sha256: view.draft_sha256 ?? '', evidence_sha256: view.evidence_sha256 ?? '' };
}

export function decideChangemakerDraft(runID: string, input: DigestBinding & { decision: 'approved' | 'rejected'; notes: string }) {
  return postChangemakerAction<{ decision: ChangemakerReviewDecision }>(changemakerDraftPath(runID, '/decisions'), input);
}

export function requestChangemakerRevision(runID: string, input: DigestBinding & { notes: string; sources: 'same' | 'recollect' }) {
  return postChangemakerAction<{ version: ChangemakerDraftVersionView; decision: ChangemakerReviewDecision; run: Run }>(changemakerDraftPath(runID, '/revisions'), input);
}

export function publishChangemakerDraft(runID: string, input: { approval_id: string; draft_sha256: string }) {
  return postChangemakerAction<{ outcome: string; receipt: ChangemakerPublicationReceipt }>(changemakerDraftPath(runID, '/publications'), input);
}

export function selectedVersion(review: ChangemakerDraftReview): ChangemakerDraftVersionView | undefined {
  return review.versions.find((version) => version.version === review.selected_version);
}

// Review actions are open only on the current version of an unpublished thread.
export function reviewOpen(review: ChangemakerDraftReview, view: ChangemakerDraftVersionView) {
  return view.ready && !review.published_version && view.version === review.current_version && view.state !== 'publishing';
}

export function approvalPublishable(review: ChangemakerDraftReview, view: ChangemakerDraftVersionView, now: Date) {
  const decision = view.current_decision;
  return Boolean(
    review.publication.enabled &&
      reviewOpen(review, view) &&
      view.draft_generated &&
      decision &&
      decision.decision === 'approved' &&
      decision.draft_sha256 === view.draft_sha256 &&
      decision.evidence_sha256 === view.evidence_sha256 &&
      decision.adapter === review.publication.adapter &&
      decision.destination === (review.publication.destination ?? '') &&
      new Date(decision.expires_at).getTime() > now.getTime(),
  );
}
