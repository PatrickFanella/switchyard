import { describe, expect, it } from 'vitest';
import {
  allowedExternalHref,
  collectCorrelations,
  correlationAllowlist,
  formatEvidenceHash,
  giteaCommitHref,
  giteaIssueHref,
  parseEvidenceHash,
  resolveCorrelation,
  summarizeRunFailure,
  traceHref,
} from './correlation';
import { parseHash } from './hooks/useHashNavigation';
import type { RunDetail } from './types';

const targets = { gitea_base_url: 'https://git.example.test/gitea', trace_ui_base_url: 'https://trace.example.test/projects/p1/traces/{trace_id}' };
const allowlist = correlationAllowlist(targets);

describe('correlation allowlist', () => {
  it('derives origins and path prefixes from the configured bases only', () => {
    expect(allowlist).toEqual([
      { protocol: 'https:', host: 'git.example.test', pathPrefix: '/gitea' },
      { protocol: 'https:', host: 'trace.example.test', pathPrefix: '/projects/p1/traces' },
    ]);
    expect(correlationAllowlist({})).toEqual([]);
    expect(correlationAllowlist({ gitea_base_url: 'ftp://git.example.test', trace_ui_base_url: 'javascript:alert(1)' })).toEqual([]);
  });

  it('accepts URLs on an approved origin under its path prefix', () => {
    expect(allowedExternalHref('https://git.example.test/gitea/OWNER/REPO/pulls/7', allowlist)).toBe('https://git.example.test/gitea/OWNER/REPO/pulls/7');
    expect(allowedExternalHref('https://GIT.example.test/gitea/OWNER/REPO/actions/runs/1', allowlist)).toBe('https://git.example.test/gitea/OWNER/REPO/actions/runs/1');
  });

  it.each([
    ['javascript:alert(1)'],
    ['data:text/html,<script>alert(1)</script>'],
    ['ftp://git.example.test/gitea/OWNER/REPO'],
    ['mailto:ops@git.example.test'],
    ['file:///etc/passwd'],
    ['//git.example.test/gitea/OWNER/REPO'],
    ['/gitea/OWNER/REPO'],
    ['http://git.example.test/gitea/OWNER/REPO'],
    ['https://git.example.test:8443/gitea/OWNER/REPO'],
    ['https://git.example.test.evil.test/gitea/OWNER/REPO'],
    ['https://evil.test/gitea/OWNER/REPO'],
    ['https://user:secret@git.example.test/gitea/OWNER/REPO'],
    ['https://git.example.test/giteax/OWNER/REPO'],
    ['https://git.example.test/other/OWNER/REPO'],
    ['https://git.example.test/gitea/OWNER REPO'],
  ])('rejects unsupported scheme, host, or path %s', (url) => {
    expect(allowedExternalHref(url, allowlist)).toBeNull();
  });

  it('rejects everything when no destination is configured', () => {
    expect(allowedExternalHref('https://git.example.test/gitea/OWNER/REPO', [])).toBeNull();
  });
});

