import { useCallback, useEffect, useState, type ReactNode } from 'react';
import { messageFrom } from '../api';
import { ArtifactPreview } from './ArtifactPreview';
import { downloadMediaType, formatByteSize, sanitizeLinkHref } from './artifactPreview';
import {
  approvalUsable,
  decideDevlogReport,
  deliverDevlogReport,
  devlogStateLabels,
  fetchDevlogExport,
  loadDevlogReview,
  type DevlogDeliveryReceipt,
  type DevlogReportExport,
  type DevlogReportReview,
} from './devlogReview';
import { formatTime, HudEmptyState, HudErrorState, HudField, HudLabel, HudLoadingState, HudPanel, HudPanelStrong, HudSection, statusClasses } from './hud';

export type DevlogReviewPanelProps = {
  runID: string;
  // Changes when the run record changes so the review reloads after it finishes.
  runStatus?: string;
  now?: () => Date;
};

const buttonClass =
  'hud-chip border-[hsl(var(--border))] bg-[hsl(var(--panel-soft))] px-3 py-2 text-[0.62rem] text-[hsl(var(--muted))] transition hover:border-[hsl(var(--signal))] hover:text-[hsl(var(--signal))] disabled:cursor-not-allowed disabled:opacity-50';
const approveClass =
  'hud-chip border-[hsl(var(--confirm))] bg-[hsl(var(--confirm-soft))] px-3 py-2 text-[0.62rem] text-[hsl(var(--confirm))] transition disabled:cursor-not-allowed disabled:opacity-50';
const rejectClass =
  'hud-chip border-[hsl(var(--danger))] bg-[hsl(var(--danger-soft))] px-3 py-2 text-[0.62rem] text-[hsl(var(--danger))] transition disabled:cursor-not-allowed disabled:opacity-50';

const defaultNow = () => new Date();

function saveBytes(bytes: Uint8Array<ArrayBuffer>, fileName: string, format: DevlogReportExport['format']) {
  if (typeof URL.createObjectURL !== 'function' || typeof document === 'undefined') {
    return false;
  }
  const blob = new Blob([bytes], { type: downloadMediaType(format === 'json' ? 'json' : 'markdown') });
  const objectURL = URL.createObjectURL(blob);
  const anchor = document.createElement('a');
  anchor.href = objectURL;
  anchor.download = fileName;
  anchor.rel = 'noopener';
  anchor.style.display = 'none';
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  setTimeout(() => URL.revokeObjectURL(objectURL), 0);
  return true;
}

function Notice({ tone, children }: { tone: 'danger' | 'muted'; children: ReactNode }) {
  const toneClass = tone === 'danger' ? 'border-[hsl(var(--danger))] text-[hsl(var(--danger))]' : 'border-[hsl(var(--border-faint))] text-[hsl(var(--muted))]';
  return (
    <p className={`mt-3 border-l-2 pl-3 text-xs leading-5 ${toneClass}`} role="status">
      {children}
    </p>
  );
}

function ReceiptRow({ receipt }: { receipt: DevlogDeliveryReceipt }) {
  const href = receipt.external_url ? sanitizeLinkHref(receipt.external_url) : null;
  return (
    <HudPanel className="bg-[hsl(var(--panel-soft))] p-3" >
      <div className="flex flex-wrap items-center justify-between gap-2" data-receipt={receipt.id}>
        <span className={`hud-chip px-2 py-1 text-[0.58rem] ${statusClasses(receipt.status === 'delivered' ? 'succeeded' : receipt.status === 'failed' ? 'failed' : 'running')}`}>{receipt.status}</span>
        <span className="text-xs text-[hsl(var(--muted))]">
          {receipt.attempts} attempt{receipt.attempts === 1 ? '' : 's'}
          {receipt.reconciled ? ' · reconciled with destination' : ''}
        </span>
      </div>
      <dl className="mt-2 grid gap-2 text-xs sm:grid-cols-2">
        <HudField label="Destination" value={`${receipt.adapter} → ${receipt.destination}`} />
        <HudField label="Report sha256" value={<code className="break-all">{receipt.artifact_sha256}</code>} />
        <HudField label="Requested by" value={receipt.requested_by} />
        <HudField label="Updated" value={formatTime(receipt.updated_at)} />
        {receipt.external_url ? (
          <HudField
            label="Published at"
            value={
              href ? (
                <a className="text-[hsl(var(--signal))] underline" href={href} rel="noreferrer noopener" target="_blank">
                  {receipt.external_url}
                </a>
              ) : (
                receipt.external_url
              )
            }
          />
        ) : null}
        {receipt.last_error ? <HudField className="sm:col-span-2" label="Last error" value={receipt.last_error} /> : null}
      </dl>
    </HudPanel>
  );
}

