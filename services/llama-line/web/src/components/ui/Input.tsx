import * as React from 'react'
import { cn } from '@/lib/utils'

export const Input = React.forwardRef<HTMLInputElement, React.InputHTMLAttributes<HTMLInputElement>>(
  ({ className, ...props }, ref) => (
    <input ref={ref}
      className={cn(
        'h-9 px-2 rounded-[var(--radius-sm)] bg-[var(--color-bg)] border border-[var(--color-border)] text-sm',
        'focus:outline-none focus:border-[var(--color-accent)]',
        className,
      )}
      {...props} />
  ),
)
Input.displayName = 'Input'

export const Select = React.forwardRef<HTMLSelectElement, React.SelectHTMLAttributes<HTMLSelectElement>>(
  ({ className, ...props }, ref) => (
    <select ref={ref}
      className={cn(
        'h-9 px-2 rounded-[var(--radius-sm)] bg-[var(--color-bg)] border border-[var(--color-border)] text-sm',
        'focus:outline-none focus:border-[var(--color-accent)]',
        className,
      )}
      {...props} />
  ),
)
Select.displayName = 'Select'
