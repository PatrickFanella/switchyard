import type { Artifact } from '../types';

// Inline artifacts above this decoded size are not rendered in the cockpit;
// download still decodes on demand up to ARTIFACT_DOWNLOAD_MAX_BYTES, which
// matches the control plane's upper bound for SWITCHYARD_MAX_ARTIFACT_BYTES.
export const ARTIFACT_PREVIEW_MAX_BYTES = 256 * 1024;
export const ARTIFACT_DOWNLOAD_MAX_BYTES = 16 * 1024 * 1024;

export type ArtifactPreviewKind = 'text' | 'json' | 'markdown';

export type ArtifactContent = {
  mediaType: string;
  charset: string;
  base64: boolean;
  bytes: Uint8Array<ArrayBuffer>;
  // UTF-8 text when the payload decodes as valid text in a supported charset; null otherwise.
  text: string | null;
};

export type ArtifactDecodeResult =
  | { status: 'ok'; content: ArtifactContent }
  | { status: 'unsupported' | 'malformed'; reason: string }
  | { status: 'oversized'; reason: string; estimatedBytes: number };

export type ArtifactIntegrity = 'pending' | 'verified' | 'mismatch' | 'undeclared' | 'unavailable' | 'unchecked';

export type ArtifactRetention = { until: Date | null; expired: boolean; invalid: boolean };

export type ArtifactAnalysis = {
  inline: boolean;
  decode: ArtifactDecodeResult;
  previewKind: ArtifactPreviewKind | null;
  text: string | null;
  declaredBytes: number | null;
  decodedBytes: number | null;
  sizeMismatch: boolean;
  retention: ArtifactRetention;
};

