import { existsSync, lstatSync, readFileSync, writeFileSync, renameSync, unlinkSync } from 'node:fs';
import { join } from 'node:path';
import { randomUUID } from 'node:crypto';

// V2 imports legacy auth.json into its session database. Strict mode keeps that
// database in memory, so save refreshed credentials before provider dispatch.
export function persistCredential(integrationID, value, dataHome = process.env.XDG_DATA_HOME) {
  if (!dataHome || !integrationID || !value) return;
  let credential;
  if (value.type === 'key' && typeof value.key === 'string' && value.key) {
    credential = { type: 'api', key: value.key };
  } else if (value.type === 'oauth' && typeof value.refresh === 'string' && value.refresh &&
      typeof value.access === 'string' && value.access && Number.isSafeInteger(value.expires) && value.expires >= 0) {
    credential = { type: 'oauth', refresh: value.refresh, access: value.access, expires: value.expires };
    if (typeof value.metadata?.accountID === 'string') credential.accountId = value.metadata.accountID;
    if (typeof value.metadata?.enterpriseUrl === 'string') credential.enterpriseUrl = value.metadata.enterpriseUrl;
  } else {
    throw new Error('Managed OpenCode received an unsupported credential');
  }
  const target = join(dataHome, 'opencode', 'auth.json');
  let info;
  try { info = lstatSync(target); } catch (error) { if (error.code !== 'ENOENT') throw error; }
  if (info && (!info.isFile() || info.isSymbolicLink())) {
    throw new Error('Managed OpenCode auth must be a regular file');
  }
  const current = existsSync(target) ? JSON.parse(readFileSync(target, 'utf8')) : {};
  if (!current || Array.isArray(current) || typeof current !== 'object') throw new Error('Invalid managed auth document');
  if (JSON.stringify(current[integrationID]) === JSON.stringify(credential)) return;
  const temporary = target + '.' + randomUUID();
  try {
    writeFileSync(temporary, JSON.stringify({ ...current, [integrationID]: credential }) + '\n', { mode: 0o600, flag: 'wx' });
    renameSync(temporary, target);
  } finally {
    if (existsSync(temporary)) unlinkSync(temporary);
  }
}
