const password = process.env.OPENCODE_SERVER_PASSWORD;
const headers = { authorization: 'Basic ' + Buffer.from('opencode:' + password).toString('base64') };
async function get(path) {
  const response = await fetch('http://127.0.0.1:4096/api' + path, { headers, signal: AbortSignal.timeout(4000) });
  if (!response.ok) throw new Error('OpenCode health request failed');
  return response.json();
}
try {
  const info = await get('/info');
  if (info.version !== '2.0.22') throw new Error('Unexpected OpenCode version');
  if ((process.env.OPENCODE_SECURITY_MODE ?? 'strict') === 'strict') {
    const plugins = await get('/plugin');
    for (const id of ['switchyard.rtk', 'switchyard.managed']) {
      if (!plugins.data.some((plugin) => plugin.id === id && plugin.state.status === 'active')) throw new Error('Managed plugins are not ready');
    }
    const agents = await get('/agent');
    const build = agents.data.find((agent) => agent.id === 'build');
    if (!build) throw new Error('Managed agent is not ready');
    for (const action of ['shell', 'subagent', 'external_directory', 'question']) {
      const rules = build.permissions.filter((rule) => [action, '*'].includes(rule.action) && rule.resource === '*');
      if (rules.at(-1)?.effect !== 'deny') throw new Error('Managed permissions are not ready');
    }
  }
} catch {
  process.exit(1);
}
