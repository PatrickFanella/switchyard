import { useEffect, useRef, useState } from 'react'
import type { LogRecord, LogLevel, AuditEntry } from '@/lib/api/types'
import { subscribeLogs, subscribeAudit } from '@/lib/api/streams'
import { getRecentLogs, getRecentAudit } from '@/lib/api/endpoints'
import { ApiError } from '@/lib/api/client'
import { Button } from '@/components/ui/Button'
import { Input, Select } from '@/components/ui/Input'
import { Badge } from '@/components/ui/Badge'
import { BodyView } from '@/components/BodyView'
import { cn } from '@/lib/utils'

const LEVELS: LogLevel[] = ['DEBUG', 'INFO', 'WARN', 'ERROR']
const LEVEL_ORDER: Record<LogLevel, number> = { DEBUG: 0, INFO: 1, WARN: 2, ERROR: 3 }

const LEVEL_STYLES: Record<LogLevel, { badge: 'neutral' | 'info' | 'warn' | 'error'; text: string }> = {
  DEBUG: { badge: 'neutral', text: 'text-[var(--color-muted)]' },
  INFO:  { badge: 'info',    text: 'text-[var(--color-text)]' },
  WARN:  { badge: 'warn',    text: 'text-[var(--color-warn)]' },
  ERROR: { badge: 'error',   text: 'text-[var(--color-error)]' },
}

// Full request/response bodies live in the structured request trace feed, not
// the terse broker logs. Convert those entries into regular rows only when the
// user asks to see full messages.
function fullMessageToLog(e: AuditEntry): LogRecord {
  const level = (typeof e.level === 'string' && (e.level.toUpperCase() as LogLevel)) || 'INFO'
  const { time, level: _l, msg, ...rest } = e
  return {
    time,
    level: (LEVELS.includes(level) ? level : 'INFO') as LogLevel,
    msg: msg || 'request',
    fields: { ...rest, _fullMessage: true },
    raw: JSON.stringify(e),
  }
}

interface Props {
  /** Only show records matching this predicate (e.g. queue events). */
  filterFn?: (r: LogRecord) => boolean
  /** Default level threshold. */
  defaultLevel?: LogLevel
  /** Initial backfill count. */
  initialCount?: number
  /** Cap on retained entries. */
  maxEntries?: number
  /** Compact UI (smaller toolbar). */
  compact?: boolean
  /** Whether the "full messages" toggle is shown. Default: true. */
  showFullMessagesToggle?: boolean
  /** Initial state of the full-message toggle. Default: false. */
  defaultShowFullMessages?: boolean
  /** For filtered views, keep matching full-message audit rows even when their msg differs. */
  includeFullMessagesWhenFiltered?: boolean
  /** Visible height; default flexible. */
  className?: string
}

