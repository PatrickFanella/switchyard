import { ChatPane } from '../components/ChatPane'
import { StatusBadge } from '../components/StatusBadge'
import type { GatewayClient } from '../lib/api'
import type { SessionDetail, SessionSummary } from '../lib/types'

type SessionsProps = {
  sessions: SessionSummary[]
  selected?: SessionDetail
  selectedId: string | null
  onSelect: (id: string) => void | Promise<void>
  client: GatewayClient | null
  refreshSelected: (id?: string) => Promise<void>
  isRefreshing: boolean
  syncError: string | null
}

function formatTimestamp(value: number | null) {
  if (!value) return 'unknown'
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: 'medium',
    timeStyle: 'short',
  }).format(value)
}

export function Sessions({ sessions, selected, selectedId, onSelect, client, refreshSelected, isRefreshing, syncError }: SessionsProps) {
  const selectedSummary = sessions.find((session) => session.id === selectedId)

  async function send(prompt: string) {
    if (!client || !selected) return
    await client.sendSessionMessage(selected.id, prompt)
    await refreshSelected(selected.id)
  }

  return (
    <section className="grid gap-4 xl:grid-cols-[24rem_minmax(0,1fr)]">
      <aside className="rounded-3xl border border-slate-800/90 bg-slate-950/70 p-5 backdrop-blur xl:sticky xl:top-6 xl:h-[calc(100vh-7rem)] xl:overflow-hidden">
        <div className="flex items-start justify-between gap-3">
          <div>
            <p className="text-xs uppercase tracking-[0.35em] text-cyan-300/70">sessions</p>
            <h2 className="mt-3 text-3xl font-semibold tracking-tight text-slate-50">Live session deck.</h2>
          </div>
          <StatusBadge tone={syncError ? 'warning' : isRefreshing ? 'neutral' : 'success'}>
            {syncError ? 'partial sync' : isRefreshing ? 'polling' : 'synced'}
          </StatusBadge>
        </div>

        <p className="mt-3 text-sm leading-6 text-slate-400">
          Safe navigation only. Opening a session fetches detail with GET; send is the only mutating action.
        </p>

        <div className="mt-5 grid gap-2 overflow-y-auto pr-1 xl:h-[calc(100%-9rem)]">
          {sessions.length ? (
            sessions.map((session) => {
              const active = session.id === selectedId
              return (
                <button
                  key={session.id}
                  type="button"
                  onClick={() => {
                    void Promise.resolve(onSelect(session.id)).catch(() => undefined)
                  }}
                  aria-current={active ? 'page' : undefined}
                  className={`group rounded-2xl border p-4 text-left transition focus-visible:ring-2 focus-visible:ring-cyan-400/70 ${
                    active
                      ? 'border-cyan-400/25 bg-cyan-400/10 shadow-[0_0_0_1px_rgba(34,211,238,0.1)]'
                      : 'border-slate-800 bg-slate-900/35 hover:border-slate-700 hover:bg-slate-900/75'
                  }`}
                >
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0">
                      <p className="truncate font-medium text-slate-100">{session.title}</p>
                      <p className="mt-1 truncate font-mono text-[11px] uppercase tracking-[0.22em] text-slate-500">
                        {session.id}
                      </p>
                    </div>
                    <StatusBadge tone={active ? 'accent' : 'neutral'}>{active ? 'open' : 'watch'}</StatusBadge>
                  </div>

                  <div className="mt-3 flex flex-wrap gap-2 text-xs text-slate-500">
                    <span className="rounded-full border border-slate-800 bg-slate-950/60 px-2 py-1">updated {formatTimestamp(session.updatedAt)}</span>
                    <span className="rounded-full border border-slate-800 bg-slate-950/60 px-2 py-1">created {formatTimestamp(session.createdAt)}</span>
                  </div>
                </button>
              )
            })
          ) : (
            <div className="rounded-2xl border border-dashed border-slate-800 bg-slate-950/40 p-6 text-sm text-slate-500">
              No sessions yet. Once the gateway discovers them, they will appear here.
            </div>
          )}
        </div>

        {selectedSummary ? (
          <p className="mt-4 text-xs uppercase tracking-[0.28em] text-slate-500">
            Selected in list · {selectedSummary.title}
          </p>
        ) : null}
      </aside>

      <ChatPane session={selected} onSend={send} />
    </section>
  )
}
