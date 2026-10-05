// @vitest-environment jsdom

import { act } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import type { Artifact, RunDetail } from '../types';
import { ArtifactPreview } from './ArtifactPreview';
import { parseInline, parseMarkdown } from './artifactMarkdown';
import { ARTIFACT_PREVIEW_MAX_BYTES, analyzeArtifact, formatByteSize, parseInlineArtifactURI, safeArtifactFileName, sanitizeLinkHref } from './artifactPreview';
import { RunCockpitPage } from './RunCockpitPage';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const now = () => new Date('2026-09-24T00:00:00.000Z');
const future = '2026-10-24T00:00:00.000Z';
const past = '2026-09-01T00:00:00.000Z';

async function sha256(body: string) {
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(body));
  return Array.from(new Uint8Array(digest), (byte) => byte.toString(16).padStart(2, '0')).join('');
}

type InlineOptions = { base64?: boolean; retention?: string | null; sha256?: string; byte_size?: number; storage_class?: string; content_type?: string | null };

// Mirrors the control plane producers: data:<type>,<path-escaped body> with measured metadata.
async function inlineArtifact(name: string, kind: string, contentType: string, body: string, options: InlineOptions = {}): Promise<Artifact> {
  const bytes = new TextEncoder().encode(body);
  const payload = options.base64 ? `;base64,${btoa(Array.from(bytes, (byte) => String.fromCharCode(byte)).join(''))}` : `,${encodeURIComponent(body)}`;
  return {
    id: `art-${name}`,
    run_id: 'run-1',
    name,
    kind,
    uri: `data:${contentType}${payload}`,
    storage_class: options.storage_class ?? 'inline-data-uri',
    content_type: options.content_type === null ? undefined : (options.content_type ?? contentType),
    byte_size: options.byte_size ?? bytes.length,
    sha256: options.sha256 ?? (await sha256(body)),
    retention_until: options.retention === undefined ? future : options.retention,
    created_at: '2026-09-23T12:00:00.000Z',
  };
}

const issueJSON = JSON.stringify({ repo: 'OWNER/REPO', issue: 70, title: 'Add safe artifact preview', labels: ['ready-for-agent'] });
const ciChecksJSON = JSON.stringify({ head_sha: 'abc123', checks: [{ name: 'verify', status: 'success' }, { name: 'web-test', status: 'failure' }] });
const mergeJSON = JSON.stringify({ merged: true, merge_commit_sha: 'deadbeefcafe', pull_request_number: 101 });
const diffstat = ' web/src/components/ArtifactPreview.tsx | 42 +++++++---\n 1 file changed, 30 insertions(+), 12 deletions(-)\n';
const verifyLog = 'go test ./...\nok  \tswitchyard/internal/controlplane\t12.3s\npnpm --dir web run test\n 19 passed\n';
const aiReview = [
  '# AI review',
  '',
  'Reviewed **actual committed content** for issue [#70](https://git.subcult.tv/subculture-collective/switchyard/issues/70).',
  '',
  '## Findings',
  '',
  '- Sanitizes `href` values',
  '- Ignores <script>alert("xss")</script> markup',
  '- Rejects [dangerous](javascript:alert(1)) links',
  '- Does not load ![diagram](https://example.test/diagram.png)',
  '',
  '> Verdict: approve',
  '',
  '```diff',
  '+ const safe = true;',
  '```',
].join('\n');
const devlog = ['# Devlog', '', '## Progress', '', '1. Planned the change', '2. Implemented preview', '', '---', '', 'Follow-up: *none* pending.'].join('\n');

type Fixtures = Record<'issue' | 'ciChecks' | 'merge' | 'diffstat' | 'verify' | 'review' | 'devlog', Artifact>;
let fixtures: Fixtures;

