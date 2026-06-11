import { useBrokerStatus } from '@/lib/api/hooks'
import { Card, CardHeader, CardTitle, CardContent } from '@/components/ui/Card'
import { Badge } from '@/components/ui/Badge'
import { formatDuration, formatRelative } from '@/lib/utils'
import type { UpstreamStatus } from '@/lib/api/types'

export function ConfigPage() {
  const { data, isError } = useBrokerStatus()
  const brokerStatus = data ? 'ok' : isError ? 'down' : 'checking'
  const brokerVariant = data ? 'success' : isError ? 'error' : 'info'
  const inFlight = data?.in_flight_client || data?.in_flight_id
  const inFlightLabel = data?.in_flight_client
    ? `${data.in_flight_client}${data.in_flight_id ? ` (${data.in_flight_id})` : ''}`
    : data?.in_flight_id ?? 'idle'
  return (
    <div className="grid gap-4 grid-cols-1 lg:grid-cols-2">
      <Card>
        <CardHeader><CardTitle>Broker</CardTitle></CardHeader>
        <CardContent className="space-y-2 text-sm">
          <Row label="Status"><Badge variant={brokerVariant}>{brokerStatus}</Badge></Row>
          <Row label="Queue depth">{data?.queue_depth ?? '—'} / {data?.max_depth ?? '∞'}</Row>
          <Row label="In-flight">{inFlight ? inFlightLabel : 'idle'}</Row>
        </CardContent>
      </Card>
      <Card>
        <CardHeader><CardTitle>Upstreams</CardTitle></CardHeader>
        <CardContent className="p-0">
          <table className="w-full text-sm">
            <thead className="border-b border-[var(--color-border)] text-xs text-[var(--color-muted)]">
              <tr>
                <th className="px-3 py-2 text-left font-medium">Name</th>
                <th className="px-3 py-2 text-left font-medium">URL</th>
                <th className="px-3 py-2 text-left font-medium">Health</th>
                <th className="px-3 py-2 text-left font-medium">Probe</th>
              </tr>
            </thead>
            <tbody>
              {(data?.upstreams ?? []).map(u => (
                <tr key={u.name} className="border-b border-[var(--color-border)] last:border-0">
                  <td className="px-3 py-2"><Badge variant="info">{u.name}</Badge></td>
                  <td className="px-3 py-2 font-mono text-xs">{u.url}</td>
                  <td className="px-3 py-2"><UpstreamHealthBadge healthy={u.healthy} /></td>
                  <td className="px-3 py-2"><ProbeMeta upstream={u} /></td>
                </tr>
              ))}
              {(data?.upstreams ?? []).length === 0 && (
                <tr><td className="px-3 py-2 text-[var(--color-muted)]" colSpan={4}>no upstreams</td></tr>
              )}
            </tbody>
          </table>
        </CardContent>
      </Card>
    </div>
  )
}

function UpstreamHealthBadge({ healthy }: { healthy?: boolean }) {
  if (healthy === undefined) {
    return <Badge variant="neutral">unknown</Badge>
  }
  return <Badge variant={healthy ? 'success' : 'error'}>{healthy ? 'up' : 'down'}</Badge>
}

function ProbeMeta({ upstream }: { upstream: UpstreamStatus }) {
  const hasProbeInfo = upstream.last_probe_at || upstream.last_probe_ms !== undefined || upstream.last_probe_error

  if (!hasProbeInfo) {
    return <span className="text-[var(--color-muted)]">not probed yet</span>
  }

  return (
    <div className="space-y-1">
      <div className="text-xs text-[var(--color-muted)]">
        fresh {formatRelative(upstream.last_probe_at)} · latency {formatDuration(upstream.last_probe_ms)}
      </div>
      {upstream.last_probe_error && (
        <div className="flex items-center gap-2 min-w-0">
          <Badge variant="error" title={upstream.last_probe_error}>probe failed</Badge>
          <div className="text-xs text-[var(--color-error)] truncate" title={upstream.last_probe_error}>
            {upstream.last_probe_error}
          </div>
        </div>
      )}
    </div>
  )
}

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-4">
      <span className="text-[var(--color-muted)]">{label}</span>
      <span>{children}</span>
    </div>
  )
}
