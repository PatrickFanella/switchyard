import { constants, open } from 'node:fs/promises';
import { join } from 'node:path';

export function nativeCredential(id, value) {
  if (value.type === 'api' && typeof value.key === 'string') {
    return { type: 'key', key: value.key, ...(value.metadata ? { metadata: value.metadata } : {}) };
  }
  if (value.type !== 'oauth' || typeof value.refresh !== 'string' || typeof value.access !== 'string' ||
      !Number.isSafeInteger(value.expires) || value.expires < 0) throw new Error('Invalid managed provider credential');
  const metadata = {
    ...(value.accountId ? { accountID: value.accountId } : {}),
    ...(value.enterpriseUrl ? { enterpriseUrl: value.enterpriseUrl } : {}),
    ...(value.metadata?.clientID ? { clientID: value.metadata.clientID } : {}),
    ...(value.metadata?.scopes ? { scopes: value.metadata.scopes } : {}),
  };
  return {
    type: 'oauth', methodID: value.methodID ?? (id === 'openai' ? 'chatgpt-browser' :
      ['github-copilot', 'opencode', 'xai'].includes(id) ? 'device' : 'oauth'),
    refresh: value.refresh, access: value.access, expires: value.expires,
    ...(Object.keys(metadata).length ? { metadata } : {}),
  };
}

export async function restoreCredentials(request, dataHome) {
  let file;
  try { file = await open(join(dataHome, 'opencode', 'auth.json'), constants.O_RDONLY | constants.O_NOFOLLOW); }
  catch (error) { if (error.code === 'ENOENT') return 0; throw error; }
  let auth;
  try {
    const info = await file.stat();
    if (!info.isFile() || info.size > 1024 * 1024) throw new Error('Invalid managed auth file');
    auth = JSON.parse(await file.readFile('utf8'));
  } finally { await file.close(); }
  if (!auth || typeof auth !== 'object' || Array.isArray(auth)) throw new Error('Invalid managed auth document');
  const existing = await request('/credential');
  const integrations = new Set(existing.data.map((entry) => entry.integrationID));
  let restored = 0;
  for (const [integrationID, value] of Object.entries(auth)) {
    if (integrations.has(integrationID)) continue;
    await request('/credential', { integrationID, label: 'Switchyard managed credential', value: nativeCredential(integrationID, value) });
    restored++;
  }
  return restored;
}
