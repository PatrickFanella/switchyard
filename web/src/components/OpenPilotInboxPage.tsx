import { useCallback, useEffect, useState } from 'react';
import { messageFrom, resumeOpenPilotState } from '../api';
import {
  dispatchOpenPilotIssue,
  emptyInboxLink,
  formatInboxHash,
  loadOpenPilotInbox,
  parseInboxHash,
  safeExternalURL,
  type InboxEligibility,
  type InboxLink,
  type InboxPickup,
  type OpenPilotInboxIssue,
  type OpenPilotInboxReport,
} from '../openPilotInbox';
import { formatTime, HudEmptyState, HudErrorState, HudField, HudLabel, HudLoadingState, HudPanel, HudPanelStrong, HudSection, statusClasses } from './hud';

type OpenPilotInboxPageProps = {
  onOpenRun: (runID: string) => void;
};

const phases = ['requested', 'validated', 'branch_prepared', 'agent_completed', 'verified', 'committed', 'pushed', 'pr_created', 'ai_reviewed', 'ci_passed', 'merge_ready', 'merged', 'issue_closed', 'failed', 'paused'];

const upstreamCopy: Record<Exclude<OpenPilotInboxReport['upstream'], 'available'>, string> = {
  partial: 'Gitea listing failed for some repositories. The queue may be incomplete; durable claims are still shown.',
  unavailable: 'Gitea listing failed for every admitted repository. This is not an empty queue; durable claims are still shown.',
  not_queried: 'Gitea was not queried: no repository is admitted or no issue reader is configured.',
};

function currentLink() {
  return typeof window === 'undefined' ? { ...emptyInboxLink } : parseInboxHash(window.location.hash);
}

function eligibilityChip(issue: OpenPilotInboxIssue) {
  if (issue.eligible) return { label: 'eligible', classes: statusClasses('succeeded') };
  if (issue.claim) return { label: issue.claim.active_run ? 'running' : 'claimed', classes: statusClasses(issue.claim.active_run ? 'running' : 'queued') };
  return { label: 'ineligible', classes: statusClasses('failed') };
}

function ExternalLink({ href, children }: { href?: string; children: string }) {
  const safe = safeExternalURL(href);
  if (!safe) return null;
  return (
    <a className="hud-chip border-[hsl(var(--signal))] px-3 py-2 text-[0.62rem] text-[hsl(var(--signal))]" href={safe} rel="noreferrer noopener" target="_blank">
      {children}
    </a>
  );
}

