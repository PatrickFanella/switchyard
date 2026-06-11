import { describe, expect, it } from 'vitest';
import { authorizeRequest, redactConfigValue } from './auth.js';

describe('server auth', () => {
  it('accepts a matching bearer token', () => {
    const request = new Request('http://localhost/api/posts', {
      headers: { authorization: 'Bearer test-token' },
    });

    expect(authorizeRequest(request, 'test-token')).toBe(true);
  });

  it('rejects missing and wrong bearer tokens', () => {
    expect(authorizeRequest(new Request('http://localhost/api/posts'), 'test-token')).toBe(false);
    expect(
      authorizeRequest(
        new Request('http://localhost/api/posts', { headers: { authorization: 'Bearer wrong' } }),
        'test-token',
      ),
    ).toBe(false);
  });

  it('redacts secret-like config values', () => {
    expect(redactConfigValue('LLAMA_LINE_API_KEY', 'abc123')).toEqual({ present: true, redacted: '••••••' });
    expect(redactConfigValue('CHANGEMAKER_LLM_MODEL', 'gpt-5.4-mini-fast')).toBe('gpt-5.4-mini-fast');
  });
});
