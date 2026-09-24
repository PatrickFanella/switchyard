// Workflow catalog and draft-editor model. The editor works on a form model
// (WorkflowDraft) and converts to and from the backend WorkflowDefinition
// contract served by /api/workflow-definitions. Server-side validation stays
// authoritative; the checks here only stop drafts that cannot be encoded.
import { apiPath, APIError, arrayOrEmpty, fetchJSON } from './api';
import type { AgentProfile, JSONShape, JSONShapeProperty, PipelineStepDefinition, WorkflowDefinition } from './types';

// ---------------------------------------------------------------------------
// Contract constants

/** Executors a generic workflow-definition step may use (see validateWorkflowDefinitionStructure). */
export const definitionExecutors = ['internal', 'model', 'command', 'http'] as const;
export type DefinitionExecutor = (typeof definitionExecutors)[number];

export const inputFieldTypes = ['string', 'number', 'integer', 'boolean', 'array', 'object'] as const;
export const outputTypes = inputFieldTypes;

export type ExecutorSetting = { name: string; label: string; multiline?: boolean; checkbox?: boolean; hint?: string };

/** Step inputs each executor reads, in display order (backend/internal/controlplane/executor.go). */
export const executorSettings: Record<DefinitionExecutor, ExecutorSetting[]> = {
  internal: [
    { name: 'summary', label: 'Summary', multiline: true, hint: 'Published as the summary output.' },
    { name: 'artifact_name', label: 'Artifact name' },
    { name: 'artifact_kind', label: 'Artifact kind', hint: 'Defaults to json.' },
  ],
  model: [{ name: 'prompt', label: 'Prompt', multiline: true, hint: 'Required before activation.' }],
  command: [{ name: 'command', label: 'Command', hint: 'Runs only if the command allowlist permits it. Required before activation.' }],
  http: [
    { name: 'url', label: 'URL', hint: 'The host must be listed in SWITCHYARD_HTTP_ALLOWED_HOSTS. Required before activation.' },
    { name: 'allow_http', label: 'Allow HTTP request', checkbox: true, hint: 'Must be enabled before activation.' },
    { name: 'method', label: 'Method', hint: 'Defaults to GET.' },
    { name: 'body', label: 'Body', multiline: true },
    { name: 'accept', label: 'Accept header' },
    { name: 'content_type', label: 'Content-Type header' },
    { name: 'artifact_name', label: 'Artifact name' },
    { name: 'artifact_kind', label: 'Artifact kind' },
  ],
};

/** Outputs each executor publishes on success (docs/reference/step-outputs.md). */
export const executorPublishedOutputs: Record<DefinitionExecutor, string[]> = {
  internal: ['summary', 'artifact_name'],
  model: ['content', 'content_bytes', 'content_sha256', 'model', 'artifact_name'],
  command: ['output', 'output_bytes', 'output_sha256', 'command', 'executed', 'policy_reason'],
  http: ['status', 'content_type', 'body', 'body_bytes', 'body_sha256', 'url', 'method', 'artifact_name'],
};

export const internalOutputInputPrefix = 'output.';
const outputNamePattern = /^[a-z][a-z0-9_]{0,63}$/;
const credentialMarkers = ['password', 'secret', 'token', 'authorization', 'api_key', 'apikey', 'credential'];
const newWorkflowIDPattern = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;

export function isDefinitionExecutor(value: string): value is DefinitionExecutor {
  return (definitionExecutors as readonly string[]).includes(value);
}

export function validOutputName(name: string) {
  return outputNamePattern.test(name) && !credentialMarkers.some((marker) => name.includes(marker));
}

// ---------------------------------------------------------------------------
// Form model

export type DraftInputField = { key: string; name: string; type: string; description: string; required: boolean; extra: Record<string, unknown> };
export type DraftStepInput = { key: string; name: string; value: string };
export type DraftOutput = { key: string; name: string; type: string; description: string; required: boolean; maxLength: string; enumValues: string; maxItems: string; keywords: string };
export type DraftStep = { key: string; id: string; name: string; executor: string; profile: string; model: string; inputs: DraftStepInput[]; outputs: DraftOutput[]; outputDescription: string };
export type WorkflowDraft = {
  id: string;
  name: string;
  description: string;
  owns: string;
  uses: string;
  pipelineID: string;
  pipelineName: string;
  pipelineDescription: string;
  inputFields: DraftInputField[];
  steps: DraftStep[];
};

