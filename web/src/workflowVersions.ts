// Workflow version history, comparison, activation, and rollback. Every saved
// version is immutable. The latest version is what the editor edits; the
// active version (WorkflowDefinition.active_version) is what new runs snapshot.
// Activation and rollback go through the backend contract in
// http_workflow_definitions.go, which stays authoritative for validation.
import { apiPath, arrayOrEmpty, fetchJSON } from './api';
import type { JSONShape, PipelineStepDefinition, WorkflowDefinition } from './types';
import { orderedDefinitionSteps } from './workflowDrafts';

// ---------------------------------------------------------------------------
// History

export async function loadWorkflowDefinitionVersions(id: string) {
  const response = await fetchJSON<{ workflow_definitions: WorkflowDefinition[] | null }>(`/api/workflow-definitions/${encodeURIComponent(id)}/versions`);
  return sortVersionsNewestFirst(arrayOrEmpty(response.workflow_definitions));
}

export function sortVersionsNewestFirst(versions: WorkflowDefinition[]) {
  return versions.slice().sort((left, right) => (right.version ?? 0) - (left.version ?? 0));
}

/** The active version number, or 0 when no version serves new runs. */
export function activeVersionOf(versions: WorkflowDefinition[]) {
  const pointer = versions.find((version) => (version.active_version ?? 0) > 0)?.active_version;
  if (pointer) return pointer;
  return versions.find((version) => version.status === 'active')?.version ?? 0;
}

export function latestVersionOf(versions: WorkflowDefinition[]) {
  return versions.reduce((latest, version) => Math.max(latest, version.version ?? 0), 0);
}

/**
 * active: serves new runs. draft: saved after the active version (or with none
 * active) and never activated. earlier: older than the active version.
 */
export type VersionRole = 'active' | 'draft' | 'earlier';

export function versionRole(version: number, activeVersion: number): VersionRole {
  if (activeVersion > 0 && version === activeVersion) return 'active';
  if (activeVersion === 0 || version > activeVersion) return 'draft';
  return 'earlier';
}

export const versionRoleLabels: Record<VersionRole, string> = { active: 'active', draft: 'draft', earlier: 'earlier' };

// ---------------------------------------------------------------------------
// Comparison

export type FieldChange = { field: string; before: string; after: string };
export type StepChange =
  | { kind: 'added'; stepID: string; after: PipelineStepDefinition }
  | { kind: 'removed'; stepID: string; before: PipelineStepDefinition }
  | { kind: 'changed'; stepID: string; before: PipelineStepDefinition; after: PipelineStepDefinition; fields: FieldChange[] }
  | { kind: 'unchanged'; stepID: string; after: PipelineStepDefinition };
export type VersionComparison = { metadata: FieldChange[]; steps: StepChange[]; changed: boolean };

function listText(values?: string[]) {
  return arrayOrEmpty(values).join(', ');
}

function inputFieldsText(shape?: JSONShape) {
  const required = new Set(arrayOrEmpty(shape?.required));
  return Object.entries(shape?.properties ?? {})
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([name, property]) => `${name}: ${property.type ?? ''}${required.has(name) ? ' (required)' : ''}`)
    .join('\n');
}

/** Deterministic JSON so that key order never shows up as a change. */
export function stableJSON(value: unknown): string {
  if (value === undefined) return '';
  return JSON.stringify(value, (_key, item: unknown) => {
    if (!item || typeof item !== 'object' || Array.isArray(item)) return item;
    return Object.fromEntries(Object.entries(item as Record<string, unknown>).sort(([left], [right]) => left.localeCompare(right)));
  });
}

function changes(pairs: Array<[string, string, string]>): FieldChange[] {
  return pairs.filter(([, before, after]) => before !== after).map(([field, before, after]) => ({ field, before, after }));
}

function stepFieldChanges(before: PipelineStepDefinition, after: PipelineStepDefinition): FieldChange[] {
  const inputNames = [...new Set([...Object.keys(before.inputs ?? {}), ...Object.keys(after.inputs ?? {})])].sort();
  return changes([
    ['position', String(before.position), String(after.position)],
    ['name', before.name, after.name],
    ['executor', before.executor, after.executor],
    ['profile', before.profile ?? '', after.profile ?? ''],
    ['model', before.model ?? '', after.model ?? ''],
    ...inputNames.map((name): [string, string, string] => [`inputs.${name}`, before.inputs?.[name] ?? '', after.inputs?.[name] ?? '']),
    ['output_shape', stableJSON(before.output_shape), stableJSON(after.output_shape)],
  ]);
}

/** Metadata and step differences from `base` to `target`, steps matched by ID in target run order. */
export function compareWorkflowVersions(base: WorkflowDefinition, target: WorkflowDefinition): VersionComparison {
  const metadata = changes([
    ['name', base.name ?? '', target.name ?? ''],
    ['description', base.description ?? '', target.description ?? ''],
    ['owns', listText(base.owns), listText(target.owns)],
    ['uses', listText(base.uses), listText(target.uses)],
    ['input fields', inputFieldsText(base.input_shape), inputFieldsText(target.input_shape)],
    ['pipeline.id', base.pipeline?.id ?? '', target.pipeline?.id ?? ''],
    ['pipeline.name', base.pipeline?.name ?? '', target.pipeline?.name ?? ''],
    ['pipeline.description', base.pipeline?.description ?? '', target.pipeline?.description ?? ''],
  ]);
  const baseSteps = new Map(orderedDefinitionSteps(base).map((step) => [step.id, step]));
  const targetSteps = orderedDefinitionSteps(target);
  const targetIDs = new Set(targetSteps.map((step) => step.id));
  const steps: StepChange[] = targetSteps.map((after) => {
    const before = baseSteps.get(after.id);
    if (!before) return { kind: 'added', stepID: after.id, after };
    const fields = stepFieldChanges(before, after);
    return fields.length > 0 ? { kind: 'changed', stepID: after.id, before, after, fields } : { kind: 'unchanged', stepID: after.id, after };
  });
  for (const before of baseSteps.values()) {
    if (!targetIDs.has(before.id)) steps.push({ kind: 'removed', stepID: before.id, before });
  }
  return { metadata, steps, changed: metadata.length > 0 || steps.some((step) => step.kind !== 'unchanged') };
}