beforeAll(async () => {
  fixtures = {
    issue: await inlineArtifact('open-pilot-issue.json', 'json', 'application/json;charset=utf-8', issueJSON),
    ciChecks: await inlineArtifact('open-pilot-ci-checks.json', 'json', 'application/json;charset=utf-8', ciChecksJSON),
    merge: await inlineArtifact('open-pilot-merge.json', 'json', 'application/json;charset=utf-8', mergeJSON),
    diffstat: await inlineArtifact('open-pilot-git-diffstat.txt', 'text', 'text/plain;charset=utf-8', diffstat),
    verify: await inlineArtifact('verify.log', 'text', 'text/plain;charset=utf-8', verifyLog, { base64: true }),
    review: await inlineArtifact('open-pilot-ai-review.md', 'markdown', 'text/markdown;charset=utf-8', aiReview),
    devlog: await inlineArtifact('devlog.md', 'markdown', 'text/markdown;charset=utf-8', devlog),
  };
});

let mounted: { root: Root; container: HTMLElement }[] = [];

async function render(element: React.ReactElement) {
  const container = document.createElement('div');
  document.body.appendChild(container);
  const root = createRoot(container);
  await act(async () => {
    root.render(element);
  });
  mounted.push({ root, container });
  return container;
}

async function settle() {
  // Web Crypto completion depends on the worker pool, so wait for its rendered
  // result instead of assuming a fixed number of timer ticks is sufficient.
  const deadline = Date.now() + 2_000;
  do {
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 20));
    });
  } while (document.querySelector('[data-integrity="pending"]') && Date.now() < deadline);
  expect(document.querySelector('[data-integrity="pending"]')).toBeNull();
}

function buttons(container: HTMLElement) {
  return Array.from(container.querySelectorAll('button'));
}

function button(container: HTMLElement, name: string) {
  const element = buttons(container).find((item) => item.textContent?.trim() === name);
  if (!element) throw new Error(`button ${name} not found`);
  return element;
}

async function click(element: HTMLElement) {
  await act(async () => {
    element.dispatchEvent(new MouseEvent('click', { bubbles: true }));
  });
  await settle();
}

