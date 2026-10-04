import { useEffect, useState } from 'react';
import { loadModelBrokerObservability, messageFrom } from '../api';
import type { ModelBrokerObservability } from '../types';
import { formatTime, HudEmptyState, HudField, HudLabel, HudPanel, HudPanelStrong, HudSection, statusClasses } from './hud';

export function ObservabilityPage() {
  const [data, setData] = useState<ModelBrokerObservability | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let alive = true;
    loadModelBrokerObservability()
      .then((next) => {
        if (alive) {
          setData(next);
          setError('');
        }
      })
      .catch((err: unknown) => {
        if (alive) {
          setError(messageFrom(err));
        }
      })
      .finally(() => {
        if (alive) {
          setLoading(false);
        }
      });
    return () => {
      alive = false;
    };
  }, []);

  return (
    <HudSection kicker="Observability" title="Model broker signal" note="Durable request history, upstream status, and trace correlation for the llama-line-compatible ingress.">
      {loading ? <HudEmptyState>Loading broker observability…</HudEmptyState> : null}
      {error ? <HudPanelStrong className="border-[hsl(var(--danger))] p-4 text-sm text-[hsl(var(--danger))]">{error}</HudPanelStrong> : null}
      {data ? (
        <>
          <div className="grid gap-3 md:grid-cols-4">
            <HudField label="Broker" value={data.status.status} />
            <HudField label="Backend" value={data.status.backend} />
            <HudField label="Avg latency" value={`${data.latency_ms.avg}ms`} />
            <HudField label="Failures" value={data.totals.failed ?? 0} />
          </div>

          <div className="mt-4 grid gap-4 lg:grid-cols-2">
            <HudPanelStrong className="p-4">
              <HudLabel className="text-[hsl(var(--signal))]">Upstreams</HudLabel>
              <div className="mt-4 grid gap-3">
                {data.status.upstreams.map((upstream) => (
                  <HudPanel key={upstream.name} className="bg-[hsl(var(--panel-soft))] p-3">
                    <div className="flex flex-wrap items-center justify-between gap-3">
                      <span className="text-sm font-semibold text-[hsl(var(--ink))]">{upstream.name}</span>
                      <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(upstream.status)}`}>{upstream.status}</span>
                    </div>
                    <p className="mt-2 break-all text-xs text-[hsl(var(--muted))]">{upstream.url}</p>
                  </HudPanel>
                ))}
              </div>
            </HudPanelStrong>

            <HudPanelStrong className="p-4">
              <HudLabel className="text-[hsl(var(--signal))]">Request totals</HudLabel>
              <div className="mt-4 grid gap-2 sm:grid-cols-2">
                {Object.entries(data.totals).map(([status, count]) => (
                  <HudField key={status} label={status} value={count} />
                ))}
              </div>
            </HudPanelStrong>
          </div>

          <div className="mt-4 grid gap-4 lg:grid-cols-2">
            <HudPanelStrong className="p-4">
              <HudLabel className="text-[hsl(var(--signal))]">Recent requests</HudLabel>
              <div className="mt-4 grid gap-3">
                {data.recent_requests.map((request) => (
                  <HudPanel key={request.id} className="bg-[hsl(var(--panel-soft))] p-3">
                    <div className="flex flex-wrap items-center justify-between gap-3">
                      <span className="text-sm font-semibold text-[hsl(var(--ink))]">{request.model}</span>
                      <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(request.status)}`}>{request.status}</span>
                    </div>
                    <p className="mt-2 break-all text-xs text-[hsl(var(--faint))]">
                      {request.id} · {request.path} · {request.duration_ms ?? 0}ms · {formatTime(request.created_at)}
                    </p>
                    {request.correlation_ids ? <p className="mt-2 break-all text-xs text-[hsl(var(--muted))]">Trace: {request.correlation_ids.trace_id ?? request.correlation_ids.llama_line_request_id ?? 'recorded'}</p> : null}
                  </HudPanel>
                ))}
              </div>
            </HudPanelStrong>

            <HudPanelStrong className="p-4">
              <HudLabel className="text-[hsl(var(--signal))]">Recent errors</HudLabel>
              {data.recent_errors.length === 0 ? <HudEmptyState className="mt-4">No recent broker errors.</HudEmptyState> : null}
              <div className="mt-4 grid gap-3">
                {data.recent_errors.map((request) => (
                  <HudPanel key={request.id} className="bg-[hsl(var(--panel-soft))] p-3">
                    <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(request.status)}`}>{request.status}</span>
                    <p className="mt-3 text-sm text-[hsl(var(--muted))]">{request.error || 'No error text recorded'}</p>
                    <p className="mt-2 break-all text-xs text-[hsl(var(--faint))]">{request.id}</p>
                  </HudPanel>
                ))}
              </div>
            </HudPanelStrong>
          </div>
        </>
      ) : null}
    </HudSection>
  );
}
