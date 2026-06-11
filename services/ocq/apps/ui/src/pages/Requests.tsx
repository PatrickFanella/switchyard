import { ArrowUpRight, CircleDashed } from 'lucide-react'
import { StatusBadge } from '../components/StatusBadge'
import type { GatewayRequest } from '../lib/types'

type RequestsProps = {
  requests: GatewayRequest[]
  isRefreshing: boolean
  syncError: string | null
}

function toneForStatus(status: number) {
  if (status >= 500) return 'danger' as const
  if (status >= 400) return 'warning' as const
  return 'success' as const
}

function statusGroup(status: number) {
  if (status >= 500) return '5xx'
  if (status >= 400) return '4xx'
  if (status >= 300) return '3xx'
  return '2xx'
}

function formatDuration(durationMs?: number) {
  if (typeof durationMs !== 'number') return '—'
  return `${durationMs.toFixed(durationMs >= 100 ? 0 : 1)}ms`
}

export function Requests({ requests, isRefreshing, syncError }: RequestsProps) {
  return (
    <section className="space-y-4">
      <div className="rounded-3xl border border-slate-800/90 bg-slate-950/70 p-6 backdrop-blur">
        <div className="flex flex-col gap-3 md:flex-row md:items-end md:justify-between">
          <div>
            <p className="text-xs uppercase tracking-[0.35em] text-cyan-300/70">requests</p>
            <h2 className="mt-3 text-3xl font-semibold tracking-tight text-slate-50">Wire-level request ledger.</h2>
            <p className="mt-3 max-w-2xl text-sm leading-6 text-slate-400">
              Each poll captures route, model, status, and timing. Short, scannable, and built for ops triage.
            </p>
          </div>
          <StatusBadge tone={syncError ? 'warning' : isRefreshing ? 'neutral' : 'success'}>
            {syncError ? 'partial sync' : isRefreshing ? 'polling' : 'synced'}
          </StatusBadge>
        </div>
      </div>

      {requests.length ? (
        <div className="overflow-hidden rounded-3xl border border-slate-800/90 bg-slate-950/70 backdrop-blur">
          <div className="hidden grid-cols-[1.4fr_0.7fr_0.55fr_0.75fr_0.55fr] gap-4 border-b border-slate-800 px-5 py-4 text-xs uppercase tracking-[0.32em] text-slate-500 md:grid">
            <span>Route</span>
            <span>Model</span>
            <span>Status</span>
            <span>Latency</span>
            <span>Flags</span>
          </div>
          <div className="divide-y divide-slate-800">
            {requests.map((request, index) => (
              <div key={`${request.ts}-${index}`} className="grid gap-4 px-5 py-4 text-sm md:grid-cols-[1.4fr_0.7fr_0.55fr_0.75fr_0.55fr] md:items-center">
                <div>
                  <div className="flex items-center gap-2">
                    <span className="font-medium text-slate-100">{request.route}</span>
                    {request.stream ? <CircleDashed className="h-3.5 w-3.5 text-cyan-300" /> : null}
                  </div>
                  <p className="mt-1 text-xs text-slate-500">{request.ts}</p>
                  {request.sessionID ? <p className="mt-1 text-xs text-slate-500">session {request.sessionID}</p> : null}
                </div>

                <div className="text-slate-300">{request.model ?? '—'}</div>

                <div className="flex items-center gap-2">
                  <StatusBadge tone={toneForStatus(request.status)}>{statusGroup(request.status)}</StatusBadge>
                  <span className="font-medium tabular-nums text-slate-200">{request.status}</span>
                </div>

                <div className="font-medium tabular-nums text-slate-200">{formatDuration(request.durationMs)}</div>

                <div className="text-slate-500">
                  {request.error ? (
                    <span className="inline-flex items-center gap-1 text-rose-300">
                      <ArrowUpRight className="h-3.5 w-3.5" />
                      error
                    </span>
                  ) : (
                    request.stream ? 'stream' : 'poll'
                  )}
                </div>
              </div>
            ))}
          </div>
        </div>
      ) : (
        <div className="rounded-3xl border border-slate-800/90 bg-slate-950/60 p-10 text-center text-sm text-slate-400 backdrop-blur">
          No requests yet. Once the gateway polls, the ledger will populate here.
        </div>
      )}
    </section>
  )
}
