import { useCallback, useEffect, useState, type ReactNode } from 'react';
import { messageFrom } from '../api';
import { ArtifactPreview } from './ArtifactPreview';
import { sanitizeLinkHref } from './artifactPreview';
import {
  approvalPublishable,
  bindingFor,
  changemakerSourceModeLabels,
  changemakerStateLabels,
  decideChangemakerDraft,
  loadChangemakerReview,
  publishChangemakerDraft,
  requestChangemakerRevision,
  reviewOpen,
  type ChangemakerDraftReview,
  type ChangemakerDraftVersionView,
  type ChangemakerPublicationReceipt,
} from './changemakerReview';
import { formatTime, HudEmptyState, HudErrorState, HudField, HudLabel, HudLoadingState, HudPanel, HudPanelStrong, HudSection, statusClasses } from './hud';

export type ChangemakerReviewPanelProps = {
  runID: string;
  // Changes when the run record changes so the review reloads after it finishes.
  runStatus?: string;
  now?: () => Date;
  // Called with the run id of a new revision so the cockpit can open it.
  onRevisionCreated?: (runID: string) => void;
};

const buttonClass =
  'hud-chip border-[hsl(var(--border))] bg-[hsl(var(--panel-soft))] px-3 py-2 text-[0.62rem] text-[hsl(var(--muted))] transition hover:border-[hsl(var(--signal))] hover:text-[hsl(var(--signal))] disabled:cursor-not-allowed disabled:opacity-50';
const approveClass =
  'hud-chip border-[hsl(var(--confirm))] bg-[hsl(var(--confirm-soft))] px-3 py-2 text-[0.62rem] text-[hsl(var(--confirm))] transition disabled:cursor-not-allowed disabled:opacity-50';
const rejectClass =
  'hud-chip border-[hsl(var(--danger))] bg-[hsl(var(--danger-soft))] px-3 py-2 text-[0.62rem] text-[hsl(var(--danger))] transition disabled:cursor-not-allowed disabled:opacity-50';

const defaultNow = () => new Date();

function shortDigest(value?: string) {
  return value ? `${value.slice(0, 12)}…` : '—';
}

function Notice({ tone, children }: { tone: 'danger' | 'muted'; children: ReactNode }) {
  const toneClass = tone === 'danger' ? 'border-[hsl(var(--danger))] text-[hsl(var(--danger))]' : 'border-[hsl(var(--border-faint))] text-[hsl(var(--muted))]';
  return (
    <p className={`mt-3 border-l-2 pl-3 text-xs leading-5 ${toneClass}`} role="status">
      {children}
    </p>
  );
}

