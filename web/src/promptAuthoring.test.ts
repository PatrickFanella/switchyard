import { describe, expect, it } from 'vitest';
import { definitionBindingLabel, definitionWithTemplate, modelConstraint, previewRequest, promptPolicySteps, templateInputOf, templateVariables, type PromptTemplate } from './promptAuthoring';
import type { RunModelPolicy, WorkflowDefinition } from './types';

const definition: WorkflowDefinition = {
  id: 'digest',
  name: 'Digest',
  description: '',
  status: 'active',
  version: 4,
  pipeline: {
    id: 'digest-pipeline',
    workflow_id: 'digest',
    name: 'Digest',
    steps: [
      { id: 'fetch', position: 1, name: 'fetch', executor: 'internal', inputs: { 'output.body': 'x' } },
      { id: 'write', position: 2, name: 'write', executor: 'model', inputs: { template: 'Old {{topic}}', model: '${model}' } },
    ],
  },
};

const template = { workflow_id: 'digest', step_id: 'write' } as PromptTemplate;

describe('templateVariables', () => {
  it('lists run inputs and well-formed step output references once, in order', () => {
    expect(templateVariables('{{topic}} {{ audience }} {{steps.fetch.outputs.body}} {{topic}} {{steps.bad}} {{}}')).toEqual({
      inputs: ['topic', 'audience'],
      stepOutputs: [{ stepID: 'fetch', name: 'body', key: 'steps.fetch.outputs.body' }],
    });
  });
});

describe('previewRequest', () => {
  it('sends only the samples the draft template reads', () => {
    expect(previewRequest(template, 'Hi {{topic}} {{steps.fetch.outputs.body}}', { topic: 't', stale: 's', 'steps.fetch.outputs.body': 'b', 'steps.gone.outputs.x': 'g' })).toEqual({
      workflow_id: 'digest',
      step_id: 'write',
      template: 'Hi {{topic}} {{steps.fetch.outputs.body}}',
      inputs: { topic: 't' },
      step_outputs: { fetch: { body: 'b' } },
    });
  });
});

describe('definitionWithTemplate', () => {
  it('replaces the input the executor reads and leaves everything else unchanged', () => {
    const next = definitionWithTemplate(definition, 'write', 'New {{topic}}')!;
    expect(next.pipeline.steps[1].inputs).toEqual({ template: 'New {{topic}}', model: '${model}' });
    expect(next.pipeline.steps[0]).toBe(definition.pipeline.steps[0]);
    expect(definition.pipeline.steps[1].inputs?.template).toBe('Old {{topic}}');
    expect(definitionWithTemplate(definition, 'missing', 'x')).toBeNull();
  });

  it('prefers prompt over template like the model executor', () => {
    expect(templateInputOf({ prompt: 'p', template: 't' })).toEqual({ input: 'prompt', text: 'p' });
    expect(templateInputOf({ prompt: ' ', template: 't' })).toEqual({ input: 'template', text: 't' });
    expect(templateInputOf(undefined)).toEqual({ input: 'prompt', text: '' });
  });
});

describe('run prompt binding', () => {
  const policy: RunModelPolicy = {
    workflow_id: 'digest',
    definition_id: 'digest',
    definition_version: 3,
    override_status: 'none',
    accepts_override: false,
    override_restricted: false,
    steps: [
      { step_id: 'fetch', step_name: 'fetch', position: 1, executor: 'internal', profile_version: 0, accepts_override: false, override_restricted: false, inherited_model_source: 'none', model_source: 'none', recorded: true },
      { step_id: 'write', step_name: 'write', position: 2, executor: 'model', profile_version: 0, accepts_override: false, override_restricted: false, inherited_model_source: 'workflow', model_source: 'workflow', recorded: true, prompt_owner: 'workflow-definition', prompt_input: 'prompt', prompt_template_sha256: 'a'.repeat(64) },
    ],
  };

  it('names the bound definition version and the steps with prompts', () => {
    expect(definitionBindingLabel(policy)).toBe('Workflow definition digest version 3');
    expect(definitionBindingLabel({ ...policy, definition_version: 0 })).toContain('No workflow-definition version recorded');
    expect(promptPolicySteps(policy).map((step) => step.step_id)).toEqual(['write']);
  });

  it('describes model constraints', () => {
    expect(modelConstraint({ allowed_models: ['a', 'b'], default_model: 'a' })).toBe('allowed: a, b · default a');
    expect(modelConstraint({})).toBe('any model');
  });
});
