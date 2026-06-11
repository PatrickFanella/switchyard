import { useHistory } from '@/lib/api/hooks'
import { Card, CardHeader, CardTitle, CardContent } from '@/components/ui/Card'
import { Table, THead, TBody, TR, TH, TD } from '@/components/ui/Table'
import { Badge } from '@/components/ui/Badge'
import { formatDuration, formatRelative } from '@/lib/utils'

export function HistoryPage() {
  const { data } = useHistory(200)
  const entries = data?.entries ?? []
  return (
    <Card>
      <CardHeader><CardTitle>Recent requests ({entries.length})</CardTitle></CardHeader>
      <CardContent className="p-0">
        <Table>
          <THead>
            <TR>
              <TH>When</TH><TH>Client</TH><TH>Model</TH><TH>Path</TH><TH>Status</TH>
              <TH title="prompt tokens">Prompt T</TH>
              <TH title="completion tokens">Completion T</TH>
              <TH title="time spent waiting in queue">Queue</TH>
              <TH title="time spent waiting on the upstream LLM">Upstream</TH>
              <TH title="total request→response wall time">Total</TH>
              <TH>ID</TH>
            </TR>
          </THead>
          <TBody>
            {entries.length === 0 ? (
              <TR><TD colSpan={11} className="text-[var(--color-muted)]">no history</TD></TR>
            ) : entries.slice().reverse().map(e => (
              <TR key={e.id}>
                <TD>{formatRelative(e.started_at || e.enqueued_at)}</TD>
                <TD><Badge variant="info">{e.client}</Badge></TD>
                <TD className="font-mono text-xs">{e.model}</TD>
                <TD className="text-xs">{e.path}</TD>
                <TD>
                  <Badge variant={e.status === 'success' ? 'success' : e.status === 'error' ? 'error' : 'neutral'}>
                    {e.status}
                  </Badge>
                </TD>
                <TD className="text-xs text-[var(--color-muted)]">{e.prompt_tokens != null ? e.prompt_tokens : '—'}</TD>
                <TD className="text-xs text-[var(--color-muted)]">{e.completion_tokens != null ? e.completion_tokens : '—'}</TD>
                <TD className="text-xs text-[var(--color-muted)]">{e.queue_ms != null ? formatDuration(e.queue_ms) : '—'}</TD>
                <TD className="text-xs">{e.upstream_ms != null ? formatDuration(e.upstream_ms) : '—'}</TD>
                <TD>{formatDuration(e.duration_ms)}</TD>
                <TD className="font-mono text-xs">{e.id}</TD>
              </TR>
            ))}
          </TBody>
        </Table>
      </CardContent>
    </Card>
  )
}
