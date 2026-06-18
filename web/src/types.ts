export type JSONShapeProperty = {
  type: string;
  description?: string;
};

export type JSONShape = {
  type: string;
  description?: string;
  properties?: Record<string, JSONShapeProperty>;
  required?: string[];
};

export type Workflow = {
  id: string;
  kind: string;
  name: string;
  description: string;
  owns: string[];
  uses: string[];
  status: string;
};

export type PipelineStepDefinition = {
  id: string;
  position: number;
  name: string;
  executor: string;
  profile?: string;
  model?: string;
  inputs?: Record<string, string>;
  output_shape?: JSONShape;
};

export type PipelineDefinition = {
  id: string;
  workflow_id?: string;
  name: string;
  description?: string;
  steps: PipelineStepDefinition[];
};

export type WorkflowDefinition = {
  id: string;
  name: string;
  description: string;
  status: string;
  owns?: string[];
  uses?: string[];
  input_shape?: JSONShape;
  pipeline: PipelineDefinition;
  created_at?: string;
  updated_at?: string;
};

export type Trigger = {
  id: string;
  workflow_id: string;
  type: string;
  name: string;
  description: string;
  enabled: boolean;
  schedule?: string;
  inputs?: Record<string, string>;
  next_run_at?: string;
  last_run_at?: string;
  updated_at?: string;
  last_error?: string;
};

export type Run = {
  id: string;
  workflow_id: string;
  trigger_id?: string;
  status: 'queued' | 'running' | 'succeeded' | 'failed' | 'canceled';
  inputs?: Record<string, string>;
  created_at: string;
  started_at?: string;
  finished_at?: string;
  summary?: string;
};

export type RunEvent = {
  id: string;
  run_id: string;
  type: string;
  message: string;
  created_at: string;
};

export type RunStep = {
  id: string;
  run_id: string;
  position: number;
  name: string;
  executor: string;
  model?: string;
  status: 'queued' | 'running' | 'succeeded' | 'failed' | 'canceled' | 'timed_out';
  inputs?: Record<string, string>;
  output_summary?: string;
  error?: string;
  attempt_count: number;
  correlation_ids?: Record<string, string>;
  created_at: string;
  started_at?: string;
  finished_at?: string;
};

export type Artifact = {
  id: string;
  run_id: string;
  name: string;
  kind: string;
  uri: string;
  created_at: string;
};

export type RunDetail = {
  run: Run;
  steps: RunStep[];
  events: RunEvent[];
  artifacts: Artifact[];
};

export type OpenPilotIssueState = {
  id: string;
  repo: string;
  issue: string;
  phase: string;
  run_id?: string;
  last_step_name?: string;
  branch?: string;
  base?: string;
  worktree_path?: string;
  pull_request_url?: string;
  pull_request_id?: number;
  pull_request_number?: number;
  merge_commit_sha?: string;
  issue_closed: boolean;
  gate_status?: Record<string, string>;
  resume_inputs?: Record<string, string>;
  repair_attempts: number;
  max_repair_attempts: number;
  last_error?: string;
  updated_at: string;
  created_at: string;
};

export type OpenPilotStateResponse = {
  state: OpenPilotIssueState;
  next_action: string;
  run?: Run;
};

export type SchedulerStatus = {
  enabled: boolean;
  running: boolean;
  interval?: string;
  last_tick?: string;
  last_error?: string;
};

export type WorkerPoolStatus = {
  enabled: boolean;
  running: boolean;
  workers?: number;
  active?: number;
  lease?: string;
  poll_interval?: string;
  last_recover_at?: string;
  recovered_runs?: number;
  failed_leases?: number;
  completed_runs?: number;
  failed_runs?: number;
  last_error?: string;
};

export type AdapterQueue = {
  id: string;
  name: string;
  status: string;
  depth: number;
  capacity: number;
  workers: number;
  updated_at: string;
};

