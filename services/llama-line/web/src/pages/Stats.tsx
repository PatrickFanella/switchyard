import { useState } from 'react'
import { Card, CardHeader, CardTitle, CardContent } from '@/components/ui/Card'
import { Select } from '@/components/ui/Input'
import { Badge } from '@/components/ui/Badge'
import { useStatsSummary } from '@/lib/api/hooks'
import { formatDuration } from '@/lib/utils'

export function StatsPage() {
  const [range, setRange] = useState('1h')
  const stats = useStatsSummary(range)
  const s = stats.data
  return (
    <div className="space-y-4">
      <Card>
        <CardHeader>
          <CardTitle>Stats</CardTitle>
          <Select value={range} onChange={e => setRange(e.target.value)} className="w-36">
            <option value="1h">last hour</option>
            <option value="6h">last 6h</option>
            <option value="24h">last 24h</option>
          </Select>
        </CardHeader>
        <CardContent>
          {stats.error ? (
            <div className="text-[var(--color-error)] text-sm">stats unavailable</div>
          ) : !s ? (
            <div className="text-[var(--color-muted)]">loading…</div>
          ) : (
            <div className="grid gap-3 md:grid-cols-3 xl:grid-cols-5">
              <Stat label="requests" value={s.total_requests} />
              <Stat label="prompt tokens" value={formatCompact(s.total_prompt_tokens)} />
              <Stat label="completion tokens" value={formatCompact(s.total_completion_tokens)} />
              <Stat label="success" value={s.success_count} badge="success" />
              <Stat label="errors" value={s.error_count} badge={s.error_count ? 'error' : 'neutral'} />
              <Stat label="tool errors" value={s.tool_error_count} badge={s.tool_error_count ? 'error' : 'neutral'} />
              <Stat label="malformed" value={s.malformed_count} badge={s.malformed_count ? 'warn' : 'neutral'} />
              <Stat label="cache hits" value={s.cache_hit_count} />
              <Stat label="p50 total" value={formatDuration(s.p50_ms)} />
              <Stat label="p95 total" value={formatDuration(s.p95_ms)} />
              <Stat label="p99 total" value={formatDuration(s.p99_ms)} />
              <Stat label="p95 queue" value={formatDuration(s.queue_p95_ms)} />
              <Stat label="p95 llm" value={formatDuration(s.upstream_p95_ms)} />
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  )
}

function Stat({ label, value, badge = 'info' }: { label: string; value: string | number; badge?: 'neutral' | 'info' | 'warn' | 'error' | 'success' }) {
  return (
    <div className="rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-bg)] p-3 space-y-2">
      <div className="text-xs text-[var(--color-muted)]">{label}</div>
      <Badge variant={badge}>{value}</Badge>
    </div>
  )
}

function formatCompact(value: number) {
  return new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 }).format(value)
}
