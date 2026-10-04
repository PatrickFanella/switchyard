import { useEffect, useRef, useState, type ReactNode } from 'react';
import { loadCorrelationTargets } from '../api';
import {
  correlationKindLabels,
  correlationReasonText,
  evidenceAnchorID,
  parseEvidenceHash,
  type CorrelationDestination,
  type EvidenceFocus,
  type EvidenceKind,
  type RunFailureSummary,
} from '../correlation';
import type { CorrelationTargets } from '../types';
import { HudEmptyState, HudLabel, HudPanel, HudPanelStrong, HudSection, statusClasses } from './hud';

export type CorrelationTargetsState = { targets: CorrelationTargets; status: 'loading' | 'ready' | 'unavailable' };

// useCorrelationTargets loads the approved external link bases once per mount.
// A failed load leaves every external destination as copy-only text.
export function useCorrelationTargets(): CorrelationTargetsState {
  const [state, setState] = useState<CorrelationTargetsState>({ targets: {}, status: 'loading' });
  useEffect(() => {
    let active = true;
    loadCorrelationTargets()
      .then((targets) => {
        if (active) setState({ targets, status: 'ready' });
      })
      .catch(() => {
        if (active) setState({ targets: {}, status: 'unavailable' });
      });
    return () => {
      active = false;
    };
  }, []);
  return state;
}

function useLocationHash() {
  const [hash, setHash] = useState(() => (typeof window === 'undefined' ? '' : window.location.hash));
  useEffect(() => {
    if (typeof window === 'undefined') return;
    const sync = () => setHash(window.location.hash);
    sync();
    window.addEventListener('hashchange', sync);
    return () => window.removeEventListener('hashchange', sync);
  }, []);
  return hash;
}

export type EvidenceFocusState = { focus: EvidenceFocus | null; found: boolean };

// useEvidenceFocus follows `#/runs/<run>/<kind>/<id>` evidence deep links. It
// focuses and scrolls to the anchored evidence once per link, and reports
// when the evidence is not rendered (unknown, filtered, or retained out).
// `renderKey` must change whenever the rendered evidence may have changed.
export function useEvidenceFocus(runID: string, renderKey: unknown): EvidenceFocusState {
  const hash = useLocationHash();
  const parsed = parseEvidenceHash(hash);
  const focus = parsed && parsed.runID === runID ? parsed : null;
  const focusKind = focus?.kind;
  const focusID = focus?.id;
  const [found, setFound] = useState(false);
  const lastFocused = useRef('');

  useEffect(() => {
    if (!focusKind || focusID === undefined || typeof document === 'undefined') {
      lastFocused.current = '';
      setFound(false);
      return;
    }
    const element = document.getElementById(evidenceAnchorID(focusKind, focusID));
    setFound(element !== null);
    const token = `${runID}\u0000${focusKind}\u0000${focusID}`;
    if (!element || lastFocused.current === token) return;
    lastFocused.current = token;
    if (typeof element.scrollIntoView === 'function') element.scrollIntoView({ block: 'center' });
    element.focus({ preventScroll: true });
  }, [focusID, focusKind, renderKey, runID]);

  return { focus, found };
}

// EvidenceAnchor wraps a step, artifact, or event so deep links can target it.
export function EvidenceAnchor({ kind, id, focus, children }: { kind: EvidenceKind; id: string; focus: EvidenceFocus | null; children: ReactNode }) {
  const focused = focus?.kind === kind && focus.id === id;
  return (
    <div
      className={focused ? 'outline outline-2 outline-offset-2 outline-[hsl(var(--signal))]' : undefined}
      data-evidence-focused={focused ? 'true' : undefined}
      id={evidenceAnchorID(kind, id)}
      tabIndex={-1}
    >
      {children}
    </div>
  );
}

const evidenceKindLabel: Record<EvidenceKind, string> = { steps: 'Step', artifacts: 'Artifact', events: 'Event' };

export function EvidenceFocusNotice({ state }: { state: EvidenceFocusState }) {
  if (!state.focus || state.found) return null;
  return (
    <div className="hud-panel border-[hsl(var(--warn))] bg-[hsl(var(--panel-soft))] px-4 py-3 text-sm text-[hsl(var(--muted))]" role="status">
      {evidenceKindLabel[state.focus.kind]} <code className="break-all text-[hsl(var(--ink))]">{state.focus.id}</code> is not shown in this run. It may be hidden by the current filters, unknown, or removed by retention.
    </div>
  );
}

export function CopyValueButton({ value, label }: { value: string; label: string }) {
  const [notice, setNotice] = useState('');
  const copy = async () => {
    const clipboard = typeof navigator !== 'undefined' ? navigator.clipboard : undefined;
    if (!clipboard || typeof clipboard.writeText !== 'function') {
      setNotice('Clipboard unavailable; select the value to copy it.');
      return;
    }
    try {
      await clipboard.writeText(value);
      setNotice('Copied');
    } catch {
      setNotice('Copy denied; select the value to copy it.');
    }
  };
  return (
    <span className="inline-flex items-center gap-2">
      <button
        aria-label={`Copy ${label}`}
        className="hud-chip border-[hsl(var(--border))] bg-[hsl(var(--panel-soft))] px-2 py-1 text-[0.58rem] text-[hsl(var(--muted))] transition hover:border-[hsl(var(--signal))] hover:text-[hsl(var(--signal))]"
        onClick={() => void copy()}
        type="button"
      >
        Copy
      </button>
      {notice ? <span className="text-[0.62rem] uppercase tracking-[0.18em] text-[hsl(var(--faint))]" role="status">{notice}</span> : null}
    </span>
  );
}

