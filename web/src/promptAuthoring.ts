// Prompt authoring client. Prompts have two owners and no separate store:
// model-step templates belong to workflow-definition versions, and prompt
// contracts belong to agent-profile versions. This module reads the derived
// inventory, renders previews, and saves template edits as workflow drafts.
import { apiPath, arrayOrEmpty, fetchJSON } from './api';
import type { AgentProfile, RunModelPolicy, RunModelPolicyStep, WorkflowDefinition } from './types';
import { loadWorkflowDefinition, saveWorkflowDraft, type SaveDraftResult } from './workflowDrafts';

export type PromptVariable = { name: string; kind: 'input' | 'step-output'; step_id?: string; output?: string; declared: boolean; required: boolean };
export type PromptProblem = { severity: 'error' | 'warning'; message: string };
export type PromptProfileConstraints = {
  profile_id: string;
  name?: string;
  role?: string;
  version: number;
  source?: 'static' | 'persisted';
  status?: AgentProfile['status'];
  default_model?: string;
  allowed_models?: string[];
  allowed_executors: string[];
  prompt_contract: string;
  prompt_contract_sha256: string;
};
export type PromptTemplate = {
  owner: 'workflow-definition';
  workflow_id: string;
  workflow_name: string;
  version: number;
  active_version: number;
  step_id: string;
  step_name: string;
  position: number;
  executor: string;
  profile_id?: string;
  model?: string;
  input: 'prompt' | 'template';
  template: string;
  template_sha256: string;
  active_template_sha256?: string;
  changed_since_active: boolean;
  variables: PromptVariable[];
  problems: PromptProblem[];
};
export type PromptReference = { workflow_id: string; step_id: string; version: number };
export type PromptContractEntry = PromptProfileConstraints & { owner: 'agent-profile'; referenced_by: PromptReference[] };
export type PromptBuiltInStep = { owner: 'built-in'; workflow_id: string; pipeline_id: string; step_id: string; step_name: string; executor: string; profile_id?: string; note: string };
export type PromptInventory = { templates: PromptTemplate[]; contracts: PromptContractEntry[]; built_in: PromptBuiltInStep[] };

export type PromptPreviewRequest = {
  workflow_id: string;
  step_id: string;
  version?: number;
  template?: string;
  inputs?: Record<string, string>;
  step_outputs?: Record<string, Record<string, string>>;
};
export type PromptMissingVariable = { name: string; kind: 'input' | 'step-output'; message: string };
export type PromptPreview = {
  workflow_id: string;
  version: number;
  active_version: number;
  step_id: string;
  step_name: string;
  input: 'prompt' | 'template';
  draft: boolean;
  template_sha256: string;
  profile?: PromptProfileConstraints;
  variables: PromptVariable[];
  problems: PromptProblem[];
  missing_variables: PromptMissingVariable[];
  ok: boolean;
  workflow_request: string;
  rendered: string;
  redacted: boolean;
};

export function promptKey(template: Pick<PromptTemplate, 'workflow_id' | 'step_id'>) {
  return `${template.workflow_id}/${template.step_id}`;
}

export async function loadPromptInventory(): Promise<PromptInventory> {
  const response = await fetchJSON<Partial<PromptInventory>>('/api/prompts');
  return {
    templates: arrayOrEmpty(response.templates).map((template) => ({ ...template, variables: arrayOrEmpty(template.variables), problems: arrayOrEmpty(template.problems) })),
    contracts: arrayOrEmpty(response.contracts).map((contract) => ({ ...contract, referenced_by: arrayOrEmpty(contract.referenced_by) })),
    built_in: arrayOrEmpty(response.built_in),
  };
}

export type PromptPreviewResult = { ok: true; preview: PromptPreview } | { ok: false; kind: 'forbidden' | 'validation' | 'not-found' | 'error'; message: string };

/** POST /api/prompts/preview: a dry run that renders without calling a model. */
export async function previewPrompt(request: PromptPreviewRequest): Promise<PromptPreviewResult> {
  let response: Response;
  try {
    response = await fetch(apiPath('/api/prompts/preview'), { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(request) });
  } catch (error) {
    return { ok: false, kind: 'error', message: error instanceof Error ? error.message : 'The preview request failed.' };
  }
  const payload = (await response.json().catch(() => ({}))) as { preview?: PromptPreview; error?: { message?: string } };
  if (response.ok && payload.preview) {
    const preview = payload.preview;
    return { ok: true, preview: { ...preview, variables: arrayOrEmpty(preview.variables), problems: arrayOrEmpty(preview.problems), missing_variables: arrayOrEmpty(preview.missing_variables) } };
  }
  const message = payload.error?.message ?? `Switchyard API returned ${response.status}`;
  if (response.status === 401 || response.status === 403) return { ok: false, kind: 'forbidden', message: 'Previewing needs an admin credential. This credential can read prompts but not render drafts.' };
  if (response.status === 400) return { ok: false, kind: 'validation', message };
  if (response.status === 404) return { ok: false, kind: 'not-found', message };
  return { ok: false, kind: 'error', message };
}

