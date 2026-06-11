import type { ButtonHTMLAttributes, InputHTMLAttributes, ReactNode, TextareaHTMLAttributes } from 'react';

export function cn(...values: Array<string | false | null | undefined>): string {
  return values.filter(Boolean).join(' ');
}

type PanelProps = {
  children: ReactNode;
  className?: string;
  as?: 'div' | 'article' | 'aside' | 'section';
};

export function Panel({ children, className, as: Element = 'div' }: PanelProps) {
  return <Element className={cn('hud-panel hud-bracket', className)}>{children}</Element>;
}

export function PanelHeader({ label, title, note, action, className }: { label: string; title?: string; note?: string; action?: ReactNode; className?: string }) {
  return (
    <div className={cn('border-b border-border-faint pb-3', className)}>
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0">
          <MicroLabel tone="signal">{label}</MicroLabel>
          {title ? <h3 className="mt-2 text-xl font-extrabold tracking-[-0.02em] text-ink">{title}</h3> : null}
          {note ? <p className="mt-2 text-sm leading-6 text-ink-dim">{note}</p> : null}
        </div>
        {action ? <div className="shrink-0">{action}</div> : null}
      </div>
    </div>
  );
}

export function SectionIntro({ label, title, note, meta }: { label: string; title: string; note?: string; meta?: ReactNode }) {
  return (
    <div className="mb-4 flex flex-col gap-2 md:flex-row md:items-end md:justify-between">
      <div>
        <MicroLabel tone="signal">{label}</MicroLabel>
        <h2 className="mt-2 text-2xl font-extrabold tracking-[-0.02em] text-ink sm:text-3xl">{title}</h2>
      </div>
      {note || meta ? <div className="max-w-xl text-sm leading-6 text-ink-dim">{meta ?? note}</div> : null}
    </div>
  );
}

export type Tone = 'signal' | 'pulse' | 'alert' | 'caution' | 'confirm' | 'dead' | 'muted';

const toneText: Record<Tone, string> = {
  signal: 'text-signal',
  pulse: 'text-pulse',
  alert: 'text-alert',
  caution: 'text-caution',
  confirm: 'text-confirm',
  dead: 'text-dead',
  muted: 'text-ink-mute',
};

const toneBorder: Record<Tone, string> = {
  signal: 'border-signal text-signal bg-signal/10',
  pulse: 'border-pulse text-pulse bg-pulse/10',
  alert: 'border-alert text-alert bg-alert/10',
  caution: 'border-caution text-caution bg-caution/10',
  confirm: 'border-confirm text-confirm bg-confirm/10',
  dead: 'border-dead text-dead bg-dead/10',
  muted: 'border-border text-ink-dim bg-void-800',
};

export function MicroLabel({ children, tone = 'muted', className }: { children: ReactNode; tone?: Tone; className?: string }) {
  return <p className={cn('text-2xs font-bold uppercase tracking-[0.14em]', toneText[tone], className)}>{children}</p>;
}

export function Badge({ children, tone = 'muted', className }: { children: ReactNode; tone?: Tone; className?: string }) {
  return <span className={cn('hud-badge', toneBorder[tone], className)}>{children}</span>;
}

export function StatusLed({ state = 'dead', className }: { state?: 'live' | 'sync' | 'ok' | 'warn' | 'dead'; className?: string }) {
  const variant = state === 'dead' ? '' : `hud-led-${state}`;
  return <span aria-hidden="true" className={cn('hud-led', variant, className)} />;
}

type ButtonVariant = 'primary' | 'secondary' | 'destructive' | 'ghost';

const buttonVariants: Record<ButtonVariant, string> = {
  primary: 'border-pulse bg-pulse/15 text-pulse hover:bg-pulse/25',
  secondary: 'border-border-faint bg-panel text-ink-dim hover:border-pulse hover:bg-panel-raised hover:text-ink',
  destructive: 'border-alert bg-alert/15 text-alert hover:bg-alert/25',
  ghost: 'border-transparent bg-transparent text-ink-dim hover:border-border-faint hover:bg-panel-raised hover:text-ink',
};

