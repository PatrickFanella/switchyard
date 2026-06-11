import { useBrokerStatus, useDropRequest, useDrainQueue, useCancelInflight } from '@/lib/api/hooks'
import { Card, CardHeader, CardTitle, CardContent } from '@/components/ui/Card'
import { Button } from '@/components/ui/Button'
import { Badge } from '@/components/ui/Badge'
import { Table, THead, TBody, TR, TH, TD } from '@/components/ui/Table'
import { LogStream } from '@/components/LogStream'
import { formatDuration, formatElapsedSeconds, formatRelative, formatTimestamp } from '@/lib/utils'
import type { LogRecord, UpstreamStatus } from '@/lib/api/types'

const QUEUE_EVENT_MSGS = new Set([
  'request queued', 'request started', 'request completed', 'request failed',
  'request dropped', 'request cancelled', 'queue full', 'request dequeued',
])
const queueFilter = (r: LogRecord) => QUEUE_EVENT_MSGS.has(r.msg)

export function QueuePage() {
  const broker = useBrokerStatus()
  const drop = useDropRequest()
  const drain = useDrainQueue()
  const cancel = useCancelInflight()
  const upstreams = broker.data?.upstreams ?? []
  const totalDepth = broker.data?.queue_depth ?? 0
  const maxDepth = broker.data?.max_depth ?? 0
  const hasInflight = Boolean(
    broker.data?.in_flight_client ||
    broker.data?.in_flight_id ||
    upstreams.some(upstream => upstream.in_flight_client || upstream.in_flight_id),
  )

  return (
    <div className="grid gap-4 grid-cols-1 xl:grid-cols-3">
      <div className="xl:col-span-2 flex flex-col gap-4">
        <Card>
          <CardHeader>
            <div className="flex items-center gap-2 flex-wrap">
              <CardTitle>Broker queue</CardTitle>
              <Badge variant={totalDepth >= maxDepth && maxDepth > 0 ? 'warn' : 'info'}>
                queued {totalDepth}/{maxDepth || '∞'}
              </Badge>
            </div>
            <div className="flex items-center gap-2">
              <Button size="sm" variant="destructive" onClick={() => cancel.mutate()} disabled={cancel.isPending || !hasInflight}>
                Global cancel
              </Button>
              <Button
                size="sm"
                variant="secondary"
                onClick={() => { if (confirm('Drain entire queue?')) drain.mutate() }}
                disabled={drain.isPending || totalDepth === 0}
              >
                Global drain
              </Button>
            </div>
          </CardHeader>
          <CardContent className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4 text-sm">
            <Metric label="Total depth" value={String(totalDepth)} sub={`max ${maxDepth || '∞'}`} />
            <Metric label="Connected clients" value={String(broker.data?.connected_clients ?? '—')} />
            <Metric
              label="In-flight"
              value={broker.data?.in_flight_client || 'idle'}
              sub={broker.data?.in_flight_id ? `id ${broker.data.in_flight_id}` : undefined}
            />
            <Metric label="Upstreams" value={String(upstreams.length)} />
          </CardContent>
        </Card>

        <div className="grid gap-4 md:grid-cols-2">
          {upstreams.length === 0 ? (
            <Card className="md:col-span-2">
              <CardContent className="text-[var(--color-muted)]">no upstreams</CardContent>
            </Card>
          ) : upstreams.map(upstream => (
            <UpstreamQueueCard key={upstream.name} upstream={upstream} onDrop={(id) => drop.mutate(id)} />
          ))}
        </div>
      </div>

      <Card className="flex flex-col">
        <CardHeader><CardTitle>Queue events</CardTitle></CardHeader>
        <CardContent className="flex-1 p-2">
          <LogStream filterFn={queueFilter} compact initialCount={100} maxEntries={500} includeFullMessagesWhenFiltered />
        </CardContent>
      </Card>
    </div>
  )
}

