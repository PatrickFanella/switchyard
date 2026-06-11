import type { ChangeEvent } from 'react'
import { ShieldAlert, TimerReset } from 'lucide-react'
import type { Settings } from '../lib/types'
import { StatusBadge } from './StatusBadge'

type SettingsPanelProps = {
  settings: Settings
  onChange: (settings: Settings) => void
}

function updateField(
  settings: Settings,
  onChange: (settings: Settings) => void,
  key: keyof Settings,
  value: string | number,
) {
  onChange({ ...settings, [key]: value } as Settings)
}

export function SettingsPanel({ settings, onChange }: SettingsPanelProps) {
  return (
    <section className="grid gap-4 xl:grid-cols-[1.15fr_0.85fr]">
      <div className="rounded-3xl border border-slate-800/90 bg-slate-950/70 p-6 shadow-[0_0_0_1px_rgba(15,23,42,0.55)] backdrop-blur">
        <div className="flex items-start justify-between gap-4">
          <div>
            <p className="text-xs uppercase tracking-[0.35em] text-cyan-300/70">settings</p>
            <h2 className="mt-3 text-3xl font-semibold tracking-tight text-slate-50">gateway connection</h2>
            <p className="mt-3 max-w-2xl text-sm leading-6 text-slate-400">
              Saved locally. App polls metrics, request traces, and logs from the gateway with your key.
            </p>
          </div>
          <StatusBadge tone="accent">local only</StatusBadge>
        </div>

        <div className="mt-8 grid gap-5">
          <label className="grid gap-2">
            <span className="text-xs uppercase tracking-[0.32em] text-slate-500">Gateway URL</span>
            <input
              className="rounded-2xl border border-slate-800 bg-slate-950 px-4 py-3 text-slate-100 transition placeholder:text-slate-600 focus-visible:border-cyan-400/50 focus-visible:ring-2 focus-visible:ring-cyan-400/20"
              value={settings.baseUrl}
              onChange={(event: ChangeEvent<HTMLInputElement>) => updateField(settings, onChange, 'baseUrl', event.target.value)}
              placeholder="http://127.0.0.1:8088"
              spellCheck={false}
            />
          </label>

          <label className="grid gap-2">
            <span className="text-xs uppercase tracking-[0.32em] text-slate-500">API key</span>
            <input
              className="rounded-2xl border border-slate-800 bg-slate-950 px-4 py-3 text-slate-100 transition placeholder:text-slate-600 focus-visible:border-cyan-400/50 focus-visible:ring-2 focus-visible:ring-cyan-400/20"
              type="password"
              value={settings.apiKey}
              onChange={(event: ChangeEvent<HTMLInputElement>) => updateField(settings, onChange, 'apiKey', event.target.value)}
              placeholder="Bearer token"
              spellCheck={false}
              autoComplete="off"
            />
          </label>

          <label className="grid gap-2">
            <span className="text-xs uppercase tracking-[0.32em] text-slate-500">Refresh interval (ms)</span>
            <input
              className="rounded-2xl border border-slate-800 bg-slate-950 px-4 py-3 text-slate-100 transition placeholder:text-slate-600 focus-visible:border-cyan-400/50 focus-visible:ring-2 focus-visible:ring-cyan-400/20"
              type="number"
              min={1000}
              step={500}
              value={settings.refreshMs}
              onChange={(event: ChangeEvent<HTMLInputElement>) =>
                updateField(settings, onChange, 'refreshMs', Number(event.target.value) || 3000)
              }
            />
          </label>
        </div>
      </div>

      <div className="grid gap-4 rounded-3xl border border-slate-800/90 bg-slate-950/55 p-6 text-sm text-slate-400 backdrop-blur">
        <div className="rounded-2xl border border-slate-800 bg-slate-900/40 p-4">
          <div className="flex items-center gap-2 text-slate-200">
            <TimerReset className="h-4 w-4 text-cyan-300" />
            refresh behavior
          </div>
          <p className="mt-3 leading-6">Polling wakes every {Math.max(1, Math.round(settings.refreshMs / 1000))}s and keeps the deck current.</p>
        </div>

        <div className="rounded-2xl border border-slate-800 bg-slate-900/40 p-4">
          <div className="flex items-center gap-2 text-slate-200">
            <ShieldAlert className="h-4 w-4 text-amber-300" />
            safety note
          </div>
          <p className="mt-3 leading-6">Task 7 stays read-first. Sessions chat remains a stub until the next task.</p>
        </div>
      </div>
    </section>
  )
}
