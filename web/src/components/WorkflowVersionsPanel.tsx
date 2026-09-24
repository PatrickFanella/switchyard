import { useEffect, useId, useMemo, useRef, useState, type KeyboardEvent } from 'react';
import { APIError, messageFrom } from '../api';
import type { WorkflowDefinition } from '../types';
import { orderedDefinitionSteps } from '../workflowDrafts';
import {
  actionConsequences,
  activateWorkflowVersion,
  activationProblems,
  activeVersionOf,
  compareWorkflowVersions,
  latestVersionOf,
  loadWorkflowDefinitionVersions,
  rollbackWorkflowVersion,
  versionRole,
  versionRoleLabels,
  type FieldChange,
  type StepChange,
  type VersionAction,
  type VersionActionResult,
} from '../workflowVersions';
import { formatTime, HudEmptyState, HudErrorState, HudLabel, HudLoadingState, HudPanel, statusClasses } from './hud';

const chipClass = 'hud-chip px-3 py-1.5 text-[0.65rem] disabled:opacity-50';
const selectClass = 'mt-1 w-full border border-[hsl(var(--border-faint))] bg-[hsl(var(--void))] px-2 py-1 text-sm text-[hsl(var(--ink))]';

type Outcome = { kind: Exclude<VersionActionResult, { ok: true }>['kind']; action: VersionAction['kind']; message: string } | null;

export type WorkflowVersionsPanelProps = {
  definition: WorkflowDefinition;
  readOnly: boolean;
  /** Called after the stored definition changed or should be re-read; `notice` describes a completed action. */
  onChanged: (notice: string) => void;
  onReadOnly: () => void;
};

function roleClasses(role: string) {
  return statusClasses(role === 'active' ? 'succeeded' : role === 'draft' ? 'queued' : '');
}

function FieldChanges({ changes }: { changes: FieldChange[] }) {
  return (
    <dl className="mt-1 grid gap-1 text-xs">
      {changes.map((change) => (
        <div key={change.field} className="grid gap-1 sm:grid-cols-[10rem_1fr]" data-field-change={change.field}>
          <dt className="font-mono text-[hsl(var(--faint))]">{change.field}</dt>
          <dd className="grid gap-0.5 font-mono">
            <span className="whitespace-pre-wrap break-words text-[hsl(var(--warn))]"><span aria-hidden="true">− </span><span className="sr-only">before: </span>{change.before || '(empty)'}</span>
            <span className="whitespace-pre-wrap break-words text-[hsl(var(--confirm))]"><span aria-hidden="true">+ </span><span className="sr-only">after: </span>{change.after || '(empty)'}</span>
          </dd>
        </div>
      ))}
    </dl>
  );
}

const stepChangeLabels: Record<StepChange['kind'], string> = { added: 'added', removed: 'removed', changed: 'changed', unchanged: 'unchanged' };

function StepChangeItem({ change }: { change: StepChange }) {
  const step = change.kind === 'removed' ? change.before : change.after;
  return (
    <li data-step-change={change.kind} data-step-id={change.stepID}>
      <HudPanel className="bg-[hsl(var(--panel-soft))] p-2">
        <div className="flex flex-wrap items-center justify-between gap-2 text-xs">
          <span className="text-[hsl(var(--ink))]">{step.position}. {step.name} <span className="font-mono text-[hsl(var(--muted))]">({change.stepID})</span></span>
          <span className={`hud-chip px-2 py-0.5 text-[0.6rem] ${statusClasses(change.kind === 'added' ? 'succeeded' : change.kind === 'removed' ? 'failed' : change.kind === 'changed' ? 'queued' : '')}`}>{stepChangeLabels[change.kind]}</span>
        </div>
        {change.kind === 'changed' ? <FieldChanges changes={change.fields} /> : null}
      </HudPanel>
    </li>
  );
}

