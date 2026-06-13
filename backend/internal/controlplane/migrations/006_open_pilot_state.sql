create table if not exists open_pilot_issue_states (
  id text primary key,
  repo text not null,
  issue text not null,
  phase text not null,
  run_id text not null default '',
  last_step_name text not null default '',
  branch text not null default '',
  base text not null default '',
  worktree_path text not null default '',
  pull_request_url text not null default '',
  pull_request_id integer,
  pull_request_number integer,
  merge_commit_sha text not null default '',
  issue_closed boolean not null default false,
  gate_status jsonb not null default '{}'::jsonb,
  resume_inputs jsonb not null default '{}'::jsonb,
  repair_attempts integer not null default 0,
  max_repair_attempts integer not null default 2,
  last_error text not null default '',
  created_at timestamptz not null,
  updated_at timestamptz not null,
  unique(repo, issue),
  constraint open_pilot_issue_states_gate_status_check check (jsonb_typeof(gate_status) = 'object'),
  constraint open_pilot_issue_states_resume_inputs_check check (jsonb_typeof(resume_inputs) = 'object'),
  constraint open_pilot_issue_states_phase_check check (phase in ('requested', 'validated', 'branch_prepared', 'agent_completed', 'verified', 'pushed', 'pr_created', 'ai_reviewed', 'ci_passed', 'merge_ready', 'merged', 'issue_closed', 'failed', 'paused')),
  constraint open_pilot_issue_states_repair_attempts_check check (repair_attempts >= 0 and max_repair_attempts >= 0 and repair_attempts <= max_repair_attempts)
);

create index if not exists open_pilot_issue_states_phase_idx on open_pilot_issue_states(phase);
create index if not exists open_pilot_issue_states_run_id_idx on open_pilot_issue_states(run_id);
