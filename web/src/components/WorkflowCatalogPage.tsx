import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react';
import { APIError, arrayOrEmpty, messageFrom } from '../api';
import { confirmNavigation } from '../navigationGuard';
import type { AgentProfile, WorkflowDefinition } from '../types';
import {
  draftFromDefinition,
  duplicateDraft,
  emptyDraft,
  filterWorkflowCatalog,
  formatWorkflowHash,
  hasActiveVersion,
  hasPendingDraft,
  loadControlAccess,
  loadEditorProfiles,
  loadWorkflowDefinition,
  loadWorkflowDefinitions,
  orderedDefinitionSteps,
  parseStepOutputReferences,
  parseWorkflowHash,
  type CatalogStatusFilter,
  type ControlAccess,
  type WorkflowRoute,
} from '../workflowDrafts';
import { formatTime, HudEmptyState, HudErrorState, HudLabel, HudLoadingState, HudPanel, HudPanelStrong, HudSection, statusClasses } from './hud';
import { WorkflowDraftEditor } from './WorkflowDraftEditor';
import { WorkflowVersionsPanel } from './WorkflowVersionsPanel';

const chipClass = 'hud-chip px-3 py-1.5 text-[0.65rem] disabled:opacity-50';

function currentRoute(): WorkflowRoute {
  return typeof window === 'undefined' ? { mode: 'catalog', id: '' } : parseWorkflowHash(window.location.hash);
}

function sameRoute(left: WorkflowRoute, right: WorkflowRoute) {
  return left.mode === right.mode && left.id === right.id;
}

export function definitionStatusClasses(status: string) {
  return statusClasses(status === 'active' ? 'succeeded' : status === 'draft' ? 'queued' : '');
}

/** Catalog status: the latest version's status, plus the active version when a newer draft is pending. */
export function definitionStatusText(definition: WorkflowDefinition) {
  const active = definition.active_version ?? 0;
  if (definition.status === 'draft' && active > 0) return `draft · v${active} active`;
  return definition.status;
}

function definitionServingText(definition: WorkflowDefinition) {
  const active = definition.active_version ?? 0;
  if (definition.status === 'active') return 'This version serves new runs.';
  if (active > 0) return `This is a saved draft. Version ${active} is active and keeps serving new runs until this draft is activated.`;
  return "This is a saved draft. No version is active, so new runs use the workflow's built-in pipeline or executor until a version is activated.";
}

function loadErrorMessage(error: unknown) {
  if (error instanceof APIError && (error.status === 401 || error.status === 403)) return 'The control API rejected this request. Sign in with a read or admin credential to view workflow definitions.';
  return messageFrom(error);
}

/** Read-only, ordered view of a stored definition. */
export function WorkflowDefinitionSteps({ definition }: { definition: WorkflowDefinition }) {
  const steps = orderedDefinitionSteps(definition);
  if (steps.length === 0) return <HudEmptyState>This definition has no steps.</HudEmptyState>;
  return (
    <ol className="grid gap-2" aria-label={`Steps of ${definition.name || definition.id}`}>
      {steps.map((step) => {
        const inputs = Object.entries(step.inputs ?? {});
        const bindings = inputs.flatMap(([, value]) => parseStepOutputReferences(value).references);
        const outputs = Object.entries(step.output_shape?.properties ?? {});
        const required = new Set(arrayOrEmpty(step.output_shape?.required));
        return (
          <li key={step.id} data-step-id={step.id}>
            <HudPanel className="bg-[hsl(var(--panel-soft))] p-3">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <span className="text-sm font-semibold text-[hsl(var(--ink))]">{step.position}. {step.name} <span className="font-mono text-xs text-[hsl(var(--muted))]">({step.id})</span></span>
                <span className="flex flex-wrap gap-1.5 text-[0.65rem]">
                  <span className={`hud-chip px-2 py-0.5 ${statusClasses('')}`}>{step.executor}</span>
                  {step.profile ? <span className={`hud-chip px-2 py-0.5 ${statusClasses('seeded')}`}>profile {step.profile}</span> : null}
                  {step.model ? <span className={`hud-chip px-2 py-0.5 ${statusClasses('')}`}>model {step.model}</span> : null}
                </span>
              </div>
              {inputs.length > 0 ? (
                <dl className="mt-2 grid gap-1 text-xs">
                  {inputs.map(([name, value]) => (
                    <div key={name} className="grid gap-1 sm:grid-cols-[10rem_1fr]">
                      <dt className="font-mono text-[hsl(var(--faint))]">{name}</dt>
                      <dd className="whitespace-pre-wrap break-words font-mono text-[hsl(var(--ink))]">{value}</dd>
                    </div>
                  ))}
                </dl>
              ) : <p className="mt-2 text-xs text-[hsl(var(--faint))]">No inputs.</p>}
              {bindings.length > 0 ? <p className="mt-2 text-xs text-[hsl(var(--muted))]">Reads outputs: {[...new Set(bindings.map((binding) => `${binding.stepID}.${binding.name}`))].join(', ')}</p> : null}
              {outputs.length > 0 ? (
                <p className="mt-2 text-xs text-[hsl(var(--muted))]">
                  Declared outputs: {outputs.map(([name, property]) => `${name} (${property.type}${required.has(name) ? ', required' : ''})`).join(', ')}
                </p>
              ) : null}
            </HudPanel>
          </li>
        );
      })}
    </ol>
  );
}