function VersionComparisonView({ base, target }: { base: WorkflowDefinition; target: WorkflowDefinition }) {
  const comparison = useMemo(() => compareWorkflowVersions(base, target), [base, target]);
  if (!comparison.changed) return <HudEmptyState>Versions {base.version} and {target.version} have the same metadata and steps.</HudEmptyState>;
  return (
    <div className="grid gap-3" data-version-comparison={`${base.version}-${target.version}`}>
      <div>
        <HudLabel className="text-[hsl(var(--faint))]">Metadata</HudLabel>
        {comparison.metadata.length === 0 ? <p className="mt-1 text-xs text-[hsl(var(--faint))]">No metadata changes.</p> : <FieldChanges changes={comparison.metadata} />}
      </div>
      <div>
        <HudLabel className="text-[hsl(var(--faint))]">Steps</HudLabel>
        <ul className="mt-1 grid gap-2" aria-label={`Step changes from version ${base.version} to version ${target.version}`}>
          {comparison.steps.map((change) => <StepChangeItem key={`${change.kind}:${change.stepID}`} change={change} />)}
        </ul>
      </div>
    </div>
  );
}

function ConfirmVersionAction({ action, target, busy, onConfirm, onCancel }: { action: VersionAction; target: WorkflowDefinition | undefined; busy: boolean; onConfirm: () => void; onCancel: () => void }) {
  const titleID = useId();
  const dialogRef = useRef<HTMLDivElement>(null);
  useEffect(() => { dialogRef.current?.focus(); }, []);
  const problems = target ? activationProblems(target) : ['This version is no longer in the history.'];
  const verb = action.kind === 'activate' ? `Activate version ${action.version}` : `Roll back to version ${action.version}`;
  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key === 'Escape') {
      event.preventDefault();
      event.stopPropagation();
      if (!busy) onCancel();
    }
  };
  return (
    <div ref={dialogRef} role="dialog" aria-modal="true" aria-labelledby={titleID} tabIndex={-1} onKeyDown={onKeyDown} data-version-dialog={action.kind} className="hud-panel grid gap-3 border-[hsl(var(--signal))] bg-[hsl(var(--panel-soft))] p-4">
      <h4 id={titleID} className="text-sm font-semibold text-[hsl(var(--ink))]">{verb} of {action.workflowID}?</h4>
      <p className="font-mono text-xs text-[hsl(var(--muted))]">Workflow {action.workflowID} · target version {action.version} · active now: {action.activeVersion > 0 ? `version ${action.activeVersion}` : 'none'}</p>
      <ul className="grid list-disc gap-1 pl-5 text-xs text-[hsl(var(--ink))]" aria-label="Consequences">
        {actionConsequences(action).map((line) => <li key={line}>{line}</li>)}
      </ul>
      {problems.length > 0 ? (
        <HudErrorState role="alert">
          <strong>Version {action.version} cannot be activated</strong>
          <ul className="grid gap-1">{problems.map((problem) => <li key={problem}>{problem}</li>)}</ul>
          <span>Edit the workflow and save a new draft that fixes these problems.</span>
        </HudErrorState>
      ) : <p className="text-xs text-[hsl(var(--confirm))]">Activation checks passed. The server validates the version again before it changes anything.</p>}
      <div className="flex flex-wrap gap-2">
        <button type="button" className={chipClass} disabled={busy || problems.length > 0} onClick={onConfirm}>{busy ? 'Working…' : verb}</button>
        <button type="button" className={chipClass} disabled={busy} onClick={onCancel}>Cancel</button>
      </div>
    </div>
  );
}

