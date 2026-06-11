import { readdir, readFile } from 'node:fs/promises';
import { basename, join } from 'node:path';

const POST_FILE_NAME_PATTERN = /^\d{4}-\d{2}-\d{2}-post\.md$/;

export type PostListItem = {
  fileName: string;
  date: string;
  title: string;
};

export type PostDetail = PostListItem & {
  markdown: string;
};

export async function listGeneratedPosts(postsDir: string): Promise<PostListItem[]> {
  const entries = await readdir(postsDir, { withFileTypes: true }).catch((error: unknown) => {
    if (isMissingDirectoryError(error)) return [];
    throw error;
  });

  const files = entries
    .filter((entry) => entry.isFile() && POST_FILE_NAME_PATTERN.test(entry.name))
    .map((entry) => entry.name)
    .sort()
    .reverse();

  return Promise.all(files.map((fileName) => readPostSummary(postsDir, fileName)));
}

export async function readGeneratedPost(postsDir: string, fileName: string): Promise<PostDetail> {
  const safeName = validatePostFileName(fileName);
  const summary = await readPostSummary(postsDir, safeName);
  const markdown = await readFile(join(postsDir, safeName), 'utf8');

  return {
    ...summary,
    markdown,
  };
}

function validatePostFileName(fileName: string): string {
  const safeName = basename(fileName);
  if (safeName !== fileName || !POST_FILE_NAME_PATTERN.test(safeName)) {
    throw new Error('Invalid post file name');
  }

  return safeName;
}

async function readPostSummary(postsDir: string, fileName: string): Promise<PostListItem> {
  const markdown = await readFile(join(postsDir, fileName), 'utf8');
  const title = markdown.match(/^#\s+(.+)$/m)?.[1] ?? fileName;

  return {
    fileName,
    date: fileName.slice(0, 10),
    title,
  };
}

function isMissingDirectoryError(error: unknown): boolean {
  return typeof error === 'object' && error !== null && 'code' in error && (error as { code?: string }).code === 'ENOENT';
}
