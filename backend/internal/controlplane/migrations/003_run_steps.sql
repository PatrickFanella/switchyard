create table if not exists run_steps (
  id text primary key,
  run_id text not null references runs(id) on delete cascade,
  position integer not null,
  name text not null,
  executor text not null,
  model text not null default '',
  status text not null,
  inputs jsonb not null default '{}'::jsonb,
  output_summary text not null default '',
  error text not null default '',
  attempt_count integer not null default 0,
  correlation_ids jsonb not null default '{}'::jsonb,
  created_at timestamptz not null,
  started_at timestamptz,
  finished_at timestamptz,
  unique(run_id, position)
);

create index if not exists run_steps_run_id_position_idx on run_steps(run_id, position);

alter table run_events add column if not exists run_step_id text references run_steps(id) on delete set null;
alter table artifacts add column if not exists run_step_id text references run_steps(id) on delete set null;

create index if not exists run_events_run_step_id_idx on run_events(run_step_id);
create index if not exists artifacts_run_step_id_idx on artifacts(run_step_id);
