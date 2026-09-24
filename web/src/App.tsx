import type { ReactNode } from 'react';
import { HudErrorState, HudLed } from './components/hud';
import { FeaturePage } from './components/FeaturePage';
import { RunCockpitPage } from './components/RunCockpitPage';
import { ObservabilityPage } from './components/ObservabilityPage';
import { OverviewDashboard } from './components/OverviewDashboard';
import { OpenPilotInboxPage } from './components/OpenPilotInboxPage';
import { QueuesPage } from './components/QueuesPage';
import { WorkflowCatalogPage } from './components/WorkflowCatalogPage';
import { apiBase } from './api';
import { useDashboardData } from './hooks/useDashboardData';
import { useHashNavigation } from './hooks/useHashNavigation';
import type { ViewID, ViewMeta } from './types';
import { views } from './types';

export { openPilotStateFields } from './types';

const navigationGroups: { label: string; items: ViewID[] }[] = [
  { label: 'Operate', items: ['overview', 'inbox', 'runs', 'queues'] },
  { label: 'Inspect', items: ['observability', 'sessions', 'logs'] },
  { label: 'Configure', items: ['workflows', 'prompts', 'agents', 'skills', 'config'] },
];

const pageCopy: Record<ViewID, { eyebrow: string; title: string; description: string }> = {
  overview: { eyebrow: 'Control plane', title: 'Operations overview', description: 'Monitor live work, dispatch workflows, and resolve exceptions.' },
  inbox: { eyebrow: 'Open Pilot', title: 'Issue inbox', description: 'Review intake candidates, eligibility, and lifecycle claims before dispatch.' },
  workflows: { eyebrow: 'Workflow authoring', title: 'Workflow catalog', description: 'Browse workflow definitions, edit linear drafts, and compare, activate, or roll back versions. Saving a draft never activates it.' },
  runs: { eyebrow: 'Execution', title: 'Run cockpit', description: 'Trace every step, artifact, event, and operator decision.' },
  queues: { eyebrow: 'Model broker', title: 'Request queues', description: 'Inspect queued and inflight model work across upstreams.' },
  observability: { eyebrow: 'Telemetry', title: 'Observability', description: 'Follow broker health, latency, errors, and trace correlation.' },
  sessions: { eyebrow: 'OpenCode executor', title: 'Sessions', description: 'Review active execution sessions and their workflow context.' },
  logs: { eyebrow: 'Evidence', title: 'Logs and search', description: 'Search runtime evidence across native Switchyard modules.' },
  prompts: { eyebrow: 'Workflow policy', title: 'Prompt library', description: 'Review prompt contracts, inputs, and generated outputs.' },
  config: { eyebrow: 'Runtime', title: 'Configuration', description: 'Effective values, their sources and drift, and how to change each setting.' },
  agents: { eyebrow: 'Profiles', title: 'Agent profiles', description: 'Inspect effective profiles, version history, drafts, activation, and rollback.' },
  skills: { eyebrow: 'Capabilities', title: 'Runtime capabilities', description: 'Review profile-scoped executors; capability management is not a dashboard control.' },
};

function SwitchyardMark() {
  return (
    <span className="brand-mark" aria-hidden="true">
      <span />
      <span />
      <span />
    </span>
  );
}

