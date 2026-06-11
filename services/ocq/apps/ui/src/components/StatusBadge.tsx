import type { ReactNode } from 'react'

type Tone = 'success' | 'warning' | 'danger' | 'neutral' | 'accent'

const styles: Record<Tone, string> = {
  success: 'border-emerald-400/20 bg-emerald-400/10 text-emerald-200',
  warning: 'border-amber-400/20 bg-amber-400/10 text-amber-200',
  danger: 'border-rose-400/20 bg-rose-400/10 text-rose-200',
  neutral: 'border-slate-700 bg-slate-900/80 text-slate-300',
  accent: 'border-cyan-400/20 bg-cyan-400/10 text-cyan-200',
}

type StatusBadgeProps = {
  tone?: Tone
  children: ReactNode
  className?: string
}

export function StatusBadge({ tone = 'neutral', children, className = '' }: StatusBadgeProps) {
  return (
    <span
      className={`inline-flex items-center gap-2 rounded-full border px-3 py-1 text-[11px] font-medium uppercase tracking-[0.28em] ${styles[tone]} ${className}`}
    >
      <span className={`h-1.5 w-1.5 rounded-full ${tone === 'danger' ? 'bg-rose-300' : tone === 'warning' ? 'bg-amber-300' : tone === 'success' ? 'bg-emerald-300' : tone === 'accent' ? 'bg-cyan-300' : 'bg-slate-400'}`} />
      {children}
    </span>
  )
}
