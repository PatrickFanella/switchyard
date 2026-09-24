import { useEffect, useId, useMemo, useState, type ReactNode } from 'react';
import type { Artifact } from '../types';
import { formatTime } from './hud';
import { parseMarkdown, type MarkdownBlock, type MarkdownInline } from './artifactMarkdown';
import {
  ARTIFACT_DOWNLOAD_MAX_BYTES,
  ARTIFACT_PREVIEW_MAX_BYTES,
  analyzeArtifact,
  downloadMediaType,
  formatByteSize,
  formatJSONPreview,
  parseInlineArtifactURI,
  safeArtifactFileName,
  verifyArtifactDigest,
  type ArtifactAnalysis,
  type ArtifactIntegrity,
} from './artifactPreview';

export type ArtifactPreviewProps = {
  artifact: Artifact;
  now?: () => Date;
  previewMaxBytes?: number;
  // Start with the preview open, for review surfaces where reading is the task.
  initiallyExpanded?: boolean;
};

const actionButtonClass =
  'hud-chip border-[hsl(var(--border))] bg-[hsl(var(--panel-soft))] px-3 py-2 text-[0.62rem] text-[hsl(var(--muted))] transition hover:border-[hsl(var(--signal))] hover:text-[hsl(var(--signal))] disabled:cursor-not-allowed disabled:opacity-50';
const preClass = 'max-h-96 overflow-auto whitespace-pre-wrap break-words border border-[hsl(var(--border-faint))] bg-[hsl(var(--void))] p-3 font-mono text-xs leading-5 text-[hsl(var(--ink))]';

const integrityLabels: Record<ArtifactIntegrity, string> = {
  pending: 'checking',
  verified: 'sha256 verified',
  mismatch: 'sha256 mismatch',
  undeclared: 'no digest recorded',
  unavailable: 'digest check unavailable',
  unchecked: 'not checked',
};

function integrityClass(integrity: ArtifactIntegrity) {
  switch (integrity) {
    case 'verified':
      return 'border-[hsl(var(--confirm))] bg-[hsl(var(--confirm-soft))] text-[hsl(var(--confirm))]';
    case 'mismatch':
      return 'border-[hsl(var(--danger))] bg-[hsl(var(--danger-soft))] text-[hsl(var(--danger))]';
    case 'undeclared':
    case 'unavailable':
      return 'border-[hsl(var(--warn))] bg-[hsl(var(--warn-soft))] text-[hsl(var(--warn))]';
    default:
      return 'border-[hsl(var(--border))] bg-[hsl(var(--panel-soft))] text-[hsl(var(--muted))]';
  }
}

function Meta({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="hud-label text-[hsl(var(--faint))]">{label}</dt>
      <dd className="mt-1 break-words text-xs text-[hsl(var(--ink))]">{value}</dd>
    </div>
  );
}

function Notice({ tone, children }: { tone: 'warn' | 'danger' | 'muted'; children: ReactNode }) {
  const toneClass =
    tone === 'danger'
      ? 'border-[hsl(var(--danger))] text-[hsl(var(--danger))]'
      : tone === 'warn'
        ? 'border-[hsl(var(--warn))] text-[hsl(var(--warn))]'
        : 'border-[hsl(var(--border-faint))] text-[hsl(var(--muted))]';
  return <p className={`mt-3 border-l-2 pl-3 text-xs leading-5 ${toneClass}`}>{children}</p>;
}

function InlineNodes({ nodes }: { nodes: MarkdownInline[] }) {
  return (
    <>
      {nodes.map((node, index) => {
        switch (node.type) {
          case 'text':
            return <span key={index}>{node.value}</span>;
          case 'code':
            return (
              <code key={index} className="bg-[hsl(var(--void))] px-1 font-mono text-[0.7rem]">
                {node.value}
              </code>
            );
          case 'strong':
            return (
              <strong key={index}>
                <InlineNodes nodes={node.children} />
              </strong>
            );
          case 'em':
            return (
              <em key={index}>
                <InlineNodes nodes={node.children} />
              </em>
            );
          case 'link':
            return node.href ? (
              <a key={index} className="text-[hsl(var(--signal))] underline" href={node.href} rel="noopener noreferrer" target="_blank">
                <InlineNodes nodes={node.children} />
              </a>
            ) : (
              <span key={index}>
                <InlineNodes nodes={node.children} />
                <span className="text-[hsl(var(--faint))]"> (unsafe link removed)</span>
              </span>
            );
        }
      })}
    </>
  );
}