export type AdapterSession = {
  id: string;
  name: string;
  surface: string;
  status: string;
  workflow_id?: string;
  started_at: string;
  updated_at: string;
};

export type AdapterLog = {
  id: string;
  level: string;
  source: string;
  message: string;
  created_at: string;
};

export type AdapterPrompt = {
  id: string;
  name: string;
  surface: string;
  template: string;
  variables: string[];
  description: string;
};

export type AdapterConfig = {
  environment: string;
  version: string;
  base_url: string;
  features: string[];
  limits: Record<string, number>;
};

export type AdapterAgent = {
  id: string;
  name: string;
  role: string;
  status: string;
  model: string;
  capabilities: string[];
};

export type AdapterSkill = {
  id: string;
  name: string;
  category: string;
  status: string;
  description: string;
};

export type AdapterService = {
  name: string;
  status: string;
  details?: string;
};

export type AdapterHealth = {
  status: string;
  services: AdapterService[];
  checked_at: string;
};

export type AdapterLlamaLine = {
  requests: { id: string; model: string; route: string; status: string; latency_ms: number; queue_depth: number; created_at: string }[];
  stats: { active_requests: number; queued_requests: number; avg_latency_ms: number; success_rate: number };
  search: { query: string; match: string; source: string; score: number }[];
  upstreams: { name: string; url: string; status: string; pending: number; last_seen_at: string }[];
};

export type AdapterOCQ = {
  requests: { id: string; workflow: string; trigger: string; status: string; actor: string; started_at: string; updated_at: string }[];
  logs: { id: string; level: string; channel: string; message: string; created_at: string }[];
  sessions: { id: string; title: string; status: string; user: string; started_at: string; updated_at: string }[];
  settings: { workspace: string; provider: string; retention_days: number; cors_allowed: string[] };
};

export type AdapterBlacktower = {
  tasks: { id: string; name: string; status: string; owner: string; progress: number; updated_at: string }[];
  reports: { id: string; title: string; type: string; status: string; findings: string[]; generated_at: string }[];
  scheduler_health: { status: string; next_run_at: string; last_tick_at: string; skew_seconds: number };
};

export type AdapterChangemaker = {
  posts: { id: string; title: string; status: string; tags: string[]; published_at: string }[];
  service: { name: string; status: string; version: string; healthy: boolean; updated_at: string };
  run_control: { enabled: boolean; mode: string; active_run_id?: string; queued_runs: number };
};

export type AdapterPayload = {
  queues: AdapterQueue[];
  sessions: AdapterSession[];
  logs: AdapterLog[];
  prompts: AdapterPrompt[];
  config: AdapterConfig;
  agents: AdapterAgent[];
  skills: AdapterSkill[];
  health: AdapterHealth;
  llama_line: AdapterLlamaLine;
  ocq: AdapterOCQ;
  blacktower: AdapterBlacktower;
  changemaker: AdapterChangemaker;
};

export type DashboardState = {
  workflows: Workflow[];
  triggers: Trigger[];
  runs: Run[];
  adapters: AdapterPayload;
  scheduler: SchedulerStatus;
  workers: WorkerPoolStatus;
};

export type DashboardResponse<T> = {
  [key: string]: T[] | null;
};

export type ViewID = 'overview' | 'queues' | 'sessions' | 'logs' | 'prompts' | 'config' | 'agents' | 'skills';

export type ViewMeta = {
  id: ViewID;
  label: string;
  source: string;
};

export const openPilotStateFields = ['Phase', 'Next action', 'Gate status', 'Pull request URL', 'Merge SHA', 'Issue closed', 'Repair attempts', 'Last error'] as const;