export function LogStream({
  filterFn,
  defaultLevel = 'INFO',
  initialCount = 200,
  maxEntries = 2000,
  compact = false,
  showFullMessagesToggle = true,
  defaultShowFullMessages = false,
  includeFullMessagesWhenFiltered = false,
  className,
}: Props) {
  const [records, setRecords] = useState<LogRecord[]>([])
  const [fullMessageRecords, setFullMessageRecords] = useState<LogRecord[]>([])
  const [level, setLevel] = useState<LogLevel>(defaultLevel)
  const [filter, setFilter] = useState('')
  const [paused, setPaused] = useState(false)
  const [autoscroll, setAutoscroll] = useState(true)
  const [showFullMessages, setShowFullMessages] = useState(defaultShowFullMessages)
  const [error, setError] = useState<string | null>(null)
  const pausedRef = useRef(paused)
  pausedRef.current = paused
  const scrollRef = useRef<HTMLDivElement | null>(null)

  // Backfill regular logs.
  useEffect(() => {
    let cancelled = false
    getRecentLogs(initialCount)
      .then(res => { if (!cancelled) { setRecords(res.entries); setError(null) } })
      .catch(err => { if (!cancelled) setError(logErrorMessage(err, 'logs')) })
    return () => { cancelled = true }
  }, [initialCount])

  // Subscribe to regular log stream.
  useEffect(() => {
    const s = subscribeLogs({
      onMessage: (rec) => {
        setError(null)
        if (pausedRef.current) return
        setRecords(prev => {
          const next = prev.concat(rec)
          if (next.length > maxEntries) next.splice(0, next.length - maxEntries)
          return next
        })
      },
      onError: () => setError('Log stream disconnected. Check admin key, then refresh.'),
    })
    return () => s.close()
  }, [maxEntries])

  // Backfill + subscribe to full request/response messages only when visible.
  useEffect(() => {
    if (!showFullMessages) {
      setFullMessageRecords([])
      return
    }
    let cancelled = false
    getRecentAudit(initialCount)
      .then(res => {
        if (cancelled) return
        setFullMessageRecords(res.entries.map(fullMessageToLog))
        setError(null)
      })
      .catch(err => { if (!cancelled) setError(logErrorMessage(err, 'audit log')) })
    const s = subscribeAudit({
      onMessage: (e) => {
        setError(null)
        if (pausedRef.current) return
        setFullMessageRecords(prev => {
          const next = prev.concat(fullMessageToLog(e))
          if (next.length > maxEntries) next.splice(0, next.length - maxEntries)
          return next
        })
      },
      onError: () => setError('Audit stream disconnected. Check admin key, then refresh.'),
    })
    return () => { cancelled = true; s.close() }
  }, [showFullMessages, initialCount, maxEntries])

  // Merge + sort by time for display.
  const merged = showFullMessages
    ? [...records, ...fullMessageRecords].sort((a, b) => {
        const ta = a.time ? Date.parse(a.time) : 0
        const tb = b.time ? Date.parse(b.time) : 0
        return ta - tb
      })
    : records

  // Autoscroll.
  useEffect(() => {
    if (!autoscroll) return
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [merged.length, autoscroll])

  const minLevel = LEVEL_ORDER[level]
  const filterLower = filter.toLowerCase()
  const visible = merged.filter(r => {
    if (LEVEL_ORDER[r.level] < minLevel) return false
    const isFullMessage = r.fields?._fullMessage === true
    if (filterFn && !filterFn(r) && !(showFullMessages && includeFullMessagesWhenFiltered && isFullMessage)) return false
    if (!filterLower) return true
    if (r.msg.toLowerCase().includes(filterLower)) return true
    if (r.raw.toLowerCase().includes(filterLower)) return true
    return false
  })

  return (
    <div className={cn('flex flex-col gap-2 min-h-0', className)}>
      <div className={cn('flex items-center gap-2 flex-wrap', compact && 'text-xs')}>
        <Input
          placeholder="filter…"
          value={filter}
          onChange={e => setFilter(e.target.value)}
          className={cn(compact ? 'h-7 w-40' : 'w-64')}
        />
        <Select
          value={level}
          onChange={e => setLevel(e.target.value as LogLevel)}
          className={cn(compact && 'h-7 text-xs px-1')}
          title="minimum level"
        >
          {LEVELS.map(l => <option key={l} value={l}>{l}+</option>)}
        </Select>
        <Button size="sm" variant={paused ? 'default' : 'secondary'} onClick={() => setPaused(p => !p)}>
          {paused ? 'Resume' : 'Pause'}
        </Button>
        {showFullMessagesToggle && (
          <label
            className="text-xs flex items-center gap-1 text-[var(--color-muted)]"
            title="show full request and response messages when available"
          >
            <input type="checkbox" checked={showFullMessages} onChange={e => setShowFullMessages(e.target.checked)} />
            full messages
          </label>
        )}
        <label className="text-xs flex items-center gap-1 text-[var(--color-muted)]">
          <input type="checkbox" checked={autoscroll} onChange={e => setAutoscroll(e.target.checked)} />
          autoscroll
        </label>
        <Button size="sm" variant="ghost" onClick={() => { setRecords([]); setFullMessageRecords([]) }}>Clear</Button>
        <span className="text-xs text-[var(--color-muted)] ml-auto">
          {visible.length} / {merged.length}
          {showFullMessages && <span className="ml-1">({fullMessageRecords.length} full)</span>}
        </span>
      </div>
      {error && (
        <div className="rounded-[var(--radius-sm)] border border-[var(--color-error)]/50 bg-[var(--color-error)]/10 p-2 text-xs text-[var(--color-error)]">
          {error}
        </div>
      )}
      <div
        ref={scrollRef}
        className="flex-1 min-h-[200px] overflow-y-auto font-mono text-xs rounded-[var(--radius-sm)] border border-[var(--color-border)] bg-[var(--color-bg)] p-2"
      >
        {visible.length === 0 ? (
          <div className="text-[var(--color-muted)] p-2">no log entries</div>
        ) : visible.map((r, i) => (
          <LogRow key={i} rec={r} showFullMessages={showFullMessages} />
        ))}
      </div>
    </div>
  )
}

function logErrorMessage(err: unknown, label: string): string {
  if (err instanceof ApiError && err.isAuth) {
    return `Cannot load ${label}: admin key missing or invalid. Enter admin key in the header and save.`
  }
  if (err instanceof ApiError) return `Cannot load ${label}: HTTP ${err.status}.`
  return `Cannot load ${label}.`
}

function LogRow({ rec, showFullMessages }: { rec: LogRecord; showFullMessages: boolean }) {
  const lvl = LEVEL_STYLES[rec.level] ?? LEVEL_STYLES.INFO
  const time = rec.time ? new Date(rec.time).toLocaleTimeString(undefined, { hour12: false }) : ''
  const isFullMessage = rec.fields?._fullMessage === true
  const requestBody = typeof rec.fields?.request_body === 'string' ? rec.fields.request_body : ''
  const responseBody = typeof rec.fields?.response_body === 'string' ? rec.fields.response_body : ''
  const fields = rec.fields
    ? Object.entries(rec.fields).filter(([k]) => k !== '_fullMessage' && k !== 'request_body' && k !== 'response_body')
    : []
  return (
    <div className={cn(
      'py-1.5 border-b border-[var(--color-border)]/40 last:border-0',
      isFullMessage && 'bg-[var(--color-accent)]/5',
    )}>
      <div className="flex items-baseline gap-2 flex-wrap">
        <span className="text-[var(--color-muted)] tabular-nums">{time}</span>
        <Badge variant={lvl.badge}>{rec.level}</Badge>
        {isFullMessage && (
          <span
            className="text-[10px] uppercase tracking-wide text-[var(--color-accent)] border border-[var(--color-accent)]/40 rounded px-1"
            title="contains full request/response message payloads"
          >
            full
          </span>
        )}
        <span className={cn('font-medium', lvl.text)}>{rec.msg}</span>
        {fields.map(([k, v]) => (
          <span key={k} className="text-[var(--color-muted)]">
            <span className="text-[var(--color-accent)]">{k}</span>=<span className="text-[var(--color-text)]">{fmtVal(v)}</span>
          </span>
        ))}
      </div>
      {showFullMessages && (requestBody || responseBody) && (
        <div className="mt-2 ml-4 space-y-2 font-sans text-xs">
          {requestBody && <BodyView label="request" body={requestBody} preferChat />}
          {responseBody && <BodyView label="response" body={responseBody} preferChat />}
        </div>
      )}
    </div>
  )
}

function fmtVal(v: unknown): string {
  if (v === null || v === undefined) return ''
  if (typeof v === 'string') return v
  if (typeof v === 'number' || typeof v === 'boolean') return String(v)
  try { return JSON.stringify(v) } catch { return String(v) }
}
