import { evidenceScopes, type EvidenceScope } from '../types';

export type EvidenceSearchLink = { scope: EvidenceScope; query: string; cursor: string };

export const emptyEvidenceSearchLink: EvidenceSearchLink = { scope: 'all', query: '', cursor: '' };

function safeDecode(value: string) {
  try {
    return decodeURIComponent(value);
  } catch {
    return '';
  }
}

export function isEvidenceScope(value: string): value is EvidenceScope {
  return (evidenceScopes as string[]).includes(value);
}

// Deep links live under the existing `#/logs` view as path segments so the
// shell router keeps resolving the Logs / Search view without changes:
// `#/logs/search/<scope>/<query>[/<cursor>]`. An absent or malformed link
// yields the idle state (no search issued).
export function parseEvidenceSearchHash(hash: string | undefined): EvidenceSearchLink | null {
  const normalized = hash?.trim() ?? '';
  if (!normalized.startsWith('#/logs/search/')) {
    return null;
  }
  const [scopePart, queryPart = '', cursorPart = ''] = normalized.slice('#/logs/search/'.length).split('/');
  if (!isEvidenceScope(scopePart)) {
    return null;
  }
  const cursor = safeDecode(cursorPart);
  return { scope: scopePart, query: safeDecode(queryPart), cursor: /^[A-Za-z0-9_-]*$/.test(cursor) ? cursor : '' };
}

export function formatEvidenceSearchHash({ scope, query, cursor }: EvidenceSearchLink) {
  const base = `#/logs/search/${scope}/${encodeURIComponent(query.trim())}`;
  return cursor ? `${base}/${encodeURIComponent(cursor)}` : base;
}
