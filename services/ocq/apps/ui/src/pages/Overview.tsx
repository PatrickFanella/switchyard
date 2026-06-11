import { Clock3, Activity, AlertTriangle, Gauge, Waves } from 'lucide-react'
import { MetricCard } from '../components/MetricCard'
import { StatusBadge } from '../components/StatusBadge'
import type { MetricsSummary } from '../lib/types'

type OverviewProps = {
  metrics?: MetricsSummary
  isRefreshing: boolean
  syncError: string | null
  lastRefreshAt: number | null
  refreshMs: number
}

function formatTime(ts: number | null) {
  if (!ts) return 'waiting for first poll'
  return new Intl.DateTimeFormat(undefined, {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  }).format(new Date(ts))
}

export function Overview({ metrics, isRefreshing, syncError, lastRefreshAt, refreshMs }: OverviewProps) {
  const cards = [
    { label: 'Uptime', value: `${metrics?.uptimeSeconds ?? 0}s`, detail: 'gateway runtime', accent: 'from-cyan-400/30 to-sky-500/0' },
    { label: 'Requests', value: metrics?.requestsTotal ?? 0, detail: 'all routes observed', accent: 'from-emerald-400/25 to-teal-500/0' },
    { label: 'Errors', value: metrics?.errorsTotal ?? 0, detail: '4xx and 5xx combined', accent: 'from-rose-400/25 to-pink-500/0' },
    { label: 'Active streams', value: metrics?.activeStreams ?? 0, detail: 'open SSE responses', accent: 'from-violet-400/25 to-fuchsia-500/0' },
    { label: 'p95 latency', value: `${metrics?.p95LatencyMs ?? 0}ms`, detail: 'tail latency', accent: 'from-amber-300/25 to-orange-500/0' },
  ]

  return (
    <section className="space-y-6">
      <div className="flex flex-col gap-4 rounded-3xl border border-slate-800/90 bg-slate-950/70 p-6 backdrop-blur md:flex-row md:items-end md:justify-between">
        <div className="max-w-3xl">
          <p className="text-xs uppercase tracking-[0.35em] text-cyan-300/70">overview</p>
          <h2 className="mt-3 text-4xl font-semibold tracking-tight text-slate-50">Gateway pulse, at a glance.</h2>
          <p className="mt-3 text-sm leading-6 text-slate-400">
            Dense, low-friction telemetry for operators. Request volume, error pressure, and latency in one pass.
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          <StatusBadge tone={syncError ? 'warning' : isRefreshing ? 'neutral' : 'success'}>
            {syncError ? 'partial sync' : isRefreshing ? 'polling' : 'live'}
          </StatusBadge>
          <StatusBadge tone="accent">refresh {Math.max(1, Math.round(refreshMs / 1000))}s</StatusBadge>
        </div>
      </div>

      {syncError ? (
        <div className="flex items-start gap-3 rounded-3xl border border-amber-400/20 bg-amber-400/10 p-4 text-sm text-amber-100">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-300" />
          <p className="leading-6">{syncError}</p>
        </div>
      ) : null}

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-5">
        {cards.map((card) => (
          <MetricCard key={card.label} {...card} />
        ))}
      </div>

      <div className="grid gap-4 lg:grid-cols-[1.2fr_0.8fr]">
        <div className="rounded-3xl border border-slate-800/90 bg-slate-950/65 p-6 backdrop-blur">
          <div className="flex items-center gap-2 text-sm text-slate-300">
            <Gauge className="h-4 w-4 text-cyan-300" />
            live sampling notes
          </div>
          <p className="mt-3 max-w-2xl text-sm leading-6 text-slate-400">
            The panel polls metrics, requests, and logs in one sweep. If an endpoint fails, stale data remains visible
            and the console shows the partial-sync banner instead of blanking the deck.
          </p>
        </div>

        <div className="rounded-3xl border border-slate-800/90 bg-slate-950/65 p-6 backdrop-blur">
          <div className="flex items-center gap-2 text-sm text-slate-300">
            <Clock3 className="h-4 w-4 text-cyan-300" />
            last refresh
          </div>
          <p className="mt-3 text-2xl font-semibold tracking-tight text-slate-50">{formatTime(lastRefreshAt)}</p>
          <p className="mt-2 text-sm leading-6 text-slate-400">Polling cadence stays fixed unless you change settings.</p>
        </div>
      </div>

      <div className="grid gap-4 md:grid-cols-3">
        <div className="rounded-3xl border border-cyan-400/10 bg-cyan-400/5 p-5 text-sm leading-6 text-slate-300">
          <div className="flex items-center gap-2 text-cyan-200">
            <Activity className="h-4 w-4" />
            request flow
          </div>
          <p className="mt-3 text-slate-400">Watch route pressure, status mix, and latency shape without leaving the overview.</p>
        </div>
        <div className="rounded-3xl border border-emerald-400/10 bg-emerald-400/5 p-5 text-sm leading-6 text-slate-300">
          <div className="flex items-center gap-2 text-emerald-200">
            <Waves className="h-4 w-4" />
            logs
          </div>
          <p className="mt-3 text-slate-400">Error bursts and warnings stay readable in the dedicated log tab.</p>
        </div>
        <div className="rounded-3xl border border-amber-400/10 bg-amber-400/5 p-5 text-sm leading-6 text-slate-300">
          <div className="flex items-center gap-2 text-amber-200">
            <AlertTriangle className="h-4 w-4" />
            session safety
          </div>
          <p className="mt-3 text-slate-400">Sessions are intentionally placeholder-only until Task 8 adds safe watch/chat.</p>
        </div>
      </div>
    </section>
  )
}
