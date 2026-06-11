const workflows = [
  {
    name: 'Open Pilot',
    description: 'Guided execution for high-signal tasks with operator visibility.',
    tone: 'Ready',
  },
  {
    name: 'Changemaker',
    description: 'Structured change flows with reviewable handoffs and traceable intent.',
    tone: 'Queued',
  },
  {
    name: 'Devlog',
    description: 'Capture decisions, progress, and follow-ups as work moves.',
    tone: 'Streaming',
  },
] as const;

const triggers = ['Manual', 'Cron', 'Gitea poll'] as const;

const runs = [
  { label: 'Active run', value: '12', detail: '2 awaiting approval' },
  { label: 'Healthy', value: '98%', detail: 'Last 24 hours' },
  { label: 'Blocked', value: '1', detail: 'Needs input' },
] as const;

const sessions = [
  { label: 'Sessions', value: '34', detail: 'Live orchestration contexts' },
  { label: 'Queues', value: '7', detail: 'Work waiting to route' },
  { label: 'Adapters', value: '5', detail: 'Bridge layers online' },
] as const;

export default function App() {
  return (
    <main className="min-h-screen bg-[radial-gradient(circle_at_top,_rgba(217,70,239,0.18),_transparent_36%),linear-gradient(180deg,#09090b_0%,#111113_52%,#09090b_100%)] px-6 py-8 text-zinc-100 sm:px-8 lg:px-12">
      <section className="mx-auto flex min-h-[calc(100vh-4rem)] max-w-7xl flex-col gap-6 overflow-hidden rounded-[2rem] border border-white/10 bg-zinc-950/80 p-6 shadow-[0_20px_80px_rgba(0,0,0,0.45)] backdrop-blur md:p-8">
        <header className="flex flex-col gap-6 border-b border-white/10 pb-6 lg:flex-row lg:items-end lg:justify-between">
          <div className="max-w-3xl">
            <p className="text-xs uppercase tracking-[0.38em] text-fuchsia-300/90">Switchyard control-plane suite</p>
            <h1 className="mt-3 text-4xl font-semibold tracking-tight text-white sm:text-5xl">
              Orchestrate workflows, triggers, and runs from one polished command surface.
            </h1>
            <p className="mt-4 max-w-2xl text-sm leading-6 text-zinc-300 sm:text-base">
              Switchyard routes work across operator-facing workflows, scheduled entry points, and live execution
              state so teams can see what is moving, what is blocked, and what is next.
            </p>
          </div>

          <div className="rounded-2xl border border-fuchsia-400/20 bg-fuchsia-400/10 px-4 py-3 text-sm text-fuchsia-100 shadow-lg shadow-fuchsia-950/20">
            <div className="text-[11px] uppercase tracking-[0.28em] text-fuchsia-200/75">Architecture</div>
            <div className="mt-2 font-medium text-white">UI → Switchyard → llama-line → OCQ → Blacktower</div>
          </div>
        </header>

        <div className="grid gap-6 lg:grid-cols-[1.45fr_0.95fr]">
          <section className="grid gap-6">
            <article className="rounded-3xl border border-white/10 bg-white/5 p-5 shadow-[inset_0_1px_0_rgba(255,255,255,0.04)]">
              <div className="flex items-center justify-between gap-4">
                <div>
                  <p className="text-xs uppercase tracking-[0.3em] text-zinc-400">Workflows</p>
                  <h2 className="mt-2 text-xl font-semibold text-white">Operator lanes</h2>
                </div>
                <span className="rounded-full border border-emerald-400/20 bg-emerald-400/10 px-3 py-1 text-xs font-medium text-emerald-200">
                  3 active lanes
                </span>
              </div>

              <div className="mt-5 grid gap-4 md:grid-cols-3">
                {workflows.map((workflow) => (
                  <div key={workflow.name} className="rounded-2xl border border-white/10 bg-zinc-900/70 p-4">
                    <div className="flex items-center justify-between gap-3">
                      <h3 className="font-semibold text-white">{workflow.name}</h3>
                      <span className="rounded-full border border-white/10 bg-white/5 px-2.5 py-1 text-[11px] uppercase tracking-[0.24em] text-zinc-300">
                        {workflow.tone}
                      </span>
                    </div>
                    <p className="mt-3 text-sm leading-6 text-zinc-300">{workflow.description}</p>
                  </div>
                ))}
              </div>
            </article>

            <article className="grid gap-6 md:grid-cols-[1.05fr_0.95fr]">
              <div className="rounded-3xl border border-white/10 bg-zinc-900/70 p-5">
                <p className="text-xs uppercase tracking-[0.3em] text-zinc-400">Triggers</p>
                <h2 className="mt-2 text-xl font-semibold text-white">Entry points</h2>
                <div className="mt-5 flex flex-wrap gap-3">
                  {triggers.map((trigger) => (
                    <span
                      key={trigger}
                      className="rounded-full border border-cyan-400/20 bg-cyan-400/10 px-3 py-2 text-sm text-cyan-100"
                    >
                      {trigger}
                    </span>
                  ))}
                </div>
                <p className="mt-4 text-sm leading-6 text-zinc-300">
                  Manual dispatch, scheduled cadence, and repository polling all enter the same routing surface.
                </p>
              </div>

              <div className="rounded-3xl border border-white/10 bg-zinc-900/70 p-5">
                <p className="text-xs uppercase tracking-[0.3em] text-zinc-400">Runs / status</p>
                <h2 className="mt-2 text-xl font-semibold text-white">Live posture</h2>
                <div className="mt-5 space-y-3">
                  {runs.map((run) => (
                    <div key={run.label} className="flex items-center justify-between gap-4 rounded-2xl border border-white/10 bg-black/20 px-4 py-3">
                      <div>
                        <div className="text-sm font-medium text-white">{run.label}</div>
                        <div className="text-xs text-zinc-400">{run.detail}</div>
                      </div>
                      <div className="text-2xl font-semibold tracking-tight text-fuchsia-200">{run.value}</div>
                    </div>
                  ))}
                </div>
              </div>
            </article>
          </section>

          <aside className="grid gap-6">
            <article className="rounded-3xl border border-white/10 bg-zinc-900/70 p-5">
              <p className="text-xs uppercase tracking-[0.3em] text-zinc-400">Sessions / queues / adapters</p>
              <h2 className="mt-2 text-xl font-semibold text-white">Control surface health</h2>
              <div className="mt-5 grid gap-4">
                {sessions.map((item) => (
                  <div key={item.label} className="rounded-2xl border border-white/10 bg-white/5 p-4">
                    <div className="flex items-end justify-between gap-4">
                      <div>
                        <div className="text-sm text-zinc-300">{item.label}</div>
                        <div className="mt-1 text-xs text-zinc-500">{item.detail}</div>
                      </div>
                      <div className="text-3xl font-semibold text-white">{item.value}</div>
                    </div>
                  </div>
                ))}
              </div>
            </article>

            <article className="rounded-3xl border border-fuchsia-400/15 bg-gradient-to-b from-fuchsia-400/10 to-transparent p-5">
              <p className="text-xs uppercase tracking-[0.3em] text-fuchsia-200/80">Routing model</p>
              <div className="mt-4 flex flex-col gap-3 text-sm text-zinc-200">
                <div className="rounded-2xl border border-white/10 bg-black/20 px-4 py-3">UI</div>
                <div className="pl-4 text-zinc-500">↓</div>
                <div className="rounded-2xl border border-white/10 bg-black/20 px-4 py-3">Switchyard</div>
                <div className="pl-4 text-zinc-500">↓</div>
                <div className="rounded-2xl border border-white/10 bg-black/20 px-4 py-3">llama-line</div>
                <div className="pl-4 text-zinc-500">↓</div>
                <div className="rounded-2xl border border-white/10 bg-black/20 px-4 py-3">OCQ</div>
                <div className="pl-4 text-zinc-500">↓</div>
                <div className="rounded-2xl border border-white/10 bg-black/20 px-4 py-3">Blacktower</div>
              </div>
            </article>
          </aside>
        </div>
      </section>
    </main>
  );
}
