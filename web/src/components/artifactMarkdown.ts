import { sanitizeLinkHref } from './artifactPreview';

// A deliberately small Markdown subset for operator artifacts: headings,
// paragraphs, fenced code, lists, quotes, rules, inline code/strong/em/links.
// Raw HTML is never interpreted; it flows through as literal text and React
// escapes it on render. Images are described, never loaded.

export type MarkdownInline =
  | { type: 'text'; value: string }
  | { type: 'code'; value: string }
  | { type: 'strong'; children: MarkdownInline[] }
  | { type: 'em'; children: MarkdownInline[] }
  | { type: 'link'; href: string | null; raw: string; children: MarkdownInline[] };

export type MarkdownBlock =
  | { type: 'heading'; level: number; children: MarkdownInline[] }
  | { type: 'paragraph'; children: MarkdownInline[] }
  | { type: 'code'; language: string; value: string }
  | { type: 'list'; ordered: boolean; items: MarkdownInline[][] }
  | { type: 'quote'; children: MarkdownInline[] }
  | { type: 'rule' };

const inlinePattern =
  /(`+)([\s\S]*?)\1|!\[([^\]]*)\]\(((?:[^()\s]|\([^()\s]*\))*)(?:\s+"[^"]*")?\)|\[([^\]]+)\]\(((?:[^()\s]|\([^()\s]*\))*)(?:\s+"[^"]*")?\)|\*\*([^*]+)\*\*|__([^_]+)__|\*([^*\s][^*]*?)\*|(?<![\w])_([^_\s][^_]*?)_(?![\w])|<(https?:\/\/[^\s>]+)>/g;

export function parseInline(text: string): MarkdownInline[] {
  const nodes: MarkdownInline[] = [];
  let last = 0;
  const pushText = (value: string) => {
    if (!value) return;
    const previous = nodes[nodes.length - 1];
    if (previous && previous.type === 'text') {
      previous.value += value;
    } else {
      nodes.push({ type: 'text', value });
    }
  };
  for (const match of text.matchAll(inlinePattern)) {
    const index = match.index ?? 0;
    pushText(text.slice(last, index));
    last = index + match[0].length;
    const [, , code, imageAlt, , linkText, linkHref, strong, strongAlt, em, emAlt, autolink] = match;
    if (code !== undefined) {
      nodes.push({ type: 'code', value: code });
    } else if (imageAlt !== undefined) {
      pushText(`${imageAlt || 'image'} (image not loaded)`);
    } else if (linkText !== undefined) {
      nodes.push({ type: 'link', href: sanitizeLinkHref(linkHref ?? ''), raw: linkHref ?? '', children: parseInline(linkText) });
    } else if (strong !== undefined || strongAlt !== undefined) {
      nodes.push({ type: 'strong', children: parseInline(strong ?? strongAlt ?? '') });
    } else if (em !== undefined || emAlt !== undefined) {
      nodes.push({ type: 'em', children: parseInline(em ?? emAlt ?? '') });
    } else if (autolink !== undefined) {
      nodes.push({ type: 'link', href: sanitizeLinkHref(autolink), raw: autolink, children: [{ type: 'text', value: autolink }] });
    }
  }
  pushText(text.slice(last));
  return nodes;
}

export function parseMarkdown(source: string): MarkdownBlock[] {
  const lines = source.replace(/\r\n?/g, '\n').split('\n');
  const blocks: MarkdownBlock[] = [];
  let paragraph: string[] = [];
  let list: { ordered: boolean; items: string[] } | null = null;
  let quote: string[] = [];

  const flushParagraph = () => {
    if (paragraph.length > 0) {
      blocks.push({ type: 'paragraph', children: parseInline(paragraph.join(' ').trim()) });
      paragraph = [];
    }
  };
  const flushList = () => {
    if (list) {
      blocks.push({ type: 'list', ordered: list.ordered, items: list.items.map((item) => parseInline(item.trim())) });
      list = null;
    }
  };
  const flushQuote = () => {
    if (quote.length > 0) {
      blocks.push({ type: 'quote', children: parseInline(quote.join(' ').trim()) });
      quote = [];
    }
  };
  const flushAll = () => {
    flushParagraph();
    flushList();
    flushQuote();
  };

  for (let index = 0; index < lines.length; index += 1) {
    const line = lines[index];
    const fence = /^\s*(```|~~~)\s*([\w.+-]*)\s*$/.exec(line);
    if (fence) {
      flushAll();
      const marker = fence[1];
      const body: string[] = [];
      index += 1;
      while (index < lines.length && !new RegExp(`^\\s*${marker}\\s*$`).test(lines[index])) {
        body.push(lines[index]);
        index += 1;
      }
      blocks.push({ type: 'code', language: fence[2] ?? '', value: body.join('\n') });
      continue;
    }
    if (line.trim() === '') {
      flushAll();
      continue;
    }
    const heading = /^(#{1,6})\s+(.+?)\s*#*\s*$/.exec(line);
    if (heading) {
      flushAll();
      blocks.push({ type: 'heading', level: heading[1].length, children: parseInline(heading[2]) });
      continue;
    }
    if (/^\s*(?:-{3,}|\*{3,}|_{3,})\s*$/.test(line)) {
      flushAll();
      blocks.push({ type: 'rule' });
      continue;
    }
    const item = /^\s{0,3}(?:([-*+])|(\d{1,9}[.)]))\s+(.*)$/.exec(line);
    if (item) {
      flushParagraph();
      flushQuote();
      const ordered = item[2] !== undefined;
      if (!list || list.ordered !== ordered) {
        flushList();
        list = { ordered, items: [] };
      }
      list.items.push(item[3]);
      continue;
    }
    const quoted = /^\s{0,3}>\s?(.*)$/.exec(line);
    if (quoted) {
      flushParagraph();
      flushList();
      quote.push(quoted[1]);
      continue;
    }
    if (list && /^\s+\S/.test(line)) {
      list.items[list.items.length - 1] += ` ${line.trim()}`;
      continue;
    }
    flushList();
    flushQuote();
    paragraph.push(line.trim());
  }
  flushAll();
  return blocks;
}
