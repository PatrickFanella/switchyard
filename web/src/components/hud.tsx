import type { ReactNode } from 'react';

export function statusClasses(status: string) {
  switch (status) {
    case 'succeeded':
      return 'border-[hsl(var(--confirm))] bg-[hsl(var(--confirm-soft))] text-[hsl(var(--confirm))]';
    case 'failed':
      return 'border-[hsl(var(--danger))] bg-[hsl(var(--danger-soft))] text-[hsl(var(--danger))]';
    case 'running':
      return 'border-[hsl(var(--pulse))] bg-[hsl(var(--pulse-soft))] text-[hsl(var(--pulse))]';
    case 'queued':
      return 'border-[hsl(var(--border))] bg-[hsl(var(--panel-soft))] text-[hsl(var(--muted))]';
    case 'canceled':
      return 'border-[hsl(var(--faint))] bg-[hsl(var(--panel-soft))] text-[hsl(var(--muted))]';
    case 'paused':
    case 'timed_out':
      return 'border-[hsl(var(--warn))] bg-[hsl(var(--warn-soft))] text-[hsl(var(--warn))]';
    case 'seeded':
      return 'border-[hsl(var(--signal))] bg-[hsl(var(--signal-soft))] text-[hsl(var(--signal))]';
    default:
      return 'border-[hsl(var(--border))] bg-[hsl(var(--panel-soft))] text-[hsl(var(--ink))]';
  }
}

export function formatTime(value?: string) {
  if (!value) {
    return 'Not scheduled';
  }

  return new Intl.DateTimeFormat(undefined, {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  }).format(new Date(value));
}

export function HudPanel({ className = '', children }: { className?: string; children: ReactNode }) {
  return <article className={`hud-panel ${className}`.trim()}>{children}</article>;
}

export function HudPanelStrong({ className = '', children }: { className?: string; children: ReactNode }) {
  return <article className={`hud-panel-strong ${className}`.trim()}>{children}</article>;
}

export function HudLabel({ className = '', children }: { className?: string; children: ReactNode }) {
  return <p className={`hud-label ${className}`.trim()}>{children}</p>;
}

export function HudChip({ className = '', children, ...props }: React.ButtonHTMLAttributes<HTMLButtonElement> & { className?: string }) {
  return (
    <button className={`hud-chip ${className}`.trim()} {...props}>
      {children}
    </button>
  );
}

export function HudTag({ className = '', children }: { className?: string; children: ReactNode }) {
  return <span className={`hud-chip ${className}`.trim()}>{children}</span>;
}

export function HudLed({ className = '' }: { className?: string }) {
  return <span aria-hidden="true" className={`hud-led ${className}`.trim()} />;
}

export function HudField({ label, value, className = '' }: { label: string; value: ReactNode; className?: string }) {
  return (
    <div className={`hud-panel bg-[hsl(var(--panel-soft))] px-3.5 py-3 ${className}`.trim()}>
      <div className="hud-label text-[hsl(var(--faint))]">{label}</div>
      <div className="mt-2 break-words text-sm font-medium text-[hsl(var(--ink))]">{value}</div>
    </div>
  );
}

export function HudEmptyState({ children, className = '' }: { children: ReactNode; className?: string }) {
  return <p className={`hud-panel bg-[hsl(var(--panel-soft))] px-4 py-5 text-center text-sm leading-6 text-[hsl(var(--muted))] ${className}`.trim()}>{children}</p>;
}

export function HudErrorState({ children, className = '', ...props }: React.HTMLAttributes<HTMLDivElement> & { children: ReactNode; className?: string }) {
  return (
    <div className={`hud-panel mb-4 flex flex-col gap-1 border-[hsl(var(--danger))] bg-[hsl(var(--danger-soft))] px-4 py-3 text-sm text-[hsl(var(--ink))] ${className}`.trim()} {...props}>
      {children}
    </div>
  );
}

export function HudLoadingState({ children, className = '' }: { children: ReactNode; className?: string }) {
  return <div className={`hud-panel bg-[hsl(var(--panel-soft))] px-4 py-3 text-sm text-[hsl(var(--muted))] ${className}`.trim()}>{children}</div>;
}

export function HudSection({
  kicker,
  title,
  note,
  actions,
  children,
  className = '',
}: {
  kicker: string;
  title: string;
  note?: string;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={`hud-panel-strong p-4 sm:p-5 ${className}`.trim()}>
      <div className="flex flex-wrap items-start justify-between gap-4 border-b border-[hsl(var(--border-faint))] pb-4">
        <div>
          <HudLabel className="text-[hsl(var(--signal))]">{kicker}</HudLabel>
          <h2 className="mt-2 text-lg font-semibold tracking-[-0.02em] text-[hsl(var(--ink))] sm:text-xl">{title}</h2>
          {note ? <p className="mt-2 max-w-3xl text-sm leading-6 text-[hsl(var(--muted))]">{note}</p> : null}
        </div>
        {actions ? <div className="flex flex-wrap gap-2">{actions}</div> : null}
      </div>
      <div className="mt-4">{children}</div>
    </section>
  );
}
