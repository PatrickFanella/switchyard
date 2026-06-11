import { TerminalSquare, TriangleAlert } from 'lucide-react'
import { StatusBadge } from '../components/StatusBadge'
import type { GatewayLog } from '../lib/types'

type LogsProps = {
  logs: GatewayLog[]
  isRefreshing: boolean
  syncError: string | null
}

function toneForLevel(level: GatewayLog['level']) {
  switch (level) {
    case 'error':
      return 'danger' as const
    case 'warn':
      return 'warning' as const
    case 'info':
      return 'accent' as const
    default:
      return 'neutral' as const
  }
}

export function Logs({ logs, isRefreshing, syncError }: LogsProps) {
  return (
    <section className="space-y-4">
      <div className="rounded-3xl border border-slate-800/90 bg-slate-950/70 p-6 backdrop-blur">
        <div className="flex flex-col gap-3 md:flex-row md:items-end md:justify-between">
          <div>
            <p className="text-xs uppercase tracking-[0.35em] text-cyan-300/70">logs</p>
            <h2 className="mt-3 text-3xl font-semibold tracking-tight text-slate-50">Operator log stream.</h2>
            <p className="mt-3 max-w-2xl text-sm leading-6 text-slate-400">
              Sortable by eye: timestamp, level, message, and nested metadata when present.
            </p>
          </div>
          <StatusBadge tone={syncError ? 'warning' : isRefreshing ? 'neutral' : 'success'}>
            {syncError ? 'partial sync' : isRefreshing ? 'polling' : 'synced'}
          </StatusBadge>
        </div>
      </div>

      {logs.length ? (
        <div className="space-y-3">
          {logs.map((log, index) => (
            <article key={`${log.ts}-${index}`} className="rounded-3xl border border-slate-800/90 bg-slate-950/70 p-5 backdrop-blur">
              <div className="flex flex-wrap items-center gap-3">
                <StatusBadge tone={toneForLevel(log.level)}>{log.level}</StatusBadge>
                <span className="font-mono text-xs text-slate-500">{log.ts}</span>
              </div>
              <p className="mt-4 text-sm leading-6 text-slate-200">{log.message}</p>
              {log.meta && Object.keys(log.meta).length ? (
                <pre className="mt-4 overflow-x-auto rounded-2xl border border-slate-800 bg-slate-900/60 p-4 font-mono text-xs leading-6 text-slate-400">
                  {JSON.stringify(log.meta, null, 2)}
                </pre>
              ) : null}
            </article>
          ))}
        </div>
      ) : (
        <div className="rounded-3xl border border-slate-800/90 bg-slate-950/60 p-10 text-center text-sm text-slate-400 backdrop-blur">
          <div className="flex justify-center">
            <TerminalSquare className="h-5 w-5 text-cyan-300" />
          </div>
          <p className="mt-4">No logs yet. The stream stays calm until traffic or errors arrive.</p>
          <p className="mt-2 text-slate-500">Warnings and errors will surface here with level badges.</p>
          <div className="mt-6 inline-flex items-center gap-2 text-amber-200">
            <TriangleAlert className="h-4 w-4" />
            partial sync banner appears above when polling fails
          </div>
        </div>
      )}
    </section>
  )
}