afterEach(async () => {
  for (const { root } of mounted) {
    await act(async () => {
      root.unmount();
    });
  }
  mounted = [];
  document.body.innerHTML = '';
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('inline artifact decoding', () => {
  it('decodes percent-encoded and base64 payloads and reports metadata', () => {
    const text = parseInlineArtifactURI(fixtures.diffstat.uri);
    expect(text.status).toBe('ok');
    if (text.status === 'ok') {
      expect(text.content.text).toBe(diffstat);
      expect(text.content.mediaType).toBe('text/plain');
      expect(text.content.charset).toBe('utf-8');
    }
    const encoded = parseInlineArtifactURI(fixtures.verify.uri);
    expect(encoded.status).toBe('ok');
    if (encoded.status === 'ok') {
      expect(encoded.content.base64).toBe(true);
      expect(encoded.content.text).toBe(verifyLog);
    }
  });

  it('never treats remote, javascript or file locations as decodable', () => {
    for (const uri of ['https://example.test/bundle.zip', 'javascript:alert(1)', 'file:///etc/passwd', 'blob:http://example.test/x']) {
      expect(parseInlineArtifactURI(uri).status).toBe('unsupported');
    }
  });

  it('flags malformed payloads instead of throwing', () => {
    expect(parseInlineArtifactURI('data:text/plain;base64,not*base64').status).toBe('malformed');
    expect(parseInlineArtifactURI('data:text/plain,%E0%A4%A').status).toBe('malformed');
    expect(parseInlineArtifactURI('data:text/plain;evil=1,hi').status).toBe('malformed');
    expect(parseInlineArtifactURI('data:').status).toBe('malformed');
    expect(parseInlineArtifactURI('').status).toBe('malformed');
    const binary = parseInlineArtifactURI('data:application/octet-stream;base64,/w==');
    expect(binary.status).toBe('ok');
    if (binary.status === 'ok') expect(binary.content.text).toBeNull();
  });

  it('refuses oversized payloads before decoding them', () => {
    const large = parseInlineArtifactURI(`data:text/plain,${'x'.repeat(ARTIFACT_PREVIEW_MAX_BYTES + 1)}`);
    expect(large.status).toBe('oversized');
    const declared = analyzeArtifact({ ...fixtures.diffstat, byte_size: ARTIFACT_PREVIEW_MAX_BYTES + 1 }, now());
    expect(declared.decode.status).toBe('oversized');
    expect(analyzeArtifact(fixtures.diffstat, now(), 8).decode.status).toBe('oversized');
  });

  it('detects size mismatches, expiry and legacy rows without measurements', async () => {
    expect(analyzeArtifact({ ...fixtures.diffstat, byte_size: 3 }, now()).sizeMismatch).toBe(true);
    expect(analyzeArtifact(await inlineArtifact('a.txt', 'text', 'text/plain', 'x', { retention: past }), now()).retention.expired).toBe(true);
    const legacy = analyzeArtifact({ id: 'legacy', run_id: 'run-1', name: 'old.md', kind: 'markdown', uri: 'data:text/markdown,hello', created_at: past }, now());
    expect(legacy.declaredBytes).toBeNull();
    expect(legacy.previewKind).toBe('markdown');
    expect(legacy.retention.until).toBeNull();
  });

  it('formats sizes, file names and links defensively', () => {
    expect(formatByteSize(512)).toBe('512 B');
    expect(formatByteSize(1536)).toBe('1.5 KiB');
    expect(formatByteSize(undefined)).toBe('unknown size');
    expect(safeArtifactFileName('../../etc/passwd')).toBe('.._.._etc_passwd'.replace(/^\.+/, ''));
    expect(safeArtifactFileName('')).toBe('artifact');
    expect(sanitizeLinkHref('https://example.test/a?b=1')).toBe('https://example.test/a?b=1');
    expect(sanitizeLinkHref('javascript:alert(1)')).toBeNull();
    expect(sanitizeLinkHref('data:text/html,<script>')).toBeNull();
    expect(sanitizeLinkHref('/relative')).toBeNull();
  });
});

describe('markdown parser', () => {
  it('keeps raw HTML as text and strips unsafe links', () => {
    const blocks = parseMarkdown(aiReview);
    expect(blocks[0]).toMatchObject({ type: 'heading', level: 1 });
    const list = blocks.find((block) => block.type === 'list');
    expect(list && list.type === 'list' ? list.items.length : 0).toBe(4);
    const inline = parseInline('see [x](javascript:alert(1)) and <https://ok.test/>');
    expect(inline).toContainEqual({ type: 'link', href: null, raw: 'javascript:alert(1)', children: [{ type: 'text', value: 'x' }] });
    expect(inline).toContainEqual({ type: 'link', href: 'https://ok.test/', raw: 'https://ok.test/', children: [{ type: 'text', value: 'https://ok.test/' }] });
    expect(parseInline('<b>bold</b>')).toEqual([{ type: 'text', value: '<b>bold</b>' }]);
  });

  it('parses fenced code, quotes, ordered lists and rules', () => {
    const blocks = parseMarkdown(devlog);
    expect(blocks.map((block) => block.type)).toEqual(['heading', 'heading', 'list', 'rule', 'paragraph']);
    const code = parseMarkdown('```json\n{"a": 1}\n```').find((block) => block.type === 'code');
    expect(code).toEqual({ type: 'code', language: 'json', value: '{"a": 1}' });
  });
});

describe('ArtifactPreview', () => {
  it('renders Open Pilot issue, CI, merge and test artifacts with type, size and integrity metadata', async () => {
    for (const artifact of [fixtures.issue, fixtures.ciChecks, fixtures.merge, fixtures.diffstat, fixtures.verify]) {
      const container = await render(<ArtifactPreview artifact={artifact} now={now} />);
      await settle();
      expect(container.textContent).toContain(artifact.content_type ?? '');
      expect(container.textContent).toContain(formatByteSize(artifact.byte_size));
      expect(container.querySelector('[data-integrity]')?.getAttribute('data-integrity')).toBe('verified');
      expect(container.textContent).toContain('sha256 verified');
      expect(container.textContent).toContain('until');
      await click(button(container, 'Show preview'));
      const pre = container.querySelector('section pre');
      expect(pre).not.toBeNull();
      if (artifact.kind === 'json') {
        expect(pre?.textContent).toBe(JSON.stringify(JSON.parse(artifact === fixtures.issue ? issueJSON : artifact === fixtures.ciChecks ? ciChecksJSON : mergeJSON), null, 2));
      } else {
        expect(pre?.textContent).toBe(artifact === fixtures.diffstat ? diffstat : verifyLog);
      }
      expect(button(container, 'Hide preview').getAttribute('aria-expanded')).toBe('true');
    }
  });

  it('renders Devlog and AI review Markdown safely without HTML, scripts, images or unsafe links', async () => {
    const container = await render(<ArtifactPreview artifact={fixtures.review} now={now} />);
    await click(button(container, 'Show preview'));
    const section = container.querySelector('section');
    expect(section).not.toBeNull();
    expect(section?.querySelector('script')).toBeNull();
    expect(section?.querySelector('img')).toBeNull();
    expect(section?.textContent).toContain('<script>alert("xss")</script>');
    expect(section?.textContent).toContain('diagram (image not loaded)');
    expect(section?.textContent).toContain('dangerous (unsafe link removed)');
    const links = Array.from(section?.querySelectorAll('a') ?? []);
    expect(links.map((link) => link.getAttribute('href'))).toEqual(['https://git.subcult.tv/subculture-collective/switchyard/issues/70']);
    expect(links[0].getAttribute('rel')).toBe('noopener noreferrer');
    expect(section?.querySelector('blockquote')?.textContent).toBe('Verdict: approve');
    expect(section?.querySelector('pre')?.textContent).toBe('+ const safe = true;');
    expect(section?.querySelector('strong')?.textContent).toBe('actual committed content');

    const devlogContainer = await render(<ArtifactPreview artifact={fixtures.devlog} now={now} />);
    await click(button(devlogContainer, 'Show preview'));
    expect(devlogContainer.querySelector('section ol')?.querySelectorAll('li').length).toBe(2);
    expect(devlogContainer.querySelector('section hr')).not.toBeNull();
    expect(devlogContainer.querySelector('section em')?.textContent).toBe('none');
  });

  it('falls back explicitly for malformed payloads and malformed JSON', async () => {
    const malformed: Artifact = { ...fixtures.issue, id: 'bad', uri: 'data:application/json;base64,@@@' };
    const container = await render(<ArtifactPreview artifact={malformed} now={now} />);
    await settle();
    expect(container.textContent).toContain('Preview unavailable: Base64 payload is not valid.');
    expect(buttons(container).map((item) => item.textContent?.trim())).toEqual(['Copy', 'Download']);
    expect(button(container, 'Copy').disabled).toBe(true);
    expect(button(container, 'Download').disabled).toBe(true);
    expect(container.textContent).toContain('not checked');

    const brokenJSON = await inlineArtifact('open-pilot-pr.json', 'json', 'application/json;charset=utf-8', '{"merged": tru');
    const jsonContainer = await render(<ArtifactPreview artifact={brokenJSON} now={now} />);
    await click(button(jsonContainer, 'Show preview'));
    expect(jsonContainer.textContent).toContain('Malformed JSON');
    expect(jsonContainer.querySelector('section pre')?.textContent).toBe('{"merged": tru');
  });

  it('falls back for oversized artifacts while keeping download available', async () => {
    const oversized = { ...fixtures.diffstat, byte_size: ARTIFACT_PREVIEW_MAX_BYTES + 1 };
    const container = await render(<ArtifactPreview artifact={oversized} now={now} />);
    await settle();
    expect(container.textContent).toContain('above the 256 KiB limit');
    expect(buttons(container).map((item) => item.textContent?.trim())).toEqual(['Copy', 'Download']);
    expect(button(container, 'Copy').disabled).toBe(true);
    expect(button(container, 'Download').disabled).toBe(false);

    const small = await render(<ArtifactPreview artifact={fixtures.diffstat} now={now} previewMaxBytes={8} />);
    await settle();
    expect(small.textContent).toContain('above the 8 B limit');
  });

  it('never fetches remote artifact URLs and says so', async () => {
    const fetch = vi.fn();
    vi.stubGlobal('fetch', fetch);
    const remote: Artifact = { id: 'remote', run_id: 'run-1', name: 'bundle.zip', kind: 'artifact', uri: 'https://example.test/bundle.zip', created_at: past };
    const container = await render(<ArtifactPreview artifact={remote} now={now} />);
    await settle();
    expect(fetch).not.toHaveBeenCalled();
    expect(container.textContent).toContain('not fetched by the cockpit');
    expect(container.textContent).toContain('https://example.test/bundle.zip');
    expect(container.querySelector('a')).toBeNull();
    expect(button(container, 'Copy').disabled).toBe(true);
    expect(button(container, 'Download').disabled).toBe(true);
  });

  it('hides expired content until explicitly requested', async () => {
    const expired = await inlineArtifact('devlog.md', 'markdown', 'text/markdown;charset=utf-8', devlog, { retention: past });
    const container = await render(<ArtifactPreview artifact={expired} now={now} />);
    await settle();
    expect(container.textContent).toContain('Declared retention expired');
    expect(container.textContent).toContain('expired');
    expect(container.querySelector('section')).toBeNull();
    const toggle = button(container, 'Show expired content');
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
    await click(toggle);
    expect(container.querySelector('section')).not.toBeNull();
    expect(button(container, 'Hide preview').getAttribute('aria-expanded')).toBe('true');
  });

  it('reports digest mismatches, missing digests and size mismatches', async () => {
    const mismatch = await render(<ArtifactPreview artifact={{ ...fixtures.issue, sha256: 'a'.repeat(64) }} now={now} />);
    await settle();
    expect(mismatch.querySelector('[data-integrity]')?.getAttribute('data-integrity')).toBe('mismatch');
    expect(mismatch.textContent).toContain('Treat this artifact as altered');

    const legacy = await render(<ArtifactPreview artifact={{ id: 'legacy', run_id: 'run-1', name: 'old.md', kind: 'markdown', uri: 'data:text/markdown,hello', storage_class: 'legacy-inline', created_at: past }} now={now} />);
    await settle();
    expect(legacy.querySelector('[data-integrity]')?.getAttribute('data-integrity')).toBe('undeclared');
    expect(legacy.textContent).toContain('legacy-inline');
    expect(legacy.textContent).toContain('not declared');

    const sized = await render(<ArtifactPreview artifact={{ ...fixtures.diffstat, byte_size: 3 }} now={now} />);
    await settle();
    expect(sized.textContent).toContain('Declared byte size does not match');
    expect(sized.textContent).toContain('3 B declared');
  });

  it('copies decoded text and explains clipboard failures', async () => {
    const writeText = vi.fn(async () => undefined);
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } });
    const container = await render(<ArtifactPreview artifact={fixtures.review} now={now} />);
    await click(button(container, 'Copy'));
    expect(writeText).toHaveBeenCalledWith(aiReview);
    expect(container.querySelector('[role="status"]')?.textContent).toContain('Copied open-pilot-ai-review.md');

    vi.stubGlobal('navigator', { ...navigator, clipboard: undefined });
    const unavailable = await render(<ArtifactPreview artifact={fixtures.review} now={now} />);
    await click(button(unavailable, 'Copy'));
    expect(unavailable.querySelector('[role="status"]')?.textContent).toContain('clipboard access is unavailable');
  });

  it('downloads through a sanitized blob link instead of the raw URI', async () => {
    const createObjectURL = vi.fn<(blob: Blob) => string>().mockReturnValue('blob:switchyard/artifact');
    const revokeObjectURL = vi.fn<(url: string) => void>();
    vi.stubGlobal('URL', Object.assign(URL, { createObjectURL, revokeObjectURL }));
    const anchorClick = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined);
    const traversal = { ...fixtures.issue, name: '../open-pilot:issue.json' };
    const container = await render(<ArtifactPreview artifact={traversal} now={now} />);
    await click(button(container, 'Download'));
    expect(createObjectURL).toHaveBeenCalledTimes(1);
    const blob = createObjectURL.mock.calls[0][0];
    expect(blob.type).toBe('application/json;charset=utf-8');
    expect(await blob.text()).toBe(issueJSON);
    expect(anchorClick).toHaveBeenCalledTimes(1);
    const anchor = anchorClick.mock.instances[0] as HTMLAnchorElement;
    expect(anchor.download).toBe('.._open-pilot_issue.json'.replace(/^\.+/, ''));
    expect(anchor.getAttribute('href')).toBe('blob:switchyard/artifact');
    expect(container.querySelector('[role="status"]')?.textContent).toContain('Downloaded');
    await settle();
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:switchyard/artifact');

    const oversized = await render(<ArtifactPreview artifact={{ ...fixtures.diffstat, byte_size: ARTIFACT_PREVIEW_MAX_BYTES + 1 }} now={now} />);
    await click(button(oversized, 'Download'));
    expect(createObjectURL).toHaveBeenCalledTimes(2);
  });

  it('exposes every control as a focusable native button with expansion state', async () => {
    const container = await render(<ArtifactPreview artifact={fixtures.devlog} now={now} />);
    await settle();
    const controls = buttons(container);
    expect(controls.map((item) => item.textContent?.trim())).toEqual(['Show preview', 'Copy', 'Download']);
    for (const control of controls) {
      expect(control.type).toBe('button');
      expect(control.tabIndex).toBeGreaterThanOrEqual(0);
      control.focus();
      expect(document.activeElement).toBe(control);
    }
    expect(container.querySelectorAll('[onclick], div[tabindex], span[tabindex]').length).toBe(0);
    const toggle = controls[0];
    toggle.focus();
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
    expect(toggle.getAttribute('aria-controls')).toBeTruthy();
    await click(document.activeElement as HTMLElement);
    const region = container.querySelector(`[id="${toggle.getAttribute('aria-controls')}"]`);
    expect(region?.getAttribute('aria-label')).toBe('Preview of devlog.md');
    expect(button(container, 'Hide preview').getAttribute('aria-expanded')).toBe('true');
    await click(document.activeElement as HTMLElement);
    expect(container.querySelector('section')).toBeNull();
    expect(container.querySelector('[aria-live="polite"]')).not.toBeNull();
  });

  it('is wired into the run cockpit evidence panel', async () => {
    const detail: RunDetail = {
      run: { id: 'run-1', workflow_id: 'devlog', status: 'succeeded', created_at: past, summary: 'Devlog run' },
      steps: [],
      events: [],
      artifacts: [fixtures.devlog, fixtures.issue],
    };
    const container = await render(
      <RunCockpitPage
        selectedRunID="run-1"
        selectedRunDetail={detail}
        openPilotState={null}
        runActionID=""
        onRetryRun={() => undefined}
        onCancelRun={() => undefined}
        onResumeOpenPilotState={() => undefined}
        onStepAction={() => undefined}
        onBackToOverview={() => undefined}
        onRefresh={() => undefined}
      />,
    );
    await settle();
    expect(container.querySelectorAll('[data-artifact-preview]').length).toBe(2);
    expect(container.textContent).not.toContain(fixtures.devlog.uri);
    expect(container.textContent).toContain('sha256 verified');
    await click(buttons(container).filter((item) => item.textContent?.trim() === 'Show preview')[0]);
    expect(container.querySelector('section[aria-label="Preview of devlog.md"]')).not.toBeNull();
  });
});
