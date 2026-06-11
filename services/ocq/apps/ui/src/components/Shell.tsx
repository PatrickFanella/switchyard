import type { ReactNode } from 'react'
import { Activity, Logs, LayoutGrid, MessageSquareOff, Settings2, type LucideIcon } from 'lucide-react'
import { StatusBadge } from './StatusBadge'

const tabs = ['Overview', 'Requests', 'Logs', 'Sessions', 'Settings'] as const

export type Tab = (typeof tabs)[number]

type ShellProps = {
  tab: Tab
  onTab: (tab: Tab) => void
  connectionLabel: string
  connectionTone?: 'success' | 'warning' | 'danger' | 'neutral' | 'accent'
  statusCopy?: string
  children: ReactNode
}

const navItems: Array<{ tab: Tab; icon: LucideIcon }> = [
  { tab: 'Overview', icon: LayoutGrid },
  { tab: 'Requests', icon: Activity },
  { tab: 'Logs', icon: Logs },
  { tab: 'Sessions', icon: MessageSquareOff },
  { tab: 'Settings', icon: Settings2 },
]

export function Shell({ tab, onTab, connectionLabel, connectionTone = 'neutral', statusCopy, children }: ShellProps) {
  return (
    <div className="min-h-screen text-slate-100">
      <div className="mx-auto flex min-h-screen w-full max-w-[1600px] flex-col lg:flex-row">
        <aside className="border-b border-slate-800/80 bg-slate-950/80 px-5 py-5 backdrop-blur lg:sticky lg:top-0 lg:h-screen lg:w-72 lg:border-b-0 lg:border-r">
          <div className="flex items-start justify-between gap-3 lg:flex-col">
            <div>
              <p className="text-[11px] uppercase tracking-[0.4em] text-cyan-300/70">ocq</p>
              <h1 className="mt-2 text-2xl font-semibold tracking-tight text-slate-50">ops console</h1>
              <p className="mt-2 max-w-[18rem] text-sm leading-6 text-slate-400">
                command deck for gateway health, request flow, and log pressure.
              </p>
            </div>
            <StatusBadge tone={connectionTone}>{connectionLabel}</StatusBadge>
          </div>

          <nav aria-label="Primary" className="mt-8 grid gap-2">
            {navItems.map(({ tab: item, icon: Icon }) => {
              const active = tab === item
              return (
                <button
                  key={item}
                  type="button"
                  aria-current={active ? 'page' : undefined}
                  onClick={() => onTab(item)}
                  className={`group flex items-center gap-3 rounded-2xl border px-4 py-3 text-left transition duration-200 focus-visible:ring-2 focus-visible:ring-cyan-400/70 ${
                    active
                      ? 'border-cyan-400/20 bg-cyan-400/10 text-cyan-100 shadow-[0_0_0_1px_rgba(34,211,238,0.12)]'
                      : 'border-transparent bg-slate-900/30 text-slate-400 hover:border-slate-700 hover:bg-slate-900/70 hover:text-slate-200'
                  }`}
                >
                  <Icon className={`h-4 w-4 transition ${active ? 'text-cyan-200' : 'text-slate-500 group-hover:text-slate-300'}`} />
                  <span className="font-medium">{item}</span>
                </button>
              )
            })}
          </nav>

          <div className="mt-8 rounded-3xl border border-slate-800/80 bg-slate-900/40 p-4 text-sm text-slate-400">
            <p className="text-xs uppercase tracking-[0.32em] text-slate-500">status</p>
            <p className="mt-3 leading-6 text-slate-300">{statusCopy ?? 'ready for operator input'}</p>
          </div>
        </aside>

        <main className="relative flex-1 px-4 py-4 sm:px-6 lg:px-8 lg:py-8">
          <div className="pointer-events-none absolute inset-0 -z-10 overflow-hidden">
            <div className="absolute left-1/2 top-0 h-72 w-[42rem] -translate-x-1/2 rounded-full bg-cyan-500/10 blur-3xl" />
            <div className="absolute right-0 top-32 h-80 w-80 rounded-full bg-emerald-500/5 blur-3xl" />
            <div className="absolute inset-0 bg-[linear-gradient(rgba(148,163,184,0.05)_1px,transparent_1px),linear-gradient(90deg,rgba(148,163,184,0.05)_1px,transparent_1px)] bg-[size:48px_48px] opacity-[0.15]" />
          </div>

          <header className="mb-6 flex flex-col gap-4 rounded-3xl border border-slate-800/80 bg-slate-950/60 p-5 backdrop-blur sm:flex-row sm:items-end sm:justify-between">
            <div>
              <p className="text-xs uppercase tracking-[0.4em] text-slate-500">monitoring</p>
              <p className="mt-2 text-lg text-slate-300">ops-first, dark, low-noise, fast to scan.</p>
            </div>
            <StatusBadge tone={connectionTone}>{connectionLabel}</StatusBadge>
          </header>

          <div className="space-y-6">{children}</div>
        </main>
      </div>
    </div>
  )
}