/** Version history with comparison and admin-only activation and rollback. */
export function WorkflowVersionsPanel({ definition, readOnly, onChanged, onReadOnly }: WorkflowVersionsPanelProps) {
  const [versions, setVersions] = useState<WorkflowDefinition[] | null>(null);
  const [loadError, setLoadError] = useState('');
  const [reloadToken, setReloadToken] = useState(0);
  const [baseVersion, setBaseVersion] = useState(0);
  const [targetVersion, setTargetVersion] = useState(0);
  const [pending, setPending] = useState<VersionAction | null>(null);
  const [busy, setBusy] = useState(false);
  const [outcome, setOutcome] = useState<Outcome>(null);
  const outcomeRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let current = true;
    setVersions(null);
    setLoadError('');
    loadWorkflowDefinitionVersions(definition.id)
      .then((loaded) => {
        if (!current) return;
        const latest = latestVersionOf(loaded);
        const active = activeVersionOf(loaded);
        setVersions(loaded);
        setTargetVersion(latest);
        setBaseVersion(active > 0 && active !== latest ? active : loaded.find((item) => (item.version ?? 0) < latest)?.version ?? latest);
      })
      .catch((error) => {
        if (!current) return;
        setLoadError(error instanceof APIError && (error.status === 401 || error.status === 403) ? 'The control API rejected this request. Sign in to view version history.' : messageFrom(error));
      });
    return () => { current = false; };
  }, [definition.id, definition.version, reloadToken]);

  useEffect(() => {
    if (outcome) outcomeRef.current?.focus();
  }, [outcome]);

  if (loadError) {
    return (
      <HudErrorState role="alert">
        <strong>Version history unavailable</strong>
        <span>{loadError}</span>
        <button type="button" className={`${chipClass} self-start`} onClick={() => setReloadToken((token) => token + 1)}>Retry</button>
      </HudErrorState>
    );
  }
  if (!versions) return <HudLoadingState>Loading version history…</HudLoadingState>;

  const latest = latestVersionOf(versions);
  const active = activeVersionOf(versions);
  const byVersion = new Map(versions.map((item) => [item.version ?? 0, item]));
  const base = byVersion.get(baseVersion);
  const target = byVersion.get(targetVersion);
  const canActivateLatest = !readOnly && latest > 0 && latest !== active;

  const compareWith = (version: number) => {
    setTargetVersion(version);
    setBaseVersion(active > 0 && active !== version ? active : versions.find((item) => (item.version ?? 0) < version)?.version ?? version);
  };

  const run = async () => {
    if (!pending || busy) return;
    setBusy(true);
    const action = pending;
    const result = action.kind === 'activate'
      ? await activateWorkflowVersion(action.workflowID, action.version)
      : await rollbackWorkflowVersion(action.workflowID, action.version, action.latestVersion);
    setBusy(false);
    setPending(null);
    if (result.ok) {
      const now = result.definition.version ?? action.version;
      const previous = result.previousActiveVersion > 0 ? `version ${result.previousActiveVersion}` : 'no stored version';
      onChanged(action.kind === 'activate'
        ? `Activated version ${now} of ${action.workflowID}. New runs use it instead of ${previous}; existing runs keep their snapshot.`
        : `Rolled ${action.workflowID} back to version ${action.version} as new active version ${now}. New runs use it instead of ${previous}; existing runs keep their snapshot.`);
      return;
    }
    if (result.kind === 'forbidden') onReadOnly();
    const still = active > 0 ? `Version ${active} is still active.` : 'No version is active.';
    setOutcome({ kind: result.kind, action: action.kind, message: result.kind === 'forbidden' ? result.message : `${result.message}. Nothing changed; ${still.charAt(0).toLowerCase()}${still.slice(1)}` });
  };

  const outcomeTitle = outcome?.kind === 'conflict' ? 'The workflow changed' : outcome?.kind === 'validation' ? (outcome.action === 'activate' ? 'Activation rejected' : 'Rollback rejected') : outcome?.kind === 'forbidden' ? 'Read-only access' : 'Action failed';

  return (
    <div className="grid gap-3" data-version-history={definition.id}>
      <p className="text-xs text-[hsl(var(--muted))]" data-active-version={active}>
        Latest version {latest} ({versionRoleLabels[versionRole(latest, active)]}).{' '}
        {active > 0 ? `Version ${active} is active and serves new runs.` : "No version is active; new runs use the workflow's built-in pipeline or executor."}
        {latest > active && active > 0 ? ` Version ${latest} is a saved draft and does not serve runs until it is activated.` : ''}
      </p>
      {readOnly ? <p className="text-xs text-[hsl(var(--warn))]" data-read-only="true">Read-only access: you can compare versions, but activation and rollback need an admin credential.</p> : null}

      {outcome ? (
        <div ref={outcomeRef} tabIndex={-1} data-version-outcome={outcome.kind}>
          <HudErrorState role="alert">
            <strong>{outcomeTitle}</strong>
            <span className="break-words">{outcome.message}</span>
            {outcome.kind === 'conflict' || outcome.kind === 'not-found' ? (
              <button type="button" className={`${chipClass} mt-2 self-start`} onClick={() => { setOutcome(null); onChanged(''); }}>Reload workflow</button>
            ) : null}
          </HudErrorState>
        </div>
      ) : null}

      {canActivateLatest && !pending ? (
        <div>
          <button type="button" className={chipClass} onClick={() => { setOutcome(null); setPending({ kind: 'activate', workflowID: definition.id, version: latest, activeVersion: active }); }}>Activate version {latest}…</button>
        </div>
      ) : null}

      {pending ? (
        <ConfirmVersionAction action={pending} target={byVersion.get(pending.version)} busy={busy} onConfirm={() => void run()} onCancel={() => setPending(null)} />
      ) : null}

      <ol className="grid gap-2" aria-label={`Versions of ${definition.name || definition.id}`}>
        {versions.map((item) => {
          const version = item.version ?? 0;
          const role = versionRole(version, active);
          return (
            <li key={version} data-version={version} data-version-role={role}>
              <HudPanel className="flex flex-wrap items-center justify-between gap-2 bg-[hsl(var(--panel-soft))] p-2">
                <span className="text-xs text-[hsl(var(--ink))]">
                  <span className="font-mono">v{version}</span> · {item.name}
                  <span className="text-[hsl(var(--muted))]"> · {orderedDefinitionSteps(item).length} steps{item.updated_at ? ` · saved ${formatTime(item.updated_at)}` : ''}{version === latest ? ' · latest' : ''}</span>
                </span>
                <span className="flex flex-wrap items-center gap-1.5">
                  <span className={`hud-chip px-2 py-0.5 text-[0.6rem] ${roleClasses(role)}`}>{versionRoleLabels[role]}</span>
                  <button type="button" className={chipClass} aria-label={`Compare version ${version}`} onClick={() => compareWith(version)}>Compare</button>
                  {!readOnly && role === 'earlier' && !pending ? (
                    <button type="button" className={chipClass} onClick={() => { setOutcome(null); setPending({ kind: 'rollback', workflowID: definition.id, version, activeVersion: active, latestVersion: latest }); }}>Roll back to v{version}…</button>
                  ) : null}
                </span>
              </HudPanel>
            </li>
          );
        })}
      </ol>

      <div className="grid gap-2" data-version-compare="true">
        <HudLabel className="text-[hsl(var(--faint))]">Compare versions</HudLabel>
        <div className="grid gap-2 sm:grid-cols-2">
          <label className="block text-xs text-[hsl(var(--muted))]">
            Compare from
            <select className={selectClass} value={baseVersion} onChange={(event) => setBaseVersion(Number(event.target.value))}>
              {versions.map((item) => <option key={item.version} value={item.version}>v{item.version} ({versionRoleLabels[versionRole(item.version ?? 0, active)]})</option>)}
            </select>
          </label>
          <label className="block text-xs text-[hsl(var(--muted))]">
            Compare to
            <select className={selectClass} value={targetVersion} onChange={(event) => setTargetVersion(Number(event.target.value))}>
              {versions.map((item) => <option key={item.version} value={item.version}>v{item.version} ({versionRoleLabels[versionRole(item.version ?? 0, active)]})</option>)}
            </select>
          </label>
        </div>
        {versions.length < 2 ? <HudEmptyState>Only one version is saved, so there is nothing to compare.</HudEmptyState> : base && target ? <VersionComparisonView base={base} target={target} /> : null}
      </div>
    </div>
  );
}
