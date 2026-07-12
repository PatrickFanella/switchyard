import {
  emptyAdapters,
  emptyScheduler,
  emptyWorkers,
  type AdapterPayload,
  type DashboardResponse,
  type DashboardState,
  type ModelBrokerHistoryResponse,
  type ModelBrokerRequestHistoryItem,
  type ModelBrokerObservability,
  type OpenPilotStateResponse,
  type Run,
  type RunDetail,
  type RunStep,
  type SchedulerStatus,
  type Trigger,
  type Workflow,
  type WorkflowDefinition,
  type WorkerPoolStatus,
} from './types';

export const apiBase = (import.meta.env.VITE_SWITCHYARD_API_URL ?? '').replace(/\/$/, '');

export function apiPath(path: string) {
  return `${apiBase}${path}`;
}

export class APIError extends Error {
  status: number;

  constructor(status: number) {
    super(`Switchyard API returned ${status}`);
    this.status = status;
  }
}

export async function fetchJSON<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(apiPath(path), {
    headers: { 'content-type': 'application/json', ...(init?.headers ?? {}) },
    ...init,
  });

  if (!response.ok) {
    throw new APIError(response.status);
  }

  return (await response.json()) as T;
}

export function messageFrom(error: unknown) {
  return error instanceof Error ? error.message : 'Unexpected Switchyard dashboard error';
}

export function arrayOrEmpty<T>(value: T[] | null | undefined): T[] {
  return Array.isArray(value) ? value : [];
}

export function formatTime(value?: string) {
  if (!value) {
    return 'Not scheduled';
  }

  return new Intl.DateTimeFormat(undefined, {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  }).format(new Date(value));
}

export function workflowName(workflows: Workflow[], workflowID: string) {
  return workflows.find((workflow) => workflow.id === workflowID)?.name ?? workflowID;
}

export function workflowDefinitionExecutors(definition: WorkflowDefinition) {
  return arrayOrEmpty(definition.pipeline?.steps)
    .slice()
    .sort((left, right) => left.position - right.position)
    .map((step) => step.executor)
    .join(' → ') || 'No steps';
}

export function normalizeRunDetail(detail: RunDetail): RunDetail {
  return {
    ...detail,
    steps: arrayOrEmpty(detail.steps),
    events: arrayOrEmpty(detail.events),
    artifacts: arrayOrEmpty(detail.artifacts),
  };
}

export function normalizeOpenPilotState(response: OpenPilotStateResponse): OpenPilotStateResponse {
  return {
    ...response,
    state: {
      ...response.state,
      gate_status: response.state.gate_status ?? {},
      resume_inputs: response.state.resume_inputs ?? {},
    },
  };
}

export function openPilotRunInputs(detail: RunDetail) {
  const inputs = detail.run.inputs ?? {};
  const repo = inputs.repo?.trim();
  const issue = inputs.issue?.trim();

  if (!repo || !issue) {
    return null;
  }

  return { repo, issue };
}

export function formatOpenPilotGateStatus(gateStatus?: Record<string, string>) {
  const entries = Object.entries(gateStatus ?? {});
  return entries.length > 0 ? entries.map(([key, value]) => `${key}: ${value}`).join(' · ') : 'None';
}

export function normalizeAdapters(adapters: AdapterPayload): AdapterPayload {
  return {
    ...emptyAdapters,
    ...adapters,
    queues: arrayOrEmpty(adapters.queues),
    sessions: arrayOrEmpty(adapters.sessions),
    logs: arrayOrEmpty(adapters.logs),
    prompts: arrayOrEmpty(adapters.prompts),
    agents: arrayOrEmpty(adapters.agents),
    skills: arrayOrEmpty(adapters.skills),
    health: adapters.health ?? emptyAdapters.health,
    config: adapters.config ?? emptyAdapters.config,
    llama_line: {
      ...emptyAdapters.llama_line,
      ...(adapters.llama_line ?? {}),
      requests: arrayOrEmpty(adapters.llama_line?.requests),
      search: arrayOrEmpty(adapters.llama_line?.search),
      upstreams: arrayOrEmpty(adapters.llama_line?.upstreams),
    },
    ocq: {
      ...emptyAdapters.ocq,
      ...(adapters.ocq ?? {}),
      requests: arrayOrEmpty(adapters.ocq?.requests),
      logs: arrayOrEmpty(adapters.ocq?.logs),
      sessions: arrayOrEmpty(adapters.ocq?.sessions),
    },
    blacktower: {
      ...emptyAdapters.blacktower,
      ...(adapters.blacktower ?? {}),
      tasks: arrayOrEmpty(adapters.blacktower?.tasks),
      reports: arrayOrEmpty(adapters.blacktower?.reports),
    },
    changemaker: {
      ...emptyAdapters.changemaker,
      ...(adapters.changemaker ?? {}),
      posts: arrayOrEmpty(adapters.changemaker?.posts),
    },
  };
}

