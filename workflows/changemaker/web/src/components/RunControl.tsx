import { useEffect, useMemo, useState } from 'react';
import { ApiError, type ApiClient } from '../api';
import type { RunRequest, RunStatus } from '../types';
import { Button, CodeBlock, Input, MetaCard, MicroLabel, NoticeBanner, Panel, PanelHeader, SectionIntro } from './ui';

type RunControlProps = {
  api: ApiClient | null;
  visible: boolean;
  onUnauthorized: () => void;
};

export function RunControl({ api, visible, onUnauthorized }: RunControlProps) {
  const [status, setStatus] = useState<RunStatus | null>(null);
  const [loading, setLoading] = useState(false);
  const [starting, setStarting] = useState(false);
  const [kind, setKind] = useState<RunRequest['kind']>('daily');
  const [since, setSince] = useState('');
  const [until, setUntil] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const isRunning = status?.state === 'running';

  const requestPayload = useMemo<RunRequest>(() => {
    if (kind === 'manual') {
      return {
        kind,
        since: since ? new Date(since).toISOString() : undefined,
        until: until ? new Date(until).toISOString() : undefined,
      };
    }

    return { kind };
  }, [kind, since, until]);

  useEffect(() => {
    if (!visible || !api) return;

    let cancelled = false;
    let interval = 0;

    const refresh = () => {
      setLoading(true);
      void api
        .readRunStatus()
        .then((next) => {
          if (!cancelled) {
            setStatus(next);
            setError(null);
          }
        })
        .catch((err: unknown) => {
          if (cancelled) return;
          if (err instanceof ApiError && err.status === 401) {
            onUnauthorized();
            return;
          }
          setError(err instanceof Error ? err.message : 'Failed to load run status');
        })
        .finally(() => {
          if (!cancelled) setLoading(false);
        });
    };

    refresh();
    interval = window.setInterval(refresh, 4000);

    return () => {
      cancelled = true;
      window.clearInterval(interval);
    };
  }, [api, onUnauthorized, visible]);

  async function startRun() {
    if (!api) return;

    setStarting(true);
    setError(null);
    setNotice(null);

    try {
      const next = await api.startRun(requestPayload);
      setStatus(next);
      setNotice('Run started.');
    } catch (err: unknown) {
      if (err instanceof ApiError && err.status === 401) {
        onUnauthorized();
        return;
      }
      setError(err instanceof Error ? err.message : 'Failed to start run');
    } finally {
      setStarting(false);
    }
  }

  return (
    <section id="admin-tabpanel-runs" role="tabpanel" aria-labelledby="admin-tab-runs" className={visible ? 'block' : 'hidden'} hidden={!visible}>
      <SectionIntro label="Runs" title="Trigger generation and watch the log tail" note="Live status refreshes while you wait. Manual runs can carry optional since/until windows." />

      {error ? <NoticeBanner tone="alert">{error}</NoticeBanner> : null}
      {notice ? <NoticeBanner tone="confirm">{notice}</NoticeBanner> : null}

      <div className="grid gap-4 xl:grid-cols-[0.9fr_1.1fr]">
        <Panel className="p-5 sm:p-7">
          <PanelHeader label="Trigger" note="Choose the run type and start it manually." action={loading ? <span className="text-2xs uppercase tracking-[0.12em] text-ink-mute">Refreshing…</span> : null} />

          <div className="mt-5 flex border border-border-faint bg-void-800 p-1">
            {(['daily', 'manual'] as const).map((option) => (
              <button
                key={option}
                type="button"
                onClick={() => setKind(option)}
                className={`flex-1 border px-4 py-2 text-sm font-bold uppercase tracking-[0.12em] transition duration-100 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-pulse ${
                  kind === option ? 'border-pulse bg-panel-raised text-pulse' : 'border-transparent text-ink-dim hover:border-border-faint hover:text-ink'
                }`}
              >
                {option === 'daily' ? 'Daily run' : 'Manual run'}
              </button>
            ))}
          </div>

          {kind === 'manual' ? (
            <div className="mt-5 grid gap-4">
              <label className="block">
                <span className="field-label">Since</span>
                <Input type="datetime-local" value={since} onChange={(event) => setSince(event.target.value)} className="mt-2" />
              </label>
              <label className="block">
                <span className="field-label">Until</span>
                <Input type="datetime-local" value={until} onChange={(event) => setUntil(event.target.value)} className="mt-2" />
              </label>
              <p className="text-xs leading-5 text-ink-dim">Both fields are optional; blanks let the backend pick defaults.</p>
            </div>
          ) : null}

          <Button
            type="button"
            onClick={startRun}
            disabled={starting}
            variant="primary"
            className="mt-6"
          >
            {starting ? 'Starting…' : 'Start run'}
          </Button>
        </Panel>

        <Panel className="p-5 sm:p-7">
          <PanelHeader label="Status" title={status?.state ?? 'idle'} action={status?.request ? <MicroLabel>{status.request.kind} run</MicroLabel> : null} />

          <div className="mt-5 grid gap-3 sm:grid-cols-2">
            <MetaCard label="Started" value={formatDate(status?.startedAt)} />
            <MetaCard label="Finished" value={formatDate(status?.finishedAt)} />
          </div>

          {status?.error ? <NoticeBanner tone="alert" className="mt-4">{status.error}</NoticeBanner> : null}

          <CodeBlock className="mt-5" label="Log tail" value={(status?.logs.length ? status.logs : ['No log lines yet.']).join('\n')} />

          <div className="mt-4 grid gap-3 sm:grid-cols-2">
            <MetaCard label="Current kind" value={kind} />
            <MetaCard label="Running" value={isRunning ? 'yes' : 'no'} />
          </div>
        </Panel>
      </div>
    </section>
  );
}

function formatDate(value: string | undefined): string {
  if (!value) return '—';
  return new Date(value).toLocaleString();
}
