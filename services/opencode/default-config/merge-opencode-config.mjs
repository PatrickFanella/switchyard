import { existsSync, readFileSync, writeFileSync } from 'node:fs';

const [targetPath, defaultsPath] = process.argv.slice(2);
const defaults = JSON.parse(readFileSync(defaultsPath, 'utf8'));
const securityMode = process.env.OPENCODE_SECURITY_MODE ?? 'strict';

if (!['strict', 'development'].includes(securityMode)) {
  throw new Error('OPENCODE_SECURITY_MODE must be strict or development');
}

const preserveUserExtensions = securityMode === 'development';
// Persisted configuration is untrusted in strict mode. Do not even read it:
// malformed or oversized state must not affect secure startup.
const current = preserveUserExtensions && existsSync(targetPath)
  ? JSON.parse(readFileSync(targetPath, 'utf8'))
  : {};

function uniqueArray(left = [], right = []) {
  return [...new Set([...(Array.isArray(left) ? left : []), ...(Array.isArray(right) ? right : [])])];
}

function mergeObject(currentValue = {}, defaultValue = {}) {
  return { ...(defaultValue ?? {}), ...(currentValue ?? {}) };
}

const developmentMerged = {
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

// Strict mode treats the persisted file as untrusted. Keeping the managed
// document whole avoids accidentally carrying new security-sensitive keys as
// OpenCode adds configuration surfaces.
const merged = preserveUserExtensions ? developmentMerged : defaults;

writeFileSync(targetPath, JSON.stringify(merged, null, 2) + '\n');