function NavIcon({ view }: { view: ViewID }) {
  const paths: Record<ViewID, ReactNode> = {
    overview: <><rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="3" width="7" height="7" rx="1"/><rect x="3" y="14" width="7" height="7" rx="1"/><rect x="14" y="14" width="7" height="7" rx="1"/></>,
    inbox: <><path d="M4 13h4l2 3h4l2-3h4"/><path d="M4 13 6 5h12l2 8v6H4z"/></>,
    workflows: <><rect x="3" y="3" width="6" height="6" rx="1"/><rect x="15" y="15" width="6" height="6" rx="1"/><path d="M9 6h6a3 3 0 0 1 3 3v6"/></>,
    runs: <><path d="M8 5h8"/><path d="M8 12h8"/><path d="M8 19h8"/><circle cx="4" cy="5" r="1"/><circle cx="4" cy="12" r="1"/><circle cx="4" cy="19" r="1"/></>,
    queues: <><path d="M4 6h16"/><path d="M7 12h13"/><path d="M10 18h10"/><circle cx="4" cy="12" r="1"/><circle cx="7" cy="18" r="1"/></>,
    observability: <><path d="M3 12h4l2-6 4 12 2-6h6"/></>,
    sessions: <><rect x="3" y="4" width="18" height="16" rx="2"/><path d="m7 9 3 3-3 3"/><path d="M13 15h4"/></>,
    logs: <><path d="M5 4h14v16H5z"/><path d="M8 8h8M8 12h8M8 16h5"/></>,
    prompts: <><path d="M4 5h16v11H8l-4 4z"/><path d="M8 9h8M8 12h5"/></>,
    config: <><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.9l.1.1-2.8 2.8-.1-.1a1.7 1.7 0 0 0-1.9-.3 1.7 1.7 0 0 0-1 1.6v.2h-4V21a1.7 1.7 0 0 0-1-1.6 1.7 1.7 0 0 0-1.9.3l-.1.1L4.2 17l.1-.1a1.7 1.7 0 0 0 .3-1.9A1.7 1.7 0 0 0 3 14H2.8v-4H3a1.7 1.7 0 0 0 1.6-1 1.7 1.7 0 0 0-.3-1.9L4.2 7 7 4.2l.1.1A1.7 1.7 0 0 0 9 4.6 1.7 1.7 0 0 0 10 3v-.2h4V3a1.7 1.7 0 0 0 1 1.6 1.7 1.7 0 0 0 1.9-.3l.1-.1L19.8 7l-.1.1a1.7 1.7 0 0 0-.3 1.9 1.7 1.7 0 0 0 1.6 1h.2v4H21a1.7 1.7 0 0 0-1.6 1Z"/></>,
    agents: <><circle cx="12" cy="8" r="4"/><path d="M4 21a8 8 0 0 1 16 0"/></>,
    skills: <><path d="m12 2 3 6 6 .8-4.5 4.5 1 6.2-5.5-3-5.5 3 1-6.2L3 8.8 9 8z"/></>,
  };
  return <svg aria-hidden="true" className="nav-icon" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth="1.7">{paths[view]}</svg>;
}

function NavigationItem({ view, active, onSelect }: { view: ViewMeta; active: boolean; onSelect: () => void }) {
  return (
    <button aria-current={active ? 'page' : undefined} className={`sidebar-link ${active ? 'is-active' : ''}`} onClick={onSelect} type="button">
      <NavIcon view={view.id} />
      <span>{view.label}</span>
    </button>
  );
}

