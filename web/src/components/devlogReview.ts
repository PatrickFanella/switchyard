import { apiPath, APIError, arrayOrEmpty } from '../api';
import type { Artifact, Run } from '../types';
import { verifyArtifactDigest, type ArtifactIntegrity } from './artifactPreview';

export type DevlogReviewState =
  | 'not-ready'
  | 'delivery-disabled'
  | 'awaiting-approval'
  | 'approved'
  | 'rejected'
  | 'approval-expired'
  | 'delivering'
  | 'delivered'
  | 'delivery-failed';

export type DevlogReportSource = {
  mode: 'repository-evidence' | 'run-history';
  description: string;
  since?: string;
  until?: string;
  repositories: string[];
  complete?: boolean;
  items?: number;
  missing: string[];
};

export type DevlogReportExport = { format: 'markdown' | 'json'; name: string; path: string; sha256: string; byte_size: number };

export type DevlogDeliveryStatus = {
  enabled: boolean;
  adapter: string;
  destination?: string;
  reason?: string;
  approval_ttl_seconds: number;
  policy: string;
};

export type DevlogDeliveryApproval = {
  id: string;
  run_id: string;
  artifact_id: string;
  artifact_sha256: string;
  adapter: string;
  destination: string;
  decision: 'approved' | 'rejected';
  actor: string;
  reason: string;
  created_at: string;
  expires_at: string;
};

export type DevlogDeliveryReceipt = {
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

export type DevlogReportReview = {
  run: Run;
  state: DevlogReviewState;
  report: Artifact | null;
  evidence: Artifact | null;
  source: DevlogReportSource;
  exports: DevlogReportExport[];
  delivery: DevlogDeliveryStatus;
  idempotency_key?: string;
  current_approval?: DevlogDeliveryApproval;
  current_receipt?: DevlogDeliveryReceipt;
  approvals: DevlogDeliveryApproval[];
  receipts: DevlogDeliveryReceipt[];
  access?: { role: string; can_write: boolean };
};

export type DevlogActionResult<T> =
  | { ok: true; status: number; body: T }
  | { ok: false; status: number; code?: string; message: string; body?: T };

export const devlogStateLabels: Record<DevlogReviewState, string> = {
  'not-ready': 'Report not ready',
  'delivery-disabled': 'Delivery disabled (export only)',
  'awaiting-approval': 'Awaiting approval',
  approved: 'Approved, not delivered',
  rejected: 'Delivery rejected',
  'approval-expired': 'Approval expired',
  delivering: 'Delivery in progress',
  delivered: 'Delivered',
  'delivery-failed': 'Delivery failed; retry is safe',
};

function devlogReportPath(runID: string, suffix = '') {
  return `/api/devlog/reports/${encodeURIComponent(runID)}${suffix}`;
}

export function normalizeDevlogReview(review: DevlogReportReview): DevlogReportReview {
  return {
    ...review,
    report: review.report ?? null,
    evidence: review.evidence ?? null,
    exports: arrayOrEmpty(review.exports),
    approvals: arrayOrEmpty(review.approvals),
    receipts: arrayOrEmpty(review.receipts),
    source: { ...review.source, repositories: arrayOrEmpty(review.source?.repositories), missing: arrayOrEmpty(review.source?.missing) },
  };
}

export async function loadDevlogReview(runID: string): Promise<DevlogReportReview> {
  const response = await fetch(apiPath(devlogReportPath(runID)), { headers: { accept: 'application/json' } });
  if (!response.ok) {
    throw new APIError(response.status);
  }
  return normalizeDevlogReview((await response.json()) as DevlogReportReview);
}

async function postDevlogAction<T>(path: string, payload: unknown): Promise<DevlogActionResult<T>> {
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

export function decideDevlogReport(runID: string, input: { artifact_sha256: string; destination: string; decision: 'approved' | 'rejected'; reason: string }) {
  return postDevlogAction<{ approval: DevlogDeliveryApproval }>(devlogReportPath(runID, '/approvals'), input);
}

export function deliverDevlogReport(runID: string, input: { approval_id: string; artifact_sha256: string }) {
  return postDevlogAction<{ outcome: string; receipt: DevlogDeliveryReceipt }>(devlogReportPath(runID, '/deliveries'), input);
}

export type DevlogExportResult =
  | { ok: true; bytes: Uint8Array<ArrayBuffer>; integrity: ArtifactIntegrity; fileName: string }
  | { ok: false; message: string };

// fetchDevlogExport downloads the exact artifact bytes from the control plane
// and checks them against both the review digest and the response header.
export async function fetchDevlogExport(item: DevlogReportExport): Promise<DevlogExportResult> {
  let response: Response;
  try {
    response = await fetch(apiPath(item.path));
  } catch {
    return { ok: false, message: 'Export failed: the control plane is unreachable.' };
  }
  if (!response.ok) {
    return { ok: false, message: `Export failed: Switchyard API returned ${response.status}.` };
  }
  const header = (response.headers.get('X-Switchyard-Artifact-SHA256') ?? '').toLowerCase();
  if (header && header !== item.sha256) {
    return { ok: false, message: 'Export refused: the served digest does not match the reviewed report.' };
  }
  const bytes = new Uint8Array(await response.arrayBuffer());
  const integrity = await verifyArtifactDigest(bytes, item.sha256);
  if (integrity === 'mismatch') {
    return { ok: false, message: 'Export refused: the downloaded bytes do not match the reviewed SHA-256.' };
  }
  const extension = item.format === 'json' ? '.json' : '.md';
  return { ok: true, bytes, integrity, fileName: `devlog-${item.name.replace(/\.[a-z]+$/i, '')}-${item.sha256.slice(0, 12)}${extension}` };
}

export function approvalUsable(review: DevlogReportReview, now: Date) {
  const approval = review.current_approval;
  return Boolean(
    review.delivery.enabled &&
      review.report &&
      approval &&
      approval.decision === 'approved' &&
      approval.artifact_sha256 === review.report.sha256 &&
      new Date(approval.expires_at).getTime() > now.getTime() &&
      review.current_receipt?.status !== 'delivered' &&
      review.current_receipt?.status !== 'sending',
  );
}
