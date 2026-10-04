import { spawn } from 'node:child_process';
import { rm, writeFile } from 'node:fs/promises';
import { setTimeout as delay } from 'node:timers/promises';
import { restoreCredentials } from './switchyard-opencode-credentials.mjs';

const ready = '/tmp/switchyard-opencode-ready';
await rm(ready, { force: true });
const child = spawn('opencode', ['serve', '--hostname', process.env.OPENCODE_SERVER_HOSTNAME ?? '0.0.0.0',
  '--port', process.env.OPENCODE_SERVER_PORT ?? '4096'], { stdio: 'inherit' });
let shutdown = false;
let bootstrapFailed = false;
for (const signal of ['SIGTERM', 'SIGINT']) process.on(signal, () => { shutdown = true; child.kill(signal); });
child.on('error', () => { console.error('Managed OpenCode server could not start'); process.exit(1); });
child.on('exit', async (code) => { await rm(ready, { force: true }); process.exit(bootstrapFailed ? 1 : code ?? (shutdown ? 0 : 1)); });
const headers = { authorization: 'Basic ' + Buffer.from('opencode:' + process.env.OPENCODE_SERVER_PASSWORD).toString('base64'),
  'Content-Type': 'application/json' };
const request = async (path, body) => {
  const response = await fetch('http://127.0.0.1:' + (process.env.OPENCODE_SERVER_PORT ?? '4096') + '/api' + path,
    { headers, method: body ? 'POST' : 'GET', body: body ? JSON.stringify(body) : undefined, signal: AbortSignal.timeout(5000) });
  if (!response.ok) throw new Error('Managed OpenCode bootstrap request failed');
  return response.json();
};
try {
  let listening = false;
  for (let attempt = 0; attempt < 60; attempt++) {
    try { await request('/info'); listening = true; break; } catch { await delay(500); }
  }
  if (!listening) throw new Error('Managed OpenCode startup timed out');
  await restoreCredentials(request, process.env.XDG_DATA_HOME);
  await writeFile(ready, 'ready\n', { mode: 0o600 });
} catch {
  bootstrapFailed = true;
  console.error('Managed OpenCode credential bootstrap failed');
  child.kill('SIGTERM');
  process.exitCode = 1;
}
