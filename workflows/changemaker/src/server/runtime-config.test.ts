import { mkdtempSync, statSync } from 'node:fs';
import { readFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { ensureRuntimeFiles, readRuntimeConfig, writePromptTemplate, writeRuntimeConfig } from './runtime-config.js';

function fileMode(path: string): number {
  return statSync(path).mode & 0o777;
}

describe('runtime config', () => {
  it('creates editable prompt and config files under data dir', async () => {
    const dataDir = mkdtempSync(join(tmpdir(), 'changemaker-runtime-'));

    await ensureRuntimeFiles(dataDir);

    expect(await readFile(join(dataDir, 'prompts', 'summary-system.md'), 'utf8')).toContain('summarize');
    expect(await readFile(join(dataDir, 'prompts', 'post-format.md'), 'utf8')).toContain('Format');
    expect(await readFile(join(dataDir, 'prompts', 'social-posts.md'), 'utf8')).toContain('Twitter/X');
    expect(await readRuntimeConfig(dataDir)).toMatchObject({
      generation: {
        owner: 'subculture-collective',
        summarize: false,
        post: true,
        markCovered: true,
        summaryRetries: 3,
        includeCovered: false,
        includeDiffs: true,
        narrativeUpdate: true,
        socialSection: true,
        lookbackHours: 36,
        cadenceHours: 24,
        maxDiffChars: 12_000,
        reportDateOffsetDays: 1,
        extraPeriods: ['weekly', 'monthly', 'quarterly'],
      },
    });
    expect(fileMode(join(dataDir, 'admin-config.json'))).toBe(0o600);
    expect(fileMode(join(dataDir, 'prompts', 'summary-system.md'))).toBe(0o600);
  });

  it('writes prompt templates without touching repo files', async () => {
    const dataDir = mkdtempSync(join(tmpdir(), 'changemaker-runtime-'));
    await ensureRuntimeFiles(dataDir);

    await writePromptTemplate(dataDir, 'summary-system.md', 'New prompt');

    expect(await readFile(join(dataDir, 'prompts', 'summary-system.md'), 'utf8')).toBe('New prompt');
    expect(fileMode(join(dataDir, 'prompts', 'summary-system.md'))).toBe(0o600);
  });

  it('rejects unsafe prompt file names', async () => {
    const dataDir = mkdtempSync(join(tmpdir(), 'changemaker-runtime-'));

    await expect(writePromptTemplate(dataDir, '../escape.md', 'New prompt')).rejects.toThrow(
      'Invalid prompt file name',
    );
  });

  it('writes runtime config with mode 600', async () => {
    const dataDir = mkdtempSync(join(tmpdir(), 'changemaker-runtime-'));
    await ensureRuntimeFiles(dataDir);

    await writeRuntimeConfig(dataDir, {
      generation: { summarize: true, post: false, markCovered: false, summaryRetries: 7 },
    });

    expect(fileMode(join(dataDir, 'admin-config.json'))).toBe(0o600);
    expect(await readRuntimeConfig(dataDir)).toMatchObject({
      generation: { summarize: true, post: false, markCovered: false, summaryRetries: 7 },
    });
  });

  it('normalizes granular runtime config fields', async () => {
    const dataDir = mkdtempSync(join(tmpdir(), 'changemaker-runtime-'));
    await ensureRuntimeFiles(dataDir);

    await writeRuntimeConfig(dataDir, {
      generation: {
        llmProvider: 'llama-line',
        llmModel: 'qwen3',
        includeCovered: true,
        includeDiffs: false,
        narrativeUpdate: false,
        socialSection: false,
        lookbackHours: 48,
        cadenceHours: 24,
        maxDiffChars: 8000,
        reportDateOffsetDays: 2,
        extraPeriods: ['weekly', 'weekly', 'quarterly', 'bad'],
      },
    });

    expect(await readRuntimeConfig(dataDir)).toMatchObject({
      generation: {
        llmProvider: 'llama-line',
        llmModel: 'qwen3',
        includeCovered: true,
        includeDiffs: false,
        narrativeUpdate: false,
        socialSection: false,
        lookbackHours: 48,
        cadenceHours: 24,
        maxDiffChars: 8000,
        reportDateOffsetDays: 2,
        extraPeriods: ['weekly', 'quarterly'],
      },
    });
  });
});