const textCharsets = new Set(['utf-8', 'utf8', 'us-ascii', 'ascii']);
const mediaTypePattern = /^[a-z0-9!#$&^_.+-]+\/[a-z0-9!#$&^_.+-]+$/;

export function estimateInlinePayloadBytes(payload: string, base64: boolean) {
  if (base64) {
    return Math.floor((payload.replace(/\s+/g, '').replace(/=+$/, '').length * 3) / 4);
  }
  let escapes = 0;
  for (let index = 0; index < payload.length; index += 1) {
    if (payload.charCodeAt(index) === 37) escapes += 1;
  }
  return Math.max(0, payload.length - escapes * 2);
}

function oversized(estimatedBytes: number, limit: number): ArtifactDecodeResult {
  return { status: 'oversized', estimatedBytes, reason: `Artifact is about ${formatByteSize(estimatedBytes)}, above the ${formatByteSize(limit)} limit.` };
}

// parseInlineArtifactURI decodes an RFC 2397 data URI without touching the
// network. Every other scheme is reported as unsupported and is never fetched.
export function parseInlineArtifactURI(uri: string, limit = ARTIFACT_PREVIEW_MAX_BYTES): ArtifactDecodeResult {
  if (typeof uri !== 'string' || uri.length === 0) {
    return { status: 'malformed', reason: 'Artifact URI is empty.' };
  }
  if (!/^data:/i.test(uri)) {
    return { status: 'unsupported', reason: 'Only inline data URIs are decoded; the cockpit never fetches remote artifact locations.' };
  }
  const comma = uri.indexOf(',');
  if (comma < 5) {
    return { status: 'malformed', reason: 'Data URI has no payload separator.' };
  }
  const meta = uri.slice(5, comma).split(';');
  const mediaType = (meta[0] ?? '').trim().toLowerCase() || 'text/plain';
  if (!mediaTypePattern.test(mediaType)) {
    return { status: 'malformed', reason: `Media type "${mediaType}" is not valid.` };
  }
  let base64 = false;
  let charset = 'us-ascii';
  for (const raw of meta.slice(1)) {
    const parameter = raw.trim();
    if (/^base64$/i.test(parameter) && !base64) {
      base64 = true;
      continue;
    }
    if (/^charset=/i.test(parameter) && parameter.length > 8) {
      charset = parameter.slice(8).trim().toLowerCase();
      continue;
    }
    return { status: 'malformed', reason: `Unsupported data URI parameter "${parameter}".` };
  }
  const payload = uri.slice(comma + 1);
  const estimated = estimateInlinePayloadBytes(payload, base64);
  if (estimated > limit) {
    return oversized(estimated, limit);
  }
  let bytes: Uint8Array<ArrayBuffer>;
  if (base64) {
    const compact = payload.replace(/\s+/g, '');
    if (!/^[A-Za-z0-9+/]*={0,2}$/.test(compact) || compact.length % 4 !== 0) {
      return { status: 'malformed', reason: 'Base64 payload is not valid.' };
    }
    try {
      bytes = Uint8Array.from(atob(compact), (character) => character.charCodeAt(0));
    } catch {
      return { status: 'malformed', reason: 'Base64 payload could not be decoded.' };
    }
  } else {
    try {
      bytes = new TextEncoder().encode(decodeURIComponent(payload));
    } catch {
      return { status: 'malformed', reason: 'Percent-encoded payload is not valid.' };
    }
  }
  if (bytes.length > limit) {
    return oversized(bytes.length, limit);
  }
  let text: string | null = null;
  if (textCharsets.has(charset)) {
    try {
      text = new TextDecoder('utf-8', { fatal: true }).decode(bytes);
    } catch {
      text = null;
    }
  }
  return { status: 'ok', content: { mediaType, charset, base64, bytes, text } };
}

export function artifactPreviewKind(artifact: Pick<Artifact, 'kind' | 'name' | 'content_type'>, mediaType?: string): ArtifactPreviewKind | null {
  const media = (mediaType ?? artifact.content_type ?? '').split(';')[0].trim().toLowerCase();
  const kind = (artifact.kind ?? '').trim().toLowerCase();
  const name = (artifact.name ?? '').trim().toLowerCase();
  if (media === 'application/json' || media.endsWith('+json') || kind === 'json' || name.endsWith('.json')) return 'json';
  if (media === 'text/markdown' || kind === 'markdown' || name.endsWith('.md') || name.endsWith('.markdown')) return 'markdown';
  if (media.startsWith('text/') || kind === 'text' || /\.(txt|log|diff|patch|csv)$/.test(name)) return 'text';
  return null;
}

export function artifactRetention(artifact: Pick<Artifact, 'retention_until'>, now: Date): ArtifactRetention {
  if (!artifact.retention_until) {
    return { until: null, expired: false, invalid: false };
  }
  const until = new Date(artifact.retention_until);
  if (!Number.isFinite(until.getTime())) {
    return { until: null, expired: false, invalid: true };
  }
  return { until, expired: until.getTime() <= now.getTime(), invalid: false };
}

export function analyzeArtifact(artifact: Artifact, now: Date, previewMaxBytes = ARTIFACT_PREVIEW_MAX_BYTES): ArtifactAnalysis {
  const inline = typeof artifact.uri === 'string' && /^data:/i.test(artifact.uri);
  const declaredBytes = typeof artifact.byte_size === 'number' && Number.isFinite(artifact.byte_size) && artifact.byte_size >= 0 ? artifact.byte_size : null;
  const decode: ArtifactDecodeResult =
    inline && declaredBytes !== null && declaredBytes > previewMaxBytes ? oversized(declaredBytes, previewMaxBytes) : parseInlineArtifactURI(artifact.uri, previewMaxBytes);
  const content = decode.status === 'ok' ? decode.content : null;
  const previewKind = inline ? artifactPreviewKind(artifact, content?.mediaType) : null;
  const decodedBytes = content ? content.bytes.length : null;
  return {
    inline,
    decode,
    previewKind,
    text: content?.text ?? null,
    declaredBytes,
    decodedBytes,
    sizeMismatch: declaredBytes !== null && decodedBytes !== null && declaredBytes !== decodedBytes,
    retention: artifactRetention(artifact, now),
  };
}

function hex(buffer: ArrayBuffer) {
  return Array.from(new Uint8Array(buffer), (byte) => byte.toString(16).padStart(2, '0')).join('');
}

export async function verifyArtifactDigest(bytes: Uint8Array<ArrayBuffer>, declared?: string): Promise<ArtifactIntegrity> {
  const expected = (declared ?? '').trim().toLowerCase();
  if (!/^[a-f0-9]{64}$/.test(expected)) {
    return 'undeclared';
  }
  const subtle = globalThis.crypto?.subtle;
  if (!subtle) {
    return 'unavailable';
  }
  try {
    return hex(await subtle.digest('SHA-256', bytes)) === expected ? 'verified' : 'mismatch';
  } catch {
    return 'unavailable';
  }
}

export function formatByteSize(bytes: number | null | undefined) {
  if (typeof bytes !== 'number' || !Number.isFinite(bytes) || bytes < 0) {
    return 'unknown size';
  }
  if (bytes < 1024) {
    return `${bytes} B`;
  }
  const units = ['KiB', 'MiB', 'GiB'];
  let value = bytes / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value < 10 ? value.toFixed(1) : Math.round(value)} ${units[unit]}`;
}

export function formatJSONPreview(text: string): { ok: true; text: string } | { ok: false; reason: string } {
  try {
    return { ok: true, text: JSON.stringify(JSON.parse(text), null, 2) };
  } catch (error) {
    return { ok: false, reason: error instanceof Error ? error.message : 'invalid JSON' };
  }
}

// sanitizeLinkHref allows only navigable web and mail links. Anything else
// (javascript:, data:, file:, relative paths) is rendered as text.
function hasControlCharacters(value: string) {
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code < 32 || code === 127) return true;
  }
  return false;
}

export function sanitizeLinkHref(href: string): string | null {
  const trimmed = href.trim();
  if (!trimmed || hasControlCharacters(trimmed) || /[\s<>"']/.test(trimmed)) {
    return null;
  }
  try {
    const parsed = new URL(trimmed);
    return parsed.protocol === 'http:' || parsed.protocol === 'https:' || parsed.protocol === 'mailto:' ? trimmed : null;
  } catch {
    return null;
  }
}

export function safeArtifactFileName(name: string) {
  const cleaned = Array.from(name ?? '', (character) => (character.charCodeAt(0) < 32 || character.charCodeAt(0) === 127 || /[\\/:*?"<>|]/.test(character) ? '_' : character))
    .join('')
    .replace(/^\.+/, '')
    .trim()
    .slice(0, 120);
  return cleaned || 'artifact';
}

export function downloadMediaType(kind: ArtifactPreviewKind | null) {
  switch (kind) {
    case 'json':
      return 'application/json;charset=utf-8';
    case 'markdown':
      return 'text/markdown;charset=utf-8';
    case 'text':
      return 'text/plain;charset=utf-8';
    default:
      return 'application/octet-stream';
  }
}