let keySequence = 0;
export function nextDraftKey(prefix = 'k') {
  keySequence += 1;
  return `${prefix}-${keySequence}`;
}

export function emptyStep(executor: DefinitionExecutor = 'internal', id = ''): DraftStep {
  return { key: nextDraftKey('step'), id, name: id, executor, profile: '', model: '', inputs: [], outputs: [], outputDescription: '' };
}

export function emptyOutput(): DraftOutput {
  return { key: nextDraftKey('out'), name: '', type: 'string', description: '', required: false, maxLength: '', enumValues: '', maxItems: '', keywords: '' };
}

export function emptyInputField(): DraftInputField {
  return { key: nextDraftKey('in'), name: '', type: 'string', description: '', required: false, extra: {} };
}

export function emptyDraft(): WorkflowDraft {
  return { id: '', name: '', description: '', owns: '', uses: '', pipelineID: '', pipelineName: '', pipelineDescription: '', inputFields: [], steps: [emptyStep('internal', 'start')] };
}

function listText(values?: string[]) {
  return arrayOrEmpty(values).join(', ');
}

function textList(value: string) {
  return value.split(',').map((item) => item.trim()).filter(Boolean);
}

const modelledOutputKeywords = new Set(['type', 'description', 'maxLength', 'enum', 'maxItems']);

function outputFromProperty(name: string, property: JSONShapeProperty, required: boolean): DraftOutput {
  // Keywords without a dedicated control (nested properties, items, and any
  // keyword outside the v1 subset) stay visible as JSON so nothing is dropped.
  const keywords = Object.fromEntries(Object.entries(property).filter(([keyword]) => !modelledOutputKeywords.has(keyword)));
  return {
    key: nextDraftKey('out'),
    name,
    type: property.type ?? '',
    description: property.description ?? '',
    required,
    maxLength: property.maxLength === undefined ? '' : String(property.maxLength),
    enumValues: arrayOrEmpty(property.enum).join(', '),
    maxItems: property.maxItems === undefined ? '' : String(property.maxItems),
    keywords: Object.keys(keywords).length > 0 ? JSON.stringify(keywords, null, 2) : '',
  };
}

function orderedSteps(definition: WorkflowDefinition) {
  return arrayOrEmpty(definition.pipeline?.steps).slice().sort((left, right) => left.position - right.position);
}

export function draftFromDefinition(definition: WorkflowDefinition): WorkflowDraft {
  const inputShape = definition.input_shape;
  const inputRequired = new Set(arrayOrEmpty(inputShape?.required));
  return {
    id: definition.id,
    name: definition.name ?? '',
    description: definition.description ?? '',
    owns: listText(definition.owns),
    uses: listText(definition.uses),
    pipelineID: definition.pipeline?.id ?? '',
    pipelineName: definition.pipeline?.name ?? '',
    pipelineDescription: definition.pipeline?.description ?? '',
    inputFields: Object.entries(inputShape?.properties ?? {}).map(([name, property]) => {
      const { type, description, ...extra } = property;
      return { key: nextDraftKey('in'), name, type: type ?? '', description: description ?? '', required: inputRequired.has(name), extra };
    }),
    steps: orderedSteps(definition).map((step) => {
      const outputRequired = new Set(arrayOrEmpty(step.output_shape?.required));
      return {
        key: nextDraftKey('step'),
        id: step.id,
        name: step.name,
        executor: step.executor,
        profile: step.profile ?? '',
        model: step.model ?? '',
        inputs: Object.entries(step.inputs ?? {}).map(([name, value]) => ({ key: nextDraftKey('input'), name, value })),
        outputs: Object.entries(step.output_shape?.properties ?? {}).map(([name, property]) => outputFromProperty(name, property, outputRequired.has(name))),
        outputDescription: step.output_shape?.description ?? '',
      };
    }),
  };
}

/** A new draft pre-filled from an existing definition under a new, unused ID. */
export function duplicateDraft(definition: WorkflowDefinition, existingIDs: Iterable<string>): WorkflowDraft {
  const taken = new Set(existingIDs);
  let id = `${definition.id}-copy`;
  for (let suffix = 2; taken.has(id); suffix += 1) id = `${definition.id}-copy-${suffix}`;
  const draft = draftFromDefinition(definition);
  return { ...draft, id, name: `${definition.name} (copy)`, pipelineID: '', pipelineName: '' };
}

