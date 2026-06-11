import { describe, expect, it } from 'vitest';
import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { loadDotEnvFile, loadGenerateConfig, loadServerConfig, resolveGenerateWindow } from './config.js';

describe('loadGenerateConfig', () => {
  it('defaults to git.subcult.tv and a 36 hour window', () => {
    const now = new Date('2026-05-25T12:00:00.000Z');
    const config = loadGenerateConfig({ now }, {});

    expect(config.baseUrl).toBe('https://git.subcult.tv');
    expect(config.lookbackHours).toBe(36);
    expect(config.cadenceHours).toBe(24);
    expect(config.since.toISOString()).toBe('2026-05-24T00:00:00.000Z');
  });

  it('loads token from a custom token env name', () => {
    const config = loadGenerateConfig(
      { tokenEnv: 'CUSTOM_GITEA_TOKEN', now: new Date('2026-05-25T12:00:00.000Z') },
      { CUSTOM_GITEA_TOKEN: 'secret' },
    );

    expect(config.token).toBe('secret');
  });

  it('defaults local SQLite state to the ignored cache directory', () => {
    const config = loadGenerateConfig({ now: new Date('2026-05-25T12:00:00.000Z') }, {});

    expect(config.dbPath).toBe('.cache/changemaker.sqlite');
    expect(config.includeCovered).toBe(false);
    expect(config.markCovered).toBe(false);
    expect(config.summarize).toBe(false);
    expect(config.llmProvider).toBe('openai');
    expect(config.extraPeriods).toEqual([]);
    expect(config.reportDateOffsetDays).toBe(1);
    expect(config.socialSection).toBe(true);
  });

  it('loads granular generation controls from environment', () => {
    const config = loadGenerateConfig(
      { now: new Date('2026-05-25T12:00:00.000Z') },
      {
        CHANGEMAKER_EXTRA_PERIODS: 'weekly,monthly,quarterly',
        CHANGEMAKER_REPORT_DATE_OFFSET_DAYS: '2',
        CHANGEMAKER_SOCIAL_SECTION: 'false',
      },
    );

    expect(config.extraPeriods).toEqual(['weekly', 'monthly', 'quarterly']);
    expect(config.reportDateOffsetDays).toBe(2);
    expect(config.socialSection).toBe(false);
  });

  it('uses the LLM provider from environment when no flag overrides it', () => {
    const config = loadGenerateConfig(
      { now: new Date('2026-05-25T12:00:00.000Z') },
      { CHANGEMAKER_LLM_PROVIDER: 'llama-line', CHANGEMAKER_LLM_MODEL: 'gpt-5.4-mini-fast' },
    );

    expect(config.llmProvider).toBe('llama-line');
    expect(config.llmModel).toBe('gpt-5.4-mini-fast');
  });

  it('resolves default cadence window from last post plus 24 hours', () => {
    const window = resolveGenerateWindow({
      now: new Date('2026-05-25T12:00:00.000Z'),
      lastPostAt: new Date('2026-05-24T08:00:00.000Z'),
      lookbackHours: 36,
      cadenceHours: 24,
    });

    expect(window.until.toISOString()).toBe('2026-05-25T08:00:00.000Z');
    expect(window.since.toISOString()).toBe('2026-05-23T20:00:00.000Z');
  });

  it('does not set a future window end if cadence point is after now', () => {
    const window = resolveGenerateWindow({
      now: new Date('2026-05-25T12:00:00.000Z'),
      lastPostAt: new Date('2026-05-25T08:00:00.000Z'),
      lookbackHours: 36,
      cadenceHours: 24,
    });

    expect(window.until.toISOString()).toBe('2026-05-25T12:00:00.000Z');
  });

  it('can force a current-time window', () => {
    const window = resolveGenerateWindow({
      now: new Date('2026-05-25T12:00:00.000Z'),
      lastPostAt: new Date('2026-05-24T08:00:00.000Z'),
      lookbackHours: 36,
      cadenceHours: 24,
      nowWindow: true,
    });

    expect(window.until.toISOString()).toBe('2026-05-25T12:00:00.000Z');
    expect(window.since.toISOString()).toBe('2026-05-24T00:00:00.000Z');
  });

  it('accepts an explicit generate window', () => {
    const config = loadGenerateConfig(
      {
        since: '2026-05-21T06:00:00-05:00',
        until: '2026-05-26T12:34:56-05:00',
        now: new Date('2026-05-30T00:00:00.000Z'),
      },
      {},
    );

    expect(config.manualWindow).toBe(true);
    expect(config.since.toISOString()).toBe('2026-05-21T11:00:00.000Z');
    expect(config.now.toISOString()).toBe('2026-05-26T17:34:56.000Z');
  });

  it('loads local dotenv values without overriding explicit environment', () => {
    const dir = mkdtempSync(join(tmpdir(), 'changemaker-env-'));
    const envPath = join(dir, '.env');
    const env: NodeJS.ProcessEnv = { LLAMA_LINE_API_KEY: 'existing' };
    writeFileSync(
      envPath,
      [
        'CHANGEMAKER_LLM_PROVIDER=llama-line',
        'LLAMA_LINE_BASE_URL="http://10.0.0.56:3034"',
        'LLAMA_LINE_API_KEY=from-file',
        '',
      ].join('\n'),
    );

    loadDotEnvFile(envPath, env);

    expect(env.CHANGEMAKER_LLM_PROVIDER).toBe('llama-line');
    expect(env.LLAMA_LINE_BASE_URL).toBe('http://10.0.0.56:3034');
    expect(env.LLAMA_LINE_API_KEY).toBe('existing');
  });

  it('rejects ports above the valid range', () => {
    expect(() => loadServerConfig({ port: 70000 }, {})).toThrow('Invalid port: 70000');
  });

  it('rejects non-integer and partially numeric ports', () => {
    expect(() => loadServerConfig({ port: '3038abc' }, {})).toThrow('Invalid port: 3038abc');
    expect(() => loadServerConfig({ port: '1.5' }, {})).toThrow('Invalid port: 1.5');
  });

  it('defaults blank host and clears blank admin token', () => {
    const config = loadServerConfig({ host: '   ', adminToken: '   ' }, {});

    expect(config.host).toBe('127.0.0.1');
    expect(config.adminToken).toBeUndefined();
  });
});