function IssueRow({ issue, selected, onSelect }: { issue: OpenPilotInboxIssue; selected: boolean; onSelect: (key: string) => void }) {
  const chip = eligibilityChip(issue);
  return (
    <button
      aria-pressed={selected}
      className={`hud-panel w-full p-3 text-left transition hover:border-[hsl(var(--signal))] ${selected ? 'border-[hsl(var(--signal))] bg-[hsl(var(--signal-soft))]' : 'bg-[hsl(var(--panel-soft))]'}`}
      data-inbox-issue={issue.key}
      onClick={() => onSelect(issue.key)}
      type="button"
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <HudLabel className="text-[hsl(var(--faint))]">{issue.key}</HudLabel>
          <div className="mt-2 text-sm font-semibold text-[hsl(var(--ink))]">{issue.title || (issue.source === 'state' ? 'Durable claim (not listed by Gitea)' : 'Untitled issue')}</div>
        </div>
        <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${chip.classes}`}>{chip.label}</span>
      </div>
      <p className="mt-3 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">
        pickup: {issue.pickup}
        {issue.claim ? ` · phase: ${issue.claim.phase}` : ''}
        {issue.reason_codes.length > 0 ? ` · ${issue.reason_codes.join(', ')}` : ''}
      </p>
    </button>
  );
}

function IssueDetail({ issue, busy, onDispatch, onResume, onOpenRun }: { issue: OpenPilotInboxIssue | null; busy: boolean; onDispatch: (issue: OpenPilotInboxIssue) => void; onResume: (issue: OpenPilotInboxIssue) => void; onOpenRun: (runID: string) => void }) {
  if (!issue) {
    return <HudEmptyState>Select an issue to inspect eligibility, the existing claim, and evidence links.</HudEmptyState>;
  }
  const claim = issue.claim;
  return (
    <HudPanelStrong className="p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <HudLabel className="text-[hsl(var(--faint))]">Selected issue</HudLabel>
          <h3 className="mt-2 break-all text-xl font-semibold text-[hsl(var(--ink))]">{issue.key}</h3>
          {issue.title ? <p className="mt-2 text-sm text-[hsl(var(--muted))]">{issue.title}</p> : null}
        </div>
        <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${eligibilityChip(issue).classes}`}>{eligibilityChip(issue).label}</span>
      </div>

      <div className="mt-4 grid gap-3 sm:grid-cols-2">
        <HudField label="Pickup" value={issue.pickup} />
        <HudField label="Bot assigned" value={issue.bot_assigned ? 'yes' : 'no'} />
        <HudField label="Labels" value={issue.labels.join(', ') || '—'} />
        <HudField label="Missing labels" value={issue.missing_labels.join(', ') || '—'} />
        <HudField label="Specification" value={issue.spec_ready ? 'ready' : issue.spec_required ? 'missing (required)' : 'missing'} />
        <HudField label="Test command" value={issue.test_command || '—'} />
        <HudField label="Executor" value={`${issue.repository_id}/${issue.executor_generation}`} />
        <HudField label="Source" value={issue.source === 'upstream' ? 'Gitea listing' : 'durable claim only'} />
      </div>

      {claim ? (
        <div className="mt-4 grid gap-3 sm:grid-cols-2" data-inbox-claim="">
          <HudField label="Lifecycle phase" value={claim.phase} />
          <HudField label="Next action" value={claim.next_action} />
          <HudField label="Run" value={claim.run_id ? `${claim.run_id}${claim.run_status ? ` (${claim.run_status})` : ''}` : '—'} />
          <HudField label="Pull request" value={claim.pull_request_number ? `#${claim.pull_request_number}` : '—'} />
          <HudField className="sm:col-span-2" label="Last failure" value={claim.last_error || claim.operator_summary || '—'} />
          <HudField label="Repair attempts" value={`${claim.repair_attempts} / ${claim.max_repair_attempts}`} />
          <HudField label="Updated" value={formatTime(claim.updated_at)} />
        </div>
      ) : null}

      <div className="mt-4 flex flex-wrap gap-2">
        <ExternalLink href={issue.links.issue}>Gitea issue</ExternalLink>
        <ExternalLink href={issue.links.pull_request}>Pull request</ExternalLink>
        {claim?.run_id ? (
          <a className="hud-chip border-[hsl(var(--signal))] px-3 py-2 text-[0.62rem] text-[hsl(var(--signal))]" href={`#/runs/${encodeURIComponent(claim.run_id)}`} onClick={(event) => { event.preventDefault(); onOpenRun(claim.run_id ?? ''); }}>
            Open run
          </a>
        ) : null}
        {issue.action === 'dispatch' ? (
          <button className="hud-chip border-[hsl(var(--pulse))] bg-[hsl(var(--pulse-soft))] px-3 py-2 text-[0.62rem] text-[hsl(var(--ink))] disabled:cursor-not-allowed disabled:opacity-50" disabled={busy} onClick={() => onDispatch(issue)} type="button">
            {busy ? 'Dispatching…' : 'Dispatch'}
          </button>
        ) : null}
        {issue.action === 'resume' ? (
          <button className="hud-chip border-[hsl(var(--pulse))] bg-[hsl(var(--pulse-soft))] px-3 py-2 text-[0.62rem] text-[hsl(var(--ink))] disabled:cursor-not-allowed disabled:opacity-50" disabled={busy} onClick={() => onResume(issue)} type="button">
            {busy ? 'Resuming…' : 'Resume lifecycle'}
          </button>
        ) : null}
      </div>

      <div className="mt-4 grid gap-2">
        <HudLabel className="text-[hsl(var(--faint))]">Eligibility checks</HudLabel>
        {issue.checks.map((check, index) => (
          <HudPanel key={`${check.name}-${index}`} className="bg-[hsl(var(--panel-soft))] px-3 py-2">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span className="text-xs uppercase tracking-[0.18em] text-[hsl(var(--signal))]">{check.name}</span>
              <span className={`hud-chip px-2 py-0.5 text-[0.6rem] ${statusClasses(check.status === 'pass' ? 'succeeded' : check.status === 'fail' ? 'failed' : check.status === 'warn' ? 'paused' : 'canceled')}`}>{check.code}</span>
            </div>
            <p className="mt-1 text-sm text-[hsl(var(--ink))]">{check.message}</p>
            {check.remediation ? <p className="mt-1 text-xs text-[hsl(var(--muted))]">{check.remediation}</p> : null}
          </HudPanel>
        ))}
      </div>
    </HudPanelStrong>
  );
}

