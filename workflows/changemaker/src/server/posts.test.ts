import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { listGeneratedPosts, readGeneratedPost } from './posts.js';

describe('generated posts', () => {
  it('lists markdown posts newest first', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'changemaker-posts-'));
    writeFileSync(join(dir, '2026-05-22-post.md'), '# May 22 field report: Code progress\n\nBody');
    writeFileSync(join(dir, '2026-05-23-post.md'), '# May 23 field report: Infrastructure\n\nBody');

    const posts = await listGeneratedPosts(dir);

    expect(posts.map((post) => post.fileName)).toEqual(['2026-05-23-post.md', '2026-05-22-post.md']);
    expect(posts[0]).toMatchObject({ date: '2026-05-23', title: 'May 23 field report: Infrastructure' });
  });

  it('returns an empty list for a missing posts dir', async () => {
    const dir = join(tmpdir(), `changemaker-missing-posts-${Date.now()}-${Math.random()}`);

    await expect(listGeneratedPosts(dir)).resolves.toEqual([]);
  });

  it('reads one markdown post by file name', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'changemaker-posts-'));
    writeFileSync(join(dir, '2026-05-22-post.md'), '# Title\n\nReadable body');

    const post = await readGeneratedPost(dir, '2026-05-22-post.md');

    expect(post.markdown).toContain('Readable body');
    expect(post).toMatchObject({ fileName: '2026-05-22-post.md', date: '2026-05-22', title: 'Title' });
  });

  it('rejects unsafe file names', async () => {
    const dir = mkdtempSync(join(tmpdir(), 'changemaker-posts-'));

    await expect(readGeneratedPost(dir, '../2026-05-22-post.md')).rejects.toThrow('Invalid post file name');
  });
});