// ---------------------------------------------------------------------------
// Activation checks and consequences

/**
 * Mirrors validateWorkflowDefinitionStructure(requireExecutable=true) for the
 * settings a draft may leave empty. The server repeats every check.
 */
export function activationProblems(definition: WorkflowDefinition): string[] {
  const problems: string[] = [];
  const steps = orderedDefinitionSteps(definition);
  if (steps.length === 0) problems.push('The pipeline has no steps.');
  for (const step of steps) {
    const inputs = step.inputs ?? {};
    const label = `Step "${step.id}"`;
    switch (step.executor.trim()) {
      case 'internal':
      case 'source':
        break;
      case 'model':
        if (![inputs.prompt, inputs.template].some((value) => (value ?? '').trim())) problems.push(`${label}: the model executor needs a prompt or template input.`);
        break;
      case 'command':
        if (!(inputs.command ?? '').trim()) problems.push(`${label}: the command executor needs a command input.`);
        break;
      case 'http':
        if (!(inputs.url ?? '').trim()) problems.push(`${label}: the http executor needs a url input.`);
        if ((inputs.allow_http ?? '').trim().toLowerCase() !== 'true') problems.push(`${label}: the http executor needs allow_http set to "true".`);
        break;
      default:
        problems.push(`${label}: executor "${step.executor}" is not supported.`);
    }
  }
  return problems;
}

export type VersionAction =
  | { kind: 'activate'; workflowID: string; version: number; activeVersion: number }
  | { kind: 'rollback'; workflowID: string; version: number; activeVersion: number; latestVersion: number };

/** Plain statements of what the action changes, shown before confirmation. */
export function actionConsequences(action: VersionAction): string[] {
  const { workflowID, version, activeVersion } = action;
  const lines: string[] = [];
  if (action.kind === 'activate') {
    lines.push(activeVersion > 0
      ? `New runs of ${workflowID} will use version ${version} instead of version ${activeVersion}.`
      : `New runs of ${workflowID} will use version ${version}. No version is active now, so new runs currently use the workflow's built-in pipeline or executor.`);
    if (activeVersion > 0) lines.push(`Version ${activeVersion} stays in the history and can be restored with a rollback.`);
  } else {
    const next = action.latestVersion + 1;
    lines.push(`Creates version ${next} as a copy of version ${version} and makes it active. New runs of ${workflowID} will use version ${next}${activeVersion > 0 ? ` instead of version ${activeVersion}` : ''}.`);
    lines.push('No version is changed or deleted; all existing versions stay in the history.');
    if (action.latestVersion > activeVersion && activeVersion > 0) lines.push(`Draft version ${action.latestVersion} stays saved but is no longer the latest version; its edits are not included.`);
  }
  lines.push('Runs already created, including queued and running ones, keep the pipeline snapshot they were created with.');
  return lines;
}

// ---------------------------------------------------------------------------
// API

export type VersionActionResult =
  | { ok: true; definition: WorkflowDefinition; previousActiveVersion: number }
  | { ok: false; kind: 'validation' | 'forbidden' | 'conflict' | 'not-found' | 'error'; message: string };

async function postVersionAction(path: string, body: unknown): Promise<VersionActionResult> {
  let response: Response;
  try {
    response = await fetch(apiPath(path), { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body) });
  } catch (error) {
    return { ok: false, kind: 'error', message: error instanceof Error ? error.message : 'The request failed.' };
  }
  const payload = (await response.json().catch(() => ({}))) as { workflow_definition?: WorkflowDefinition; previous_active_version?: number; error?: { message?: string } };
  if (response.ok && payload.workflow_definition) return { ok: true, definition: payload.workflow_definition, previousActiveVersion: payload.previous_active_version ?? 0 };
  const message = payload.error?.message ?? `Switchyard API returned ${response.status}`;
  if (response.status === 401 || response.status === 403) return { ok: false, kind: 'forbidden', message: 'This credential is read-only. An admin credential is required to activate or roll back workflow versions. Nothing changed.' };
  if (response.status === 400) return { ok: false, kind: 'validation', message };
  if (response.status === 404) return { ok: false, kind: 'not-found', message };
  if (response.status === 409) return { ok: false, kind: 'conflict', message };
  return { ok: false, kind: 'error', message };
}

/** POST .../activate {"version": N}: activates N only if it is still the latest version. */
export function activateWorkflowVersion(id: string, version: number) {
  return postVersionAction(`/api/workflow-definitions/${encodeURIComponent(id)}/activate`, { version });
}

/** POST .../rollback: appends a copy of `version` as the new active version if `expectedVersion` is still the latest. */
export function rollbackWorkflowVersion(id: string, version: number, expectedVersion: number) {
  return postVersionAction(`/api/workflow-definitions/${encodeURIComponent(id)}/rollback`, { version, expected_version: expectedVersion });
}