export function MarkdownView({ blocks }: { blocks: MarkdownBlock[] }) {
  return (
    <div className="max-h-96 space-y-3 overflow-auto border border-[hsl(var(--border-faint))] bg-[hsl(var(--void))] p-3 text-sm leading-6 text-[hsl(var(--ink))]">
      {blocks.map((block, index) => {
        switch (block.type) {
          case 'heading': {
            const level = Math.min(6, Math.max(3, block.level + 2));
            const Tag = `h${level}` as 'h3' | 'h4' | 'h5' | 'h6';
            return (
              <Tag key={index} className="font-semibold text-[hsl(var(--ink))]">
                <InlineNodes nodes={block.children} />
              </Tag>
            );
          }
          case 'paragraph':
            return (
              <p key={index} className="text-[hsl(var(--muted))]">
                <InlineNodes nodes={block.children} />
              </p>
            );
          case 'code':
            return (
              <pre key={index} className="overflow-auto whitespace-pre-wrap break-words border border-[hsl(var(--border-faint))] p-2 font-mono text-xs" data-language={block.language || undefined}>
                {block.value}
              </pre>
            );
          case 'list': {
            const Tag = block.ordered ? 'ol' : 'ul';
            return (
              <Tag key={index} className={`${block.ordered ? 'list-decimal' : 'list-disc'} pl-5 text-[hsl(var(--muted))]`}>
                {block.items.map((item, itemIndex) => (
                  <li key={itemIndex}>
                    <InlineNodes nodes={item} />
                  </li>
                ))}
              </Tag>
            );
          }
          case 'quote':
            return (
              <blockquote key={index} className="border-l-2 border-[hsl(var(--border))] pl-3 text-[hsl(var(--muted))]">
                <InlineNodes nodes={block.children} />
              </blockquote>
            );
          case 'rule':
            return <hr key={index} className="border-[hsl(var(--border-faint))]" />;
        }
      })}
    </div>
  );
}

function PreviewBody({ analysis }: { analysis: ArtifactAnalysis }) {
  const text = analysis.text ?? '';
  if (analysis.previewKind === 'json') {
    const formatted = formatJSONPreview(text);
    return formatted.ok ? (
      <pre className={preClass}>{formatted.text}</pre>
    ) : (
      <>
        <Notice tone="danger">Malformed JSON ({formatted.reason}). Showing the raw text instead.</Notice>
        <pre className={`${preClass} mt-3`}>{text}</pre>
      </>
    );
  }
  if (analysis.previewKind === 'markdown') {
    return <MarkdownView blocks={parseMarkdown(text)} />;
  }
  return <pre className={preClass}>{text}</pre>;
}

function fallbackReason(analysis: ArtifactAnalysis) {
  if (!analysis.inline) {
    return { tone: 'muted' as const, text: 'Preview unavailable: this artifact points outside inline storage and is not fetched by the cockpit.' };
  }
  switch (analysis.decode.status) {
    case 'oversized':
      return { tone: 'warn' as const, text: `Preview unavailable: ${analysis.decode.reason} Download is still available.` };
    case 'malformed':
      return { tone: 'danger' as const, text: `Preview unavailable: ${analysis.decode.reason}` };
    case 'unsupported':
      return { tone: 'muted' as const, text: `Preview unavailable: ${analysis.decode.reason}` };
    default:
      break;
  }
  if (analysis.text === null) {
    return { tone: 'muted' as const, text: `Preview unavailable: ${analysis.decode.content.mediaType} (${analysis.decode.content.charset}) is not renderable as UTF-8 text. Download is still available.` };
  }
  if (!analysis.previewKind) {
    return { tone: 'muted' as const, text: `Preview unavailable: ${analysis.decode.content.mediaType} is not a supported text, JSON or Markdown artifact. Copy and download remain available.` };
  }
  return null;
}

// Stable default so the analysis memo (and the digest effect keyed on it) does
// not re-run on every render when callers omit `now`.
const defaultNow = () => new Date();