const tokenPattern = /\{\{([^{}]*)\}\}/g;
const stepReference = /^steps\.([^.\s{}]+)\.outputs\.([a-z][a-z0-9_]*)$/;

export type TemplateStepOutput = { stepID: string; name: string; key: string };

/**
 * Lists the sample values a template needs, mirroring the server: `{{name}}`
 * reads a run input and `{{steps.<id>.outputs.<name>}}` reads an earlier
 * step's output. Malformed tokens are left to the server's problem list.
 */
export function templateVariables(template: string): { inputs: string[]; stepOutputs: TemplateStepOutput[] } {
  const inputs: string[] = [];
  const stepOutputs: TemplateStepOutput[] = [];
  for (const match of template.matchAll(tokenPattern)) {
    const body = match[1].trim();
    if (!body) continue;
    if (body.startsWith('steps.')) {
      const parsed = stepReference.exec(body);
      if (parsed && !stepOutputs.some((item) => item.key === body)) stepOutputs.push({ stepID: parsed[1], name: parsed[2], key: body });
      continue;
    }
    if (!inputs.includes(body)) inputs.push(body);
  }
  return { inputs, stepOutputs };
}

/** Builds the preview body, sending only samples the template reads. */
export function previewRequest(template: PromptTemplate, text: string, samples: Record<string, string>): PromptPreviewRequest {
  const variables = templateVariables(text);
  const inputs: Record<string, string> = {};
  for (const name of variables.inputs) if (name in samples) inputs[name] = samples[name];
  const stepOutputs: Record<string, Record<string, string>> = {};
  for (const output of variables.stepOutputs) {
    if (!(output.key in samples)) continue;
    stepOutputs[output.stepID] = { ...(stepOutputs[output.stepID] ?? {}), [output.name]: samples[output.key] };
  }
  return { workflow_id: template.workflow_id, step_id: template.step_id, template: text, inputs, step_outputs: stepOutputs };
}

/** The template input the model executor reads: `prompt` when set, otherwise `template`. */
export function templateInputOf(inputs: Record<string, string> | undefined): { input: 'prompt' | 'template'; text: string } {
  if ((inputs?.prompt ?? '').trim()) return { input: 'prompt', text: inputs?.prompt ?? '' };
  if ((inputs?.template ?? '').trim()) return { input: 'template', text: inputs?.template ?? '' };
  return { input: 'prompt', text: '' };
}

/** Returns a copy of `definition` with one step's template replaced. */
export function definitionWithTemplate(definition: WorkflowDefinition, stepID: string, text: string): WorkflowDefinition | null {
  const steps = definition.pipeline?.steps ?? [];
  const index = steps.findIndex((step) => step.id === stepID);
  if (index < 0) return null;
  const step = steps[index];
  const { input } = templateInputOf(step.inputs);
  const nextSteps = steps.map((item, position) => (position === index ? { ...item, inputs: { ...(item.inputs ?? {}), [input]: text } } : item));
  return { ...definition, pipeline: { ...definition.pipeline, steps: nextSteps } };
}

export function stepTemplateText(definition: WorkflowDefinition, stepID: string) {
  const step = (definition.pipeline?.steps ?? []).find((item) => item.id === stepID);
  return step ? templateInputOf(step.inputs).text : null;
}

/**
 * Saves an edited template as a new workflow-definition draft version with
 * expected_version = the version the editor opened. It never activates the
 * draft and never dispatches a run.
 */
export async function savePromptDraft(definition: WorkflowDefinition, stepID: string, text: string): Promise<SaveDraftResult> {
  const next = definitionWithTemplate(definition, stepID, text);
  if (!next) return { ok: false, kind: 'conflict', message: `Step "${stepID}" is not in version ${definition.version ?? '?'} of ${definition.id}. Nothing was saved.` };
  return saveWorkflowDraft(next, { creating: false, baseVersion: definition.version });
}

export { loadWorkflowDefinition };

export function shortHash(value?: string) {
  return value ? value.slice(0, 12) : 'none';
}

export function modelConstraint(profile: Pick<PromptProfileConstraints, 'allowed_models' | 'default_model'>) {
  const allowed = arrayOrEmpty(profile.allowed_models);
  const list = allowed.length > 0 ? `allowed: ${allowed.join(', ')}` : 'any model';
  return profile.default_model ? `${list} · default ${profile.default_model}` : list;
}

/** Steps of a run policy that have a prompt, in run order. */
export function promptPolicySteps(policy: RunModelPolicy): RunModelPolicyStep[] {
  return policy.steps.filter((step) => step.prompt_owner);
}

export function definitionBindingLabel(policy: RunModelPolicy) {
  if (policy.definition_version && policy.definition_version > 0) return `Workflow definition ${policy.definition_id ?? policy.workflow_id} version ${policy.definition_version}`;
  return 'No workflow-definition version recorded: a built-in pipeline, or a run created before template versions were captured';
}
