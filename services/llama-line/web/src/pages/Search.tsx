import { useState } from 'react'
import { Card, CardHeader, CardTitle, CardContent } from '@/components/ui/Card'
import { Button } from '@/components/ui/Button'
import { Input, Select } from '@/components/ui/Input'
import { Badge } from '@/components/ui/Badge'
import { BodyView } from '@/components/BodyView'
import { searchLogs, searchRequests, semanticSearch } from '@/lib/api/endpoints'
import type { LogSearchEntry, RequestSearchEntry, SemanticSearchEntry } from '@/lib/api/types'
import { cn, formatDuration, formatRelative } from '@/lib/utils'

type Mode = 'requests' | 'logs' | 'semantic'

export function SearchPage() {
  const [mode, setMode] = useState<Mode>('requests')
  return (
    <div className="space-y-4">
      <Card>
        <CardHeader>
          <CardTitle>Database search</CardTitle>
          <Select value={mode} onChange={e => setMode(e.target.value as Mode)} className="w-40">
            <option value="requests">requests</option>
            <option value="logs">app logs</option>
            <option value="semantic">semantic</option>
          </Select>
        </CardHeader>
        <CardContent>
          {mode === 'requests' ? <RequestSearch /> : mode === 'logs' ? <LogSearch /> : <SemanticSearch />}
        </CardContent>
      </Card>
    </div>
  )
}

function RequestSearch() {
  const [q, setQ] = useState('')
  const [client, setClient] = useState('')
  const [model, setModel] = useState('')
  const [status, setStatus] = useState('')
  const [errorsOnly, setErrorsOnly] = useState(false)
  const [showBodies, setShowBodies] = useState(false)
  const [rows, setRows] = useState<RequestSearchEntry[]>([])
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState('')

  const run = async () => {
    setLoading(true); setErr('')
    try {
      const p = new URLSearchParams({ limit: '100' })
      if (q) p.set('q', q)
      if (client) p.set('client', client)
      if (model) p.set('model', model)
      if (status) p.set('status', status)
      if (errorsOnly) p.set('errors_only', 'true')
      const res = await searchRequests(p)
      setRows(res.entries)
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    } finally { setLoading(false) }
  }

  return (
    <div className="space-y-3">
      <div className="flex items-center gap-2 flex-wrap">
        <Input placeholder="full-text query…" value={q} onChange={e => setQ(e.target.value)} className="w-64" />
        <Input placeholder="client" value={client} onChange={e => setClient(e.target.value)} className="w-32" />
        <Input placeholder="model" value={model} onChange={e => setModel(e.target.value)} className="w-48" />
        <Input placeholder="status" value={status} onChange={e => setStatus(e.target.value)} className="w-28" />
        <label className="text-xs flex items-center gap-1 text-[var(--color-muted)]">
          <input type="checkbox" checked={errorsOnly} onChange={e => setErrorsOnly(e.target.checked)} /> errors only
        </label>
        <label className="text-xs flex items-center gap-1 text-[var(--color-muted)]">
          <input type="checkbox" checked={showBodies} onChange={e => setShowBodies(e.target.checked)} /> bodies
        </label>
        <Button size="sm" onClick={run} disabled={loading}>{loading ? 'Searching…' : 'Search'}</Button>
      </div>
      {err && <div className="text-[var(--color-error)] text-sm">{err}</div>}
      <div className="rounded-[var(--radius-sm)] border border-[var(--color-border)] bg-[var(--color-bg)] p-2 font-mono text-xs max-h-[70vh] overflow-auto">
        {rows.length === 0 ? <div className="text-[var(--color-muted)] p-2">no request results</div> : rows.map(r => <RequestRow key={r.request_id} row={r} showBodies={showBodies} />)}
      </div>
    </div>
  )
}

function LogSearch() {
  const [q, setQ] = useState('')
  const [level, setLevel] = useState('')
  const [client, setClient] = useState('')
  const [rows, setRows] = useState<LogSearchEntry[]>([])
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState('')
  const run = async () => {
    setLoading(true); setErr('')
    try {
      const p = new URLSearchParams({ limit: '200' })
      if (q) p.set('q', q)
      if (level) p.set('level', level)
      if (client) p.set('client', client)
      const res = await searchLogs(p)
      setRows(res.entries)
    } catch (e) { setErr(e instanceof Error ? e.message : String(e)) }
    finally { setLoading(false) }
  }
  return (
    <div className="space-y-3">
      <div className="flex items-center gap-2 flex-wrap">
        <Input placeholder="query…" value={q} onChange={e => setQ(e.target.value)} className="w-64" />
        <Select value={level} onChange={e => setLevel(e.target.value)} className="w-28">
          <option value="">all</option><option>DEBUG</option><option>INFO</option><option>WARN</option><option>ERROR</option>
        </Select>
        <Input placeholder="client" value={client} onChange={e => setClient(e.target.value)} className="w-32" />
        <Button size="sm" onClick={run} disabled={loading}>{loading ? 'Searching…' : 'Search'}</Button>
      </div>
      {err && <div className="text-[var(--color-error)] text-sm">{err}</div>}
      <div className="rounded-[var(--radius-sm)] border border-[var(--color-border)] bg-[var(--color-bg)] p-2 font-mono text-xs max-h-[70vh] overflow-auto">
        {rows.length === 0 ? <div className="text-[var(--color-muted)] p-2">no log results</div> : rows.map(r => <LogRow key={r.id} row={r} />)}
      </div>
    </div>
  )
}

