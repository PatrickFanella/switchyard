import { Fragment, useEffect, useMemo, useState, type ReactElement, type ReactNode } from 'react';
import { ApiError, type ApiClient } from '../api';
import type { PostDetail, PostSummary } from '../types';
import { CodeBlock, EmptyState, MicroLabel, NoticeBanner, Panel, SectionIntro, SelectableCard } from './ui';

type PostReaderProps = {
  api: ApiClient | null;
  visible: boolean;
  onUnauthorized: () => void;
};

export function PostReader({ api, visible, onUnauthorized }: PostReaderProps) {
  const [posts, setPosts] = useState<PostSummary[]>([]);
  const [selectedFile, setSelectedFile] = useState('');
  const [details, setDetails] = useState<Record<string, PostDetail>>({});
  const [loadingPosts, setLoadingPosts] = useState(false);
  const [loadingPost, setLoadingPost] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!visible || !api) return;

    let cancelled = false;
    setLoadingPosts(true);
    setError(null);

    void api
      .listPosts()
      .then((items) => {
        if (cancelled) return;
        setPosts(items);
        setSelectedFile((current) => (current && items.some((item) => item.fileName === current) ? current : items[0]?.fileName ?? ''));
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        if (err instanceof ApiError && err.status === 401) {
          onUnauthorized();
          return;
        }
        setError(err instanceof Error ? err.message : 'Failed to load posts');
      })
      .finally(() => {
        if (!cancelled) setLoadingPosts(false);
      });

    return () => {
      cancelled = true;
    };
  }, [api, onUnauthorized, visible]);

  const selectedSummary = posts.find((post) => post.fileName === selectedFile);
  const selectedPost = selectedFile ? details[selectedFile] : undefined;

  useEffect(() => {
    if (!visible || !api || !selectedFile || details[selectedFile]) return;

    let cancelled = false;
    setLoadingPost(true);
    setError(null);

    void api
      .readPost(selectedFile)
      .then((item) => {
        if (cancelled) return;
        setDetails((current) => ({ ...current, [selectedFile]: item }));
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        if (err instanceof ApiError && err.status === 401) {
          onUnauthorized();
          return;
        }
        setError(err instanceof Error ? err.message : 'Failed to load post');
      })
      .finally(() => {
        if (!cancelled) setLoadingPost(false);
      });

    return () => {
      cancelled = true;
    };
  }, [api, details, onUnauthorized, selectedFile, visible]);

  const stats = useMemo(() => summarizePosts(posts), [posts]);

  return (
    <section id="admin-tabpanel-posts" role="tabpanel" aria-labelledby="admin-tab-posts" className={visible ? 'block' : 'hidden'} hidden={!visible}>
      <SectionIntro label="Posts" title="Readable posts from the API" meta={stats} />

      {error ? <NoticeBanner tone="alert">{error}</NoticeBanner> : null}

      <div className="grid gap-4 xl:grid-cols-[0.85fr_1.15fr]">
        <Panel as="aside" className="p-4">
          <div className="mb-3 flex items-center justify-between">
            <MicroLabel tone="pulse">Recent posts</MicroLabel>
            {loadingPosts ? <span className="text-2xs uppercase tracking-[0.12em] text-ink-mute">Syncing…</span> : null}
          </div>

          <div className="space-y-2">
            {posts.length ? (
              posts.map((post) => {
                const active = post.fileName === selectedFile;
                return (
                  <SelectableCard
                    key={post.fileName}
                    type="button"
                    onClick={() => setSelectedFile(post.fileName)}
                    active={active}
                  >
                    <div className="flex items-center justify-between gap-3">
                      <MicroLabel tone={active ? 'pulse' : 'muted'}>{post.date}</MicroLabel>
                      <span className="text-2xs uppercase tracking-[0.12em] text-ink-mute">{post.fileName}</span>
                    </div>
                    <p className="mt-2 text-sm font-medium leading-6 text-inherit">{post.title}</p>
                  </SelectableCard>
                );
              })
            ) : (
              <EmptyState>No posts yet.</EmptyState>
            )}
          </div>
        </Panel>

        <Panel as="article" className="p-5 sm:p-7">
          {selectedSummary ? (
            <>
              <header className="border-b border-border-faint pb-5">
                <MicroLabel tone="signal">{selectedSummary.date}</MicroLabel>
                <h3 className="mt-2 text-2xl font-extrabold tracking-[-0.02em] text-ink sm:text-3xl">{selectedSummary.title}</h3>
                <p className="mt-2 text-sm text-ink-dim">{selectedSummary.fileName}</p>
              </header>

              <div className="mt-6">
                {selectedPost ? <Markdown markdown={selectedPost.markdown} /> : <p className="text-sm text-ink-dim">{loadingPost ? 'Loading post…' : 'Loading post…'}</p>}
              </div>
            </>
          ) : (
            <EmptyState className="flex min-h-[24rem] items-center justify-center p-8">
              {loadingPosts ? 'Loading posts…' : 'Select a post to read it.'}
            </EmptyState>
          )}
        </Panel>
      </div>
    </section>
  );
}

