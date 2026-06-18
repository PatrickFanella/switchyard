import { existsSync, readFileSync, writeFileSync } from 'node:fs';

const [targetPath, defaultsPath] = process.argv.slice(2);
const defaults = JSON.parse(readFileSync(defaultsPath, 'utf8'));
const current = existsSync(targetPath) ? JSON.parse(readFileSync(targetPath, 'utf8')) : {};

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
  plugin: uniqueArray(current.plugin, defaults.plugin),
  agent: mergeObject(current.agent, defaults.agent),
  mcp: mergeObject(current.mcp, defaults.mcp),
  references: mergeObject(current.references, defaults.references),
  skills: {
    ...(defaults.skills ?? {}),
    ...(current.skills ?? {}),
    paths: uniqueArray(current.skills?.paths, defaults.skills?.paths),
    urls: uniqueArray(current.skills?.urls, defaults.skills?.urls),
  },
  compaction: mergeObject(current.compaction, defaults.compaction),
  experimental: mergeObject(current.experimental, defaults.experimental),
  tool_output: mergeObject(current.tool_output, defaults.tool_output),
};

writeFileSync(targetPath, JSON.stringify(merged, null, 2) + '\n');
