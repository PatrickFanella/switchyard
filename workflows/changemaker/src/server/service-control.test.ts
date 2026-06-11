import { describe, expect, it } from 'vitest';
import { assertServiceConfirmation, createServiceCommand } from './service-control.js';

describe('service control', () => {
  it('requires exact confirmation text', () => {
    expect(() =>
      assertServiceConfirmation('restart-timer', { confirm: true, confirmationText: 'restart-timer' }),
    ).not.toThrow();
    expect(() => assertServiceConfirmation('restart-timer', { confirm: true, confirmationText: 'wrong' })).toThrow(
      'Confirmation text must be restart-timer',
    );
  });

  it('maps allowed actions to systemctl commands', () => {
    expect(createServiceCommand('timer-status')).toEqual([
      'systemctl',
      'status',
      'changemaker-daily.timer',
      '--no-pager',
    ]);
    expect(createServiceCommand('restart-timer')).toEqual([
      'sudo',
      '-n',
      'systemctl',
      'restart',
      'changemaker-daily.timer',
    ]);
  });
});
