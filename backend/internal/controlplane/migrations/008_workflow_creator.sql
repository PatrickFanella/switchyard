create table if not exists workflow_definitions (
  id text primary key,
  name text not null,
  description text not null default '',
  status text not null default 'draft',
  owns jsonb not null default '[]'::jsonb,
  uses jsonb not null default '[]'::jsonb,
  input_shape jsonb not null default '{}'::jsonb,
  pipeline jsonb not null,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  constraint workflow_definitions_owns_array check (jsonb_typeof(owns) = 'array'),
  constraint workflow_definitions_uses_array check (jsonb_typeof(uses) = 'array'),
  constraint workflow_definitions_input_shape_object check (jsonb_typeof(input_shape) = 'object'),
  constraint workflow_definitions_pipeline_object check (jsonb_typeof(pipeline) = 'object')
);

create index if not exists workflow_definitions_status_idx on workflow_definitions(status);
