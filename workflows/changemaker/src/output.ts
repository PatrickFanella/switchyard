import { mkdir, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import type { ChangesOutput } from './types.js';
import type { DailyPost } from './writer/daily-post.js';
import type { NarrativeState } from './narrative/state.js';

export async function writeChangesOutput(output: ChangesOutput, outDir: string): Promise<string> {
  await mkdir(outDir, { recursive: true });
  const date = output.generatedAt.slice(0, 10);
  const filePath = join(outDir, `${date}-changes.json`);
  await writeFile(filePath, `${JSON.stringify(output, null, 2)}\n`, 'utf8');
  return filePath;
}

export async function writePostOutput(post: DailyPost, generatedAt: string, outDir: string, kind = 'daily'): Promise<string> {
  await mkdir(outDir, { recursive: true });
  const date = generatedAt.slice(0, 10);
  const suffix = kind === 'daily' ? 'post' : `${kind}-post`;
  const filePath = join(outDir, `${date}-${suffix}.md`);
  await writeFile(filePath, post.markdown, 'utf8');
  return filePath;
}

export async function writeNarrativeStateOutput(
  state: NarrativeState,
  generatedAt: string,
  outDir: string,
): Promise<string> {
  await mkdir(outDir, { recursive: true });
  const date = generatedAt.slice(0, 10);
  const filePath = join(outDir, `${date}-narrative-state.json`);
  await writeFile(filePath, `${JSON.stringify(state, null, 2)}\n`, 'utf8');
  return filePath;
}