function ReceiptRow({ receipt }: { receipt: ChangemakerPublicationReceipt }) {
  const href = receipt.external_url ? sanitizeLinkHref(receipt.external_url) : null;
  return (
    <HudPanel className="bg-[hsl(var(--panel-soft))] p-3">
      <div className="flex flex-wrap items-center justify-between gap-2" data-receipt={receipt.id}>
        <span className={`hud-chip px-2 py-1 text-[0.58rem] ${statusClasses(receipt.status === 'delivered' ? 'succeeded' : receipt.status === 'failed' ? 'failed' : 'running')}`}>
          {receipt.status === 'delivered' ? 'published' : receipt.status}
        </span>
        <span className="text-xs text-[hsl(var(--muted))]">
          {receipt.attempts} attempt{receipt.attempts === 1 ? '' : 's'}
          {receipt.reconciled ? ' · reconciled with destination' : ''}
        </span>
      </div>
      <dl className="mt-2 grid gap-2 text-xs sm:grid-cols-2">
        <HudField label="Destination" value={`${receipt.adapter} → ${receipt.destination}`} />
        <HudField label="Draft sha256" value={<code className="break-all">{receipt.artifact_sha256}</code>} />
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

function VersionButton({ view, active, onSelect }: { view: ChangemakerDraftVersionView; active: boolean; onSelect: () => void }) {
  return (
    <button
      aria-pressed={active}
      className={`hud-panel w-full px-3 py-2 text-left text-xs transition ${active ? 'border-[hsl(var(--signal))]' : 'border-[hsl(var(--border-faint))] hover:border-[hsl(var(--signal))]'}`}
      data-testid={`changemaker-version-${view.version}`}
      onClick={onSelect}
      type="button"
    >
      <span className="flex flex-wrap items-center justify-between gap-2">
        <span className="font-semibold text-[hsl(var(--ink))]">Version {view.version}</span>
        <span className="hud-chip px-2 py-0.5 text-[0.55rem]" data-state={view.state}>
          {changemakerStateLabels[view.state] ?? view.state}
        </span>
      </span>
      <span className="mt-1 block text-[hsl(var(--muted))]">
        {changemakerSourceModeLabels[view.source_mode] ?? view.source_mode} · run {view.run_status} · draft {shortDigest(view.draft_sha256)}
      </span>
      {view.revision_notes ? <span className="mt-1 block text-[hsl(var(--muted))]">Notes: {view.revision_notes}</span> : null}
    </button>
  );
}

export function ChangemakerReviewPanel({ runID, runStatus, now = defaultNow, onRevisionCreated }: ChangemakerReviewPanelProps) {
  const [review, setReview] = useState<ChangemakerDraftReview | null>(null);
  const [viewed, setViewed] = useState<number | null>(null);
  const [loadError, setLoadError] = useState('');
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState('');
  const [notes, setNotes] = useState('');
  const [sources, setSources] = useState<'same' | 'recollect'>('same');
  const [notice, setNotice] = useState<{ tone: 'danger' | 'muted'; text: string } | null>(null);

  const refresh = useCallback(async () => {
    try {
      const next = await loadChangemakerReview(runID);
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
    setViewed(null);
    setNotice(null);
    void refresh();
  }, [refresh, runStatus]);

  const view = review ? (review.versions.find((candidate) => candidate.version === (viewed ?? review.selected_version)) ?? review.versions[review.versions.length - 1]) : undefined;

  const decide = async (decision: 'approved' | 'rejected') => {
    if (!view) return;
    setBusy(`decide:${decision}`);
    try {
      const result = await decideChangemakerDraft(view.run_id, { ...bindingFor(view), decision, notes });
      setNotice(
        result.ok
          ? { tone: 'muted', text: decision === 'approved' ? `Approval recorded for version ${view.version}'s exact draft and source evidence.` : `Rejection recorded for version ${view.version}.` }
          : { tone: 'danger', text: `Decision refused (${result.code ?? result.status}): ${result.message}` },
      );
      if (result.ok) setNotes('');
    } catch (error) {
      setNotice({ tone: 'danger', text: messageFrom(error) });
    } finally {
      setBusy('');
      await refresh();
    }
  };

  const revise = async () => {
    if (!view) return;
    setBusy('revise');
    try {
      const result = await requestChangemakerRevision(view.run_id, { ...bindingFor(view), notes, sources });
      if (result.ok) {
        setNotes('');
        setViewed(result.body.version.version);
        setNotice({ tone: 'muted', text: `Revision requested: version ${result.body.version.version} is run ${result.body.run.id} (${result.body.run.status}). Earlier versions are kept.` });
        onRevisionCreated?.(result.body.run.id);
      } else {
        setNotice({ tone: 'danger', text: `Revision refused (${result.code ?? result.status}): ${result.message}` });
      }
    } catch (error) {
      setNotice({ tone: 'danger', text: messageFrom(error) });
    } finally {
      setBusy('');
      await refresh();
    }
  };

  const publish = async () => {
    if (!view?.current_decision) return;
    setBusy('publish');
    try {
      const result = await publishChangemakerDraft(view.run_id, { approval_id: view.current_decision.id, draft_sha256: view.draft_sha256 ?? '' });
      if (result.ok) {
        const outcome = result.body.outcome;
        setNotice({
          tone: 'muted',
          text:
            outcome === 'published'
              ? `Published version ${view.version} to ${result.body.receipt.destination}.`
              : outcome === 'reconciled'
                ? 'The destination already had this draft; the receipt was reconciled without posting again.'
                : 'This draft was already published; nothing was sent.',
        });
      } else if (result.body && 'receipt' in result.body) {
        setNotice({ tone: 'danger', text: `Publication failed: ${result.body.receipt.last_error ?? 'unknown error'}. Retrying is safe; it checks the destination before posting.` });
      } else {
        setNotice({ tone: 'danger', text: `Publication refused (${result.code ?? result.status}): ${result.message}` });
      }
    } catch (error) {
      setNotice({ tone: 'danger', text: messageFrom(error) });
    } finally {
      setBusy('');
      await refresh();
    }
  };

  const body = () => {
    if (loading && !review) return <HudLoadingState>Loading Changemaker draft review…</HudLoadingState>;
    if (!review || !view) return <HudErrorState>Changemaker review unavailable: {loadError || 'unknown error'}</HudErrorState>;
    const { publication } = review;
    const readOnly = review.access?.can_write === false;
    const open = reviewOpen(review, view);
    const canDecide = !readOnly && open && !busy;
    const canApprove = canDecide && view.draft_generated;
    const canRevise = canDecide && notes.trim().length > 0 && review.latest_version < publication.max_draft_versions;
    const canPublish = !readOnly && approvalPublishable(review, view, now()) && !busy;
    const viewReceipts = review.receipts.filter((receipt) => receipt.run_id === view.run_id);
    return (
      <div className="grid gap-4 xl:grid-cols-[1.1fr_0.9fr]">
        <div className="space-y-4">
          <HudPanelStrong className="p-4">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <HudLabel className="text-[hsl(var(--faint))]">Revision history</HudLabel>
              <span className="text-xs text-[hsl(var(--muted))]" data-testid="changemaker-thread-summary">
                {review.versions.length} version{review.versions.length === 1 ? '' : 's'} · current {review.current_version}
                {review.published_version ? ` · published version ${review.published_version}` : ''}
              </span>
            </div>
            <div className="mt-3 grid gap-2">
              {review.versions.map((candidate) => (
                <VersionButton active={candidate.version === view.version} key={candidate.version} onSelect={() => setViewed(candidate.version)} view={candidate} />
              ))}
            </div>
          </HudPanelStrong>

          <HudPanelStrong className="p-4">
            <HudLabel className="text-[hsl(var(--faint))]">Version {view.version} draft</HudLabel>
            <div className="mt-3 grid gap-3 sm:grid-cols-2">
              <HudField label="Draft sha256" value={<code className="break-all" data-testid="changemaker-draft-sha">{view.draft_sha256 ?? '—'}</code>} />
              <HudField label="Source evidence sha256" value={<code className="break-all" data-testid="changemaker-evidence-sha">{view.evidence_sha256 ?? '—'}</code>} />
              <HudField label="Collection" value={view.grounding_status ?? '—'} />
              <HudField label="Sources" value={`${changemakerSourceModeLabels[view.source_mode] ?? view.source_mode} (run ${view.sources_run_id})`} />
              <HudField label="Cited source IDs" value={view.cited_sources || '—'} />
              <HudField label="Unmatched citations" value={view.unknown_citations || 'none'} />
            </div>
            {!view.ready ? (
              <p className="mt-3 text-xs text-[hsl(var(--warn))]" data-testid="changemaker-not-ready">
                Not ready for review: {view.not_ready_reason || 'the run has not finished'}.
              </p>
            ) : null}
            {view.draft ? <ArtifactPreview artifact={view.draft} initiallyExpanded /> : <HudEmptyState className="mt-3">This version has no draft artifact yet.</HudEmptyState>}
          </HudPanelStrong>
        </div>

        <div className="space-y-4">
          <HudPanelStrong className="p-4">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <HudLabel className="text-[hsl(var(--faint))]">Review</HudLabel>
              <span className="hud-chip px-2 py-1 text-[0.58rem]" data-state={view.state} data-testid="changemaker-review-state">
                {changemakerStateLabels[view.state] ?? view.state}
              </span>
            </div>
            <p className="mt-3 text-xs leading-5 text-[hsl(var(--muted))]">{publication.policy}</p>
            {readOnly ? (
              <p className="mt-2 text-xs text-[hsl(var(--warn))]" data-testid="changemaker-read-only">
                Read-only access: an admin must decide, request revisions, and publish.
              </p>
            ) : null}
            {view.current_decision ? (
              <p className="mt-2 text-xs text-[hsl(var(--ink))]" data-testid="changemaker-current-decision">
                Latest decision on these digests: {view.current_decision.decision} by {view.current_decision.actor} at {formatTime(view.current_decision.created_at)}
                {view.current_decision.decision === 'approved' ? `; expires ${formatTime(view.current_decision.expires_at)}` : ''}.
              </p>
            ) : null}
            <label className="mt-3 block">
              <span className="hud-label text-[hsl(var(--signal))]">Notes (required for a revision)</span>
              <textarea
                className="mt-1 w-full border border-[hsl(var(--border))] bg-[hsl(var(--void))] px-2 py-1 text-xs text-[hsl(var(--ink))]"
                maxLength={2000}
                onChange={(event) => setNotes(event.target.value)}
                rows={3}
                value={notes}
              />
            </label>
            <fieldset className="mt-2 flex flex-wrap gap-3 text-xs text-[hsl(var(--muted))]">
              <legend className="sr-only">Sources for the revision</legend>
              <label>
                <input checked={sources === 'same'} name="changemaker-sources" onChange={() => setSources('same')} type="radio" /> Same sources
              </label>
              <label>
                <input checked={sources === 'recollect'} name="changemaker-sources" onChange={() => setSources('recollect')} type="radio" /> Re-collect the same window
              </label>
            </fieldset>
            <div className="mt-3 flex flex-wrap gap-2">
              <button className={approveClass} disabled={!canApprove} onClick={() => void decide('approved')} type="button">
                {busy === 'decide:approved' ? 'Recording…' : `Approve version ${view.version}`}
              </button>
              <button className={rejectClass} disabled={!canDecide} onClick={() => void decide('rejected')} type="button">
                {busy === 'decide:rejected' ? 'Recording…' : 'Reject'}
              </button>
              <button className={buttonClass} disabled={!canRevise} onClick={() => void revise()} type="button">
                {busy === 'revise' ? 'Requesting…' : 'Request revision'}
              </button>
            </div>
          </HudPanelStrong>

          <HudPanelStrong className="p-4">
            <HudLabel className="text-[hsl(var(--faint))]">Publication</HudLabel>
            {!publication.enabled ? (
              <p className="mt-3 text-xs text-[hsl(var(--muted))]" data-testid="changemaker-publication-disabled">
                Publication is disabled: {publication.reason || 'not configured'}. Review decisions are still recorded.
              </p>
            ) : (
              <>
                <div className="mt-3 grid gap-3 sm:grid-cols-2">
                  <HudField label="Adapter" value={publication.adapter} />
                  <HudField label="Destination" value={publication.destination ?? '—'} />
                </div>
                <div className="mt-3 flex flex-wrap gap-2">
                  <button className={buttonClass} disabled={!canPublish} onClick={() => void publish()} type="button">
                    {busy === 'publish' ? 'Publishing…' : view.state === 'publication-failed' ? 'Retry publication' : `Publish version ${view.version} to ${publication.destination}`}
                  </button>
                </div>
              </>
            )}
            {notice ? <Notice tone={notice.tone}>{notice.text}</Notice> : null}
            {loadError && review ? <Notice tone="danger">Refresh failed: {loadError}</Notice> : null}
            <div className="mt-3 grid gap-3">
              {viewReceipts.map((receipt) => (
                <ReceiptRow key={receipt.id} receipt={receipt} />
              ))}
              {viewReceipts.length === 0 ? <HudEmptyState>No publication attempts for this version.</HudEmptyState> : null}
            </div>
          </HudPanelStrong>

          <HudPanelStrong className="p-4">
            <HudLabel className="text-[hsl(var(--faint))]">Decisions</HudLabel>
            {review.decisions.length === 0 ? (
              <HudEmptyState className="mt-3">No decisions recorded.</HudEmptyState>
            ) : (
              <ul className="mt-3 space-y-1 text-xs text-[hsl(var(--muted))]" data-testid="changemaker-decisions">
                {review.decisions.map((decision) => (
                  <li key={decision.id}>
                    {formatTime(decision.created_at)} · v{decision.version} {decision.decision} by {decision.actor} · draft {shortDigest(decision.draft_sha256)} · evidence {shortDigest(decision.evidence_sha256)}
                    {decision.notes ? ` · ${decision.notes}` : ''}
                  </li>
                ))}
              </ul>
            )}
          </HudPanelStrong>
        </div>
      </div>
    );
  };

  return (
    <HudSection kicker="Changemaker review" title="Changemaker draft review" note="Review the grounded draft against its source evidence, request revisions, and approve before any publication. Runs never publish by themselves.">
      <div data-testid="changemaker-review-panel">{body()}</div>
    </HudSection>
  );
}
