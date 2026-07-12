alter table workflow_definitions add column if not exists version integer not null default 1;

create table if not exists workflow_definition_versions (
  id text not null,
  version integer not null,
  name text not null,
  description text not null default '',
  status text not null,
  owns jsonb not null default '[]'::jsonb,
  uses jsonb not null default '[]'::jsonb,
  input_shape jsonb not null default '{}'::jsonb,
  pipeline jsonb not null,
  created_at timestamptz not null,
  updated_at timestamptz not null,
  primary key(id, version),
  constraint workflow_definition_versions_definition_fk foreign key(id) references workflow_definitions(id) on delete cascade
);

insert into workflow_definition_versions(id, version, name, description, status, owns, uses, input_shape, pipeline, created_at, updated_at)
select id, version, name, description, status, owns, uses, input_shape, pipeline, created_at, updated_at
from workflow_definitions
on conflict(id, version) do nothing;

create index if not exists workflow_definition_versions_updated_idx on workflow_definition_versions(id, updated_at desc);
