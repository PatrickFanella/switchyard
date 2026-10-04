import type { ReactNode } from 'react';
import type { AdapterPayload, AgentProfile, ViewID } from '../types';
import { ConfigPage } from './ConfigPage';
import { EvidenceSearchPage } from './EvidenceSearchPage';
import { ProfilesPage } from './ProfilesPage';
import { PromptsPage } from './PromptsPage';
import { SessionsPage } from './SessionsPage';
import {
  formatTime,
  statusClasses,
  HudEmptyState,
  HudField,
  HudLabel,
  HudPanel,
  HudPanelStrong,
  HudSection,
} from './hud';

function FeatureCard({ children }: { children: ReactNode }) {
  return <HudPanelStrong className="p-4">{children}</HudPanelStrong>;
}

export function FeaturePage({ view, adapters, agentProfiles }: { view: ViewID; adapters: AdapterPayload; agentProfiles: AgentProfile[] }) {
  if (view === 'queues') {
    return (
      <HudSection
        kicker="ModelBroker"
        title="Queue cockpit"
        note="Queue depth, upstream health, inflight work, and cutover ingress from the native model broker."
      >
        <div className="mb-4 grid gap-3 md:grid-cols-4">
          <HudField label="Active" value={adapters.llama_line.stats.active_requests} />
          <HudField label="Queued" value={adapters.llama_line.stats.queued_requests} />
          <HudField label="Avg latency" value={`${adapters.llama_line.stats.avg_latency_ms}ms`} />
          <HudField label="Success" value={`${Math.round(adapters.llama_line.stats.success_rate * 100)}%`} />
        </div>
        {adapters.queues.length === 0 ? <HudEmptyState>No queue adapters have reported yet.</HudEmptyState> : null}
        <div className="grid gap-4 lg:grid-cols-2">
          {adapters.queues.map((queue) => (
            <FeatureCard key={queue.id}>
              <div className="flex items-start justify-between gap-3">
                <div>
                  <HudLabel className="text-[hsl(var(--faint))]">{queue.id}</HudLabel>
                  <h3 className="mt-2 text-xl font-semibold text-[hsl(var(--ink))]">{queue.name}</h3>
                </div>
                <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(queue.status)}`}>{queue.status}</span>
              </div>
              <div className="mt-4 grid gap-3 sm:grid-cols-3">
                <HudField label="Depth" value={queue.depth} />
                <HudField label="Capacity" value={queue.capacity} />
                <HudField label="Workers" value={queue.workers} />
              </div>
              <p className="mt-4 text-xs text-[hsl(var(--faint))]">Updated {formatTime(queue.updated_at)}</p>
            </FeatureCard>
          ))}
        </div>
        <div className="mt-4 grid gap-4 lg:grid-cols-2">
          <FeatureCard>
            <h3 className="hud-label text-[hsl(var(--signal))]">Upstreams</h3>
            <div className="mt-4 grid gap-3">
              {adapters.llama_line.upstreams.map((upstream) => (
                <HudPanel key={upstream.name} className="bg-[hsl(var(--panel-soft))] p-3">
                  <div className="flex items-center justify-between gap-3">
                    <span className="text-sm font-semibold text-[hsl(var(--ink))]">{upstream.name}</span>
                    <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(upstream.status)}`}>{upstream.status}</span>
                  </div>
                  <p className="mt-2 break-all text-xs text-[hsl(var(--muted))]">{upstream.url}</p>
                  <p className="mt-2 text-xs text-[hsl(var(--faint))]">Pending {upstream.pending} · seen {formatTime(upstream.last_seen_at)}</p>
                </HudPanel>
              ))}
            </div>
          </FeatureCard>
          <FeatureCard>
            <h3 className="hud-label text-[hsl(var(--signal))]">Recent requests</h3>
            <div className="mt-4 grid gap-3">
              {adapters.llama_line.requests.map((request) => (
                <HudPanel key={request.id} className="bg-[hsl(var(--panel-soft))] p-3">
                  <div className="flex items-center justify-between gap-3">
                    <span className="text-sm font-semibold text-[hsl(var(--ink))]">{request.model}</span>
                    <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(request.status)}`}>{request.status}</span>
                  </div>
                  <p className="mt-2 text-xs text-[hsl(var(--faint))]">{request.route} · {request.latency_ms}ms · depth {request.queue_depth}</p>
                </HudPanel>
              ))}
            </div>
          </FeatureCard>
        </div>
      </HudSection>
    );
  }

  if (view === 'sessions') {
    return <SessionsPage />;
  }

  if (view === 'logs') {
    return <EvidenceSearchPage />;
  }

  if (view === 'prompts') {
    return <PromptsPage />;
  }

  if (view === 'config') {
    return <ConfigPage adapters={adapters} />;
  }

  if (view === 'agents') {
    return <ProfilesPage />;
  }

  return (
    <HudSection kicker="Native policy" title="Runtime capabilities" note="Switchyard does not install, edit, or synchronize skills from the dashboard. Agent profiles expose the executable capabilities used by workflow policy.">
      <FeatureCard>
        <h3 className="hud-label text-[hsl(var(--signal))]">Profile capabilities</h3>
        {agentProfiles.length === 0 ? <HudEmptyState>No native profiles are available to describe runtime capabilities.</HudEmptyState> : null}
        <div className="mt-4 grid gap-3">
          {agentProfiles.map((profile) => (
            <HudPanel key={profile.id} className="bg-[hsl(var(--panel-soft))] p-3">
              <div className="flex flex-wrap items-center justify-between gap-3">
                <span className="text-sm font-semibold text-[hsl(var(--ink))]">{profile.name}</span>
                <span className="text-xs text-[hsl(var(--signal))]">{profile.role}</span>
              </div>
              <p className="mt-2 text-xs text-[hsl(var(--faint))]">{profile.allowed_executors.join(', ') || 'no executors declared'}</p>
            </HudPanel>
          ))}
        </div>
      </FeatureCard>
    </HudSection>
  );
}
