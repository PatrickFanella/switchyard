// SSE helpers. EventSource cannot set headers, so admin endpoints get ?key= query.
import { getAdminKey } from './client'
import type { LogRecord, AuditEntry, UIStatus } from './types'

export type Stream = { close: () => void }

type Handlers<T> = {
  onMessage: (data: T) => void
  onError?: (e: Event) => void
  onOpen?: () => void
}

function withAdminKey(path: string): string {
  const key = getAdminKey()
  if (!key) return path
  const sep = path.includes('?') ? '&' : '?'
  return `${path}${sep}key=${encodeURIComponent(key)}`
}

function rawStream(url: string, h: Handlers<string>): Stream {
  let es: EventSource | null = null
  let closed = false
  let retry = 1000

  const open = () => {
    if (closed) return
    es = new EventSource(url, { withCredentials: false })
    es.onopen = () => { retry = 1000; h.onOpen?.() }
    es.onmessage = (ev) => { h.onMessage(ev.data) }
    es.onerror = (ev) => {
      h.onError?.(ev)
      es?.close()
      if (closed) return
      setTimeout(open, retry)
      retry = Math.min(retry * 2, 15000)
    }
  }
  open()

  return { close() { closed = true; es?.close() } }
}

export function subscribeLogs(h: Handlers<LogRecord>): Stream {
  return rawStream(withAdminKey('/admin/logs/stream'), {
    onMessage: (raw) => { try { h.onMessage(JSON.parse(raw)) } catch { /* ignore */ } },
    onError: h.onError, onOpen: h.onOpen,
  })
}

export function subscribeAudit(h: Handlers<AuditEntry>): Stream {
  return rawStream(withAdminKey('/admin/audit/stream'), {
    onMessage: (raw) => { try { h.onMessage(JSON.parse(raw)) } catch { /* ignore */ } },
    onError: h.onError, onOpen: h.onOpen,
  })
}

export function subscribeStatus(h: Handlers<UIStatus>): Stream {
  return rawStream('/ui/api/stream', {
    onMessage: (raw) => { try { h.onMessage(JSON.parse(raw)) } catch { /* ignore */ } },
    onError: h.onError, onOpen: h.onOpen,
  })
}