function SemanticSearch() {
  const [q, setQ] = useState('')
  const [rows, setRows] = useState<SemanticSearchEntry[]>([])
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState('')
  const run = async () => {
    if (!q.trim()) return
    setLoading(true); setErr('')
    try {
      const res = await semanticSearch({ q, limit: 20 })
      setRows(res.entries)
    } catch (e) { setErr(e instanceof Error ? e.message : String(e)) }
    finally { setLoading(false) }
  }
  return (
    <div className="space-y-3">
      <div className="flex items-center gap-2">
        <Input placeholder="semantic query…" value={q} onChange={e => setQ(e.target.value)} className="w-96" />
        <Button size="sm" onClick={run} disabled={loading || !q.trim()}>{loading ? 'Searching…' : 'Search'}</Button>
      </div>
      {err && <div className="text-[var(--color-error)] text-sm">{err}</div>}
      <div className="rounded-[var(--radius-sm)] border border-[var(--color-border)] bg-[var(--color-bg)] p-2 font-mono text-xs max-h-[70vh] overflow-auto">
        {rows.length === 0 ? <div className="text-[var(--color-muted)] p-2">no semantic results</div> : rows.map((r, i) => <SemanticRow key={`${r.request_id}-${i}`} row={r} />)}
      </div>
    </div>
  )
}

function RequestRow({ row, showBodies }: { row: RequestSearchEntry; showBodies: boolean }) {
  const hasErr = row.tool_error_count > 0 || row.response_json_valid === false || row.status === 'error'
  return (
    <div className={cn('py-2 border-b border-[var(--color-border)]/40 last:border-0', hasErr && 'bg-[var(--color-error)]/5')}>
      <div className="flex items-baseline gap-2 flex-wrap">
        <span className="text-[var(--color-muted)]">{formatRelative(row.ts)}</span>
        <Badge variant={row.status === 'success' ? 'success' : hasErr ? 'error' : 'neutral'}>{row.status}</Badge>
        <Badge variant="info">{row.client}</Badge>
        <span>{row.path}</span>
        <span className="text-[var(--color-muted)]">model=<span className="text-[var(--color-text)]">{row.model}</span></span>
        <span className="text-[var(--color-muted)]">total={formatDuration(row.duration_ms)} · llm={formatDuration(row.upstream_ms)} · q={formatDuration(row.queue_ms)}</span>
        {row.tool_error_count > 0 && <Badge variant="error">tool errors: {row.tool_error_count}</Badge>}
        {row.response_json_valid === false && <Badge variant="error">malformed json</Badge>}
        <span className="text-[var(--color-muted)] ml-auto">{row.request_id}</span>
      </div>
      {showBodies && (row.request_body_text || row.response_body_text) && (
        <div className="mt-2 ml-4 space-y-2 font-sans">
          {row.request_body_text && <BodyView label="request" body={row.request_body_text} preferChat />}
          {row.response_body_text && <BodyView label="response" body={row.response_body_text} preferChat />}
        </div>
      )}
      {row.tool_errors && row.tool_errors.length > 0 && (
        <ul className="mt-1 ml-6 list-disc text-[var(--color-error)]">
          {row.tool_errors.map((e, i) => <li key={i}>{e}</li>)}
        </ul>
      )}
    </div>
  )
}

function LogRow({ row }: { row: LogSearchEntry }) {
  return (
    <div className="py-1.5 border-b border-[var(--color-border)]/40 last:border-0">
      <div className="flex items-baseline gap-2 flex-wrap">
        <span className="text-[var(--color-muted)]">{formatRelative(row.ts)}</span>
        <Badge variant={row.level === 'ERROR' ? 'error' : row.level === 'WARN' ? 'warn' : 'info'}>{row.level}</Badge>
        <span className="font-semibold">{row.msg}</span>
        {row.client && <span className="text-[var(--color-muted)]">client=<span className="text-[var(--color-text)]">{row.client}</span></span>}
        {row.request_id && <span className="text-[var(--color-muted)]">id=<span className="text-[var(--color-text)]">{row.request_id}</span></span>}
      </div>
      {row.raw_json && <pre className="mt-1 text-[var(--color-muted)] whitespace-pre-wrap break-words">{row.raw_json}</pre>}
    </div>
  )
}

function SemanticRow({ row }: { row: SemanticSearchEntry }) {
  return (
    <div className="py-2 border-b border-[var(--color-border)]/40 last:border-0">
      <div className="flex items-baseline gap-2 flex-wrap">
        <span className="text-[var(--color-muted)]">{formatRelative(row.ts)}</span>
        <Badge variant="info">{row.score.toFixed(3)}</Badge>
        <Badge variant="neutral">{row.role}</Badge>
        <span>{row.client}</span>
        <span className="text-[var(--color-muted)]">{row.model}</span>
        <span className="text-[var(--color-muted)] ml-auto">{row.request_id}</span>
      </div>
      <div className="mt-1 whitespace-pre-wrap break-words text-[var(--color-text)]">{row.content}</div>
    </div>
  )
}
