import { APIError, apiPath, arrayOrEmpty, fetchJSON } from './api';
import type { AgentProfile, Run, RunModelPolicy, RunModelPolicyStep, RunModelSource } from './types';

// Profile management client for the #73 versioned agent-profile contract and
// the per-run model policy routes. See docs/reference/agent-profiles.md.

/** Executors a profile may authorize; mirrors knownPipelineExecutors in profiles.go. */
export const profileExecutors = ['internal', 'model', 'opencode', 'command', 'http', 'git', 'gitea'] as const;

export const maxProfileNameLength = 128;
export const maxProfileModelLength = 128;
export const maxProfileContractLength = 16384;
export const maxProfileNoteLength = 1024;

export type AgentProfileIdentity = {
  id: string;
  active_version: number;
  draft_version: number;
  latest_version: number;
  static_default: boolean;
  created_at?: string;
  updated_at?: string;
};

export type AgentProfileVersion = AgentProfile & {
  version: number;
  created_at: string;
  updated_at: string;
  activated_at?: string;
};

export type AgentProfileAuditEntry = {
  id: string;
  profile_id: string;
  version: number;
  action: 'draft-saved' | 'activated' | 'rolled-back';
  actor: string;
  reason?: string;
  prompt_contract_sha256: string;
  previous_active_version: number;
  created_at: string;
};

export type ProfileListEntry = { id: string; profile?: AgentProfile; identity?: AgentProfileIdentity };

export type ProfileDraft = {
  name: string;
  role: string;
  defaultModel: string;
  allowedModels: string;
  allowedExecutors: string[];
  promptContract: string;
  note: string;
};

// ---------------------------------------------------------------------------
// Draft helpers

export function profileDraftFrom(profile: AgentProfile): ProfileDraft {
  return {
    name: profile.name,
    role: profile.role,
    defaultModel: profile.default_model ?? '',
    allowedModels: arrayOrEmpty(profile.allowed_models).join('\n'),
    allowedExecutors: arrayOrEmpty(profile.allowed_executors).slice(),
    promptContract: profile.prompt_contract,
    note: '',
  };
}

/** Splits a comma- or newline-separated model list, dropping blanks and case-insensitive duplicates. */
export function parseModelList(text: string) {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const raw of text.split(/[\n,]/)) {
    const model = raw.trim();
    if (!model || seen.has(model.toLowerCase())) continue;
    seen.add(model.toLowerCase());
    out.push(model);
  }
  return out;
}

export function profileDraftFingerprint(draft: ProfileDraft) {
  return JSON.stringify({ ...draft, allowedModels: parseModelList(draft.allowedModels), allowedExecutors: [...draft.allowedExecutors].sort() });
}

function invalidModelName(model: string) {
  return model.length > maxProfileModelLength || /\s/.test(model) || model === '${model}';
}

/**
 * Checks what the editor can tell before saving. The server applies the same
 * rules (normalizeAgentProfileDraft) and remains authoritative.
 */
export function validateProfileDraft(draft: ProfileDraft) {
  const problems: string[] = [];
  const models = parseModelList(draft.allowedModels);
  if (!draft.name.trim() || draft.name.trim().length > maxProfileNameLength) problems.push(`Name is required and must be at most ${maxProfileNameLength} characters.`);
  if (!draft.role.trim() || draft.role.trim().length > maxProfileNameLength) problems.push(`Role is required and must be at most ${maxProfileNameLength} characters.`);
  if (!draft.promptContract.trim() || draft.promptContract.trim().length > maxProfileContractLength) problems.push(`Prompt contract is required and must be at most ${maxProfileContractLength} characters.`);
  if (draft.note.trim().length > maxProfileNoteLength) problems.push(`Note must be at most ${maxProfileNoteLength} characters.`);
  if (draft.allowedExecutors.length === 0) problems.push('Allow at least one executor.');
  const defaultModel = draft.defaultModel.trim();
  if (defaultModel && invalidModelName(defaultModel)) problems.push(`Default model "${defaultModel}" must be a single token of at most ${maxProfileModelLength} characters.`);
  for (const model of models) {
    if (invalidModelName(model)) problems.push(`Allowed model "${model}" must be a single token of at most ${maxProfileModelLength} characters.`);
  }
  if (defaultModel && models.length > 0 && !models.some((model) => model.toLowerCase() === defaultModel.toLowerCase())) {
    problems.push(`Default model "${defaultModel}" is not in the allowed models.`);
  }
  return problems;
}

export function profileDraftRequest(draft: ProfileDraft, expectedVersion: number) {
  const body: Record<string, unknown> = {
    expected_version: expectedVersion,
    name: draft.name.trim(),
    role: draft.role.trim(),
    default_model: draft.defaultModel.trim(),
    allowed_executors: profileExecutors.filter((executor) => draft.allowedExecutors.includes(executor)),
    prompt_contract: draft.promptContract.trim(),
  };
  const models = parseModelList(draft.allowedModels);
  if (models.length > 0) body.allowed_models = models;
  if (draft.note.trim()) body.note = draft.note.trim();
  return body;
}

