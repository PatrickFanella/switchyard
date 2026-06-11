import * as React from 'react'
import { cn } from '@/lib/utils'

type BadgeVariant = 'neutral' | 'success' | 'error' | 'warn' | 'info'

const map: Record<BadgeVariant, string> = {
  neutral: 'bg-[var(--color-neutral)]/30 text-[var(--color-text)]',
  success: 'bg-[var(--color-success)]/15 text-[var(--color-success)]',
  error:   'bg-[var(--color-error)]/15 text-[var(--color-error)]',
  warn:    'bg-[var(--color-warn)]/15 text-[var(--color-warn)]',
  info:    'bg-[var(--color-accent)]/15 text-[var(--color-accent)]',
}

export function Badge({ variant = 'neutral', className, ...props }: React.HTMLAttributes<HTMLSpanElement> & { variant?: BadgeVariant }) {
  return <span className={cn('inline-flex items-center px-2 py-0.5 rounded-full text-xs font-medium', map[variant], className)} {...props} />
}