export function ArtifactPreview({ artifact, now = defaultNow, previewMaxBytes = ARTIFACT_PREVIEW_MAX_BYTES, initiallyExpanded = false }: ArtifactPreviewProps) {
  const regionID = useId();
  const [expanded, setExpanded] = useState(initiallyExpanded);
  const [showExpired, setShowExpired] = useState(false);
  const [notice, setNotice] = useState('');
  const [integrity, setIntegrity] = useState<ArtifactIntegrity>('pending');
  const analysis = useMemo(() => analyzeArtifact(artifact, now(), previewMaxBytes), [artifact, now, previewMaxBytes]);

  useEffect(() => {
    let active = true;
    if (analysis.decode.status !== 'ok') {
      setIntegrity('unchecked');
      return undefined;
    }
    setIntegrity('pending');
    void verifyArtifactDigest(analysis.decode.content.bytes, artifact.sha256).then((result) => {
      if (active) setIntegrity(result);
    });
    return () => {
      active = false;
    };
  }, [analysis, artifact.sha256]);

  const fallback = fallbackReason(analysis);
  const previewable = fallback === null;
  const expired = analysis.retention.expired;
  const contentVisible = previewable && expanded && (!expired || showExpired);
  const canCopy = analysis.text !== null;
  const canDownload = analysis.inline && analysis.decode.status !== 'malformed';
  const declaredType = artifact.content_type || (analysis.decode.status === 'ok' ? analysis.decode.content.mediaType : 'unknown');
  const sizeText =
    analysis.declaredBytes !== null
      ? analysis.sizeMismatch
        ? `${formatByteSize(analysis.declaredBytes)} declared, ${formatByteSize(analysis.decodedBytes)} decoded`
        : formatByteSize(analysis.declaredBytes)
      : analysis.decodedBytes !== null
        ? `${formatByteSize(analysis.decodedBytes)} decoded`
        : 'unknown size';

  const copy = async () => {
    if (analysis.text === null) return;
    const clipboard = typeof navigator !== 'undefined' ? navigator.clipboard : undefined;
    if (!clipboard || typeof clipboard.writeText !== 'function') {
      setNotice('Copy failed: clipboard access is unavailable in this browser. Use Download instead.');
      return;
    }
    try {
      await clipboard.writeText(analysis.text);
      setNotice(`Copied ${artifact.name} (${formatByteSize(analysis.decodedBytes)}) to the clipboard.`);
    } catch {
      setNotice('Copy failed: the browser denied clipboard access. Use Download instead.');
    }
  };

  const download = () => {
    const result = parseInlineArtifactURI(artifact.uri, ARTIFACT_DOWNLOAD_MAX_BYTES);
    if (result.status !== 'ok') {
      setNotice(`Download unavailable: ${result.reason}`);
      return;
    }
    if (typeof URL.createObjectURL !== 'function' || typeof document === 'undefined') {
      setNotice('Download unavailable: this browser cannot create local file links.');
      return;
    }
    const fileName = safeArtifactFileName(artifact.name);
    const blob = new Blob([result.content.bytes], { type: downloadMediaType(analysis.previewKind) });
    const objectURL = URL.createObjectURL(blob);
    const anchor = document.createElement('a');
    anchor.href = objectURL;
    anchor.download = fileName;
    anchor.rel = 'noopener';
    anchor.style.display = 'none';
    document.body.appendChild(anchor);
    anchor.click();
    anchor.remove();
    setTimeout(() => URL.revokeObjectURL(objectURL), 0);
    setNotice(`Downloaded ${fileName} (${formatByteSize(result.content.bytes.length)}).`);
  };

  return (
    <div className="mt-3" data-artifact-preview={artifact.id}>
      <dl className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
        <Meta label="Type" value={declaredType} />
        <Meta label="Size" value={sizeText} />
        <Meta label="Storage" value={analysis.inline ? artifact.storage_class || 'inline-data-uri' : artifact.storage_class || 'external reference'} />
        <Meta
          label="Integrity"
          value={
            <span className={`hud-chip px-2 py-1 text-[0.58rem] ${integrityClass(integrity)}`} data-integrity={integrity}>
              {integrityLabels[integrity]}
            </span>
          }
        />
        <Meta
          label="Retention"
          value={
            analysis.retention.invalid
              ? 'invalid retention timestamp'
              : analysis.retention.until
                ? `${expired ? 'expired' : 'until'} ${formatTime(analysis.retention.until.toISOString())}`
                : 'not declared'
          }
        />
        <Meta label="Digest" value={artifact.sha256 ? <span className="font-mono">{artifact.sha256.slice(0, 16)}…</span> : '—'} />
      </dl>

      {!analysis.inline ? <p className="mt-3 break-all text-xs text-[hsl(var(--muted))]">{artifact.uri}</p> : null}
      {analysis.sizeMismatch ? <Notice tone="danger">Declared byte size does not match the decoded payload.</Notice> : null}
      {integrity === 'mismatch' ? <Notice tone="danger">Decoded content does not match the recorded SHA-256 digest. Treat this artifact as altered.</Notice> : null}
      {fallback ? <Notice tone={fallback.tone}>{fallback.text}</Notice> : null}
      {previewable && expired ? (
        <Notice tone="warn">Declared retention expired {formatTime(analysis.retention.until?.toISOString())}. The content may already be pruned; it is only shown on request.</Notice>
      ) : null}

      <div className="mt-3 flex flex-wrap gap-2">
        {previewable ? (
          <button
            aria-controls={regionID}
            aria-expanded={contentVisible}
            className={actionButtonClass}
            onClick={() => {
              if (expired && !showExpired) {
                setShowExpired(true);
                setExpanded(true);
                return;
              }
              setExpanded((value) => !value);
            }}
            type="button"
          >
            {contentVisible ? 'Hide preview' : expired && !showExpired ? 'Show expired content' : 'Show preview'}
          </button>
        ) : null}
        <button className={actionButtonClass} disabled={!canCopy} onClick={() => void copy()} type="button">
          Copy
        </button>
        <button className={actionButtonClass} disabled={!canDownload} onClick={download} type="button">
          Download
        </button>
      </div>

      <p aria-live="polite" className="mt-2 min-h-4 text-xs text-[hsl(var(--muted))]" role="status">
        {notice}
      </p>

      {contentVisible ? (
        <section aria-label={`Preview of ${artifact.name}`} className="mt-3" id={regionID}>
          <PreviewBody analysis={analysis} />
        </section>
      ) : null}
    </div>
  );
}