// ---------------------------------------------------------------------------
// Version helpers

export function profileVersionLabel(profile: Pick<AgentProfile, 'version' | 'status' | 'source'>) {
  if (!profile.version || profile.source === 'static' || profile.status === 'static') return 'static default';
  return `v${profile.version} ${profile.status ?? ''}`.trim();
}

/** The saved draft that can be activated, if any. */
export function activatableDraft(versions: AgentProfileVersion[], identity?: AgentProfileIdentity) {
  if (!identity?.draft_version) return undefined;
  return versions.find((version) => version.version === identity.draft_version && version.status === 'draft');
}

/** Versions that were active at some point and are not active now (the rollback contract). */
export function rollbackCandidates(versions: AgentProfileVersion[], identity?: AgentProfileIdentity) {
  return versions.filter((version) => Boolean(version.activated_at) && version.version !== identity?.active_version && version.status !== 'active');
}

/** Merges effective profiles with persisted identities that have no active version yet. */
export function profileListEntries(profiles: AgentProfile[], identities: AgentProfileIdentity[]): ProfileListEntry[] {
  const entries = new Map<string, ProfileListEntry>();
  for (const profile of profiles) entries.set(profile.id, { id: profile.id, profile });
  for (const identity of identities) entries.set(identity.id, { ...(entries.get(identity.id) ?? { id: identity.id }), identity });
  return Array.from(entries.values()).sort((left, right) => left.id.localeCompare(right.id));
}

// ---------------------------------------------------------------------------
// Profile API

export async function loadProfileListing() {
  const response = await fetchJSON<{ profiles: AgentProfile[] | null; identities?: AgentProfileIdentity[] | null }>('/api/agent-profiles');
  return profileListEntries(arrayOrEmpty(response.profiles), arrayOrEmpty(response.identities));
}

export async function loadProfileDetail(id: string) {
  return fetchJSON<{ profile: AgentProfile; identity: AgentProfileIdentity }>(`/api/agent-profiles/${encodeURIComponent(id)}`);
}

export async function loadProfileVersions(id: string) {
  const response = await fetchJSON<{ profile_versions: AgentProfileVersion[] | null }>(`/api/agent-profiles/${encodeURIComponent(id)}/versions`);
  return arrayOrEmpty(response.profile_versions);
}

export async function loadProfileAudit(id: string) {
  const response = await fetchJSON<{ audit: AgentProfileAuditEntry[] | null }>(`/api/agent-profiles/${encodeURIComponent(id)}/audit?limit=20`);
  return arrayOrEmpty(response.audit);
}

export type ProfileMutationResult =
  | { ok: true; version: AgentProfileVersion; identity?: AgentProfileIdentity }
  | { ok: false; kind: 'validation' | 'forbidden' | 'conflict' | 'error'; message: string };

async function profileMutation(path: string, method: 'PUT' | 'POST', body: unknown, conflictMessage: string): Promise<ProfileMutationResult> {
  let response: Response;
  try {
    response = await fetch(apiPath(path), { method, headers: { 'content-type': 'application/json' }, body: JSON.stringify(body) });
  } catch {
    return { ok: false, kind: 'error', message: 'Could not reach the control plane. Nothing was changed.' };
  }
  const payload = (await response.json().catch(() => ({}))) as { profile_version?: AgentProfileVersion; identity?: AgentProfileIdentity; error?: { message?: string } };
  if (response.ok && payload.profile_version) return { ok: true, version: payload.profile_version, identity: payload.identity };
  const message = payload.error?.message ?? `Switchyard API returned ${response.status}`;
  if (response.status === 401 || response.status === 403) {
    return { ok: false, kind: 'forbidden', message: 'This credential is read-only. An admin credential is required to change agent profiles. Nothing was changed.' };
  }
  if (response.status === 409) return { ok: false, kind: 'conflict', message: `${conflictMessage} (${message})` };
  if (response.status === 400) return { ok: false, kind: 'validation', message };
  return { ok: false, kind: 'error', message };
}

/** Saves a new draft version. expectedVersion must equal the identity's latest_version (0 before the first save). */
export function saveProfileDraft(id: string, draft: ProfileDraft, expectedVersion: number) {
  return profileMutation(
    `/api/agent-profiles/${encodeURIComponent(id)}`,
    'PUT',
    profileDraftRequest(draft, expectedVersion),
    `Profile "${id}" changed since you opened it; version ${expectedVersion} is no longer the latest. Nothing was saved; your edits are still here. Reload to see the new version`,
  );
}

export function activateProfileVersion(id: string, version: number, reason: string) {
  return profileMutation(`/api/agent-profiles/${encodeURIComponent(id)}/activate`, 'POST', reasonBody(version, reason), `Version ${version} of "${id}" is no longer a draft. Nothing was activated. Reload to see the current versions`);
}

