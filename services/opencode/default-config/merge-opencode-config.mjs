import { existsSync, readFileSync, writeFileSync } from 'node:fs';

const [targetPath, defaultsPath] = process.argv.slice(2);
const defaults = JSON.parse(readFileSync(defaultsPath, 'utf8'));
const current = existsSync(targetPath) ? JSON.parse(readFileSync(targetPath, 'utf8')) : {};
const securityMode = process.env.OPENCODE_SECURITY_MODE ?? 'strict';

if (!['strict', 'development'].includes(securityMode)) {
  throw new Error('OPENCODE_SECURITY_MODE must be strict or development');
}

// A mounted config directory is persistent and may contain settings written by
// an earlier, less-restricted image. In strict mode managed permissions and
// plugins always win, and no MCP server is inherited accidentally. Local
// experimentation can opt into the historical merge behavior explicitly.
const preserveUserExtensions = securityMode === 'development';

function uniqueArray(left = [], right = []) {
  return [...new Set([...(Array.isArray(left) ? left : []), ...(Array.isArray(right) ? right : [])])];
}

function mergeObject(currentValue = {}, defaultValue = {}) {
  return { ...(defaultValue ?? {}), ...(currentValue ?? {}) };
}

const merged = {
  ...defaults,
  ...current,
  $schema: current.$schema ?? defaults.$schema,
  plugin: preserveUserExtensions ? uniqueArray(current.plugin, defaults.plugin) : defaults.plugin,
  agent: mergeObject(current.agent, defaults.agent),
  mcp: preserveUserExtensions ? mergeObject(current.mcp, defaults.mcp) : defaults.mcp,
  references: mergeObject(current.references, defaults.references),
  skills: {
    ...(defaults.skills ?? {}),
    ...(current.skills ?? {}),
    paths: uniqueArray(current.skills?.paths, defaults.skills?.paths),
    urls: uniqueArray(current.skills?.urls, defaults.skills?.urls),
  },
  compaction: mergeObject(current.compaction, defaults.compaction),
  permission: preserveUserExtensions ? (current.permission ?? defaults.permission) : defaults.permission,
  experimental: mergeObject(current.experimental, defaults.experimental),
  tool_output: mergeObject(current.tool_output, defaults.tool_output),
};

writeFileSync(targetPath, JSON.stringify(merged, null, 2) + '\n');