export function draftFingerprint(draft: WorkflowDraft) {
  // Keys are React identities, not content; exclude them from dirty checks.
  return JSON.stringify(draft, (name, value) => (name === 'key' ? undefined : value));
}

// ---------------------------------------------------------------------------
// Step inputs and output bindings

export function stepInput(step: DraftStep, name: string) {
  return step.inputs.find((input) => input.name === name)?.value ?? '';
}

export function withStepInput(step: DraftStep, name: string, value: string): DraftStep {
  const existing = step.inputs.find((input) => input.name === name);
  if (value === '') return { ...step, inputs: step.inputs.filter((input) => input.name !== name) };
  if (existing) return { ...step, inputs: step.inputs.map((input) => (input.name === name ? { ...input, value } : input)) };
  return { ...step, inputs: [...step.inputs, { key: nextDraftKey('input'), name, value }] };
}

/** Inputs without a dedicated executor setting, shown in the free-form list. */
export function additionalStepInputs(step: DraftStep) {
  const known = new Set((isDefinitionExecutor(step.executor) ? executorSettings[step.executor] : []).map((setting) => setting.name));
  return step.inputs.filter((input) => !known.has(input.name));
}

/** The agreed reference syntax, `{{steps.<step_id>.outputs.<name>}}`. */
export function stepOutputReference(stepID: string, name: string) {
  return `{{steps.${stepID}.outputs.${name}}}`;
}

const referencePattern = /\{\{\s*steps\.([^}]*?)\s*\}\}/g;
const wellFormedReference = /^([^.\s{}]+)\.outputs\.([^.\s{}]+)$/;

export type StepOutputReference = { stepID: string; name: string; text: string };

export function parseStepOutputReferences(value: string): { references: StepOutputReference[]; malformed: string[] } {
  const references: StepOutputReference[] = [];
  const malformed: string[] = [];
  for (const match of value.matchAll(referencePattern)) {
    const parsed = wellFormedReference.exec(match[1]);
    if (parsed) references.push({ stepID: parsed[1], name: parsed[2], text: match[0] });
    else malformed.push(match[0]);
  }
  return { references, malformed };
}

export type StepOutputOption = { stepID: string; stepPosition: number; name: string; source: 'declared' | 'executor' | 'internal-input'; type?: string; reference: string };

/** Outputs a step at `stepIndex` may bind: every output of an earlier step. */
export function stepOutputOptions(draft: WorkflowDraft, stepIndex: number): StepOutputOption[] {
  const options: StepOutputOption[] = [];
  draft.steps.slice(0, Math.max(0, stepIndex)).forEach((step, index) => {
    const stepID = step.id.trim();
    if (!stepID) return;
    const seen = new Set<string>();
    const add = (name: string, source: StepOutputOption['source'], type?: string) => {
      if (!name || seen.has(name)) return;
      seen.add(name);
      options.push({ stepID, stepPosition: index + 1, name, source, type, reference: stepOutputReference(stepID, name) });
    };
    step.outputs.forEach((output) => add(output.name.trim(), 'declared', output.type));
    if (step.executor === 'internal') {
      step.inputs.forEach((input) => { if (input.name.startsWith(internalOutputInputPrefix)) add(input.name.slice(internalOutputInputPrefix.length), 'internal-input'); });
    }
    (isDefinitionExecutor(step.executor) ? executorPublishedOutputs[step.executor] : []).forEach((name) => add(name, 'executor'));
  });
  return options;
}

/** References that the backend will reject: unknown, same, or later steps, and malformed syntax. */
export function stepReferenceProblems(draft: WorkflowDraft, stepIndex: number) {
  const step = draft.steps[stepIndex];
  if (!step) return [];
  const positions = new Map(draft.steps.map((item, index) => [item.id.trim(), index]));
  const problems: string[] = [];
  for (const input of step.inputs) {
    const { references, malformed } = parseStepOutputReferences(input.value);
    malformed.forEach((text) => problems.push(`Input "${input.name}": ${text} is not a {{steps.<step_id>.outputs.<name>}} reference.`));
    for (const reference of references) {
      const position = positions.get(reference.stepID);
      if (position === undefined) problems.push(`Input "${input.name}": step "${reference.stepID}" does not exist.`);
      else if (position >= stepIndex) problems.push(`Input "${input.name}": step "${reference.stepID}" does not run before this step.`);
    }
  }
  return problems;
}