function UpstreamQueueCard({ upstream, onDrop }: { upstream: UpstreamStatus; onDrop: (id: string) => void }) {
  const full = upstream.max_depth > 0 && upstream.queue_depth >= upstream.max_depth
  return (
    <Card className="overflow-hidden">
      <CardHeader className="items-start">
        <div className="flex w-full flex-col gap-2">
          <div className="flex items-center gap-2 flex-wrap">
            <CardTitle>{upstream.name}</CardTitle>
            <Badge variant={full ? 'warn' : 'info'}>queued {upstream.queue_depth}/{upstream.max_depth || '∞'}</Badge>
            {upstream.healthy === undefined
              ? <Badge variant="neutral">unknown</Badge>
              : <Badge variant={upstream.healthy ? 'success' : 'error'}>{upstream.healthy ? 'up' : 'down'}</Badge>}
          </div>
          <span className="font-mono text-xs text-[var(--color-muted)]">{upstream.url}</span>
          <ProbeMeta upstream={upstream} />
        </div>
      </CardHeader>
      <CardContent className="space-y-3">
        <div className="flex flex-wrap items-center gap-2 text-xs">
          <span className="text-[var(--color-muted)]">Running</span>
          {upstream.in_flight_client ? (
            <>
              <Badge variant="info">{upstream.in_flight_client}</Badge>
              <span className="text-[var(--color-muted)]">priority {upstream.in_flight_priority ?? 0}</span>
              <span className="text-[var(--color-muted)]">job {formatElapsedSeconds(upstream.in_flight_job_seconds)}</span>
              <span className="text-[var(--color-muted)]">total {formatElapsedSeconds(upstream.in_flight_total_seconds)}</span>
              {upstream.in_flight_id && <span className="font-mono text-[var(--color-muted)]">{upstream.in_flight_id}</span>}
            </>
          ) : (
            <span className="text-[var(--color-muted)]">idle</span>
          )}
        </div>

        <div className="flex flex-wrap gap-2">
          <span className="text-xs text-[var(--color-muted)]">model patterns</span>
          {(upstream.models ?? []).length > 0 ? (
            upstream.models!.map(model => <Badge key={model} variant="neutral">{model}</Badge>)
          ) : (
            <span className="text-xs text-[var(--color-muted)]">any</span>
          )}
        </div>

        <div className="rounded-[var(--radius)] border border-[var(--color-border)] overflow-hidden">
          <Table className="text-xs">
            <THead>
              <TR>
                <TH>#</TH><TH>Client</TH><TH>Priority</TH><TH>ID</TH><TH>Waiting</TH><TH>Queued at</TH><TH></TH>
              </TR>
            </THead>
            <TBody>
              {upstream.queue.length === 0 ? (
                <TR><TD colSpan={7} className="text-[var(--color-muted)]">empty</TD></TR>
              ) : upstream.queue.map(item => (
                <TR key={item.id}>
                  <TD>{item.position}</TD>
                  <TD><Badge variant="info">{item.client}</Badge></TD>
                  <TD>{item.priority}</TD>
                  <TD className="font-mono text-xs">{item.id}</TD>
                  <TD>{formatElapsedSeconds(item.wait_seconds)}</TD>
                  <TD className="font-mono text-xs text-[var(--color-muted)]">{formatTimestamp(item.enqueued_at)}</TD>
                  <TD>
                    <Button size="sm" variant="ghost" onClick={() => onDrop(item.id)}>Drop</Button>
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        </div>
      </CardContent>
    </Card>
  )
}

function ProbeMeta({ upstream }: { upstream: UpstreamStatus }) {
  const hasProbeInfo = upstream.last_probe_at || upstream.last_probe_ms !== undefined || upstream.last_probe_error

  if (!hasProbeInfo) {
    return <span className="text-xs text-[var(--color-muted)]">not probed yet</span>
  }

  return (
    <div className="flex flex-wrap items-center gap-2 text-xs text-[var(--color-muted)]">
      <span>fresh {formatRelative(upstream.last_probe_at)}</span>
      <span>latency {formatDuration(upstream.last_probe_ms)}</span>
      {upstream.last_probe_error && (
        <div className="flex items-center gap-2 min-w-0">
          <Badge variant="error" title={upstream.last_probe_error}>probe failed</Badge>
          <span className="truncate text-[var(--color-error)]" title={upstream.last_probe_error}>{upstream.last_probe_error}</span>
        </div>
      )}
    </div>
  )
}

function Metric({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <div className="rounded-[var(--radius)] border border-[var(--color-border)] px-3 py-2">
      <div className="text-xs text-[var(--color-muted)]">{label}</div>
      <div className="mt-1 font-medium truncate">{value}</div>
      {sub && <div className="text-xs text-[var(--color-muted)] truncate">{sub}</div>}
    </div>
  )
}