export function OpenPilotInboxPage({ onOpenRun }: OpenPilotInboxPageProps) {
  const [link, setLink] = useState<InboxLink>(currentLink);
  const [report, setReport] = useState<OpenPilotInboxReport | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [actionError, setActionError] = useState('');
  const [busyKey, setBusyKey] = useState('');
  const [reloadToken, setReloadToken] = useState(0);
  const [queryDraft, setQueryDraft] = useState(link.q);
  const [repoDraft, setRepoDraft] = useState(link.repo);

  useEffect(() => {
    const sync = () => {
      if (window.location.hash === '#/inbox' || window.location.hash.startsWith('#/inbox/')) {
        setLink(parseInboxHash(window.location.hash));
      }
    };
    window.addEventListener('hashchange', sync);
    return () => window.removeEventListener('hashchange', sync);
  }, []);

  const navigate = useCallback((next: InboxLink) => {
    setLink(next);
    const hash = formatInboxHash(next);
    if (window.location.hash !== hash) {
      window.location.hash = hash;
    }
  }, []);

  const filterKey = `${link.repo}|${link.eligibility}|${link.pickup}|${link.phase}|${link.q}|${link.cursor}`;
  useEffect(() => {
    let current = true;
    setLoading(true);
    setError('');
    loadOpenPilotInbox(link)
      .then((next) => { if (current) setReport(next); })
      .catch((caught) => { if (current) { setReport(null); setError(messageFrom(caught)); } })
      .finally(() => { if (current) setLoading(false); });
    return () => { current = false; };
    // filterKey captures every link field that changes the request.
  }, [filterKey, reloadToken]);

  const selected = report?.issues.find((issue) => issue.key === link.issue) ?? null;
  const filtered = link.repo !== '' || link.eligibility !== 'all' || link.pickup !== '' || link.phase !== '' || link.q !== '';

  const act = useCallback(async (issue: OpenPilotInboxIssue, kind: 'dispatch' | 'resume') => {
    setBusyKey(issue.key);
    setActionError('');
    try {
      const data = kind === 'dispatch' ? await dispatchOpenPilotIssue(issue) : await resumeOpenPilotState(issue.repo, issue.issue);
      onOpenRun(data.run.id);
    } catch (caught) {
      setActionError(`${kind === 'dispatch' ? 'Dispatch' : 'Resume'} of ${issue.key} was rejected: ${messageFrom(caught)}`);
      setReloadToken((value) => value + 1);
    } finally {
      setBusyKey('');
    }
  }, [onOpenRun]);

  const setFilter = (patch: Partial<InboxLink>) => navigate({ ...link, ...patch, cursor: '', issue: '' });

  return (
    <section className="grid gap-4">
      <HudSection
        kicker="Open Pilot"
        title="Issue inbox"
        note="Open issues from admitted repositories with pickup path, specification readiness, eligibility reason codes, and existing lifecycle claims. Dispatch and resume use the existing admission and claim paths."
        actions={[
          <button key="refresh" className="hud-chip border-[hsl(var(--signal))] bg-[hsl(var(--signal-soft))] px-3 py-2 text-[0.62rem] text-[hsl(var(--signal))] disabled:opacity-50" disabled={loading} onClick={() => setReloadToken((value) => value + 1)} type="button">
            {loading ? 'Loading…' : 'Refresh'}
          </button>,
        ]}
      >
        <form
          className="grid gap-3 md:grid-cols-5"
          onSubmit={(event) => { event.preventDefault(); setFilter({ q: queryDraft, repo: repoDraft }); }}
        >
          <label className="hud-panel bg-[hsl(var(--panel-soft))] px-3 py-2 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">
            <span className="text-[hsl(var(--signal))]">Repository</span>
            <input className="mt-2 w-full border border-[hsl(var(--border-faint))] bg-[hsl(var(--void))] px-2 py-2 text-sm normal-case tracking-normal text-[hsl(var(--ink))]" onChange={(event) => setRepoDraft(event.target.value)} placeholder="all registered" value={repoDraft} />
          </label>
          <label className="hud-panel bg-[hsl(var(--panel-soft))] px-3 py-2 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">
            <span className="text-[hsl(var(--signal))]">Eligibility</span>
            <select className="mt-2 w-full border border-[hsl(var(--border-faint))] bg-[hsl(var(--void))] px-2 py-2 text-sm normal-case tracking-normal text-[hsl(var(--ink))]" onChange={(event) => setFilter({ eligibility: event.target.value as InboxEligibility })} value={link.eligibility}>
              <option value="all">all</option>
              <option value="eligible">eligible</option>
              <option value="ineligible">ineligible</option>
              <option value="claimed">claimed</option>
            </select>
          </label>
          <label className="hud-panel bg-[hsl(var(--panel-soft))] px-3 py-2 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">
            <span className="text-[hsl(var(--signal))]">Pickup</span>
            <select className="mt-2 w-full border border-[hsl(var(--border-faint))] bg-[hsl(var(--void))] px-2 py-2 text-sm normal-case tracking-normal text-[hsl(var(--ink))]" onChange={(event) => setFilter({ pickup: event.target.value as InboxPickup })} value={link.pickup}>
              <option value="">any</option>
              <option value="assigned">bot assigned</option>
              <option value="labels">queue labels</option>
              <option value="none">neither</option>
            </select>
          </label>
          <label className="hud-panel bg-[hsl(var(--panel-soft))] px-3 py-2 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">
            <span className="text-[hsl(var(--signal))]">Phase</span>
            <select className="mt-2 w-full border border-[hsl(var(--border-faint))] bg-[hsl(var(--void))] px-2 py-2 text-sm normal-case tracking-normal text-[hsl(var(--ink))]" onChange={(event) => setFilter({ phase: event.target.value })} value={link.phase}>
              <option value="">any</option>
              {phases.map((phase) => <option key={phase} value={phase}>{phase}</option>)}
            </select>
          </label>
          <label className="hud-panel bg-[hsl(var(--panel-soft))] px-3 py-2 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">
            <span className="text-[hsl(var(--signal))]">Search</span>
            <input className="mt-2 w-full border border-[hsl(var(--border-faint))] bg-[hsl(var(--void))] px-2 py-2 text-sm normal-case tracking-normal text-[hsl(var(--ink))]" onChange={(event) => setQueryDraft(event.target.value)} placeholder="title, label, reason code" value={queryDraft} />
          </label>
          <button className="sr-only" type="submit">Apply filters</button>
        </form>

        {actionError ? <HudErrorState className="mt-4" role="alert"><strong>Action rejected</strong><span>{actionError}</span></HudErrorState> : null}
        {error ? <HudErrorState className="mt-4" role="alert"><strong>Inbox unavailable</strong><span>{error}</span></HudErrorState> : null}
        {report && report.blockers.length > 0 ? (
          <HudErrorState className="mt-4" data-inbox-blockers="">
            <strong>Inbox blocked: {report.blockers.join(', ')}</strong>
            {report.checks.filter((check) => check.status === 'fail').map((check) => <span key={check.code}>{check.message}{check.remediation ? ` — ${check.remediation}` : ''}</span>)}
          </HudErrorState>
        ) : null}
        {report && report.upstream !== 'available' && report.blockers.length === 0 ? (
          <div className="hud-panel mt-4 border-[hsl(var(--warn))] bg-[hsl(var(--warn-soft))] px-4 py-3 text-sm text-[hsl(var(--ink))]" data-upstream-status={report.upstream} role="status">
            <strong>Upstream {report.upstream.replace('_', ' ')}.</strong> {upstreamCopy[report.upstream]}
            {report.upstream_error ? <span className="block text-xs text-[hsl(var(--muted))]">{report.upstream_error}</span> : null}
          </div>
        ) : null}

        {report ? (
          <div className="mt-4 flex flex-wrap gap-2 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">
            {report.repositories.map((repository) => (
              <span key={repository.id} className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(!repository.admitted ? 'canceled' : repository.upstream === 'listed' ? 'succeeded' : 'failed')}`} title={repository.admission_message || repository.upstream_error || ''}>
                {repository.slug}: {repository.admitted ? repository.upstream : repository.admission_code}{repository.polled ? ' · polled' : ''}
              </span>
            ))}
            <span>Checked {formatTime(report.checked_at)} · {report.matched} matching</span>
          </div>
        ) : null}
      </HudSection>

      <div className="grid gap-4 xl:grid-cols-[0.95fr_1.05fr]">
        <HudSection kicker="Queue" title="Intake candidates">
          <div className="grid gap-3">
            {loading && !report ? <HudLoadingState>Loading the Open Pilot inbox…</HudLoadingState> : null}
            {report && report.issues.length === 0 ? (
              <HudEmptyState>
                {report.blockers.length > 0
                  ? 'No issues can be listed until the blockers above are resolved.'
                  : report.upstream !== 'available'
                    ? 'No issues to show, but upstream data is incomplete. This is not an empty queue.'
                    : filtered
                      ? 'No issues match these filters.'
                      : 'The queue is empty: no open issue carries the queue labels or bot assignment.'}
              </HudEmptyState>
            ) : null}
            {report?.issues.map((issue) => (
              <IssueRow key={issue.key} issue={issue} onSelect={(key) => navigate({ ...link, issue: key })} selected={issue.key === link.issue} />
            ))}
            {report && (link.cursor || report.has_more) ? (
              <div className="flex flex-wrap gap-2">
                {link.cursor ? <button className="hud-chip px-3 py-2 text-[0.62rem]" onClick={() => navigate({ ...link, cursor: '', issue: '' })} type="button">First page</button> : null}
                {report.has_more && report.next_cursor ? <button className="hud-chip px-3 py-2 text-[0.62rem]" onClick={() => navigate({ ...link, cursor: report.next_cursor ?? '', issue: '' })} type="button">Next page</button> : null}
              </div>
            ) : null}
          </div>
        </HudSection>
        <HudSection kicker="Inspection" title="Issue detail">
          <IssueDetail busy={selected !== null && busyKey === selected.key} issue={selected} onDispatch={(issue) => void act(issue, 'dispatch')} onOpenRun={onOpenRun} onResume={(issue) => void act(issue, 'resume')} />
        </HudSection>
      </div>
    </section>
  );
}
