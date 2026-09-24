import type { ReactNode } from 'react';
import type { AdapterPayload, AgentProfile, ViewID } from '../types';
import { EvidenceSearchPage } from './EvidenceSearchPage';
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
              <p className="mt-4 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">Updated {formatTime(queue.updated_at)}</p>
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
                  <p className="mt-2 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">Pending {upstream.pending} · seen {formatTime(upstream.last_seen_at)}</p>
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
                  <p className="mt-2 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">{request.route} · {request.latency_ms}ms · depth {request.queue_depth}</p>
                </HudPanel>
              ))}
            </div>
          </FeatureCard>
        </div>
      </HudSection>
    );
  }

  if (view === 'sessions') {
    return (
      <HudSection kicker="OpenCode executor" title="Session console" note="Read-only OpenCode session inventory and workflow correlation. Session prompting and editing are not dashboard controls.">
        <div className="mb-4 grid gap-3 md:grid-cols-4">
          <HudField label="Workspace" value={adapters.ocq.settings.workspace || 'unknown'} />
          <HudField label="Provider" value={adapters.ocq.settings.provider || 'unknown'} />
          <HudField label="Retention" value={`${adapters.ocq.settings.retention_days}d`} />
          <HudField label="CORS" value={adapters.ocq.settings.cors_allowed.join(', ') || 'none'} />
        </div>
        {adapters.sessions.length === 0 ? <HudEmptyState>No sessions have been reported yet.</HudEmptyState> : null}
        <div className="grid gap-3">
          {adapters.sessions.map((session) => (
            <HudPanel key={session.id} className="bg-[hsl(var(--panel-strong))] p-4">
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div>
                  <HudLabel className="text-[hsl(var(--faint))]">{session.surface}</HudLabel>
                  <h3 className="mt-2 text-lg font-semibold text-[hsl(var(--ink))]">{session.name}</h3>
                  <p className="mt-2 text-xs uppercase tracking-[0.18em] text-[hsl(var(--muted))]">{session.id}</p>
                </div>
                <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(session.status)}`}>{session.status}</span>
              </div>
              <div className="mt-4 grid gap-3 sm:grid-cols-3">
                <HudField label="Workflow" value={session.workflow_id || 'unbound'} />
                <HudField label="Started" value={formatTime(session.started_at)} />
                <HudField label="Updated" value={formatTime(session.updated_at)} />
              </div>
            </HudPanel>
          ))}
        </div>
        <FeatureCard>
          <h3 className="hud-label text-[hsl(var(--signal))]">OpenCode executor requests</h3>
          <div className="mt-4 grid gap-3">
            {adapters.ocq.requests.map((request) => (
              <HudPanel key={request.id} className="bg-[hsl(var(--panel-soft))] p-3">
                <div className="flex items-center justify-between gap-3">
                  <span className="text-sm font-semibold text-[hsl(var(--ink))]">{request.workflow}</span>
                  <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(request.status)}`}>{request.status}</span>
                </div>
                <p className="mt-2 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">{request.trigger} · {request.actor} · updated {formatTime(request.updated_at)}</p>
              </HudPanel>
            ))}
          </div>
        </FeatureCard>
      </HudSection>
    );
  }

  if (view === 'logs') {
    return <EvidenceSearchPage />;
  }

  if (view === 'prompts') {
    return (
      <HudSection kicker="Workflow policy" title="Prompt contracts" note="Read-only workflow prompt inventory, variables, and generated content references. Prompt editing is managed through workflow policy, not this dashboard.">
        <div className="mb-4 grid gap-3 md:grid-cols-4">
          <HudField label="Run mode" value={adapters.changemaker.run_control.mode} />
          <HudField label="Run control" value={adapters.changemaker.run_control.enabled ? 'enabled' : 'disabled'} />
          <HudField label="Queued runs" value={adapters.changemaker.run_control.queued_runs} />
          <HudField label="Active run" value={adapters.changemaker.run_control.active_run_id || 'none'} />
        </div>
        {adapters.prompts.length === 0 ? <HudEmptyState>No prompt templates have been reported yet.</HudEmptyState> : null}
        <div className="grid gap-4 lg:grid-cols-2">
          {adapters.prompts.map((prompt) => (
            <FeatureCard key={prompt.id}>
              <HudLabel className="text-[hsl(var(--faint))]">{prompt.surface}</HudLabel>
              <h3 className="mt-2 text-lg font-semibold text-[hsl(var(--ink))]">{prompt.name}</h3>
              <p className="mt-3 text-sm leading-6 text-[hsl(var(--muted))]">{prompt.description}</p>
              <pre className="mt-4 overflow-auto border border-[hsl(var(--border-faint))] bg-[hsl(var(--void))] p-3 text-xs text-[hsl(var(--ink))]">{prompt.template}</pre>
              <p className="mt-3 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">Variables: {prompt.variables.join(', ') || 'none'}</p>
            </FeatureCard>
          ))}
        </div>
        <FeatureCard>
          <h3 className="hud-label text-[hsl(var(--signal))]">Posts</h3>
          <div className="mt-4 grid gap-3">
            {adapters.changemaker.posts.map((post) => (
              <HudPanel key={post.id} className="bg-[hsl(var(--panel-soft))] p-3">
                <div className="flex items-center justify-between gap-3">
                  <span className="text-sm font-semibold text-[hsl(var(--ink))]">{post.title}</span>
                  <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(post.status)}`}>{post.status}</span>
                </div>
                <p className="mt-2 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">{post.tags.join(', ') || 'untagged'} · {formatTime(post.published_at)}</p>
              </HudPanel>
            ))}
          </div>
        </FeatureCard>
      </HudSection>
    );
  }

  if (view === 'config') {
    return (
      <HudSection kicker="Workflow policy / broker" title="Configuration matrix" note="Read-only runtime configuration, feature flags, limits, and module health. Secrets and runtime edits are intentionally excluded.">
        <div className="grid gap-3 md:grid-cols-3">
          <HudField label="Environment" value={adapters.config.environment} />
          <HudField label="Version" value={adapters.config.version} />
          <HudField label="Base URL" value={adapters.config.base_url || 'not configured'} />
          <HudField label="Workflow policy" value={`${adapters.changemaker.service.status} ${adapters.changemaker.service.version}`} />
          <HudField label="Health" value={adapters.health.status} />
          <HudField label="Checked" value={formatTime(adapters.health.checked_at)} />
        </div>
        <div className="mt-4 grid gap-4 lg:grid-cols-2">
          <FeatureCard>
            <h3 className="hud-label text-[hsl(var(--signal))]">Features</h3>
            <div className="mt-4 flex flex-wrap gap-2">
              {adapters.config.features.map((feature) => (
                <span key={feature} className="hud-chip bg-[hsl(var(--panel-soft))] px-2.5 py-1 text-[0.62rem] text-[hsl(var(--ink))]">
                  {feature}
                </span>
              ))}
            </div>
          </FeatureCard>
          <FeatureCard>
            <h3 className="hud-label text-[hsl(var(--signal))]">Limits</h3>
            <div className="mt-4 grid gap-2">
              {Object.entries(adapters.config.limits).map(([key, value]) => (
                <HudField key={key} label={key} value={value} />
              ))}
            </div>
          </FeatureCard>
        </div>
        <FeatureCard>
          <h3 className="hud-label text-[hsl(var(--signal))]">Module health</h3>
          <div className="mt-4 grid gap-3 md:grid-cols-2">
            {adapters.health.services.map((service) => (
              <HudPanel key={service.name} className="bg-[hsl(var(--panel-soft))] p-3">
                <div className="flex items-center justify-between gap-3">
                  <span className="text-sm font-semibold text-[hsl(var(--ink))]">{service.name}</span>
                  <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(service.status)}`}>{service.status}</span>
                </div>
                <p className="mt-2 text-xs text-[hsl(var(--muted))]">{service.details || 'No details'}</p>
              </HudPanel>
            ))}
          </div>
        </FeatureCard>
      </HudSection>
    );
  }

  if (view === 'agents') {
    return (
      <HudSection kicker="Native profiles" title="Agent profiles" note="Control-plane profile contracts, default models, and allowed executors. These are read-only policy records, not adapter-reported agents.">
        {agentProfiles.length === 0 ? <HudEmptyState>No agent profiles have been returned by the control plane.</HudEmptyState> : null}
        <div className="grid gap-4 lg:grid-cols-2">
          {agentProfiles.map((profile) => (
            <FeatureCard key={profile.id}>
              <div className="flex items-start justify-between gap-3">
                <div>
                  <HudLabel className="text-[hsl(var(--faint))]">{profile.role}</HudLabel>
                  <h3 className="mt-2 text-lg font-semibold text-[hsl(var(--ink))]">{profile.name}</h3>
                </div>
                <span className="hud-chip border-[hsl(var(--confirm))] bg-[hsl(var(--confirm-soft))] px-2.5 py-1 text-[0.62rem] text-[hsl(var(--confirm))]">native</span>
              </div>
              <HudField className="mt-4" label="Default model" value={profile.default_model || 'not configured'} />
              <p className="mt-4 text-sm leading-6 text-[hsl(var(--muted))]">{profile.prompt_contract || 'No prompt contract recorded.'}</p>
              <p className="mt-4 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">Allowed executors: {profile.allowed_executors.join(', ') || 'none'}</p>
            </FeatureCard>
          ))}
        </div>
      </HudSection>
    );
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
                <span className="text-xs uppercase tracking-[0.18em] text-[hsl(var(--signal))]">{profile.role}</span>
              </div>
              <p className="mt-2 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">{profile.allowed_executors.join(', ') || 'no executors declared'}</p>
            </HudPanel>
          ))}
        </div>
      </FeatureCard>
    </HudSection>
  );
}