type DashboardSnapshot = {
  dashboard: DashboardState;
  workflowDefinitions: WorkflowDefinition[];
};

export async function loadDashboardSnapshot(): Promise<DashboardSnapshot> {
  const [workflowData, workflowDefinitionData, triggerData, runData, adapterData, schedulerData, workerData] = await Promise.all([
    fetchJSON<DashboardResponse<Workflow>>('/api/workflows'),
    fetchJSON<{ workflow_definitions: WorkflowDefinition[] }>('/api/workflow-definitions'),
    fetchJSON<DashboardResponse<Trigger>>('/api/triggers'),
    fetchJSON<DashboardResponse<Run>>('/api/runs'),
    fetchJSON<AdapterPayload>('/api/adapters'),
    fetchJSON<SchedulerStatus>('/api/scheduler'),
    fetchJSON<WorkerPoolStatus>('/api/workers'),
  ]);

  return {
    dashboard: {
      workflows: arrayOrEmpty(workflowData.workflows as Workflow[] | null | undefined),
      triggers: arrayOrEmpty(triggerData.triggers as Trigger[] | null | undefined),
      runs: arrayOrEmpty(runData.runs as Run[] | null | undefined),
      adapters: normalizeAdapters(adapterData),
      scheduler: schedulerData ?? emptyScheduler,
      workers: workerData ?? emptyWorkers,
    },
    workflowDefinitions: arrayOrEmpty(workflowDefinitionData.workflow_definitions),
  };
}

export async function loadModelBrokerObservability() {
  return fetchJSON<ModelBrokerObservability>('/api/observability/model-broker');
}

export async function loadModelBrokerHistory(query = '') {
  const path = query.trim() ? `/api/history?q=${encodeURIComponent(query.trim())}` : '/api/history';
  const response = await fetchJSON<ModelBrokerHistoryResponse | ModelBrokerRequestHistoryItem[]>(path);
  if (Array.isArray(response)) {
    return response;
  }
  return arrayOrEmpty(response.requests ?? response.history);
}

export async function loadModelBrokerRequest(requestID: string) {
  return fetchJSON<ModelBrokerRequestHistoryItem>(`/api/history/${encodeURIComponent(requestID)}`);
}

export async function cancelModelBrokerRequest(requestID: string) {
  return fetchJSON<{ canceled?: boolean }>(`/admin/queue/${encodeURIComponent(requestID)}`, { method: 'DELETE' });
}

export async function loadRunDetail(runID: string) {
  return normalizeRunDetail(await fetchJSON<RunDetail>(`/api/runs/${runID}`));
}

export async function loadOpenPilotState(repo: string, issue: string) {
  return normalizeOpenPilotState(
    await fetchJSON<OpenPilotStateResponse>(`/api/open-pilot/state?repo=${encodeURIComponent(repo)}&issue=${encodeURIComponent(issue)}`),
  );
}

export async function runTrigger(triggerID: string) {
  return fetchJSON<{ run: Run }>(`/api/triggers/${triggerID}/run`, { method: 'POST' });
}

export async function runOpenPilotTrigger(triggerID: string, repo: string, issue: string) {
  return fetchJSON<{ run: Run }>('/api/runs', {
    method: 'POST',
    body: JSON.stringify({ workflow_id: 'open-pilot', trigger_id: triggerID, inputs: { repo, issue } }),
  });
}

export async function toggleTrigger(trigger: Trigger) {
  return fetchJSON<{ trigger: Trigger }>(`/api/triggers/${trigger.id}`, {
    method: 'PATCH',
    body: JSON.stringify({ enabled: !trigger.enabled }),
  });
}

export async function runAction(runID: string, action: 'retry' | 'cancel') {
  return fetchJSON<{ run: Run }>(`/api/runs/${runID}/${action}`, { method: 'POST' });
}

export async function stepAction(runID: string, stepID: string, action: 'retry' | 'cancel') {
  return fetchJSON<{ step: RunStep } | { run: Run; step: RunStep }>(`/api/runs/${runID}/steps/${stepID}/${action}`, { method: 'POST' });
}

export async function resumeOpenPilotState(repo: string, issue: string) {
  return fetchJSON<{ run: Run }>(`/api/open-pilot/state/resume?repo=${encodeURIComponent(repo)}&issue=${encodeURIComponent(issue)}`, { method: 'POST' });
}
