import { useCallback, useEffect, useState } from 'react';
import { defaultDashboardState, type DashboardState, type OpenPilotStateResponse, type RunDetail, type Trigger, type ViewID, type WorkflowDefinition } from '../types';
import {
  APIError,
  loadDashboardSnapshot,
  loadOpenPilotState,
  loadRunDetail,
  loadRuns,
  messageFrom,
  openPilotRunInputs,
  resumeOpenPilotState,
  runAction,
  runOpenPilotTrigger,
  runTrigger,
  stepAction,
  toggleTrigger,
} from '../api';
import { isTerminalRunStatus, useLiveRefresh, type LiveRefreshTicket } from './useLiveRefresh';

type UseDashboardDataArgs = {
  activeView: ViewID;
  selectedRunID: string;
  setSelectedRunID: (runID: string) => void;
};

export function useDashboardData({ activeView, selectedRunID, setSelectedRunID }: UseDashboardDataArgs) {
  const [dashboard, setDashboard] = useState<DashboardState>(defaultDashboardState);
  const [workflowDefinitions, setWorkflowDefinitions] = useState<WorkflowDefinition[]>([]);
  const [dashboardError, setDashboardError] = useState('');
  const [selectedRunDetail, setSelectedRunDetail] = useState<RunDetail | null>(null);
  // The run ID most recently answered with 404 (unknown or retained out).
  const [missingRunID, setMissingRunID] = useState('');
  const [openPilotState, setOpenPilotState] = useState<OpenPilotStateResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [runningTriggerID, setRunningTriggerID] = useState('');
  const [updatingTriggerID, setUpdatingTriggerID] = useState('');
  const [runActionID, setRunActionID] = useState('');
  const [openPilotRepo, setOpenPilotRepo] = useState('');
  const [openPilotIssue, setOpenPilotIssue] = useState('');

  const openPilotRepoInput = openPilotRepo.trim();
  const openPilotIssueInput = openPilotIssue.trim();
  const openPilotReady = openPilotRepoInput !== '' && openPilotIssueInput !== '';

  const health = dashboard.adapters.health;

  const loadDashboard = useCallback(async (mode: 'initial' | 'refresh') => {
    setDashboardError('');
    setLoading(mode === 'initial');
    setRefreshing(mode === 'refresh');
    let ok = true;

    try {
      const snapshot = await loadDashboardSnapshot();
      setDashboard(snapshot.dashboard);
      setWorkflowDefinitions(snapshot.workflowDefinitions);
    } catch (caught) {
      setDashboardError(messageFrom(caught));
      ok = false;
    } finally {
      setLoading(false);
      setRefreshing(false);
    }

    return ok;
  }, []);

  const loadSelectedRun = useCallback(async (runID: string, ticket?: LiveRefreshTicket) => {
    const current = () => !ticket || ticket.isCurrent();
    setOpenPilotState(null);
    if (!runID) {
      setSelectedRunDetail(null);
      return;
    }

    setDashboardError('');

    try {
      const detail = await loadRunDetail(runID);
      if (!current()) {
        return;
      }
      setSelectedRunDetail(detail);
      setMissingRunID((missing) => (missing === runID ? '' : missing));

      if (detail.run.workflow_id !== 'open-pilot') {
        setOpenPilotState(null);
        return;
      }

      const inputs = openPilotRunInputs(detail);
      if (!inputs) {
        setOpenPilotState(null);
        return;
      }

      try {
        const state = await loadOpenPilotState(inputs.repo, inputs.issue);
        if (current()) {
          setOpenPilotState(state);
        }
      } catch (caught) {
        if (caught instanceof APIError && caught.status === 404) {
          if (current()) {
            setOpenPilotState(null);
          }
          return;
        }

        throw caught;
      }
    } catch (caught) {
      if (!current()) {
        return;
      }
      if (caught instanceof APIError && caught.status === 404) {
        setSelectedRunDetail(null);
        setOpenPilotState(null);
        setMissingRunID(runID);
        return;
      }

      setDashboardError(messageFrom(caught));
    }
  }, []);

  // Bounded live refresh of the selected run. Polls only while the run can still
  // change and the tab is visible. Poll results never touch dashboardError or
  // action state, and stale responses are dropped through the shared ticket.
  const pollSelectedRun = useCallback(async (ticket: LiveRefreshTicket) => {
    if (!selectedRunID) {
      return;
    }
    const [detail, runs] = await Promise.all([loadRunDetail(selectedRunID), loadRuns()]);
    let state: OpenPilotStateResponse | null = null;
    if (detail.run.workflow_id === 'open-pilot') {
      const inputs = openPilotRunInputs(detail);
      if (inputs) {
        try {
          state = await loadOpenPilotState(inputs.repo, inputs.issue);
        } catch (caught) {
          if (!(caught instanceof APIError && caught.status === 404)) {
            throw caught;
          }
        }
      }
    }
    if (!ticket.isCurrent()) {
      return;
    }
    setSelectedRunDetail(detail);
    setOpenPilotState(state);
    setDashboard((previous) => ({ ...previous, runs }));
  }, [selectedRunID]);

  const liveMode = !selectedRunID ? 'off' : !selectedRunDetail || isTerminalRunStatus(selectedRunDetail.run.status) ? 'idle' : 'active';
  const live = useLiveRefresh({ key: selectedRunID, mode: liveMode, load: pollSelectedRun });
  const { claimTicket, markUpdated } = live;

  const reloadAll = useCallback(async (runID: string) => {
    const ticket = claimTicket();
    const dashboardLoaded = await loadDashboard('refresh');
    if (dashboardLoaded) {
      await loadSelectedRun(runID, ticket);
      if (ticket.isCurrent()) {
        markUpdated();
      }
    }
  }, [claimTicket, loadDashboard, loadSelectedRun, markUpdated]);

  useEffect(() => {
    void loadDashboard('initial');
  }, [loadDashboard]);

  useEffect(() => {
    if (activeView === 'overview' && !selectedRunID && dashboard.runs.length > 0) {
      setSelectedRunID(dashboard.runs[0].id);
    }
  }, [activeView, dashboard.runs, selectedRunID, setSelectedRunID]);

  useEffect(() => {
    const ticket = claimTicket();
    void loadSelectedRun(selectedRunID, ticket).then(() => {
      if (ticket.isCurrent() && selectedRunID) {
        markUpdated();
      }
    });
  }, [claimTicket, loadSelectedRun, markUpdated, selectedRunID]);

  const refresh = useCallback(async () => {
    await reloadAll(selectedRunID);
  }, [reloadAll, selectedRunID]);

  const runTriggerAction = useCallback(async (triggerID: string) => {
    setRunningTriggerID(triggerID);
    setDashboardError('');

    try {
      const trigger = dashboard.triggers.find((item) => item.id === triggerID);
      const isOpenPilot = trigger?.workflow_id === 'open-pilot';
      const data = isOpenPilot && openPilotReady
        ? await runOpenPilotTrigger(triggerID, openPilotRepoInput, openPilotIssueInput)
        : await runTrigger(triggerID);

      setSelectedRunID(data.run.id);
      await reloadAll(data.run.id);
    } catch (caught) {
      setDashboardError(messageFrom(caught));
    } finally {
      setRunningTriggerID('');
    }
  }, [dashboard.triggers, openPilotIssueInput, openPilotReady, openPilotRepoInput, reloadAll, setSelectedRunID]);

  const toggleTriggerAction = useCallback(async (trigger: Trigger) => {
    setUpdatingTriggerID(trigger.id);
    setDashboardError('');

    try {
      await toggleTrigger(trigger);
      await reloadAll(selectedRunID);
    } catch (caught) {
      setDashboardError(messageFrom(caught));
    } finally {
      setUpdatingTriggerID('');
    }
  }, [reloadAll, selectedRunID]);

  const retryRun = useCallback(async (runID: string) => {
    setRunActionID(`${runID}:retry`);
    setDashboardError('');

    try {
      const data = await runAction(runID, 'retry');
      setSelectedRunID(data.run.id);
      await reloadAll(data.run.id);
    } catch (caught) {
      setDashboardError(messageFrom(caught));
    } finally {
      setRunActionID('');
    }
  }, [reloadAll, setSelectedRunID]);

  const cancelRun = useCallback(async (runID: string) => {
    setRunActionID(`${runID}:cancel`);
    setDashboardError('');

    try {
      const data = await runAction(runID, 'cancel');
      setSelectedRunID(data.run.id);
      await reloadAll(data.run.id);
    } catch (caught) {
      setDashboardError(messageFrom(caught));
    } finally {
      setRunActionID('');
    }
  }, [reloadAll, setSelectedRunID]);

  const resumeOpenPilotStateAction = useCallback(async (state: OpenPilotStateResponse['state']) => {
    setRunActionID(`${state.id}:open-pilot:resume`);
    setDashboardError('');

    try {
      const data = await resumeOpenPilotState(state.repo, state.issue);
      setSelectedRunID(data.run.id);
      await reloadAll(data.run.id);
    } catch (caught) {
      setDashboardError(messageFrom(caught));
    } finally {
      setRunActionID('');
    }
  }, [reloadAll, setSelectedRunID]);

  const stepActionCallback = useCallback(async (runID: string, stepID: string, action: 'retry' | 'cancel') => {
    setRunActionID(`${stepID}:step:${action}`);
    setDashboardError('');

    try {
      await stepAction(runID, stepID, action);
      await reloadAll(runID);
    } catch (caught) {
      setDashboardError(messageFrom(caught));
    } finally {
      setRunActionID('');
    }
  }, [reloadAll]);

  return {
    dashboard,
    workflowDefinitions,
    dashboardError,
    health,
    loading,
    refreshing,
    selectedRunID,
    selectedRunDetail,
    missingRunID,
    setSelectedRunID,
    refresh,
    runTrigger: runTriggerAction,
    toggleTrigger: toggleTriggerAction,
    retryRun,
    cancelRun,
    stepAction: stepActionCallback,
    openPilotRepo,
    setOpenPilotRepo,
    openPilotIssue,
    setOpenPilotIssue,
    openPilotReady,
    openPilotState,
    resumeOpenPilotState: resumeOpenPilotStateAction,
    runningTriggerID,
    updatingTriggerID,
    runActionID,
    liveRefresh: live,
  };
}
