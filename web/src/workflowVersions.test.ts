import { describe, expect, it } from 'vitest';
import type { PipelineStepDefinition, WorkflowDefinition } from './types';
import { actionConsequences, activationProblems, activeVersionOf, compareWorkflowVersions, latestVersionOf, versionRole } from './workflowVersions';

function definition(version: number, overrides: Partial<WorkflowDefinition> = {}): WorkflowDefinition {
  return {
    id: 'digest', name: 'Digest', description: '', status: 'draft', version,
    pipeline: { id: 'digest-pipeline', name: 'Digest', steps: [{ id: 'a', position: 1, name: 'a', executor: 'internal', output_shape: { type: 'object', properties: { x: { type: 'string', maxLength: 5 } } } }] },
    ...overrides,
  };
}

describe('workflow versions', () => {
  it('derives active, draft, and earlier roles from the active pointer', () => {
    const versions = [definition(3, { active_version: 2 }), definition(2, { status: 'active', active_version: 2 }), definition(1, { active_version: 2 })];
    expect(activeVersionOf(versions)).toBe(2);
    expect(latestVersionOf(versions)).toBe(3);
    expect(versions.map((item) => versionRole(item.version ?? 0, 2))).toEqual(['draft', 'active', 'earlier']);
    expect(versionRole(1, 0)).toBe('draft');
    expect(activeVersionOf([definition(1)])).toBe(0);
  });

  it('ignores key order in output shapes and reports moved and edited steps', () => {
    const base = definition(1);
    const reordered = definition(2, { pipeline: { ...base.pipeline, steps: [{ id: 'a', position: 1, name: 'a', executor: 'internal', output_shape: { properties: { x: { maxLength: 5, type: 'string' } }, type: 'object' } }] } });
    expect(compareWorkflowVersions(base, reordered).changed).toBe(false);
    const edited = definition(3, { description: 'New', pipeline: { ...base.pipeline, steps: [{ id: 'b', position: 1, name: 'b', executor: 'internal' }, { ...base.pipeline.steps[0], position: 2, profile: 'writer' }] } });
    const comparison = compareWorkflowVersions(base, edited);
    expect(comparison.metadata).toEqual([{ field: 'description', before: '', after: 'New' }]);
    expect(comparison.steps.map((step) => step.kind)).toEqual(['added', 'changed']);
    const changed = comparison.steps[1];
    expect(changed.kind === 'changed' ? changed.fields.map((field) => field.field) : []).toEqual(['position', 'profile']);
  });

  it('mirrors the executable checks the server applies on activation', () => {
    const steps: PipelineStepDefinition[] = [
      { id: 'm', position: 1, name: 'm', executor: 'model', inputs: { template: 'Hi' } },
      { id: 'c', position: 2, name: 'c', executor: 'command' },
      { id: 'h', position: 3, name: 'h', executor: 'http', inputs: { url: 'https://x.test', allow_http: 'TRUE' } },
      { id: 'o', position: 4, name: 'o', executor: 'opencode' },
    ];
    expect(activationProblems(definition(1, { pipeline: { id: 'p', name: 'p', steps } }))).toEqual([
      'Step "c": the command executor needs a command input.',
      'Step "o": executor "opencode" is not supported.',
    ]);
  });

  it('explains activation and rollback consequences, including a pending draft', () => {
    expect(actionConsequences({ kind: 'activate', workflowID: 'digest', version: 1, activeVersion: 0 })[0]).toContain('No version is active now');
    const rollback = actionConsequences({ kind: 'rollback', workflowID: 'digest', version: 1, activeVersion: 2, latestVersion: 3 });
    expect(rollback[0]).toBe('Creates version 4 as a copy of version 1 and makes it active. New runs of digest will use version 4 instead of version 2.');
    expect(rollback).toContain('Draft version 3 stays saved but is no longer the latest version; its edits are not included.');
    expect(rollback.at(-1)).toContain('keep the pipeline snapshot');
  });
});