export function rollbackProfileVersion(id: string, version: number, reason: string) {
  return profileMutation(`/api/agent-profiles/${encodeURIComponent(id)}/rollback`, 'POST', reasonBody(version, reason), `Version ${version} of "${id}" cannot be restored now; it may already be active. Nothing was changed. Reload to see the current versions`);
}

function reasonBody(version: number, reason: string) {
  return reason.trim() ? { version, reason: reason.trim() } : { version };
}

// ---------------------------------------------------------------------------
// Run model policy

export const modelSourceLabels: Record<RunModelSource, string> = {
  workflow: 'workflow step',
  override: 'run override',
  'profile-default': 'profile default',
  'executor-default': 'workflow default',
  none: 'no model',
  unrecorded: 'not recorded',
};

export function modelSourceLabel(source?: string) {
  return (source && modelSourceLabels[source as RunModelSource]) || source || 'unknown';
}

/** One sentence describing what a step uses when the run sets no override. */
export function describeInheritedModel(step: RunModelPolicyStep) {
  if (step.error && !step.inherited_model) return `Cannot resolve a model: ${step.error}`;
  if (!step.inherited_model || step.inherited_model_source === 'none') return 'Uses no model.';
  switch (step.inherited_model_source) {
    case 'workflow':
      return `Fixed to ${step.inherited_model} by the workflow step.`;
    case 'profile-default':
      return `Inherits ${step.inherited_model} from profile ${step.profile_id} (${profileVersionLabel({ version: step.profile_version, source: step.profile_source })}).`;
    case 'executor-default':
      return `Inherits ${step.inherited_model}, the workflow's configured default model.`;
    default:
      return `Uses ${step.inherited_model}.`;
  }
}

export function describeOverrideRule(step: RunModelPolicyStep) {
  if (!step.accepts_override) return 'Ignores run overrides.';
  if (!step.override_restricted) return 'Accepts any model override.';
  const allowed = arrayOrEmpty(step.allowed_overrides);
  return allowed.length > 0 ? `Accepts overrides: ${allowed.join(', ')}.` : 'No override is allowed for this step.';
}

/** Mirrors the server's first checks so the form can explain a choice before dispatch. */
export function overrideSelectionProblem(policy: RunModelPolicy, model: string) {
  const value = model.trim();
  if (!value) return '';
  if (invalidModelName(value)) return `The override must be a single token of at most ${maxProfileModelLength} characters.`;
  if (!policy.accepts_override) return `Workflow ${policy.workflow_id} has no step that accepts a run-level model override.`;
  const allowed = arrayOrEmpty(policy.allowed_overrides);
  if (policy.override_restricted && !allowed.some((item) => item.toLowerCase() === value.toLowerCase())) {
    return allowed.length > 0 ? `${value} is not allowed. Allowed overrides: ${allowed.join(', ')}.` : 'No override is allowed for this workflow.';
  }
  return '';
}

export async function loadWorkflowModelPolicy(workflowID: string, model = '') {
  const query = model.trim() ? `?model=${encodeURIComponent(model.trim())}` : '';
  const response = await fetchJSON<{ policy: RunModelPolicy }>(`/api/workflows/${encodeURIComponent(workflowID)}/model-policy${query}`);
  return { ...response.policy, steps: arrayOrEmpty(response.policy.steps) };
}

export type DispatchResult = { ok: true; run: Run } | { ok: false; kind: 'rejected' | 'forbidden' | 'error'; message: string };

/** Dispatches a run with POST /api/runs, adding a model override when one is chosen. */
export async function dispatchRunWithModel(workflowID: string, triggerID: string, inputs: Record<string, string>, model: string): Promise<DispatchResult> {
  const runInputs: Record<string, string> = { ...inputs };
  if (model.trim()) runInputs.model = model.trim();
  else delete runInputs.model;
  let response: Response;
  try {
    response = await fetch(apiPath('/api/runs'), {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ workflow_id: workflowID, ...(triggerID ? { trigger_id: triggerID } : {}), inputs: runInputs }),
    });
  } catch {
    return { ok: false, kind: 'error', message: 'Could not reach the control plane. No run was created.' };
  }
  const payload = (await response.json().catch(() => ({}))) as { run?: Run; error?: { message?: string } };
  if (response.ok && payload.run) return { ok: true, run: payload.run };
  const message = payload.error?.message ?? `Switchyard API returned ${response.status}`;
  if (response.status === 401 || response.status === 403) return { ok: false, kind: 'forbidden', message: 'This credential is read-only. An admin credential is required to dispatch runs.' };
  if (response.status === 400) return { ok: false, kind: 'rejected', message };
  return { ok: false, kind: 'error', message };
}

export function isAPIStatus(error: unknown, ...statuses: number[]) {
  return error instanceof APIError && statuses.includes(error.status);
}
