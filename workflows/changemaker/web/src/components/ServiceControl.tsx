import { useEffect, useMemo, useState } from 'react';
import { ApiError, type ApiClient } from '../api';
import type { ServiceAction, ServiceExecutionResult } from '../types';
import { Button, CodeBlock, EmptyState, Input, MetaCard, NoticeBanner, Panel, PanelHeader, SectionIntro } from './ui';

type ServiceControlProps = {
  api: ApiClient | null;
  visible: boolean;
  onUnauthorized: () => void;
};

type ActionState = Partial<Record<Exclude<ServiceAction, 'service-status' | 'timer-status'>, string>>;

const DANGEROUS_ACTIONS: Array<{
  action: Exclude<ServiceAction, 'service-status' | 'timer-status'>;
  title: string;
  description: string;
}> = [
  { action: 'restart-service', title: 'Restart service', description: 'Reload the changemaker service unit.' },
  { action: 'restart-timer', title: 'Restart timer', description: 'Bounce the scheduled timer.' },
  { action: 'stop-timer', title: 'Stop timer', description: 'Pause automatic daily runs.' },
  { action: 'start-timer', title: 'Start timer', description: 'Resume automatic daily runs.' },
];

export function ServiceControl({ api, visible, onUnauthorized }: ServiceControlProps) {
  const [serviceStatus, setServiceStatus] = useState<ServiceExecutionResult | null>(null);
  const [timerStatus, setTimerStatus] = useState<ServiceExecutionResult | null>(null);
  const [loading, setLoading] = useState(false);
  const [workingAction, setWorkingAction] = useState<ServiceAction | null>(null);
  const [confirmations, setConfirmations] = useState<ActionState>({});
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const confirmationHint = useMemo(
    () => Object.fromEntries(DANGEROUS_ACTIONS.map((item) => [item.action, item.action])) as Record<string, string>,
    [],
  );

  useEffect(() => {
    if (!visible || !api) return;

    let cancelled = false;
    let interval = 0;

    const refresh = () => {
      setLoading(true);
      void Promise.all([api.readServiceStatus(), api.readTimerStatus()])
        .then(([service, timer]) => {
          if (cancelled) return;
          setServiceStatus(service);
          setTimerStatus(timer);
          setError(null);
        })
        .catch((err: unknown) => {
          if (cancelled) return;
          if (err instanceof ApiError && err.status === 401) {
            onUnauthorized();
            return;
          }
          setError(err instanceof Error ? err.message : 'Failed to load service status');
        })
        .finally(() => {
          if (!cancelled) setLoading(false);
        });
    };

    refresh();
    interval = window.setInterval(refresh, 15_000);

    return () => {
      cancelled = true;
      window.clearInterval(interval);
    };
  }, [api, onUnauthorized, visible]);

  async function runAction(action: Exclude<ServiceAction, 'service-status' | 'timer-status'>) {
    if (!api) return;
    if (confirmations[action] !== action) return;

    setWorkingAction(action);
    setError(null);
    setNotice(null);

    try {
      const result = await api.invokeServiceAction(action);
      if (action.includes('service')) setServiceStatus(result);
      if (action.includes('timer')) setTimerStatus(result);
      setNotice(`${action} completed.`);
    } catch (err: unknown) {
      if (err instanceof ApiError && err.status === 401) {
        onUnauthorized();
        return;
      }
      setError(err instanceof Error ? err.message : 'Failed to run service action');
    } finally {
      setWorkingAction(null);
    }
  }

  return (
    <section id="admin-tabpanel-service" role="tabpanel" aria-labelledby="admin-tab-service" className={visible ? 'block' : 'hidden'} hidden={!visible}>
      <SectionIntro label="Service" title="Systemd guardrails and status" note="Status is safe to read. Destructive actions require an exact confirmation string before the request leaves the browser." />

      {error ? <NoticeBanner tone="alert">{error}</NoticeBanner> : null}
      {notice ? <NoticeBanner tone="confirm">{notice}</NoticeBanner> : null}

      <div className="grid gap-4 xl:grid-cols-[0.9fr_1.1fr]">
        <Panel className="p-5 sm:p-7">
          <PanelHeader label="Confirmation" note="Type the exact action name to unlock the button." action={loading ? <span className="text-2xs uppercase tracking-[0.12em] text-ink-mute">Refreshing…</span> : null} />

          <div className="mt-5 grid gap-3">
            {DANGEROUS_ACTIONS.map((item) => {
              const current = confirmations[item.action] ?? '';
              const ready = current === item.action;

              return (
                <div key={item.action} className="border border-border-faint bg-panel p-4">
                  <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
                    <div>
                      <p className="text-sm font-medium text-ink">{item.title}</p>
                      <p className="text-xs leading-5 text-ink-dim">{item.description}</p>
                    </div>
                    <Button
                      type="button"
                      onClick={() => runAction(item.action)}
                      disabled={!ready || workingAction !== null}
                      variant="destructive"
                    >
                      {workingAction === item.action ? 'Working…' : item.title}
                    </Button>
                  </div>

                  <label className="mt-3 block">
                    <span className="field-label">Confirmation text</span>
                    <Input
                      value={current}
                      onChange={(event) => setConfirmations((prev) => ({ ...prev, [item.action]: event.target.value }))}
                      className="mt-2"
                      placeholder={confirmationHint[item.action]}
                    />
                  </label>
                </div>
              );
            })}
          </div>
        </Panel>

        <div className="grid gap-4">
          <StatusPanel title="Service status" result={serviceStatus} />
          <StatusPanel title="Timer status" result={timerStatus} />
        </div>
      </div>
    </section>
  );
}

function StatusPanel({ title, result }: { title: string; result: ServiceExecutionResult | null }) {
  return (
    <Panel as="article" className="p-5 sm:p-7">
      <PanelHeader
        label={title}
        note="Command output returned by the backend."
        action={
          <div className="text-right text-sm text-ink-dim">
            <p>exit {result?.exitCode ?? '—'}</p>
            <p>{result?.signal ?? 'no signal'}</p>
          </div>
        }
      />

      {result ? (
        <div className="mt-5 space-y-4">
          <MetaCard label="command" value={result.command.join(' ')} tone="pulse" />

          <OutputBlock label="stdout" value={result.stdout} truncated={result.stdoutTruncated} tone="cyan" />
          <OutputBlock label="stderr" value={result.stderr} truncated={result.stderrTruncated} tone="rose" />
        </div>
      ) : (
        <EmptyState className="mt-5">No status yet.</EmptyState>
      )}
    </Panel>
  );
}

function OutputBlock({
  label,
  value,
  truncated,
  tone,
}: {
  label: string;
  value: string;
  truncated: boolean;
  tone: 'cyan' | 'rose';
}) {
  return <CodeBlock label={label} value={value} truncated={truncated} tone={tone === 'cyan' ? 'pulse' : 'alert'} />;
}
