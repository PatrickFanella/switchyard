import type { ReactNode } from 'react';
import { HudErrorState, HudLed, HudLabel, HudPanelStrong, HudTag } from './components/hud';
import { FeaturePage } from './components/FeaturePage';
import { ObservabilityPage } from './components/ObservabilityPage';
import { OverviewDashboard } from './components/OverviewDashboard';
import { apiBase } from './api';
import { useDashboardData } from './hooks/useDashboardData';
import { useHashNavigation } from './hooks/useHashNavigation';
import { views } from './types';

export { openPilotStateFields } from './types';

function ShellChip({ children }: { children: ReactNode }) {
  return <HudTag className="bg-[hsl(var(--panel-strong))] px-3 py-2 text-[0.62rem] text-[hsl(var(--ink))]">{children}</HudTag>;
}

export default function App() {
  const { activeView, selectedRunID, setActiveView, setSelectedRunID } = useHashNavigation();
  const data = useDashboardData({ activeView, selectedRunID, setSelectedRunID });

  return (
    <main className="min-h-screen bg-[hsl(var(--void))] px-4 py-4 text-[hsl(var(--ink))] sm:px-6 sm:py-6 lg:px-8">
      <section className="mx-auto flex min-h-[calc(100vh-2rem)] max-w-7xl flex-col gap-4 border-[3px] border-[hsl(var(--border))] bg-[hsl(var(--panel))] p-4 shadow-[8px_8px_0_hsl(var(--shadow))] sm:min-h-[calc(100vh-3rem)] sm:p-6 lg:p-8">
        <header className="flex flex-col gap-4 border-b-2 border-[hsl(var(--border-faint))] pb-4 lg:flex-row lg:items-end lg:justify-between">
          <div className="max-w-3xl space-y-4">
            <div className="flex flex-wrap gap-2 text-[hsl(var(--muted))]">
              <ShellChip>
                <HudLed className="bg-[hsl(var(--signal))]" />
                Switchyard control-plane suite
              </ShellChip>
              <ShellChip>API {apiBase || 'local'}</ShellChip>
              <ShellChip>
                <HudLed className={data.loading ? 'bg-[hsl(var(--warn))]' : 'bg-[hsl(var(--confirm))]'} />
                {data.loading ? 'Syncing' : data.refreshing ? 'Refreshing' : 'Live'}
              </ShellChip>
            </div>

            <div className="space-y-3">
              <HudLabel className="text-[hsl(var(--faint))]">Operator dashboard</HudLabel>
              <h1 className="max-w-4xl text-4xl font-semibold uppercase tracking-tight text-[hsl(var(--ink))] sm:text-5xl">
                Orchestrate workflows, triggers, and runs from one hard-edged command surface.
              </h1>
              <p className="max-w-2xl text-sm leading-6 text-[hsl(var(--muted))] sm:text-base">
                Switchyard routes work across operator-facing workflows, scheduled entry points, and live execution state so teams can see what is moving, what is blocked, and what is next.
              </p>
            </div>
          </div>

          <HudPanelStrong className="max-w-sm px-4 py-3">
            <HudLabel className="text-[hsl(var(--faint))]">Architecture</HudLabel>
            <div className="mt-3 text-sm font-medium uppercase tracking-[0.24em] text-[hsl(var(--ink))]">UI → Switchyard → ModelBroker → OpenCode executor → Blacktower</div>
          </HudPanelStrong>
        </header>

        {data.dashboardError ? (
          <HudErrorState role="alert">
            <HudLabel className="text-[hsl(var(--danger))]">Error</HudLabel>
            <div className="mt-2">{data.dashboardError}</div>
          </HudErrorState>
        ) : null}

        <nav className="hud-panel-strong p-3" aria-label="Switchyard UI modules">
          <div className="flex gap-2 overflow-x-auto pb-1">
            {views.map((view) => (
              <button
                key={view.id}
                aria-current={activeView === view.id ? 'page' : undefined}
                className={`hud-chip shrink-0 px-3 py-2 text-[0.62rem] transition hover:border-[hsl(var(--signal))] hover:text-[hsl(var(--signal))] ${
                  activeView === view.id ? 'border-[hsl(var(--signal))] bg-[hsl(var(--signal-soft))] text-[hsl(var(--signal))]' : 'bg-[hsl(var(--panel-soft))] text-[hsl(var(--muted))]'
                }`}
                onClick={() => setActiveView(view.id)}
                type="button"
              >
                <span>{view.label}</span>
                <span className="hidden text-[hsl(var(--faint))] sm:inline">/{view.source}</span>
              </button>
            ))}
          </div>
        </nav>

        {activeView === 'overview' ? (
          <OverviewDashboard
            dashboard={data.dashboard}
            loading={data.loading}
            openPilotIssue={data.openPilotIssue}
            openPilotReady={data.openPilotReady}
            openPilotRepo={data.openPilotRepo}
            openPilotState={data.openPilotState}
            onCancelRun={data.cancelRun}
            onRefresh={data.refresh}
            onRetryRun={data.retryRun}
            onResumeOpenPilotState={data.resumeOpenPilotState}
            onRunTrigger={data.runTrigger}
            onSelectRun={data.setSelectedRunID}
            onStepAction={data.stepAction}
            onToggleTrigger={data.toggleTrigger}
            refreshing={data.refreshing}
            runActionID={data.runActionID}
            runningTriggerID={data.runningTriggerID}
            selectedRunDetail={data.selectedRunDetail}
            selectedRunID={selectedRunID}
            setOpenPilotIssue={data.setOpenPilotIssue}
            setOpenPilotRepo={data.setOpenPilotRepo}
            workflows={data.dashboard.workflows}
            updatingTriggerID={data.updatingTriggerID}
            workflowDefinitions={data.workflowDefinitions}
          />
        ) : activeView === 'observability' ? (
          <ObservabilityPage />
        ) : (
          <FeaturePage view={activeView} adapters={data.dashboard.adapters} />
        )}
      </section>
    </main>
  );
}
