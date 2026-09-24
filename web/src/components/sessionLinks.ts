// Session deep links live under the existing `#/sessions` view as a path
// segment so the shell router keeps resolving the Sessions view unchanged:
// `#/sessions/<encoded session id>`. The Sessions page reads the segment.

function safeDecode(value: string) {
  try {
    return decodeURIComponent(value);
  } catch {
    return '';
  }
}

export function formatSessionHash(sessionID = '') {
  const trimmed = sessionID.trim();
  return trimmed ? `#/sessions/${encodeURIComponent(trimmed)}` : '#/sessions';
}

// parseSessionHash returns the selected session ID, '' for the session list,
// or null when the hash is not a Sessions route.
export function parseSessionHash(hash: string | undefined): string | null {
  const normalized = hash?.trim() ?? '';
  if (normalized === '#/sessions' || normalized === '#/sessions/') return '';
  if (!normalized.startsWith('#/sessions/')) return null;
  const [segment = ''] = normalized.slice('#/sessions/'.length).split('/');
  return safeDecode(segment).trim();
}