describe('typed destinations', () => {
  it('builds Gitea issue and commit links only from valid repository IDs', () => {
    expect(giteaIssueHref(targets, 'OWNER/REPO', '42')).toBe('https://git.example.test/gitea/OWNER/REPO/issues/42');
    expect(giteaIssueHref(targets, '../../evil', '42')).toBeNull();
    expect(giteaIssueHref(targets, 'OWNER/REPO', '42abc')).toBeNull();
    expect(giteaIssueHref({}, 'OWNER/REPO', '42')).toBeNull();
    expect(giteaCommitHref(targets, 'OWNER/REPO', 'ABC1234')).toBe('https://git.example.test/gitea/OWNER/REPO/commit/abc1234');
    expect(giteaCommitHref(targets, 'OWNER/REPO', 'not-a-sha')).toBeNull();
  });

  it('builds trace links from a placeholder or appended segment and rejects unsafe IDs', () => {
    expect(traceHref(targets, 'abc123')).toEqual({ href: 'https://trace.example.test/projects/p1/traces/abc123' });
    expect(traceHref({ trace_ui_base_url: 'https://trace.example.test/ui/' }, 'abc123')).toEqual({ href: 'https://trace.example.test/ui/abc123' });
    expect(traceHref(targets, '../../admin')).toEqual({ href: null, reason: 'invalid-id' });
    expect(traceHref({}, 'abc123')).toEqual({ href: null, reason: 'not-configured' });
  });

  it('maps correlation keys to runs, steps, requests, sessions, traces, and copy-only IDs', () => {
    const context = { runID: 'run-1', source: 'Step 1 · plan', targets };
    expect(resolveCorrelation('model_broker_request_id', 'req/1', context)).toMatchObject({ kind: 'request', href: '#/queues/requests/req%2F1', external: false });
    expect(resolveCorrelation('llama_line_request_id', 'req-2', context)).toMatchObject({ kind: 'request', href: '#/queues/requests/req-2' });
    expect(resolveCorrelation('opencode_session_id', 'ses 1', context)).toMatchObject({ kind: 'session', href: '#/logs/search/correlations/ses%201' });
    expect(resolveCorrelation('ocq_request_id', 'ocq-9', context)).toMatchObject({ kind: 'executor-request', href: '#/logs/search/correlations/ocq-9' });
    expect(resolveCorrelation('parent_run_id', 'run-0', context)).toMatchObject({ kind: 'run', href: '#/runs/run-0' });
    expect(resolveCorrelation('parent_run_id', 'run-1', context)).toMatchObject({ kind: 'run', href: null, reason: 'current' });
    expect(resolveCorrelation('retry_step_id', 'step-2', context)).toMatchObject({ kind: 'step', href: '#/runs/run-1/steps/step-2' });
    expect(resolveCorrelation('trace_id', 'abc', context)).toMatchObject({ kind: 'trace', external: true, href: 'https://trace.example.test/projects/p1/traces/abc' });
    expect(resolveCorrelation('span_id', 'span-1', context)).toMatchObject({ kind: 'span', href: null, reason: 'no-destination' });
    expect(resolveCorrelation('worktree_path', '/tmp/wt', context)).toMatchObject({ kind: 'identifier', href: null, reason: 'no-destination' });
    expect(resolveCorrelation('pull_request', 'https://evil.test/x/pulls/1', context)).toMatchObject({ kind: 'gitea-pull-request', href: null, reason: 'rejected' });
    expect(resolveCorrelation('pull_request', 'javascript:alert(1)', context)).toMatchObject({ href: null, reason: 'rejected' });
    expect(resolveCorrelation('pull_request', 'https://git.example.test/gitea/OWNER/REPO/pulls/1', { ...context, targets: {} })).toMatchObject({ href: null, reason: 'not-configured' });
  });
});

describe('evidence deep links', () => {
  it('round-trips run evidence links and stays compatible with the shell router', () => {
    const hash = formatEvidenceHash('run/1', 'steps', 'step #2');
    expect(hash).toBe('#/runs/run%2F1/steps/step%20%232');
    expect(parseEvidenceHash(hash)).toEqual({ runID: 'run/1', kind: 'steps', id: 'step #2' });
    expect(parseHash(hash)).toEqual({ activeView: 'runs', selectedRunID: 'run/1', selectedRequestID: '' });
    expect(parseEvidenceHash('#/runs/run-1')).toBeNull();
    expect(parseEvidenceHash('#/runs/run-1/secrets/x')).toBeNull();
    expect(parseEvidenceHash('#/runs/run-1/steps/%E0%A4%A')).toBeNull();
  });
});

describe('failure summary', () => {
  const base: RunDetail = {
    run: { id: 'run-1', workflow_id: 'devlog', status: 'succeeded', created_at: '2026-09-24T00:00:00Z' },
    steps: [{ id: 's1', run_id: 'run-1', position: 1, name: 'devlog.write', executor: 'model', status: 'succeeded', attempt_count: 1, created_at: '2026-09-24T00:00:00Z' }],
    events: [],
    artifacts: [],
  };

  it('is absent for a healthy run', () => {
    expect(summarizeRunFailure(base, null, collectCorrelations(base, null, targets))).toBeNull();
  });

  it('points at the failed step, failure events, and verification artifacts', () => {
    const failed: RunDetail = {
      ...base,
      run: { ...base.run, status: 'failed' },
      steps: [{ ...base.steps[0], status: 'failed', error: 'verification failed: go test\nexit 1', correlation_ids: { trace_id: 't-1' } }],
      events: [{ id: 'e1', run_id: 'run-1', type: 'step.failed', message: 'devlog.write failed', created_at: '2026-09-24T00:00:01Z' }, { id: 'e0', run_id: 'run-1', type: 'run.started', message: 'started', created_at: '2026-09-24T00:00:00Z' }],
      artifacts: [{ id: 'a1', run_id: 'run-1', name: 'verification.log', kind: 'evidence', uri: 'data:,x', created_at: '2026-09-24T00:00:01Z' }, { id: 'a2', run_id: 'run-1', name: 'draft.md', kind: 'draft', uri: 'data:,x', created_at: '2026-09-24T00:00:01Z' }],
    };
    const summary = summarizeRunFailure(failed, null, collectCorrelations(failed, null, targets));
    expect(summary).toMatchObject({ status: 'failed', headline: 'devlog.write failed', detail: 'verification failed: go test' });
    expect(summary?.evidence.map((item) => item.href)).toEqual(['#/runs/run-1/steps/s1', '#/runs/run-1/events/e1', '#/runs/run-1/artifacts/a1']);
    expect(summary?.links.map((link) => link.kind)).toEqual(['trace']);
  });
});