export function profilesForExecutor(profiles: AgentProfile[], executor: string) {
  const wanted = executor.trim().toLowerCase();
  return profiles.filter((profile) => arrayOrEmpty(profile.allowed_executors).some((allowed) => allowed.trim().toLowerCase() === wanted));
}

/** Mirrors profileAllowsModel: an empty allowlist or the ${model} placeholder always passes. */
export function profileAllowsModel(profile: AgentProfile | undefined, model: string) {
  const value = model.trim();
  if (!profile || value === '' || value === '${model}') return true;
  const allowed = arrayOrEmpty(profile.allowed_models);
  return allowed.length === 0 || allowed.some((item) => item.trim().toLowerCase() === value.toLowerCase());
}

// ---------------------------------------------------------------------------
// Conversion to the backend contract

function nonNegativeInteger(value: string) {
  if (!/^\d+$/.test(value.trim())) return undefined;
  return Number(value.trim());
}

export type DraftConversion = { definition: WorkflowDefinition; errors: string[] };

export function definitionFromDraft(draft: WorkflowDraft): DraftConversion {
  const errors: string[] = [];
  const id = draft.id.trim();
  const name = draft.name.trim();

  const inputProperties: Record<string, JSONShapeProperty> = {};
  const inputRequired: string[] = [];
  draft.inputFields.forEach((field, index) => {
    const fieldName = field.name.trim();
    if (!fieldName) { errors.push(`Input field ${index + 1}: name is required.`); return; }
    if (inputProperties[fieldName]) { errors.push(`Input field "${fieldName}" is declared more than once.`); return; }
    inputProperties[fieldName] = { ...field.extra, type: field.type, ...(field.description.trim() ? { description: field.description.trim() } : {}) } as JSONShapeProperty;
    if (field.required) inputRequired.push(fieldName);
  });

  const steps: PipelineStepDefinition[] = draft.steps.map((step, index) => {
    const stepID = step.id.trim();
    const label = stepID ? `Step "${stepID}"` : `Step ${index + 1}`;
    const inputs: Record<string, string> = {};
    for (const input of step.inputs) {
      const inputName = input.name.trim();
      if (!inputName) { if (input.value !== '') errors.push(`${label}: an input value has no name.`); continue; }
      if (inputName in inputs) errors.push(`${label}: input "${inputName}" is set more than once.`);
      inputs[inputName] = input.value;
    }
    const properties: Record<string, JSONShapeProperty> = {};
    const required: string[] = [];
    step.outputs.forEach((output, outputIndex) => {
      const outputName = output.name.trim();
      if (!outputName) { errors.push(`${label} output ${outputIndex + 1}: name is required.`); return; }
      if (properties[outputName]) { errors.push(`${label}: output "${outputName}" is declared more than once.`); return; }
      let keywords: Record<string, unknown> = {};
      if (output.keywords.trim()) {
        try {
          const parsed: unknown = JSON.parse(output.keywords);
          if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error('not an object');
          keywords = parsed as Record<string, unknown>;
        } catch {
          errors.push(`${label} output "${outputName}": additional keywords must be a JSON object.`);
        }
      }
      const property: Record<string, unknown> = { ...keywords, type: output.type };
      if (output.description.trim()) property.description = output.description.trim();
      if (output.maxLength.trim()) {
        const value = nonNegativeInteger(output.maxLength);
        if (value === undefined) errors.push(`${label} output "${outputName}": maxLength must be a whole number.`);
        else property.maxLength = value;
      }
      if (output.maxItems.trim()) {
        const value = nonNegativeInteger(output.maxItems);
        if (value === undefined) errors.push(`${label} output "${outputName}": maxItems must be a whole number.`);
        else property.maxItems = value;
      }
      const enumValues = textList(output.enumValues);
      if (enumValues.length > 0) property.enum = enumValues;
      properties[outputName] = property as JSONShapeProperty;
      if (output.required) required.push(outputName);
    });
    const outputShape: JSONShape | undefined = step.outputs.length > 0 || step.outputDescription.trim()
      ? { type: 'object', ...(step.outputDescription.trim() ? { description: step.outputDescription.trim() } : {}), properties, ...(required.length > 0 ? { required } : {}) }
      : undefined;
    return {
      id: stepID,
      position: index + 1,
      name: step.name.trim(),
      executor: step.executor,
      ...(step.profile.trim() ? { profile: step.profile.trim() } : {}),
      ...(step.model.trim() ? { model: step.model.trim() } : {}),
      ...(Object.keys(inputs).length > 0 ? { inputs } : {}),
      ...(outputShape ? { output_shape: outputShape } : {}),
    };
  });

  const inputShape: JSONShape | undefined = draft.inputFields.length > 0
    ? { type: 'object', properties: inputProperties, ...(inputRequired.length > 0 ? { required: inputRequired } : {}) }
    : undefined;

  return {
    errors,
    definition: {
      id,
      name,
      description: draft.description.trim(),
      status: 'draft',
      owns: textList(draft.owns),
      uses: textList(draft.uses),
      ...(inputShape ? { input_shape: inputShape } : {}),
      pipeline: {
        id: draft.pipelineID.trim() || (id ? `${id}-pipeline` : ''),
        workflow_id: id,
        name: draft.pipelineName.trim() || name,
        ...(draft.pipelineDescription.trim() ? { description: draft.pipelineDescription.trim() } : {}),
        steps,
      },
    },
  };
}

