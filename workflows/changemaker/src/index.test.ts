import { describe, expect, it } from 'vitest';
import { describeProject, projectName } from './index.js';

describe('project metadata', () => {
  it('names changemaker', () => {
    expect(projectName).toBe('changemaker');
  });

  it('describes the project', () => {
    expect(describeProject()).toContain('Gitea');
  });
});
