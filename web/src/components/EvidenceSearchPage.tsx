import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react';
import { messageFrom, searchEvidence } from '../api';
import { evidenceScopes, type EvidenceHit, type EvidenceScope, type EvidenceSearchResponse } from '../types';
import { emptyEvidenceSearchLink, formatEvidenceSearchHash, parseEvidenceSearchHash, type EvidenceSearchLink } from './evidenceSearch';
import { formatTime, HudChip, HudEmptyState, HudErrorState, HudField, HudLabel, HudLoadingState, HudPanel, HudPanelStrong, HudSection, statusClasses } from './hud';

const scopeHelp: Record<EvidenceScope, string> = {
  all: 'runs, steps, and model requests',
  runs: 'run IDs, workflows, triggers, statuses, and summaries',
  steps: 'step IDs, names, executors, models, summaries, errors, and correlation IDs',
  requests: 'request IDs, models, routes, clients, statuses, and errors',
  errors: 'failed or timed-out runs, errored steps, and failed requests',
  correlations: 'correlation-ID values plus step, request, and run identifiers',
};

const pageSize = 25;

function linkKey(link: EvidenceSearchLink | null) {
  return link ? `${link.scope}\n${link.query}\n${link.cursor}` : '';
}

function hitHref(hit: EvidenceHit) {
  if (hit.kind === 'request') {
    return `#/queues/requests/${encodeURIComponent(hit.id)}`;
  }
  return hit.run_id ? `#/runs/${encodeURIComponent(hit.run_id)}` : '';
}

