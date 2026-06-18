import type { ReactNode } from 'react';
import type { AdapterPayload, ViewID } from '../types';
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

export function FeaturePage({ view, adapters }: { view: ViewID; adapters: AdapterPayload }) {
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
      <HudSection kicker="OpenCode executor" title="Session console" note="OpenCode executor/Blacktower session list, transcript targets, and future send-prompt controls.">
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
    return (
      <HudSection kicker="ModelBroker / OpenCode executor" title="Logs and search" note="Unified request, log, audit, and semantic-search surface for native modules.">
        <FeatureCard>
          <h3 className="hud-label text-[hsl(var(--signal))]">Search hits</h3>
          <div className="mt-4 grid gap-3">
            {adapters.llama_line.search.map((hit) => (
              <HudPanel key={`${hit.source}-${hit.query}`} className="bg-[hsl(var(--panel-soft))] p-3">
                <div className="flex items-center justify-between gap-3">
                  <span className="text-sm font-semibold text-[hsl(var(--ink))]">{hit.match}</span>
                  <span className="hud-chip bg-[hsl(var(--pulse-soft))] px-2.5 py-1 text-[0.62rem] text-[hsl(var(--pulse))]">{Math.round(hit.score * 100)}%</span>
                </div>
                <p className="mt-2 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">{hit.query} · {hit.source}</p>
              </HudPanel>
            ))}
          </div>
        </FeatureCard>
        {adapters.logs.length === 0 ? <HudEmptyState>No logs are available yet.</HudEmptyState> : null}
        <div className="grid gap-3" role="log" aria-live="polite">
          {[...adapters.logs, ...adapters.ocq.logs.map((log) => ({ ...log, source: `ocq:${log.channel}` }))].map((log) => (
            <HudPanel key={log.id} className="bg-[hsl(var(--panel-strong))] px-4 py-3">
              <div className="flex flex-wrap items-center justify-between gap-3">
                <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(log.level)}`}>{log.level}</span>
                <span className="text-[0.65rem] uppercase tracking-[0.18em] text-[hsl(var(--faint))]">{log.source} · {formatTime(log.created_at)}</span>
              </div>
              <p className="mt-3 text-sm leading-6 text-[hsl(var(--muted))]">{log.message}</p>
            </HudPanel>
          ))}
        </div>
      </HudSection>
    );
  }

  if (view === 'prompts') {
    return (
      <HudSection kicker="Workflow policy" title="Prompt workbench" note="Editable workflow prompt inventory, variables, and future review controls.">
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
      <HudSection kicker="Workflow policy / broker" title="Configuration matrix" note="Runtime config, feature flags, limits, and future adapter edit surfaces.">
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
      <HudSection kicker="Blacktower" title="Agent board" note="Agent roles, model bindings, board controls, and capability inventory.">
        {adapters.agents.length === 0 ? <HudEmptyState>No agents have been reported yet.</HudEmptyState> : null}
        <div className="grid gap-4 lg:grid-cols-2">
          {adapters.agents.map((agent) => (
            <FeatureCard key={agent.id}>
              <div className="flex items-start justify-between gap-3">
                <div>
                  <HudLabel className="text-[hsl(var(--faint))]">{agent.role}</HudLabel>
                  <h3 className="mt-2 text-lg font-semibold text-[hsl(var(--ink))]">{agent.name}</h3>
                </div>
                <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(agent.status)}`}>{agent.status}</span>
              </div>
              <HudField className="mt-4" label="Model" value={agent.model} />
              <p className="mt-4 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">Capabilities: {agent.capabilities.join(', ') || 'none'}</p>
            </FeatureCard>
          ))}
        </div>
        <FeatureCard>
          <h3 className="hud-label text-[hsl(var(--signal))]">Control-center tasks</h3>
          <div className="mt-4 grid gap-3">
            {adapters.blacktower.tasks.map((task) => (
              <HudPanel key={task.id} className="bg-[hsl(var(--panel-soft))] p-3">
                <div className="flex items-center justify-between gap-3">
                  <span className="text-sm font-semibold text-[hsl(var(--ink))]">{task.name}</span>
                  <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(task.status)}`}>{task.progress}%</span>
                </div>
                <p className="mt-2 text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">{task.owner} · {formatTime(task.updated_at)}</p>
              </HudPanel>
            ))}
          </div>
        </FeatureCard>
      </HudSection>
    );
  }

  return (
    <HudSection kicker="Blacktower" title="Skill registry" note="Installed skills, categories, status, and future editing/sync controls.">
      <div className="mb-4 grid gap-3 md:grid-cols-4">
        <HudField label="Scheduler" value={adapters.blacktower.scheduler_health.status} />
        <HudField label="Next run" value={formatTime(adapters.blacktower.scheduler_health.next_run_at)} />
        <HudField label="Last tick" value={formatTime(adapters.blacktower.scheduler_health.last_tick_at)} />
        <HudField label="Skew" value={`${adapters.blacktower.scheduler_health.skew_seconds}s`} />
      </div>
      {adapters.skills.length === 0 ? <HudEmptyState>No skills have been reported yet.</HudEmptyState> : null}
      <div className="grid gap-4 lg:grid-cols-2">
        {adapters.skills.map((skill) => (
          <FeatureCard key={skill.id}>
            <div className="flex items-start justify-between gap-3">
              <div>
                <HudLabel className="text-[hsl(var(--faint))]">{skill.category}</HudLabel>
                <h3 className="mt-2 text-lg font-semibold text-[hsl(var(--ink))]">{skill.name}</h3>
              </div>
              <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(skill.status)}`}>{skill.status}</span>
            </div>
            <p className="mt-3 text-sm leading-6 text-[hsl(var(--muted))]">{skill.description}</p>
          </FeatureCard>
        ))}
      </div>
      <FeatureCard>
        <h3 className="hud-label text-[hsl(var(--signal))]">Reports</h3>
        <div className="mt-4 grid gap-3">
          {adapters.blacktower.reports.map((report) => (
            <HudPanel key={report.id} className="bg-[hsl(var(--panel-soft))] p-3">
              <div className="flex items-center justify-between gap-3">
                <span className="text-sm font-semibold text-[hsl(var(--ink))]">{report.title}</span>
                <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(report.status)}`}>{report.type}</span>
              </div>
              <ul className="mt-3 list-inside list-disc text-sm leading-6 text-[hsl(var(--muted))]">
                {report.findings.map((finding) => <li key={finding}>{finding}</li>)}
              </ul>
            </HudPanel>
          ))}
        </div>
      </FeatureCard>
    </HudSection>
  );
}
