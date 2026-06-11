create table if not exists workflows (
  id text primary key,
  kind text not null,
  name text not null,
  description text not null default '',
  owns jsonb not null default '[]'::jsonb,
  uses jsonb not null default '[]'::jsonb,
  status text not null default ''
);

create table if not exists triggers (
  id text primary key,
  workflow_id text not null references workflows(id) on delete cascade,
  type text not null,
  name text not null,
  description text not null default '',
  enabled boolean not null default false,
  schedule text not null default '',
  inputs jsonb not null default '{}'::jsonb,
  next_run_at timestamptz
);

create table if not exists runs (
  id text primary key,
  workflow_id text not null references workflows(id),
  trigger_id text references triggers(id),
  status text not null,
  inputs jsonb not null default '{}'::jsonb,
  created_at timestamptz not null,
  started_at timestamptz,
  finished_at timestamptz,
  summary text not null default ''
);

create index if not exists runs_created_at_idx on runs(created_at desc);
create index if not exists runs_workflow_id_idx on runs(workflow_id);
create index if not exists runs_trigger_id_idx on runs(trigger_id);

create table if not exists run_events (
  id text primary key,
  run_id text not null references runs(id) on delete cascade,
  type text not null,
  message text not null default '',
  created_at timestamptz not null
);

create index if not exists run_events_run_id_created_at_idx on run_events(run_id, created_at, id);

create table if not exists artifacts (
  id text primary key,
  run_id text not null references runs(id) on delete cascade,
  name text not null,
  kind text not null,
  uri text not null,
  created_at timestamptz not null
);

create index if not exists artifacts_run_id_created_at_idx on artifacts(run_id, created_at, id);