export const starterWorkflows: Workflow[] = [
  {
    id: 'open-pilot',
    kind: 'open-pilot',
    name: 'Open Pilot',
    description: 'Guided execution for high-signal Gitea issues with operator visibility.',
    owns: ['issue policy', 'branches', 'pull requests'],
    uses: ['ModelBroker', 'OpenCode executor', 'Blacktower'],
    status: 'seeded',
  },
  {
    id: 'changemaker',
    kind: 'changemaker',
    name: 'Changemaker',
    description: 'Timed narrative and change-report workflows with reviewable artifacts.',
    owns: ['change summaries', 'narrative state'],
    uses: ['ModelBroker'],
    status: 'seeded',
  },
  {
    id: 'devlog',
    kind: 'devlog',
    name: 'Devlog',
    description: 'Capture decisions, progress, and follow-ups as work moves.',
    owns: ['progress logs', 'handoffs'],
    uses: ['Switchyard runs'],
    status: 'seeded',
  },
];

export const starterWorkflowDefinitions: WorkflowDefinition[] = [
  {
    id: 'changemaker-daily-digest',
    name: 'Changemaker Daily Digest',
    description: 'Native workflow-creator template for timed narrative digests.',
    status: 'active',
    input_shape: { type: 'object', required: ['topic', 'prompt'] },
    pipeline: {
      id: 'changemaker-daily-digest-pipeline',
      workflow_id: 'changemaker-daily-digest',
      name: 'Changemaker Daily Digest',
      steps: [
        { id: 'prepare', position: 1, name: 'changemaker.prepare', executor: 'internal' },
        { id: 'changemaker-draft', position: 2, name: 'changemaker.generate', executor: 'model', profile: 'content-writer', model: '${model}' },
        { id: 'attach', position: 3, name: 'changemaker.attach-draft', executor: 'internal' },
      ],
    },
  },
];

export const routingSteps = ['UI', 'Switchyard', 'ModelBroker', 'OpenCode executor', 'Blacktower'];

export const views: ViewMeta[] = [
  { id: 'overview', label: 'Overview', source: 'Switchyard' },
  { id: 'queues', label: 'Queues', source: 'ModelBroker' },
  { id: 'sessions', label: 'Sessions', source: 'OpenCode executor / Blacktower' },
  { id: 'logs', label: 'Logs / Search', source: 'ModelBroker / OpenCode executor' },
  { id: 'prompts', label: 'Prompts', source: 'Workflow policy' },
  { id: 'config', label: 'Config', source: 'Workflow policy / broker' },
  { id: 'agents', label: 'Agents', source: 'Blacktower' },
  { id: 'skills', label: 'Skills', source: 'Blacktower' },
];

export const emptyAdapters: AdapterPayload = {
  queues: [],
  sessions: [],
  logs: [],
  prompts: [],
  config: { environment: 'unknown', version: 'unknown', base_url: '', features: [], limits: {} },
  agents: [],
  skills: [],
  health: { status: 'unknown', services: [], checked_at: '' },
  llama_line: { requests: [], stats: { active_requests: 0, queued_requests: 0, avg_latency_ms: 0, success_rate: 0 }, search: [], upstreams: [] },
  ocq: { requests: [], logs: [], sessions: [], settings: { workspace: '', provider: '', retention_days: 0, cors_allowed: [] } },
  blacktower: { tasks: [], reports: [], scheduler_health: { status: 'unknown', next_run_at: '', last_tick_at: '', skew_seconds: 0 } },
  changemaker: { posts: [], service: { name: '', status: 'unknown', version: '', healthy: false, updated_at: '' }, run_control: { enabled: false, mode: 'manual', queued_runs: 0 } },
};

export const emptyScheduler: SchedulerStatus = { enabled: false, running: false, interval: 'disabled' };
export const emptyWorkers: WorkerPoolStatus = { enabled: false, running: false, workers: 0, active: 0 };

export const defaultDashboardState: DashboardState = {
  workflows: starterWorkflows,
  triggers: [],
  runs: [],
  adapters: emptyAdapters,
  scheduler: emptyScheduler,
  workers: emptyWorkers,
};

export const defaultWorkflowDefinitions = starterWorkflowDefinitions;
