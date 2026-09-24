import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  activatableDraft,
  describeInheritedModel,
  describeOverrideRule,
  overrideSelectionProblem,
  parseModelList,
  profileDraftRequest,
  profileListEntries,
  profileVersionLabel,
  rollbackCandidates,
  saveProfileDraft,
  validateProfileDraft,
  type AgentProfileVersion,
  type ProfileDraft,
} from './profileVersions';
import type { RunModelPolicy, RunModelPolicyStep } from './types';

afterEach(() => {
  vi.unstubAllGlobals();
});

const draft: ProfileDraft = { name: 'Writer', role: 'writer', defaultModel: 'model-a', allowedModels: 'model-a\nmodel-b, MODEL-A', allowedExecutors: ['model'], promptContract: 'Write.', note: '' };

function version(n: number, status: AgentProfileVersion['status'], activated = false): AgentProfileVersion {
  return { id: 'writer', name: 'Writer', role: 'writer', default_model: '', prompt_contract: 'x', allowed_executors: ['model'], version: n, status, source: 'persisted', created_at: '', updated_at: '', activated_at: activated ? '2026-09-24T00:00:00Z' : undefined };
}

const step: RunModelPolicyStep = { step_id: 'write', step_name: 'write', position: 1, executor: 'model', profile_id: 'writer', profile_version: 2, profile_source: 'persisted', accepts_override: true, override_restricted: true, allowed_overrides: ['model-a'], inherited_model: 'model-a', inherited_model_source: 'profile-default', model_source: 'profile-default', recorded: false };

describe('profile drafts', () => {
  it('parses model lists without blanks or case-insensitive duplicates', () => {
    expect(parseModelList(' model-a \n\nmodel-b, MODEL-A ,')).toEqual(['model-a', 'model-b']);
  });

  it('mirrors the server draft rules', () => {
    expect(validateProfileDraft(draft)).toEqual([]);
    expect(validateProfileDraft({ ...draft, defaultModel: 'model-z' })).toContain('Default model "model-z" is not in the allowed models.');
    expect(validateProfileDraft({ ...draft, allowedModels: 'two words' }).join(' ')).toContain('must be a single token');
    expect(validateProfileDraft({ ...draft, allowedExecutors: [], name: ' ' })).toEqual(['Name is required and must be at most 128 characters.', 'Allow at least one executor.']);
  });

  it('builds the draft request with expected_version and canonical executor order', () => {
    expect(profileDraftRequest({ ...draft, allowedExecutors: ['opencode', 'model'], note: ' why ' }, 4)).toEqual({ expected_version: 4, name: 'Writer', role: 'writer', default_model: 'model-a', allowed_executors: ['model', 'opencode'], prompt_contract: 'Write.', allowed_models: ['model-a', 'model-b'], note: 'why' });
    expect(profileDraftRequest({ ...draft, allowedModels: '' }, 0)).not.toHaveProperty('allowed_models');
  });

  it('maps a 409 to a stale-edit conflict that keeps nothing saved', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: { message: 'conflict' } }), { status: 409 })));
    const result = await saveProfileDraft('writer', draft, 2);
    expect(result).toMatchObject({ ok: false, kind: 'conflict' });
    expect(!result.ok && result.message).toContain('version 2 is no longer the latest');
  });
});

describe('profile versions', () => {
  it('labels static and persisted versions', () => {
    expect(profileVersionLabel({ version: 0, status: 'static', source: 'static' })).toBe('static default');
    expect(profileVersionLabel({ version: 3, status: 'active', source: 'persisted' })).toBe('v3 active');
  });

  it('finds the activatable draft and rollback candidates', () => {
    const versions = [version(4, 'draft'), version(3, 'active', true), version(2, 'superseded', true), version(1, 'superseded')];
    const identity = { id: 'writer', active_version: 3, draft_version: 4, latest_version: 4, static_default: false };
    expect(activatableDraft(versions, identity)?.version).toBe(4);
    expect(rollbackCandidates(versions, identity).map((item) => item.version)).toEqual([2]);
    expect(activatableDraft(versions, { ...identity, draft_version: 0 })).toBeUndefined();
  });

  it('merges draft-only identities into the list', () => {
    const entries = profileListEntries([{ id: 'writer', name: 'Writer', role: 'writer', default_model: '', prompt_contract: '', allowed_executors: ['model'] }], [{ id: 'new-profile', active_version: 0, draft_version: 1, latest_version: 1, static_default: false }]);
    expect(entries.map((entry) => entry.id)).toEqual(['new-profile', 'writer']);
    expect(entries[0].profile).toBeUndefined();
  });
});

describe('run model policy helpers', () => {
  const policy: RunModelPolicy = { workflow_id: 'wf', override_status: 'none', accepts_override: true, override_restricted: true, allowed_overrides: ['model-a'], steps: [step] };

  it('explains inherited defaults and override rules', () => {
    expect(describeInheritedModel(step)).toBe('Inherits model-a from profile writer (v2).');
    expect(describeInheritedModel({ ...step, inherited_model: 'fixed', inherited_model_source: 'workflow' })).toBe('Fixed to fixed by the workflow step.');
    expect(describeOverrideRule(step)).toBe('Accepts overrides: model-a.');
    expect(describeOverrideRule({ ...step, accepts_override: false })).toBe('Ignores run overrides.');
    expect(describeOverrideRule({ ...step, override_restricted: false })).toBe('Accepts any model override.');
  });

  it('flags overrides the policy would reject', () => {
    expect(overrideSelectionProblem(policy, '')).toBe('');
    expect(overrideSelectionProblem(policy, 'MODEL-A')).toBe('');
    expect(overrideSelectionProblem(policy, 'model-b')).toBe('model-b is not allowed. Allowed overrides: model-a.');
    expect(overrideSelectionProblem({ ...policy, accepts_override: false }, 'model-a')).toContain('has no step that accepts');
    expect(overrideSelectionProblem(policy, 'a b')).toContain('single token');
  });
});