export function Button({ variant = 'secondary', className, ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: ButtonVariant }) {
  return (
    <button
      {...props}
      className={cn(
        'inline-flex items-center justify-center border px-3 py-2 text-sm font-bold uppercase tracking-[0.12em] transition duration-100 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-pulse disabled:cursor-not-allowed disabled:border-border-faint disabled:bg-void-800 disabled:text-ink-mute disabled:opacity-60',
        buttonVariants[variant],
        className,
      )}
    />
  );
}

export function Input({ className, ...props }: InputHTMLAttributes<HTMLInputElement>) {
  return <input {...props} className={cn('control-input', className)} />;
}

export function Textarea({ className, ...props }: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return <textarea {...props} className={cn('control-input', className)} />;
}

export function NoticeBanner({ tone, children, className }: { tone: 'alert' | 'confirm' | 'pulse' | 'caution'; children: ReactNode; className?: string }) {
  return (
    <div className={cn('mb-4 border px-4 py-3 text-sm leading-6', toneBorder[tone], className)} role={tone === 'alert' ? 'alert' : 'status'}>
      <div className="flex items-start gap-2">
        <StatusLed state={tone === 'alert' ? 'live' : tone === 'confirm' ? 'ok' : tone === 'caution' ? 'warn' : 'sync'} className="mt-2" />
        <div>{children}</div>
      </div>
    </div>
  );
}

export function TabButton({ active, label, note, className, ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { active: boolean; label: string; note?: string }) {
  return (
    <button
      {...props}
      className={cn(
        'border px-3 py-2 text-left transition duration-100 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-pulse',
        active ? 'border-pulse bg-panel-raised text-ink' : 'border-border-faint bg-panel text-ink-dim hover:border-pulse hover:bg-panel-raised hover:text-ink',
        className,
      )}
    >
      <span className="block text-sm font-bold uppercase tracking-[0.12em]">{label}</span>
      {note ? <span className="mt-1 block text-2xs uppercase tracking-[0.12em] text-ink-mute">{note}</span> : null}
    </button>
  );
}

export function SelectableCard({ active, children, className, ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { active?: boolean; children: ReactNode }) {
  return (
    <button
      {...props}
      className={cn(
        'w-full border p-3 text-left transition duration-100 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-pulse',
        active ? 'border-pulse bg-panel-raised text-ink' : 'border-border-faint bg-panel text-ink-dim hover:border-pulse hover:bg-panel-raised hover:text-ink',
        className,
      )}
    >
      {children}
    </button>
  );
}

export function EmptyState({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn('border border-dashed border-border-faint bg-void-800 p-6 text-center text-sm leading-6 text-ink-mute', className)}>{children}</div>;
}

export function MetaCard({ label, value, tone = 'muted', className }: { label: string; value: ReactNode; tone?: Tone; className?: string }) {
  return (
    <div className={cn('border border-border-faint bg-void-800 px-3 py-2', className)}>
      <MicroLabel tone={tone}>{label}</MicroLabel>
      <div className="mt-2 text-sm font-semibold text-ink">{value}</div>
    </div>
  );
}

export function CodeBlock({ label, value, tone = 'pulse', truncated, className }: { label?: string; value: string; tone?: Tone; truncated?: boolean; className?: string }) {
  return (
    <div className={cn('border border-border-faint bg-void-900 p-3', className)}>
      {label || truncated ? (
        <div className="mb-2 flex items-center justify-between gap-3">
          {label ? <MicroLabel tone={tone}>{label}</MicroLabel> : <span />}
          {truncated ? <Badge tone="caution">truncated</Badge> : null}
        </div>
      ) : null}
      <pre className="max-h-72 overflow-auto whitespace-pre-wrap break-words text-sm leading-6 text-ink-dim">
        <code>{value || '—'}</code>
      </pre>
    </div>
  );
}
