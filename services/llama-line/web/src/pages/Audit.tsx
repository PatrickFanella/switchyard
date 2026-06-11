import { useEffect, useRef, useState } from 'react'
import { Card, CardHeader, CardTitle, CardContent } from '@/components/ui/Card'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Input'
import { Badge } from '@/components/ui/Badge'
import { BodyView } from '@/components/BodyView'
import { subscribeAudit } from '@/lib/api/streams'
import { getRecentAudit } from '@/lib/api/endpoints'
import type { AuditEntry } from '@/lib/api/types'
import { cn, formatDuration } from '@/lib/utils'

export function AuditPage() {
  const [entries, setEntries] = useState<AuditEntry[]>([])
  const [filter, setFilter] = useState('')
  const [paused, setPaused] = useState(false)
  const [showBodies, setShowBodies] = useState(false)
  const [errorsOnly, setErrorsOnly] = useState(false)
  const [autoscroll, setAutoscroll] = useState(true)
  const [expanded, setExpanded] = useState<Set<number>>(new Set())
  const pausedRef = useRef(paused); pausedRef.current = paused
  const scrollRef = useRef<HTMLDivElement | null>(null)

  useEffect(() => {
    let cancelled = false
    getRecentAudit(200)
      .then(res => { if (!cancelled) setEntries(res.entries) })
      .catch(() => {})
    return () => { cancelled = true }
  }, [])

  useEffect(() => {
    const s = subscribeAudit({
      onMessage: (e) => {
        if (pausedRef.current) return
        setEntries(prev => {
          const next = prev.concat(e)
          if (next.length > 5000) next.splice(0, next.length - 5000)
          return next
        })
      },
    })
    return () => s.close()
  }, [])

  useEffect(() => {
    if (!autoscroll) return
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [entries, autoscroll])

  const f = filter.toLowerCase()
  const visible = entries.filter(e => {
    if (errorsOnly && !(e.request_tool_error_count && e.request_tool_error_count > 0) && e.response_json_valid !== false && e.status !== 'error') return false
    if (!f) return true
    const hay = [e.client, e.model, e.path, e.id, e.status, JSON.stringify(e.request_tool_errors || []), JSON.stringify(e.request_tool_names || [])].join(' ').toLowerCase()
    return hay.includes(f)
  })

  const toggle = (i: number) => {
    setExpanded(prev => {
      const next = new Set(prev)
      if (next.has(i)) next.delete(i); else next.add(i)
      return next
    })
  }

  return (
    <Card className="flex flex-col h-[calc(100vh-180px)]">
      <CardHeader>
        <CardTitle>Audit (live)</CardTitle>
        <div className="flex items-center gap-2 flex-wrap">
          <Input placeholder="filter…" value={filter} onChange={e => setFilter(e.target.value)} className="w-64" />
          <label className="text-xs flex items-center gap-1 text-[var(--color-muted)]">
            <input type="checkbox" checked={errorsOnly} onChange={e => setErrorsOnly(e.target.checked)} />
            errors only
          </label>
          <label className="text-xs flex items-center gap-1 text-[var(--color-muted)]">
            <input type="checkbox" checked={showBodies} onChange={e => setShowBodies(e.target.checked)} />
            show bodies
          </label>
          <Button size="sm" variant={paused ? 'default' : 'secondary'} onClick={() => setPaused(p => !p)}>
            {paused ? 'Resume' : 'Pause'}
          </Button>
          <label className="text-xs flex items-center gap-1 text-[var(--color-muted)]">
            <input type="checkbox" checked={autoscroll} onChange={e => setAutoscroll(e.target.checked)} />
            autoscroll
          </label>
          <Button size="sm" variant="ghost" onClick={() => setEntries([])}>Clear</Button>
        </div>
      </CardHeader>
      <CardContent className="flex-1 p-3">
        <div ref={scrollRef} className="h-full overflow-y-auto rounded-[var(--radius-sm)] border border-[var(--color-border)] bg-[var(--color-bg)] p-2 font-mono text-xs">
          {visible.length === 0 ? (
            <div className="text-[var(--color-muted)] p-2">no audit entries</div>
          ) : visible.map((e, i) => {
            const hasErr = (e.request_tool_error_count ?? 0) > 0 || e.response_json_valid === false || e.status === 'error'
            const open = expanded.has(i)
            return (
              <div key={i} className={cn('py-1.5 border-b border-[var(--color-border)]/40 last:border-0', hasErr && 'bg-[var(--color-error)]/5')}>
                <button onClick={() => toggle(i)} className="w-full text-left flex items-baseline gap-2 flex-wrap">
                  <span className="text-[var(--color-muted)] tabular-nums">{e.time ? new Date(e.time).toLocaleTimeString(undefined, { hour12: false }) : ''}</span>
                  <Badge variant={e.status === 'success' ? 'success' : hasErr ? 'error' : 'neutral'}>{e.status ?? 'n/a'}</Badge>
                  {e.client && <Badge variant="info">{e.client}</Badge>}
                  {e.path && <span className="text-[var(--color-text)]">{e.path}</span>}
                  {e.model && <span className="text-[var(--color-muted)]">model=<span className="text-[var(--color-text)]">{e.model}</span></span>}
                  {e.duration_ms !== undefined && (
                    <span
                      className="text-[var(--color-muted)]"
                      title={`queue ${formatDuration(e.queue_ms ?? 0)} · upstream ${formatDuration(e.upstream_ms ?? 0)}`}
                    >
                      total={formatDuration(e.duration_ms)}
                      {e.upstream_ms != null && <span> · llm={formatDuration(e.upstream_ms)}</span>}
                      {e.queue_ms != null && e.queue_ms > 0 && <span> · q={formatDuration(e.queue_ms)}</span>}
                    </span>
                  )}
                  {(e.request_body_bytes !== undefined || e.response_body_bytes !== undefined) && (
                    <span className="text-[var(--color-muted)]">
                      bytes={(e.request_body_bytes ?? 0)}/{(e.response_body_bytes ?? 0)}
                    </span>
                  )}
                  {e.response_json_valid === false && <Badge variant="error">malformed json</Badge>}
                  {(e.request_tool_error_count ?? 0) > 0 && <Badge variant="error">tool errors: {e.request_tool_error_count}</Badge>}
                  {e.id && <span className="text-[var(--color-muted)] ml-auto">{e.id}</span>}
                </button>
                {open && (
                  <div className="mt-2 ml-4 space-y-1">
                    {e.request_tool_names && e.request_tool_names.length > 0 && (
                      <div><span className="text-[var(--color-accent)]">tools</span>: {e.request_tool_names.join(', ')}</div>
                    )}
                    {e.request_tool_errors && e.request_tool_errors.length > 0 && (
                      <div>
                        <span className="text-[var(--color-error)]">tool_errors</span>:
                        <ul className="ml-4 list-disc text-[var(--color-error)]">
                          {e.request_tool_errors.map((err, j) => <li key={j} className="break-words">{err}</li>)}
                        </ul>
                      </div>
                    )}
                    {showBodies && e.request_body && (
                      <BodyView label="request_body" body={e.request_body} preferChat />
                    )}
                    {showBodies && e.response_body && (
                      <BodyView label="response_body" body={e.response_body} preferChat />
                    )}
                    {!showBodies && (e.request_body || e.response_body) && (
                      <div className="text-[var(--color-muted)] italic">bodies hidden (enable “show bodies”; server must have audit_log_include_body=true)</div>
                    )}
                  </div>
                )}
              </div>
            )
          })}
        </div>
      </CardContent>
    </Card>
  )
}