export default function App() {
  const { activeView, openRunCockpit, selectedRequestID, selectedRunID, setActiveView, setSelectedRequestID, setSelectedRunID } = useHashNavigation();
  const data = useDashboardData({ activeView, selectedRunID, setSelectedRunID });
  const copy = pageCopy[activeView];
  const connection = data.dashboardError ? 'Degraded' : data.loading ? 'Connecting' : data.refreshing ? 'Refreshing' : 'Operational';

  return (
    <div className="app-shell">
      <a className="skip-link" href="#main-content">Skip to content</a>
      <aside className="sidebar" aria-label="Primary navigation">
        <div className="brand">
          <SwitchyardMark />
          <div><strong>Switchyard</strong><span>Automation runtime</span></div>
        </div>

        <nav className="sidebar-nav">
          {navigationGroups.map((group) => (
            <div className="nav-group" key={group.label}>
              <p>{group.label}</p>
              {group.items.map((id) => {
                const view = views.find((item) => item.id === id);
                return view ? <NavigationItem active={activeView === id} key={id} onSelect={() => setActiveView(id)} view={view} /> : null;
              })}
            </div>
          ))}
        </nav>

        <div className="sidebar-status">
          <div className="status-row"><span><HudLed className={data.dashboardError ? 'bg-[hsl(var(--danger))]' : data.loading ? 'bg-[hsl(var(--warn))]' : 'bg-[hsl(var(--confirm))]'} />Runtime</span><strong>{connection}</strong></div>
          <div className="status-row"><span>API</span><strong title={apiBase || 'Local proxy'}>{apiBase ? 'Remote' : 'Local'}</strong></div>
        </div>
      </aside>

      <div className="workspace">
        <header className="topbar">
          <div className="page-heading">
            <p>{copy.eyebrow}</p>
            <h1>{copy.title}</h1>
            <span>{copy.description}</span>
          </div>
          <div className="topbar-meta" aria-label="System status">
            <span className={`connection-pill ${data.dashboardError ? 'is-danger' : ''}`}><HudLed className={data.dashboardError ? 'bg-[hsl(var(--danger))]' : 'bg-[hsl(var(--confirm))]'} />{connection}</span>
            <span className="environment-pill">{data.dashboard.adapters.config.environment || 'local'}</span>
          </div>
        </header>

        <nav className="mobile-nav" aria-label="Mobile navigation">
          {views.map((view) => <NavigationItem active={activeView === view.id} key={view.id} onSelect={() => setActiveView(view.id)} view={view} />)}
        </nav>

        <main id="main-content" className="content" tabIndex={-1}>
          {data.dashboardError ? <HudErrorState role="alert"><strong>Connection issue</strong><span>{data.dashboardError}</span></HudErrorState> : null}

          {activeView === 'overview' ? (
            <OverviewDashboard dashboard={data.dashboard} loading={data.loading} openPilotIssue={data.openPilotIssue} openPilotReady={data.openPilotReady} openPilotRepo={data.openPilotRepo} openPilotState={data.openPilotState} onCancelRun={data.cancelRun} onRefresh={data.refresh} onRetryRun={data.retryRun} onResumeOpenPilotState={data.resumeOpenPilotState} onRunTrigger={data.runTrigger} onOpenInbox={() => setActiveView('inbox')} onOpenRunCockpit={openRunCockpit} onSelectRun={data.setSelectedRunID} onToggleTrigger={data.toggleTrigger} refreshing={data.refreshing} runActionID={data.runActionID} runningTriggerID={data.runningTriggerID} selectedRunDetail={data.selectedRunDetail} selectedRunID={selectedRunID} setOpenPilotIssue={data.setOpenPilotIssue} setOpenPilotRepo={data.setOpenPilotRepo} workflows={data.dashboard.workflows} updatingTriggerID={data.updatingTriggerID} workflowDefinitions={data.workflowDefinitions} />
          ) : activeView === 'inbox' ? (
            <OpenPilotInboxPage onOpenRun={openRunCockpit} />
          ) : activeView === 'workflows' ? (
            <WorkflowCatalogPage />
          ) : activeView === 'runs' ? (
            <RunCockpitPage selectedRunID={selectedRunID} selectedRunDetail={data.selectedRunDetail} selectedRunMissing={data.missingRunID !== '' && data.missingRunID === selectedRunID} openPilotState={data.openPilotState} runActionID={data.runActionID} onRetryRun={data.retryRun} onCancelRun={data.cancelRun} onResumeOpenPilotState={data.resumeOpenPilotState} onStepAction={data.stepAction} onBackToOverview={() => setActiveView('overview')} onRefresh={data.refresh} liveRefresh={data.liveRefresh} />
          ) : activeView === 'queues' ? (
            <QueuesPage selectedRequestID={selectedRequestID} setSelectedRequestID={setSelectedRequestID} />
          ) : activeView === 'observability' ? <ObservabilityPage /> : <FeaturePage view={activeView} adapters={data.dashboard.adapters} agentProfiles={data.dashboard.agentProfiles} />}
        </main>
      </div>
    </div>
  );
}