export function DevlogReviewPanel({ runID, runStatus, now = defaultNow }: DevlogReviewPanelProps) {
  const [review, setReview] = useState<DevlogReportReview | null>(null);
  const [loadError, setLoadError] = useState('');
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState('');
  const [reason, setReason] = useState('');
  const [notice, setNotice] = useState<{ tone: 'danger' | 'muted'; text: string } | null>(null);

  const refresh = useCallback(async () => {
    try {
      const next = await loadDevlogReview(runID);
      setReview(next);
      setLoadError('');
    } catch (error) {
      setLoadError(messageFrom(error));
    } finally {
      setLoading(false);
    }
  }, [runID]);

  useEffect(() => {
    setLoading(true);
    setReview(null);
    setNotice(null);
    void refresh();
  }, [refresh, runStatus]);

  const exportReport = async (item: DevlogReportExport) => {
    setBusy(`export:${item.format}`);
    const result = await fetchDevlogExport(item);
    setBusy('');
    if (!result.ok) {
      setNotice({ tone: 'danger', text: result.message });
      return;
    }
    if (!saveBytes(result.bytes, result.fileName, item.format)) {
      setNotice({ tone: 'danger', text: 'Download unavailable: this browser cannot create local file links.' });
      return;
    }
    const check = result.integrity === 'verified' ? 'sha256 verified' : `digest ${result.integrity}`;
    setNotice({ tone: 'muted', text: `Exported ${result.fileName} (${formatByteSize(result.bytes.length)}, ${check}). Nothing was sent to any destination.` });
  };

  const decide = async (decision: 'approved' | 'rejected') => {
    if (!review?.report || !review.delivery.destination) return;
    setBusy(`decide:${decision}`);
    try {
      const result = await decideDevlogReport(runID, { artifact_sha256: review.report.sha256 ?? '', destination: review.delivery.destination, decision, reason });
      setNotice(
        result.ok
          ? { tone: 'muted', text: decision === 'approved' ? 'Approval recorded for this exact report version.' : 'Rejection recorded; this report version will not be delivered.' }
          : { tone: 'danger', text: `Decision refused (${result.code ?? result.status}): ${result.message}` },
      );
      if (result.ok) setReason('');
    } catch (error) {
      setNotice({ tone: 'danger', text: messageFrom(error) });
    } finally {
      setBusy('');
      await refresh();
    }
  };

  const deliver = async () => {
    if (!review?.report || !review.current_approval) return;
    setBusy('deliver');
    try {
      const result = await deliverDevlogReport(runID, { approval_id: review.current_approval.id, artifact_sha256: review.report.sha256 ?? '' });
      if (result.ok) {
        const outcome = result.body.outcome;
        setNotice({ tone: 'muted', text: outcome === 'delivered' ? `Delivered to ${result.body.receipt.destination}.` : outcome === 'reconciled' ? 'The destination already had this report; the receipt was reconciled without posting again.' : 'This report was already delivered; nothing was sent.' });
      } else if (result.body && 'receipt' in result.body) {
        setNotice({ tone: 'danger', text: `Delivery failed: ${result.body.receipt.last_error ?? 'unknown error'}. Retrying is safe; it checks the destination before posting.` });
      } else {
        setNotice({ tone: 'danger', text: `Delivery refused (${result.code ?? result.status}): ${result.message}` });
      }
    } catch (error) {
      setNotice({ tone: 'danger', text: messageFrom(error) });
    } finally {
      setBusy('');
      await refresh();
    }
  };

  const body = () => {
    if (loading && !review) return <HudLoadingState>Loading Devlog report review…</HudLoadingState>;
    if (!review) return <HudErrorState>Devlog review unavailable: {loadError || 'unknown error'}</HudErrorState>;
    const { source, delivery } = review;
    const canDecide = delivery.enabled && review.state !== 'not-ready' && review.state !== 'delivered' && review.state !== 'delivering' && Boolean(review.report) && !busy;
    const canDeliver = approvalUsable(review, now()) && !busy;
    return (
      <div className="grid gap-4 xl:grid-cols-[1.1fr_0.9fr]">
        <div className="space-y-4">
          <HudPanelStrong className="p-4" >
            <div className="flex flex-wrap items-center justify-between gap-3">
              <HudLabel className="text-[hsl(var(--faint))]">Source window</HudLabel>
              <span className="hud-chip px-2 py-1 text-[0.58rem]" data-testid="devlog-source-mode">{source.mode}</span>
            </div>
            <p className="mt-3 text-sm text-[hsl(var(--ink))]" data-testid="devlog-source-window">
              {source.since && source.until ? `${source.since} → ${source.until}` : source.until ? `Run history as of ${source.until}` : 'Window not recorded'}
            </p>
            <p className="mt-1 text-xs text-[hsl(var(--muted))]">{source.description}</p>
            <div className="mt-3 grid gap-3 sm:grid-cols-3">
              <HudField label="Repositories" value={source.repositories.join(', ') || '—'} />
              <HudField label="Evidence items" value={source.items ?? '—'} />
              <HudField label="Complete" value={source.complete === undefined ? '—' : source.complete ? 'yes' : 'no'} />
            </div>
            {source.missing.length > 0 ? (
              <ul className="mt-3 list-disc pl-5 text-xs text-[hsl(var(--warn))]" data-testid="devlog-source-missing">
                {source.missing.map((item) => (
                  <li key={item}>{item}</li>
                ))}
              </ul>
            ) : null}
          </HudPanelStrong>

          <HudPanelStrong className="p-4">
            <HudLabel className="text-[hsl(var(--faint))]">Report preview</HudLabel>
            {review.report ? <ArtifactPreview artifact={review.report} initiallyExpanded /> : <HudEmptyState className="mt-3">This run has no devlog.md report yet.</HudEmptyState>}
          </HudPanelStrong>
        </div>

        <div className="space-y-4">
          <HudPanelStrong className="p-4">
            <HudLabel className="text-[hsl(var(--faint))]">Local export</HudLabel>
            <p className="mt-2 text-xs text-[hsl(var(--muted))]">Downloads the exact stored bytes and checks their SHA-256. Export never contacts a destination.</p>
            <div className="mt-3 flex flex-wrap gap-2">
              {review.exports.length === 0 ? <HudEmptyState>No exportable artifacts.</HudEmptyState> : null}
              {review.exports.map((item) => (
                <button className={buttonClass} disabled={Boolean(busy)} key={item.format} onClick={() => void exportReport(item)} type="button">
                  {busy === `export:${item.format}` ? 'Exporting…' : `Export ${item.format === 'json' ? 'JSON' : 'Markdown'} (${item.name})`}
                </button>
              ))}
            </div>
          </HudPanelStrong>

          <HudPanelStrong className="p-4">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <HudLabel className="text-[hsl(var(--faint))]">Delivery</HudLabel>
              <span className="hud-chip px-2 py-1 text-[0.58rem]" data-state={review.state} data-testid="devlog-review-state">
                {devlogStateLabels[review.state] ?? review.state}
              </span>
            </div>
            {!delivery.enabled ? (
              <p className="mt-3 text-xs text-[hsl(var(--muted))]" data-testid="devlog-delivery-disabled">
                Delivery is disabled: {delivery.reason || 'not configured'}. Export remains available.
              </p>
            ) : (
              <>
                <div className="mt-3 grid gap-3 sm:grid-cols-2">
                  <HudField label="Adapter" value={delivery.adapter} />
                  <HudField label="Destination" value={delivery.destination ?? '—'} />
                  <HudField className="sm:col-span-2" label="Report sha256 to approve" value={<code className="break-all">{review.report?.sha256 ?? '—'}</code>} />
                </div>
                <p className="mt-3 text-xs leading-5 text-[hsl(var(--muted))]">{delivery.policy}</p>
                {review.current_approval ? (
                  <p className="mt-2 text-xs text-[hsl(var(--ink))]" data-testid="devlog-current-decision">
                    Latest decision: {review.current_approval.decision} by {review.current_approval.actor} at {formatTime(review.current_approval.created_at)}; expires {formatTime(review.current_approval.expires_at)}.
                  </p>
                ) : null}
                <label className="mt-3 block">
                  <span className="hud-label text-[hsl(var(--signal))]">Decision reason</span>
                  <input
                    className="mt-1 w-full border border-[hsl(var(--border))] bg-[hsl(var(--void))] px-2 py-1 text-xs text-[hsl(var(--ink))]"
                    maxLength={1000}
                    onChange={(event) => setReason(event.target.value)}
                    value={reason}
                  />
                </label>
                <div className="mt-3 flex flex-wrap gap-2">
                  <button className={approveClass} disabled={!canDecide} onClick={() => void decide('approved')} type="button">
                    {busy === 'decide:approved' ? 'Recording…' : 'Approve this version'}
                  </button>
                  <button className={rejectClass} disabled={!canDecide} onClick={() => void decide('rejected')} type="button">
                    {busy === 'decide:rejected' ? 'Recording…' : 'Reject'}
                  </button>
                  <button className={buttonClass} disabled={!canDeliver} onClick={() => void deliver()} type="button">
                    {busy === 'deliver' ? 'Delivering…' : review.state === 'delivery-failed' ? 'Retry delivery' : `Deliver to ${delivery.destination}`}
                  </button>
                </div>
              </>
            )}
            {notice ? <Notice tone={notice.tone}>{notice.text}</Notice> : null}
            {loadError && review ? <Notice tone="danger">Refresh failed: {loadError}</Notice> : null}
          </HudPanelStrong>

          <HudPanelStrong className="p-4">
            <HudLabel className="text-[hsl(var(--faint))]">Delivery receipts</HudLabel>
            <div className="mt-3 grid gap-3">
              {review.current_receipt && !review.receipts.some((receipt) => receipt.id === review.current_receipt?.id) ? <ReceiptRow receipt={review.current_receipt} /> : null}
              {review.receipts.map((receipt) => (
                <ReceiptRow key={receipt.id} receipt={receipt} />
              ))}
              {review.receipts.length === 0 && !review.current_receipt ? <HudEmptyState>No delivery attempts recorded.</HudEmptyState> : null}
            </div>
            {review.approvals.length > 0 ? (
              <ul className="mt-3 space-y-1 text-xs text-[hsl(var(--muted))]" data-testid="devlog-approvals">
                {review.approvals.map((approval) => (
                  <li key={approval.id}>
                    {formatTime(approval.created_at)} · {approval.decision} by {approval.actor} · sha256 {approval.artifact_sha256.slice(0, 12)}…{approval.reason ? ` · ${approval.reason}` : ''}
                  </li>
                ))}
              </ul>
            ) : null}
          </HudPanelStrong>
        </div>
      </div>
    );
  };

  return (
    <HudSection kicker="Devlog review" title="Devlog report review" note="Preview the report and its source window, export the exact bytes, and approve before any delivery.">
      <div data-testid="devlog-review-panel">{body()}</div>
    </HudSection>
  );
}