function summarizePosts(posts: PostSummary[]): string {
  if (!posts.length) return '0 posts loaded';
  return `${posts.length} posts loaded`;
}

function Markdown({ markdown }: { markdown: string }) {
  const blocks = parseMarkdown(markdown);

  return <div className="markdown space-y-5">{blocks.map((block, index) => renderBlock(block, index))}</div>;
}

type MarkdownBlock =
  | { type: 'heading'; level: number; text: string }
  | { type: 'paragraph'; lines: string[] }
  | { type: 'list'; items: string[] }
  | { type: 'quote'; lines: string[] }
  | { type: 'code'; text: string };

function parseMarkdown(markdown: string): MarkdownBlock[] {
  const lines = markdown.replace(/\r\n/g, '\n').split('\n');
  const blocks: MarkdownBlock[] = [];
  let index = 0;

  while (index < lines.length) {
    while (index < lines.length && !lines[index].trim()) index += 1;
    if (index >= lines.length) break;

    const line = lines[index];

    if (line.startsWith('```')) {
      index += 1;
      const code: string[] = [];
      while (index < lines.length && !lines[index].startsWith('```')) {
        code.push(lines[index]);
        index += 1;
      }
      if (index < lines.length) index += 1;
      blocks.push({ type: 'code', text: code.join('\n') });
      continue;
    }

    const heading = line.match(/^(#{1,3})\s+(.+)$/);
    if (heading) {
      blocks.push({ type: 'heading', level: heading[1].length, text: heading[2] });
      index += 1;
      continue;
    }

    if (/^[-*]\s+/.test(line)) {
      const items: string[] = [];
      while (index < lines.length && /^[-*]\s+/.test(lines[index])) {
        items.push(lines[index].replace(/^[-*]\s+/, ''));
        index += 1;
      }
      blocks.push({ type: 'list', items });
      continue;
    }

    if (/^>\s+/.test(line)) {
      const quote: string[] = [];
      while (index < lines.length && /^>\s?/.test(lines[index])) {
        quote.push(lines[index].replace(/^>\s?/, ''));
        index += 1;
      }
      blocks.push({ type: 'quote', lines: quote });
      continue;
    }

    const paragraph: string[] = [];
    while (
      index < lines.length &&
      lines[index].trim() &&
      !/^#{1,3}\s+/.test(lines[index]) &&
      !/^[-*]\s+/.test(lines[index]) &&
      !/^>\s?/.test(lines[index]) &&
      !lines[index].startsWith('```')
    ) {
      paragraph.push(lines[index]);
      index += 1;
    }
    blocks.push({ type: 'paragraph', lines: paragraph });
  }

  return blocks;
}

function renderBlock(block: MarkdownBlock, index: number): ReactElement {
  switch (block.type) {
    case 'heading':
      return block.level === 1 ? (
        <h1 key={index} className="text-3xl font-extrabold tracking-[-0.02em] text-ink">
          {renderInline(block.text)}
        </h1>
      ) : block.level === 2 ? (
        <h2 key={index} className="text-2xl font-extrabold tracking-[-0.02em] text-ink">
          {renderInline(block.text)}
        </h2>
      ) : (
        <h3 key={index} className="text-xl font-extrabold tracking-[-0.02em] text-ink">
          {renderInline(block.text)}
        </h3>
      );
    case 'list':
      return (
        <ul key={index} className="space-y-2 pl-5">
          {block.items.map((item, itemIndex) => (
            <li key={`${index}-${itemIndex}`} className="list-disc text-ink-dim marker:text-signal">
              {renderInline(item)}
            </li>
          ))}
        </ul>
      );
    case 'quote':
      return (
        <blockquote key={index} className="border-l-4 border-signal bg-signal/10 px-4 py-3 text-ink">
          {block.lines.map((line, lineIndex) => (
            <p key={`${index}-${lineIndex}`} className={lineIndex ? 'mt-2' : ''}>
              {renderInline(line)}
            </p>
          ))}
        </blockquote>
      );
    case 'code':
      return (
        <CodeBlock key={index} value={block.text} />
      );
    case 'paragraph':
    default:
      return (
        <p key={index} className="leading-8 text-ink-dim">
          {renderInline(block.lines.join(' '))}
        </p>
      );
  }
}

type InlineToken =
  | { type: 'link'; text: string; href: string }
  | { type: 'wiki'; text: string }
  | { type: 'bold'; text: string }
  | { type: 'code'; text: string };

const INLINE_PATTERN = /(\[\[[^\]]+\]\]\(<?[^)>]+>?\)|\[[^\]]+\]\(<?[^)>]+>?\)|\[\[[^\]]+\]\]|\*\*[^*]+\*\*|`[^`]+`)/g;

function renderInline(text: string): ReactNode[] {
  const nodes: ReactNode[] = [];
  let cursor = 0;

  for (const match of text.matchAll(INLINE_PATTERN)) {
    const token = match[0];
    const start = match.index ?? 0;
    if (start > cursor) nodes.push(text.slice(cursor, start));
    nodes.push(renderInlineToken(parseInlineToken(token), nodes.length));
    cursor = start + token.length;
  }

  if (cursor < text.length) nodes.push(text.slice(cursor));
  return nodes.map((node, index) => (typeof node === 'string' ? <Fragment key={index}>{node}</Fragment> : node));
}

function parseInlineToken(token: string): InlineToken {
  const wikiLink = token.match(/^\[\[([^\]]+)\]\]\(<?([^)>]+)>?\)$/);
  if (wikiLink) return { type: 'link', text: wikiLink[1], href: wikiLink[2] };

  const link = token.match(/^\[([^\]]+)\]\(<?([^)>]+)>?\)$/);
  if (link) return { type: 'link', text: cleanLinkText(link[1]), href: link[2] };

  const wiki = token.match(/^\[\[([^\]]+)\]\]$/);
  if (wiki) return { type: 'wiki', text: wiki[1] };

  if (token.startsWith('**') && token.endsWith('**')) return { type: 'bold', text: token.slice(2, -2) };
  return { type: 'code', text: token.slice(1, -1) };
}

function renderInlineToken(token: InlineToken, key: number): ReactElement {
  switch (token.type) {
    case 'link':
      return (
        <a key={key} href={token.href} target="_blank" rel="noreferrer" className="font-medium text-signal-bright underline decoration-signal/40 underline-offset-4 hover:text-ink">
          {token.text}
        </a>
      );
    case 'wiki':
      return (
        <span key={key} className="font-medium text-ink">
          {token.text}
        </span>
      );
    case 'bold':
      return (
        <strong key={key} className="font-semibold text-ink">
          {token.text}
        </strong>
      );
    case 'code':
      return (
        <code key={key} className="border border-border-faint bg-void-900 px-1.5 py-0.5 text-sm text-pulse-bright">
          {token.text}
        </code>
      );
  }
}

function cleanLinkText(text: string): string {
  return text.replace(/^\[\[([^\]]+)\]\]$/, '$1');
}
