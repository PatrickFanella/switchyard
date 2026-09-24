import { describe, expect, it } from 'vitest';
import type { AgentProfile, WorkflowDefinition } from './types';
import {
  definitionFromDraft,
  draftFingerprint,
  draftFromDefinition,
  duplicateDraft,
  filterWorkflowCatalog,
  formatWorkflowHash,
  parseStepOutputReferences,
  parseWorkflowHash,
  profileAllowsModel,
  profilesForExecutor,
  stepOutputOptions,
  stepOutputReference,
  stepReferenceProblems,
  validateDraftForSave,
} from './workflowDrafts';

const definition: WorkflowDefinition = {
  id: 'digest',
  name: 'Digest',
  description: 'Daily digest',
  status: 'draft',
  version: 2,
  owns: ['draft artifact'],
  uses: ['model-broker'],
  input_shape: { type: 'object', properties: { topic: { type: 'string', description: 'Topic' }, limit: { type: 'integer' } }, required: ['topic'] },
  pipeline: {
    id: 'digest-pipeline',
    workflow_id: 'digest',
    name: 'Digest pipeline',
    steps: [
      { id: 'write', position: 2, name: 'digest.write', executor: 'model', profile: 'writer', model: '${model}', inputs: { prompt: 'Use {{steps.fetch.outputs.body}}' } },
      {
        id: 'fetch', position: 1, name: 'digest.fetch', executor: 'http', inputs: { url: 'https://example.test', allow_http: 'true' },
        output_shape: {
          type: 'object', description: 'Fetched data',
          properties: {
            body: { type: 'string', maxLength: 10, enum: ['a', 'b'] },
            report: { type: 'object', properties: { id: { type: 'integer' } }, required: ['id'] },
            tags: { type: 'array', maxItems: 3, items: { type: 'string' } },
          },
          required: ['body'],
        },
      },
    ],
  },
};

describe('workflow draft conversion', () => {
  it('round-trips a stored definition through the form model', () => {
    const draft = draftFromDefinition(definition);
    expect(draft.steps.map((step) => step.id)).toEqual(['fetch', 'write']);
    expect(draft.steps[0].outputs.find((output) => output.name === 'report')?.keywords).toContain('"properties"');
    const { definition: converted, errors } = definitionFromDraft(draft);
    expect(errors).toEqual([]);
    const { version: _version, ...stored } = definition;
    expect(_version).toBe(2);
    expect(converted).toEqual({ ...stored, pipeline: { ...definition.pipeline, steps: [definition.pipeline.steps[1], definition.pipeline.steps[0]] } });
  });

  it('ignores React keys when detecting unsaved changes', () => {
    expect(draftFingerprint(draftFromDefinition(definition))).toBe(draftFingerprint(draftFromDefinition(definition)));
  });

  it('reports encoding problems and new-ID collisions before saving', () => {
    const draft = draftFromDefinition(definition);
    draft.steps[0].outputs[0].maxLength = 'ten';
    draft.steps[0].outputs[1].keywords = '[1]';
    const result = validateDraftForSave({ ...draft, id: 'digest' }, { creating: true, existingIDs: ['digest'] });
    expect(result.errors).toEqual([
      'A workflow definition with ID "digest" already exists. Choose another ID or edit that workflow.',
      'Step "fetch" output "body": maxLength must be a whole number.',
      'Step "fetch" output "report": additional keywords must be a JSON object.',
    ]);
    expect(validateDraftForSave({ ...draft, id: 'bad id' }, { creating: true, existingIDs: [] }).errors[0]).toContain('Workflow ID may use');
    expect(validateDraftForSave({ ...draft, id: '' }, { creating: false, existingIDs: [] }).errors[0]).toBe('Workflow ID is required.');
  });

  it('duplicates under an unused ID with a fresh pipeline ID', () => {
    const draft = duplicateDraft(definition, ['digest', 'digest-copy']);
    expect(draft.id).toBe('digest-copy-2');
    expect(definitionFromDraft(draft).definition.pipeline.id).toBe('digest-copy-2-pipeline');
  });
});