/** Checks the backend cannot report usefully (encoding problems and new-ID collisions). */
export function validateDraftForSave(draft: WorkflowDraft, options: { creating: boolean; existingIDs: Iterable<string> }): DraftConversion {
  const conversion = definitionFromDraft(draft);
  const errors = [...conversion.errors];
  const id = draft.id.trim();
  if (!id) errors.unshift('Workflow ID is required.');
  else if (options.creating && !newWorkflowIDPattern.test(id)) errors.unshift('Workflow ID may use letters, digits, ".", "_" and "-", and must start with a letter or digit.');
  else if (options.creating && new Set(options.existingIDs).has(id)) errors.unshift(`A workflow definition with ID "${id}" already exists. Choose another ID or edit that workflow.`);
  return { definition: conversion.definition, errors };
}

// ---------------------------------------------------------------------------
// Catalog

export type CatalogStatusFilter = 'all' | 'active' | 'draft';

/** True when some version of the definition serves new runs. */
export function hasActiveVersion(definition: WorkflowDefinition) {
  return (definition.active_version ?? 0) > 0 || definition.status === 'active';
}

/** True when the latest saved version is a draft that is not active. */
export function hasPendingDraft(definition: WorkflowDefinition) {
  return definition.status === 'draft';
}

export function matchesCatalogStatus(definition: WorkflowDefinition, status: CatalogStatusFilter) {
  if (status === 'active') return hasActiveVersion(definition);
  if (status === 'draft') return hasPendingDraft(definition);
  return true;
}

export function filterWorkflowCatalog(definitions: WorkflowDefinition[], query: string, status: CatalogStatusFilter) {
  const terms = query.trim().toLowerCase().split(/\s+/).filter(Boolean);
  return definitions
    .filter((definition) => matchesCatalogStatus(definition, status))
    .filter((definition) => {
      if (terms.length === 0) return true;
      const haystack = [definition.id, definition.name, definition.description, definition.status, ...orderedSteps(definition).flatMap((step) => [step.id, step.name, step.executor, step.profile ?? ''])].join(' ').toLowerCase();
      return terms.every((term) => haystack.includes(term));
    })
    .sort((left, right) => left.name.localeCompare(right.name) || left.id.localeCompare(right.id));
}

export { orderedSteps as orderedDefinitionSteps };

// ---------------------------------------------------------------------------
// Routes: #/workflows, #/workflows/view/<id>, #/workflows/edit/<id>,
// #/workflows/new, #/workflows/duplicate/<id>

export type WorkflowRoute = { mode: 'catalog' | 'view' | 'edit' | 'new' | 'duplicate'; id: string };

