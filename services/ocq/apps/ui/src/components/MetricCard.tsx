import type { ReactNode } from 'react'

type MetricCardProps = {
  label: string
  value: string | number
  detail?: string
  accent?: string
  footer?: ReactNode
}

export function MetricCard({ label, value, detail, accent = 'from-cyan-400/20 to-sky-400/5', footer }: MetricCardProps) {
  return (
    <article className="relative overflow-hidden rounded-3xl border border-slate-800/90 bg-slate-950/70 p-5 shadow-[0_0_0_1px_rgba(15,23,42,0.5)] backdrop-blur">
      <div className={`pointer-events-none absolute inset-x-0 top-0 h-1 bg-gradient-to-r ${accent}`} />
      <p className="text-xs uppercase tracking-[0.32em] text-slate-500">{label}</p>
      <p className="mt-4 text-3xl font-semibold tracking-tight text-slate-50 tabular-nums">{value}</p>
      {detail ? <p className="mt-2 text-sm leading-6 text-slate-400">{detail}</p> : null}
      {footer ? <div className="mt-4 text-xs text-slate-500">{footer}</div> : null}
    </article>
  )
}
