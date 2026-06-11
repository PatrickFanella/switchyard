import * as React from 'react'
import { cn } from '@/lib/utils'

type Variant = 'default' | 'secondary' | 'ghost' | 'destructive' | 'outline'
type Size = 'sm' | 'md' | 'icon'

const base = 'inline-flex items-center justify-center gap-2 rounded-[var(--radius-sm)] font-medium transition-colors disabled:opacity-50 disabled:cursor-not-allowed focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-accent)]'

const variants: Record<Variant, string> = {
  default:     'bg-[var(--color-accent)] text-white hover:bg-[var(--color-accent-hover)]',
  secondary:   'bg-[var(--color-card)] text-[var(--color-text)] border border-[var(--color-border)] hover:border-[var(--color-accent)]',
  outline:     'border border-[var(--color-border)] text-[var(--color-text)] hover:border-[var(--color-accent)]',
  ghost:       'text-[var(--color-text)] hover:bg-[var(--color-card)]',
  destructive: 'bg-[var(--color-error)] text-white hover:opacity-90',
}
const sizes: Record<Size, string> = {
  sm:   'h-7 px-2 text-xs',
  md:   'h-9 px-3 text-sm',
  icon: 'h-9 w-9',
}

export interface ButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: Variant
  size?: Size
}

export const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant = 'default', size = 'md', ...props }, ref) => (
    <button ref={ref} className={cn(base, variants[variant], sizes[size], className)} {...props} />
  ),
)
Button.displayName = 'Button'