export function WorkflowCatalogPage() {
  const [route, setRoute] = useState<WorkflowRoute>(currentRoute);
  const routeRef = useRef(route);
  routeRef.current = route;
  const [definitions, setDefinitions] = useState<WorkflowDefinition[] | null>(null);
  const [profiles, setProfiles] = useState<AgentProfile[]>([]);
  const [access, setAccess] = useState<ControlAccess>({ role: 'unknown', can_write: true });
  const [loadError, setLoadError] = useState('');
  const [reloadToken, setReloadToken] = useState(0);
  const [query, setQuery] = useState('');
  const [statusFilter, setStatusFilter] = useState<CatalogStatusFilter>('all');
  const [notice, setNotice] = useState('');
  const [selected, setSelected] = useState<{ definition: WorkflowDefinition | null; error: string; loading: boolean }>({ definition: null, error: '', loading: false });
  const [editorKey, setEditorKey] = useState(0);
  const resultsRef = useRef<HTMLUListElement>(null);
  const readOnly = !access.can_write;

  const navigate = useCallback((next: WorkflowRoute) => {
    if (sameRoute(next, routeRef.current)) return true;
    if (!confirmNavigation()) return false;
    routeRef.current = next;
    setRoute(next);
    setNotice('');
    const hash = formatWorkflowHash(next);
    if (window.location.hash !== hash) window.location.hash = hash;
    return true;
  }, []);

  useEffect(() => {
    const sync = () => {
      const hash = window.location.hash;
      if (hash !== '#/workflows' && !hash.startsWith('#/workflows/')) return;
      const next = parseWorkflowHash(hash);
      if (sameRoute(next, routeRef.current)) return;
      if (!confirmNavigation()) {
        window.history.pushState(null, '', formatWorkflowHash(routeRef.current));
        return;
      }
      routeRef.current = next;
      setRoute(next);
    };
    window.addEventListener('hashchange', sync);
    return () => window.removeEventListener('hashchange', sync);
  }, []);

  useEffect(() => {
    let current = true;
    setLoadError('');
    Promise.all([loadWorkflowDefinitions(), loadEditorProfiles().catch(() => [] as AgentProfile[]), loadControlAccess()])
      .then(([nextDefinitions, nextProfiles, nextAccess]) => {
        if (!current) return;
        setDefinitions(nextDefinitions);
        setProfiles(nextProfiles);
        setAccess(nextAccess);
      })
      .catch((error) => { if (current) { setDefinitions(null); setLoadError(loadErrorMessage(error)); } });
    return () => { current = false; };
  }, [reloadToken]);

  // Re-read the definition whenever the screen changes so the editor starts from the stored version.
  const selectedID = route.mode === 'view' || route.mode === 'edit' || route.mode === 'duplicate' ? route.id : '';
  const selectedMode = selectedID ? route.mode : '';
  useEffect(() => {
    if (!selectedID) { setSelected({ definition: null, error: '', loading: false }); return; }
    let current = true;
    setSelected({ definition: null, error: '', loading: true });
    loadWorkflowDefinition(selectedID)
      .then((definition) => { if (current) setSelected({ definition, error: '', loading: false }); })
      .catch((error) => {
        if (!current) return;
        const message = error instanceof APIError && error.status === 404 ? `Workflow definition "${selectedID}" was not found.` : loadErrorMessage(error);
        setSelected({ definition: null, error: message, loading: false });
      });
    return () => { current = false; };
    // selectedMode is a dependency only so each screen change re-reads.
  }, [selectedID, selectedMode, reloadToken]);

  const visible = useMemo(() => filterWorkflowCatalog(definitions ?? [], query, statusFilter), [definitions, query, statusFilter]);
  const existingIDs = useMemo(() => (definitions ?? []).map((definition) => definition.id), [definitions]);
  const counts = useMemo(() => ({ active: (definitions ?? []).filter(hasActiveVersion).length, draft: (definitions ?? []).filter(hasPendingDraft).length }), [definitions]);

  const focusResult = (index: number) => {
    const buttons = Array.from(resultsRef.current?.querySelectorAll<HTMLButtonElement>('button[data-catalog-entry]') ?? []);
    buttons[Math.max(0, Math.min(index, buttons.length - 1))]?.focus();
  };
  const onSearchKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'ArrowDown') { event.preventDefault(); focusResult(0); }
    if (event.key === 'Escape' && query) { event.preventDefault(); setQuery(''); }
  };
  const onResultKeyDown = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    if (event.key === 'ArrowDown') { event.preventDefault(); focusResult(index + 1); }
    if (event.key === 'ArrowUp') { event.preventDefault(); if (index === 0) document.getElementById('workflow-catalog-search')?.focus(); else focusResult(index - 1); }
  };

  const onSaved = (definition: WorkflowDefinition) => {
    navigate({ mode: 'view', id: definition.id });
    setNotice(`Saved ${definition.id} as draft version ${definition.version ?? '?'}. It is not active; ${definition.active_version ? `version ${definition.active_version} keeps serving new runs until you activate the draft.` : 'activation is a separate step.'}`);
    setReloadToken((token) => token + 1);
  };

  const editing = route.mode === 'new' || route.mode === 'edit' || route.mode === 'duplicate';
  const accessNote = access.role === 'read' || (!access.can_write && access.role === 'unknown')
    ? 'Read-only access: you can browse and compare workflow definitions but not create, duplicate, save, activate, or roll back.'
    : '';

  let body;
  if (loadError) {
    body = (
      <HudErrorState role="alert">
        <strong>Workflow definitions unavailable</strong>
        <span>{loadError}</span>
        <button type="button" className={`${chipClass} self-start`} onClick={() => setReloadToken((token) => token + 1)}>Retry</button>
      </HudErrorState>
    );
  } else if (definitions === null) {
    body = <HudLoadingState>Loading workflow definitions…</HudLoadingState>;
  } else if (editing) {
    if (route.mode !== 'new' && selected.loading) body = <HudLoadingState>Loading {route.id}…</HudLoadingState>;
    else if (route.mode !== 'new' && !selected.definition) body = <HudErrorState role="alert">{selected.error || `Workflow definition "${route.id}" was not found.`}</HudErrorState>;
    else if (readOnly && route.mode !== 'edit') {
      body = <HudErrorState role="alert"><strong>Read-only access</strong><span>An admin credential is required to create workflow drafts.</span></HudErrorState>;
    } else {
      const base = selected.definition ?? undefined;
      const initialDraft = route.mode === 'new' ? emptyDraft() : route.mode === 'duplicate' && base ? duplicateDraft(base, existingIDs) : draftFromDefinition(base as WorkflowDefinition);
      body = (
        <WorkflowDraftEditor
          key={`${route.mode}:${route.id}:${base?.version ?? 0}:${editorKey}`}
          mode={route.mode === 'edit' ? 'edit' : 'create'}
          initialDraft={initialDraft}
          baseDefinition={route.mode === 'edit' ? base : undefined}
          profiles={profiles}
          existingIDs={existingIDs}
          readOnly={readOnly}
          onSaved={onSaved}
          onCancel={() => navigate(route.mode === 'edit' || route.mode === 'duplicate' ? { mode: 'view', id: route.id } : { mode: 'catalog', id: '' })}
          onReadOnly={() => setAccess({ role: 'read', can_write: false })}
          onReloadLatest={route.mode === 'edit' ? () => { setEditorKey((key) => key + 1); setReloadToken((token) => token + 1); } : undefined}
        />
      );
    }
  } else {
    body = (
      <div className="grid gap-4 xl:grid-cols-[minmax(18rem,1fr)_2fr]">
        <HudPanelStrong className="grid content-start gap-3 p-4">
          <label className="block text-xs text-[hsl(var(--muted))]" htmlFor="workflow-catalog-search">Search workflows</label>
          <input
            id="workflow-catalog-search"
            type="search"
            className="w-full border border-[hsl(var(--border-faint))] bg-[hsl(var(--void))] px-2 py-1 text-sm text-[hsl(var(--ink))]"
            value={query}
            placeholder="ID, name, step, executor, or profile"
            aria-describedby="workflow-catalog-count"
            onChange={(event) => setQuery(event.target.value)}
            onKeyDown={onSearchKeyDown}
          />
          <label className="block text-xs text-[hsl(var(--muted))]">
            Status
            <select className="mt-1 w-full border border-[hsl(var(--border-faint))] bg-[hsl(var(--void))] px-2 py-1 text-sm text-[hsl(var(--ink))]" value={statusFilter} onChange={(event) => setStatusFilter(event.target.value as CatalogStatusFilter)}>
              <option value="all">All ({definitions.length})</option>
              <option value="active">Has an active version ({counts.active})</option>
              <option value="draft">Latest is a draft ({counts.draft})</option>
            </select>
          </label>
          <p id="workflow-catalog-count" className="text-xs text-[hsl(var(--faint))]" aria-live="polite">{visible.length} of {definitions.length} workflows shown</p>
          {definitions.length === 0 ? (
            <HudEmptyState>No workflow definitions yet.{readOnly ? '' : ' Create a draft to start.'}</HudEmptyState>
          ) : visible.length === 0 ? (
            <HudEmptyState>No workflows match this search.</HudEmptyState>
          ) : (
            <ul ref={resultsRef} className="grid gap-2" aria-label="Workflow definitions">
              {visible.map((definition, index) => {
                const current = route.mode === 'view' && route.id === definition.id;
                return (
                  <li key={definition.id}>
                    <button
                      type="button"
                      data-catalog-entry={definition.id}
                      aria-current={current ? 'true' : undefined}
                      className={`hud-panel w-full bg-[hsl(var(--panel-soft))] p-3 text-left ${current ? 'border-[hsl(var(--signal))]' : ''}`}
                      onClick={() => navigate({ mode: 'view', id: definition.id })}
                      onKeyDown={(event) => onResultKeyDown(event, index)}
                    >
                      <span className="flex items-center justify-between gap-2">
                        <span className="text-sm font-semibold text-[hsl(var(--ink))]">{definition.name || definition.id}</span>
                        <span className={`hud-chip px-2 py-0.5 text-[0.6rem] ${definitionStatusClasses(definition.status)}`}>{definitionStatusText(definition)}</span>
                      </span>
                      <span className="mt-1 block font-mono text-xs text-[hsl(var(--muted))]">{definition.id}{definition.version ? ` · v${definition.version}` : ''} · {orderedDefinitionSteps(definition).length} steps</span>
                    </button>
                  </li>
                );
              })}
            </ul>
          )}
        </HudPanelStrong>

        <HudPanelStrong className="grid content-start gap-3 p-4" >
          {route.mode !== 'view' ? (
            <HudEmptyState>Select a workflow to see its ordered steps{readOnly ? '.' : ', or create a new draft.'}</HudEmptyState>
          ) : selected.loading ? (
            <HudLoadingState>Loading {route.id}…</HudLoadingState>
          ) : selected.error || !selected.definition ? (
            <HudErrorState role="alert">{selected.error || `Workflow definition "${route.id}" was not found.`}</HudErrorState>
          ) : (
            <div className="grid gap-3" data-workflow-detail={selected.definition.id}>
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div>
                  <h3 className="text-base font-semibold text-[hsl(var(--ink))]">{selected.definition.name}</h3>
                  <p className="mt-1 font-mono text-xs text-[hsl(var(--muted))]">{selected.definition.id}{selected.definition.version ? ` · version ${selected.definition.version}` : ''}{selected.definition.updated_at ? ` · updated ${formatTime(selected.definition.updated_at)}` : ''}</p>
                </div>
                <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${definitionStatusClasses(selected.definition.status)}`}>{definitionStatusText(selected.definition)}</span>
              </div>
              {selected.definition.description ? <p className="text-sm text-[hsl(var(--muted))]">{selected.definition.description}</p> : null}
              <p className="text-xs text-[hsl(var(--faint))]">{definitionServingText(selected.definition)}</p>
              {!readOnly ? (
                <div className="flex flex-wrap gap-2">
                  <button type="button" className={chipClass} onClick={() => navigate({ mode: 'edit', id: selected.definition!.id })}>Edit draft</button>
                  <button type="button" className={chipClass} onClick={() => navigate({ mode: 'duplicate', id: selected.definition!.id })}>Duplicate</button>
                </div>
              ) : null}
              <div>
                <HudLabel className="text-[hsl(var(--faint))]">Run input fields</HudLabel>
                {Object.keys(selected.definition.input_shape?.properties ?? {}).length === 0 ? (
                  <p className="mt-1 text-xs text-[hsl(var(--faint))]">None declared.</p>
                ) : (
                  <ul className="mt-1 grid gap-1 text-xs text-[hsl(var(--ink))]">
                    {Object.entries(selected.definition.input_shape?.properties ?? {}).map(([name, property]) => (
                      <li key={name}><span className="font-mono">{name}</span> · {property.type}{arrayOrEmpty(selected.definition!.input_shape?.required).includes(name) ? ' · required' : ''}{property.description ? ` · ${property.description}` : ''}</li>
                    ))}
                  </ul>
                )}
              </div>
              <div>
                <HudLabel className="text-[hsl(var(--faint))]">Steps in run order (version {selected.definition.version ?? '?'})</HudLabel>
                <div className="mt-2"><WorkflowDefinitionSteps definition={selected.definition} /></div>
              </div>
              <div>
                <HudLabel className="text-[hsl(var(--faint))]">Version history</HudLabel>
                <div className="mt-2">
                  <WorkflowVersionsPanel
                    definition={selected.definition}
                    readOnly={readOnly}
                    onChanged={(message) => { setNotice(message); setReloadToken((token) => token + 1); }}
                    onReadOnly={() => setAccess({ role: 'read', can_write: false })}
                  />
                </div>
              </div>
            </div>
          )}
        </HudPanelStrong>
      </div>
    );
  }

  return (
    <HudSection
      kicker="Workflow definitions"
      title={route.mode === 'new' ? 'New workflow draft' : route.mode === 'edit' ? `Edit ${route.id}` : route.mode === 'duplicate' ? `Duplicate ${route.id}` : 'Workflow catalog'}
      note="Author linear workflow drafts with typed inputs, supported executors, step-output bindings, and output contracts. Saving stores a draft version; activate or roll back from a workflow's version history."
      actions={
        <>
          {editing ? <button type="button" className={chipClass} onClick={() => navigate({ mode: 'catalog', id: '' })}>Back to catalog</button> : null}
          {!editing && !readOnly ? <button type="button" className={chipClass} onClick={() => navigate({ mode: 'new', id: '' })}>New draft</button> : null}
          {!editing ? <button type="button" className={chipClass} onClick={() => setReloadToken((token) => token + 1)}>Refresh</button> : null}
        </>
      }
    >
      {accessNote ? <p className="mb-3 text-xs text-[hsl(var(--warn))]" role="status" data-read-only="true">{accessNote}</p> : null}
      {notice && !editing ? <p className="mb-3 text-xs text-[hsl(var(--confirm))]" role="status">{notice}</p> : null}
      {body}
    </HudSection>
  );
}