export function parseWorkflowHash(hash: string): WorkflowRoute {
  const parts = hash.replace(/^#\/?/, '').split('/').filter(Boolean);
  if (parts[0] !== 'workflows') return { mode: 'catalog', id: '' };
  const [, mode, rawID] = parts;
  let id = '';
  try { id = rawID ? decodeURIComponent(rawID) : ''; } catch { id = ''; }
  if (mode === 'new') return { mode: 'new', id: '' };
  if ((mode === 'view' || mode === 'edit' || mode === 'duplicate') && id) return { mode, id };
  return { mode: 'catalog', id: '' };
}

export function formatWorkflowHash(route: WorkflowRoute) {
  if (route.mode === 'catalog') return '#/workflows';
  if (route.mode === 'new') return '#/workflows/new';
  return `#/workflows/${route.mode}/${encodeURIComponent(route.id)}`;
}

// ---------------------------------------------------------------------------
// API

export type ControlAccess = { role: 'admin' | 'read' | 'unrestricted' | 'unknown'; can_write: boolean };

export async function loadWorkflowDefinitions() {
  const response = await fetchJSON<{ workflow_definitions: WorkflowDefinition[] | null }>('/api/workflow-definitions');
  return arrayOrEmpty(response.workflow_definitions);
}

export async function loadWorkflowDefinition(id: string) {
  const response = await fetchJSON<{ workflow_definition: WorkflowDefinition }>(`/api/workflow-definitions/${encodeURIComponent(id)}`);
  return response.workflow_definition;
}

export async function loadEditorProfiles() {
  const response = await fetchJSON<{ profiles: AgentProfile[] | null }>('/api/agent-profiles');
  return arrayOrEmpty(response.profiles);
}

/** Older backends lack /api/control-access; treat that as unknown and rely on 401/403 on save. */
export async function loadControlAccess(): Promise<ControlAccess> {
  try {
    return await fetchJSON<ControlAccess>('/api/control-access');
  } catch (error) {
    if (error instanceof APIError && (error.status === 401 || error.status === 403)) return { role: 'unknown', can_write: false };
    return { role: 'unknown', can_write: true };
  }
}

export type SaveDraftResult =
  | { ok: true; definition: WorkflowDefinition }
  | { ok: false; kind: 'validation' | 'forbidden' | 'conflict' | 'error'; message: string; currentVersion?: number };

async function currentDefinition(id: string): Promise<WorkflowDefinition | null | 'error'> {
  try {
    return await loadWorkflowDefinition(id);
  } catch (error) {
    return error instanceof APIError && error.status === 404 ? null : 'error';
  }
}

/**
 * Saves a draft with PUT /api/workflow-definitions/{id}. Saving never activates
 * and never changes the active version. The request carries expected_version
 * (0 to create, otherwise the version the editor opened), so the server rejects
 * a stale write with 409. The re-read beforehand only produces a clearer message
 * for the common case.
 */
export async function saveWorkflowDraft(definition: WorkflowDefinition, expectation: { creating: true } | { creating: false; baseVersion?: number }): Promise<SaveDraftResult> {
  const current = await currentDefinition(definition.id);
  if (current === 'error') return { ok: false, kind: 'error', message: 'Could not re-read the workflow definition before saving. Nothing was saved; try again.' };
  if (expectation.creating && current) {
    return { ok: false, kind: 'conflict', message: `A workflow definition with ID "${definition.id}" already exists. Nothing was saved; choose another ID.`, currentVersion: current.version };
  }
  if (!expectation.creating) {
    if (!current) return { ok: false, kind: 'conflict', message: `Workflow definition "${definition.id}" no longer exists. Nothing was saved.` };
    if (expectation.baseVersion !== undefined && current.version !== undefined && current.version !== expectation.baseVersion) {
      return { ok: false, kind: 'conflict', message: `Workflow definition "${definition.id}" changed since you opened it (version ${expectation.baseVersion} → ${current.version}). Nothing was saved; your edits are still here.`, currentVersion: current.version };
    }
  }
  const expectedVersion = expectation.creating ? 0 : expectation.baseVersion;
  const response = await fetch(apiPath(`/api/workflow-definitions/${encodeURIComponent(definition.id)}`), {
    method: 'PUT',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ ...definition, status: 'draft', ...(expectedVersion === undefined ? {} : { expected_version: expectedVersion }) }),
  });
  const body = (await response.json().catch(() => ({}))) as { workflow_definition?: WorkflowDefinition; error?: { message?: string } };
  if (response.ok && body.workflow_definition) return { ok: true, definition: body.workflow_definition };
  const message = body.error?.message ?? `Switchyard API returned ${response.status}`;
  if (response.status === 401 || response.status === 403) {
    return { ok: false, kind: 'forbidden', message: 'This credential is read-only. An admin credential is required to save workflow drafts. Nothing was saved.' };
  }
  if (response.status === 400) return { ok: false, kind: 'validation', message };
  if (response.status === 409) return { ok: false, kind: 'conflict', message: `${message}. Nothing was saved; your edits are still here.` };
  return { ok: false, kind: 'error', message };
}