function HitCard({ hit }: { hit: EvidenceHit }) {
  const correlations = Object.entries(hit.correlation_ids ?? {});
  const href = hitHref(hit);
  return (
    <HudPanel className="bg-[hsl(var(--panel-strong))] p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <HudLabel className="text-[hsl(var(--faint))]">{hit.kind} · {hit.id}</HudLabel>
          <h3 className="mt-2 break-words text-lg font-semibold text-[hsl(var(--ink))]">{hit.title || hit.id}</h3>
        </div>
        <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(hit.status)}`}>{hit.status}</span>
      </div>
      {hit.summary ? <p className="mt-3 break-words text-sm leading-6 text-[hsl(var(--muted))]">{hit.summary}</p> : null}
      {hit.error ? <HudErrorState className="mt-3 mb-0">{hit.error}</HudErrorState> : null}
      <p className="mt-3 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">
        {hit.workflow_id ? `${hit.workflow_id} · ` : ''}created {formatTime(hit.created_at)}{hit.finished_at ? ` · finished ${formatTime(hit.finished_at)}` : ''}
      </p>
      {correlations.length > 0 ? (
        <div className="mt-3 flex flex-wrap gap-2">
          {correlations.map(([key, value]) => (
            <span key={key} className="hud-chip break-all bg-[hsl(var(--panel-soft))] px-2.5 py-1 text-[0.62rem] normal-case tracking-normal text-[hsl(var(--ink))]">
              {key}: {value}
            </span>
          ))}
        </div>
      ) : null}
      {href ? (
        <a className="mt-3 inline-block text-xs uppercase tracking-[0.18em] text-[hsl(var(--signal))]" href={href}>
          {hit.kind === 'request' ? 'Open request' : 'Open run cockpit'}
        </a>
      ) : null}
    </HudPanel>
  );
}

export function EvidenceSearchPage() {
  const [link, setLink] = useState<EvidenceSearchLink | null>(() => parseEvidenceSearchHash(typeof window === 'undefined' ? '' : window.location.hash));
  const [scope, setScope] = useState<EvidenceScope>(link?.scope ?? 'all');
  const [query, setQuery] = useState(link?.query ?? '');
  const [result, setResult] = useState<EvidenceSearchResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [attempt, setAttempt] = useState(0);
  const previousCursors = useRef<string[]>([]);
  const requestSequence = useRef(0);

  const navigate = useCallback((next: EvidenceSearchLink | null) => {
    setLink((current) => (linkKey(current) === linkKey(next) ? current : next));
    if (typeof window !== 'undefined') {
      const hash = next ? formatEvidenceSearchHash(next) : '#/logs';
      if (window.location.hash !== hash) {
        window.location.hash = hash;
      }
    }
  }, []);

  useEffect(() => {
    if (typeof window === 'undefined') {
      return;
    }
    const syncFromHash = () => {
      const parsed = parseEvidenceSearchHash(window.location.hash);
      if (!parsed && !window.location.hash.startsWith('#/logs')) {
        return;
      }
      setLink((current) => (linkKey(current) === linkKey(parsed) ? current : parsed));
      if (parsed) {
        setScope(parsed.scope);
        setQuery(parsed.query);
      }
    };
    window.addEventListener('hashchange', syncFromHash);
    return () => window.removeEventListener('hashchange', syncFromHash);
  }, []);

  const key = linkKey(link);
  useEffect(() => {
    if (!link) {
      setResult(null);
      setError('');
      setLoading(false);
      return;
    }
    const sequence = ++requestSequence.current;
    setLoading(true);
    setError('');
    void searchEvidence({ query: link.query, scope: link.scope, cursor: link.cursor, limit: pageSize })
      .then((response) => {
        if (sequence !== requestSequence.current) return;
        setResult(response);
      })
      .catch((caught: unknown) => {
        if (sequence !== requestSequence.current) return;
        setResult(null);
        setError(messageFrom(caught));
      })
      .finally(() => {
        if (sequence === requestSequence.current) setLoading(false);
      });
    // `key` captures scope, query, and cursor; `attempt` forces a retry.
  }, [key, attempt]);

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    previousCursors.current = [];
    navigate({ scope, query: query.trim(), cursor: '' });
  };

  const nextPage = () => {
    if (!link || !result?.next_cursor) return;
    previousCursors.current.push(link.cursor);
    navigate({ ...link, cursor: result.next_cursor });
  };

  const previousPage = () => {
    if (!link) return;
    const cursor = previousCursors.current.pop() ?? '';
    navigate({ ...link, cursor });
  };

  const firstPage = () => {
    if (!link) return;
    previousCursors.current = [];
    navigate({ ...link, cursor: '' });
  };

  const hits = result?.hits ?? [];
  const searched = Boolean(link);

  return (
    <HudSection kicker="Retained evidence" title="Evidence search" note="Bounded server-side search over retained runs, steps, and model requests. Inputs and request or response bodies are never searched or shown; text fields are redacted.">
      <form aria-label="Evidence search" className="grid gap-3 md:grid-cols-[minmax(0,1fr)_auto_auto]" onSubmit={submit}>
        <label className="hud-panel bg-[hsl(var(--panel-soft))] px-3 py-2">
          <span className="hud-label text-[hsl(var(--signal))]">Query</span>
          <input className="mt-2 w-full bg-transparent text-sm text-[hsl(var(--ink))] outline-none" maxLength={256} name="q" onChange={(event) => setQuery(event.target.value)} placeholder="run ID, step name, model, error text, correlation ID" value={query} />
        </label>
        <label className="hud-panel bg-[hsl(var(--panel-soft))] px-3 py-2">
          <span className="hud-label text-[hsl(var(--signal))]">Scope</span>
          <select className="mt-2 bg-[hsl(var(--panel-soft))] text-sm text-[hsl(var(--ink))]" name="scope" onChange={(event) => setScope(event.target.value as EvidenceScope)} value={scope}>
            {evidenceScopes.map((item) => <option key={item} value={item}>{item}</option>)}
          </select>
        </label>
        <HudChip className="px-3 py-2 text-[0.62rem] text-[hsl(var(--signal))]" disabled={loading} type="submit">
          {loading ? 'Searching…' : 'Search'}
        </HudChip>
      </form>
      <p className="mt-2 text-xs text-[hsl(var(--muted))]">Scope {scope}: matches {scopeHelp[scope]}. Leave the query empty to list the most recent evidence in scope.</p>

      {result ? (
        <div className="mt-4 grid gap-3 md:grid-cols-3">
          <HudField label="Retention window" value={result.retention.window} />
          <HudField label="Searchable from" value={formatTime(result.retention.floor)} />
          <HudField label="Page size" value={result.limit} />
        </div>
      ) : null}

      <div aria-busy={loading} aria-live="polite" className="mt-4 grid gap-3">
        {!searched ? <HudEmptyState>Enter a query or choose a scope and press Search. Results link to the run cockpit or request detail.</HudEmptyState> : null}
        {searched && loading ? <HudLoadingState>Searching retained evidence…</HudLoadingState> : null}
        {searched && !loading && error ? (
          <HudErrorState role="alert">
            <strong>Search failed</strong>
            <span>{error}</span>
            <HudChip className="mt-2 w-fit px-3 py-2 text-[0.62rem] text-[hsl(var(--signal))]" onClick={() => setAttempt((value) => value + 1)} type="button">Retry</HudChip>
          </HudErrorState>
        ) : null}
        {searched && !loading && !error && result && hits.length === 0 ? (
          <HudEmptyState>No retained evidence matches {link?.query ? `“${link.query}”` : 'this scope'} within the {result.retention.window} retention window.</HudEmptyState>
        ) : null}
        {!loading && !error && hits.map((hit) => <HitCard key={`${hit.kind}-${hit.id}`} hit={hit} />)}
      </div>

      {searched && !loading && !error && result && (hits.length > 0 || link?.cursor) ? (
        <HudPanelStrong className="mt-4 flex flex-wrap items-center justify-between gap-3 p-3">
          <span className="text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">{hits.length} on this page{result.next_cursor ? ' · more available' : ' · last page'}</span>
          <div className="flex flex-wrap gap-2">
            <HudChip className="px-3 py-2 text-[0.62rem] text-[hsl(var(--muted))]" disabled={!link?.cursor} onClick={firstPage} type="button">First page</HudChip>
            <HudChip className="px-3 py-2 text-[0.62rem] text-[hsl(var(--muted))]" disabled={previousCursors.current.length === 0} onClick={previousPage} type="button">Previous page</HudChip>
            <HudChip className="px-3 py-2 text-[0.62rem] text-[hsl(var(--signal))]" disabled={!result.next_cursor} onClick={nextPage} type="button">Next page</HudChip>
          </div>
        </HudPanelStrong>
      ) : null}
    </HudSection>
  );
}

export { emptyEvidenceSearchLink };
