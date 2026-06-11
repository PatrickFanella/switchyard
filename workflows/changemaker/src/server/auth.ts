import { timingSafeEqual } from 'node:crypto';
import type { RedactedSecret } from './types.js';

const SECRET_NAME_PATTERN = /(TOKEN|KEY|SECRET|PASSWORD)/i;

export function authorizeRequest(request: Request, adminToken: string | undefined): boolean {
  if (!adminToken) return false;

  const header = request.headers.get('authorization') ?? '';
  const match = header.match(/^Bearer\s+(.+)$/i);
  if (!match) return false;

  const provided = Buffer.from(match[1], 'utf8');
  const expected = Buffer.from(adminToken, 'utf8');
  if (provided.length !== expected.length) return false;

  return timingSafeEqual(provided, expected);
}

export function redactConfigValue(
  key: string,
  value: string | undefined,
): string | RedactedSecret | undefined {
  if (!SECRET_NAME_PATTERN.test(key)) return value;

  return {
    present: value !== undefined,
    redacted: '••••••',
  };
}