describe('step output bindings', () => {
  it('offers declared and executor outputs of earlier steps only', () => {
    const draft = draftFromDefinition(definition);
    expect(stepOutputOptions(draft, 0)).toEqual([]);
    const options = stepOutputOptions(draft, 1);
    expect(options.slice(0, 3).map((option) => [option.name, option.source])).toEqual([['body', 'declared'], ['report', 'declared'], ['tags', 'declared']]);
    expect(options.map((option) => option.name)).toContain('body_sha256');
    expect(options.filter((option) => option.name === 'body')).toHaveLength(1);
    expect(options[0].reference).toBe('{{steps.fetch.outputs.body}}');
  });

  it('lists internal output.<name> inputs as bindable outputs', () => {
    const draft = draftFromDefinition({ ...definition, pipeline: { ...definition.pipeline, steps: [{ id: 'prep', position: 1, name: 'prep', executor: 'internal', inputs: { 'output.topic_line': '{{topic}}' } }, { id: 'use', position: 2, name: 'use', executor: 'internal' }] } });
    expect(stepOutputOptions(draft, 1).map((option) => `${option.name}:${option.source}`)).toEqual(['topic_line:internal-input', 'summary:executor', 'artifact_name:executor']);
  });

  it('parses references and flags unknown, later, and malformed ones', () => {
    expect(stepOutputReference('a', 'b')).toBe('{{steps.a.outputs.b}}');
    expect(parseStepOutputReferences('x {{steps.a.outputs.b}} {{ steps.c.outputs.d }} {{steps.bad}}')).toEqual({
      references: [{ stepID: 'a', name: 'b', text: '{{steps.a.outputs.b}}' }, { stepID: 'c', name: 'd', text: '{{ steps.c.outputs.d }}' }],
      malformed: ['{{steps.bad}}'],
    });
    const draft = draftFromDefinition(definition);
    draft.steps[0].inputs.push({ key: 'x', name: 'body', value: '{{steps.write.outputs.content}} {{steps.nope.outputs.x}} {{steps.bad}}' });
    expect(stepReferenceProblems(draft, 0)).toEqual([
      'Input "body": {{steps.bad}} is not a {{steps.<step_id>.outputs.<name>}} reference.',
      'Input "body": step "write" does not run before this step.',
      'Input "body": step "nope" does not exist.',
    ]);
    expect(stepReferenceProblems(draft, 1)).toEqual([]);
  });
});

describe('profiles, catalog, and routes', () => {
  const profiles: AgentProfile[] = [
    { id: 'writer', name: 'Writer', role: 'writer', default_model: 'm1', allowed_models: ['m1'], prompt_contract: '', allowed_executors: ['Model'] },
    { id: 'ops', name: 'Ops', role: 'ops', default_model: 'm2', prompt_contract: '', allowed_executors: ['command'] },
  ];

  it('limits profiles to those that allow the executor and mirrors model checks', () => {
    expect(profilesForExecutor(profiles, 'model').map((profile) => profile.id)).toEqual(['writer']);
    expect(profileAllowsModel(profiles[0], 'm1')).toBe(true);
    expect(profileAllowsModel(profiles[0], '${model}')).toBe(true);
    expect(profileAllowsModel(profiles[0], 'm2')).toBe(false);
    expect(profileAllowsModel(profiles[1], 'anything')).toBe(true);
  });

  it('searches across identity, steps, and profiles and filters by status', () => {
    const other: WorkflowDefinition = { ...definition, id: 'alpha', name: 'Alpha', status: 'active', pipeline: { ...definition.pipeline, steps: [{ id: 'run', position: 1, name: 'alpha.run', executor: 'command' }] } };
    expect(filterWorkflowCatalog([definition, other], '', 'all').map((item) => item.id)).toEqual(['alpha', 'digest']);
    expect(filterWorkflowCatalog([definition, other], 'writer', 'all').map((item) => item.id)).toEqual(['digest']);
    expect(filterWorkflowCatalog([definition, other], 'COMMAND alpha', 'all').map((item) => item.id)).toEqual(['alpha']);
    expect(filterWorkflowCatalog([definition, other], '', 'draft').map((item) => item.id)).toEqual(['digest']);
  });

  it('round-trips catalog routes and falls back for malformed hashes', () => {
    for (const route of [{ mode: 'catalog', id: '' }, { mode: 'new', id: '' }, { mode: 'view', id: 'a/b c' }, { mode: 'edit', id: 'x' }, { mode: 'duplicate', id: 'y' }] as const) {
      expect(parseWorkflowHash(formatWorkflowHash(route))).toEqual(route);
    }
    expect(parseWorkflowHash('#/workflows/edit')).toEqual({ mode: 'catalog', id: '' });
    expect(parseWorkflowHash('#/workflows/view/%E0%A4%A')).toEqual({ mode: 'catalog', id: '' });
    expect(parseWorkflowHash('#/runs/x')).toEqual({ mode: 'catalog', id: '' });
  });
});
