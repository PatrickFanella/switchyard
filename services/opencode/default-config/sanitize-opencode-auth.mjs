import { constants, open, rename, rm } from 'node:fs/promises';
import { randomBytes } from 'node:crypto';
import { basename, dirname, join } from 'node:path';

const authPath = process.argv[2];
const maxAuthBytes = 1024 * 1024;

if (!authPath) throw new Error('auth path is required');

function sanitizedCredential(value) {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return undefined;
  if (value.type === 'api' && typeof value.key === 'string') {
    const credential = { type: 'api', key: value.key };
    if (
      value.metadata !== null &&
      typeof value.metadata === 'object' &&
      !Array.isArray(value.metadata) &&
      Object.values(value.metadata).every((item) => typeof item === 'string')
    ) {
      credential.metadata = value.metadata;
    }
    return credential;
  }
  if (
    value.type === 'oauth' &&
    typeof value.refresh === 'string' &&
    typeof value.access === 'string' &&
    Number.isSafeInteger(value.expires) &&
    value.expires >= 0
  ) {
    const credential = {
      type: 'oauth',
      refresh: value.refresh,
      access: value.access,
      expires: value.expires,
    };
    if (typeof value.accountId === 'string') credential.accountId = value.accountId;
    if (typeof value.enterpriseUrl === 'string') credential.enterpriseUrl = value.enterpriseUrl;
    return credential;
  }
  return undefined;
}

function providerKeyAllowed(provider) {
  const normalized = provider.toLowerCase().replaceAll('_', '-');
  return ![
    'account',
    'accounts',
    'config',
    'remote-config',
    'remoteconfig',
    'wellknown',
    'well-known',
  ].includes(normalized);
}

async function readAuth() {
  let file;
  try {
    file = await open(authPath, constants.O_RDONLY | constants.O_NOFOLLOW);
  } catch (error) {
    if (error.code === 'ENOENT') return undefined;
    throw error;
  }
  try {
    const stat = await file.stat();
    if (!stat.isFile() || stat.size > maxAuthBytes) throw new Error('invalid auth file');
    return await file.readFile({ encoding: 'utf8' });
  } finally {
    await file.close();
  }
}

async function atomicWrite(content) {
  const temporary = join(
    dirname(authPath),
    `.${basename(authPath)}.switchyard-${process.pid}-${randomBytes(8).toString('hex')}`,
  );
  let file;
  try {
    file = await open(temporary, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL, 0o600);
    await file.chmod(0o600);
    await file.writeFile(content, { encoding: 'utf8' });
    await file.sync();
    await file.close();
    file = undefined;
    await rename(temporary, authPath);
  } finally {
    if (file) await file.close();
    await rm(temporary, { force: true });
  }
}

try {
  const raw = await readAuth();
  if (raw === undefined) process.exit(0);
  const persisted = JSON.parse(raw);
  if (persisted === null || typeof persisted !== 'object' || Array.isArray(persisted)) {
    throw new Error('invalid auth root');
  }

  const sanitized = Object.create(null);
  for (const [provider, credential] of Object.entries(persisted)) {
    if (!providerKeyAllowed(provider)) continue;
    const safeCredential = sanitizedCredential(credential);
    if (safeCredential !== undefined) sanitized[provider] = safeCredential;
  }
  await atomicWrite(`${JSON.stringify(sanitized, null, 2)}\n`);
} catch {
  console.error('strict OpenCode auth sanitization failed');
  process.exit(65);
}