export function CorrelationLink({ destination, children }: { destination: CorrelationDestination; children?: ReactNode }) {
  const text = children ?? destination.value;
  if (!destination.href) {
    return <code className="break-all text-sm text-[hsl(var(--ink))]">{text}</code>;
  }
  return (
    <a
      className="break-all text-sm text-[hsl(var(--signal))] underline decoration-dotted underline-offset-4"
      data-correlation-kind={destination.kind}
      href={destination.href}
      {...(destination.external ? { rel: 'noopener noreferrer', target: '_blank' } : {})}
    >
      {text}
    </a>
  );
}

export function CorrelationItem({ destination }: { destination: CorrelationDestination }) {
  return (
    <HudPanel className="bg-[hsl(var(--panel-soft))] p-4">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <div className="text-xs text-[hsl(var(--signal))]">{correlationKindLabels[destination.kind]}</div>
          <p className="mt-1 text-xs text-[hsl(var(--faint))]">{destination.label}</p>
        </div>
        <CopyValueButton label={`${destination.label} ${destination.value}`} value={destination.value} />
      </div>
      <div className="mt-3">
        <CorrelationLink destination={destination} />
      </div>
      {destination.reason ? <p className="mt-2 text-xs text-[hsl(var(--muted))]">{correlationReasonText[destination.reason]}</p> : null}
      <p className="mt-2 text-[0.62rem] uppercase tracking-[0.18em] text-[hsl(var(--faint))]">{destination.sources.join(' · ')}</p>
    </HudPanel>
  );
}

export function CorrelationLinks({ destinations, targets }: { destinations: CorrelationDestination[]; targets: CorrelationTargetsState }) {
  const unconfigured = targets.status === 'unavailable' ? 'External link destinations could not be loaded; external IDs are shown for copying.' : targets.status === 'ready' && !targets.targets.gitea_base_url && !targets.targets.trace_ui_base_url ? 'No Gitea or trace UI base is configured; external IDs are shown for copying.' : '';
  return (
    <div className="grid gap-3">
      {unconfigured ? <p className="text-xs text-[hsl(var(--muted))]">{unconfigured}</p> : null}
      {destinations.length === 0 ? (
        <HudEmptyState>No correlation IDs recorded for this run.</HudEmptyState>
      ) : (
        <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3" data-testid="correlation-links">
          {destinations.map((destination) => (
            <CorrelationItem destination={destination} key={`${destination.kind}-${destination.key}-${destination.value}`} />
          ))}
        </div>
      )}
    </div>
  );
}

export function RunFailureSummaryPanel({ summary }: { summary: RunFailureSummary }) {
  return (
    <HudSection kicker="Failure" title="Failure summary" note="What failed, why, and where the evidence is.">
      <HudPanelStrong className="border-[hsl(var(--danger))] p-4">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <h3 className="text-lg font-semibold text-[hsl(var(--ink))]">{summary.headline}</h3>
          <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(summary.status)}`}>{summary.status}</span>
        </div>
        <p className="mt-3 break-words text-sm leading-6 text-[hsl(var(--danger))]">{summary.detail}</p>

        {summary.gateFailures.length > 0 ? (
          <p className="mt-3 text-xs text-[hsl(var(--muted))]">
            Gates: {summary.gateFailures.map((gate) => `${gate.gate} ${gate.status}`).join(' · ')}
          </p>
        ) : null}

        <div className="mt-4 grid gap-4 md:grid-cols-2">
          <div>
            <HudLabel className="text-[hsl(var(--faint))]">Evidence</HudLabel>
            {summary.evidence.length === 0 ? (
              <p className="mt-2 text-sm text-[hsl(var(--muted))]">No failing step, event, or verification artifact was recorded.</p>
            ) : (
              <ul className="mt-2 grid gap-1">
                {summary.evidence.map((item) => (
                  <li key={`${item.kind}-${item.id}`}>
                    <a className="text-sm text-[hsl(var(--signal))] underline decoration-dotted underline-offset-4" data-evidence-link={item.kind} href={item.href}>
                      {item.label}
                    </a>
                  </li>
                ))}
              </ul>
            )}
          </div>
          <div>
            <HudLabel className="text-[hsl(var(--faint))]">Related</HudLabel>
            {summary.links.length === 0 ? (
              <p className="mt-2 text-sm text-[hsl(var(--muted))]">No CI, pull request, or trace correlation was recorded for the failure.</p>
            ) : (
              <ul className="mt-2 grid gap-2">
                {summary.links.map((destination) => (
                  <li className="flex flex-wrap items-center gap-2" key={`${destination.kind}-${destination.key}-${destination.value}`}>
                    <span className="text-[0.62rem] uppercase tracking-[0.18em] text-[hsl(var(--faint))]">{correlationKindLabels[destination.kind]}</span>
                    <CorrelationLink destination={destination} />
                    {destination.href ? null : <CopyValueButton label={`${destination.label} ${destination.value}`} value={destination.value} />}
                  </li>
                ))}
              </ul>
            )}
          </div>
        </div>
      </HudPanelStrong>
    </HudSection>
  );
}

export function RunUnavailableNotice({ runID, missing }: { runID: string; missing: boolean }) {
  return (
    <HudEmptyState>
      {missing ? (
        <>
          Run <code className="break-all text-[hsl(var(--ink))]">{runID}</code> was not found. It is unknown or was removed by retention.{' '}
          <CopyValueButton label={`run ID ${runID}`} value={runID} />
        </>
      ) : (
        'Loading run detail…'
      )}
    </HudEmptyState>
  );
}
