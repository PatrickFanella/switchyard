import { describe, expect, it } from 'vitest';
import { formatHash, parseHash } from './useHashNavigation';

describe('hash navigation helpers', () => {
  it('parses run and request deep links', () => {
    expect(parseHash('#/runs/run%2Fone')).toEqual({ activeView: 'runs', selectedRunID: 'run/one', selectedRequestID: '' });
    expect(parseHash('#/queues/requests/request%2Fone')).toEqual({ activeView: 'queues', selectedRunID: '', selectedRequestID: 'request/one' });
  });

  it('falls back safely for malformed encodings', () => {
    expect(parseHash('#/runs/%E0%A4%A')).toEqual({ activeView: 'runs', selectedRunID: '', selectedRequestID: '' });
    expect(parseHash('#/queues/requests/%E0%A4%A')).toEqual({ activeView: 'queues', selectedRunID: '', selectedRequestID: '' });
  });

  it('formats run, overview, and request deep links', () => {
    expect(formatHash('runs', 'run/one')).toBe('#/runs/run%2Fone');
    expect(formatHash('overview', 'run/one')).toBe('#/overview/runs/run%2Fone');
    expect(formatHash('queues', '', 'request/one')).toBe('#/queues/requests/request%2Fone');
  });
});
