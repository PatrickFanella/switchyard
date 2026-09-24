create table if not exists agent_profiles (
  id text primary key,
  active_version integer not null default 0,
  draft_version integer not null default 0,
  latest_version integer not null default 0,
  static_default boolean not null default false,
  created_at timestamptz not null,
  updated_at timestamptz not null,
  check (active_version >= 0 and draft_version >= 0 and latest_version >= active_version and latest_version >= draft_version)
);

create table if not exists agent_profile_versions (
  profile_id text not null,
  version integer not null,
  status text not null check (status in ('draft','active','superseded')),
  name text not null,
  role text not null,
  default_model text not null default '',
  allowed_models jsonb not null default '[]'::jsonb,
  allowed_executors jsonb not null default '[]'::jsonb,
  prompt_contract text not null,
  prompt_contract_sha256 text not null,
  provenance jsonb not null default '{}'::jsonb,
  created_at timestamptz not null,
  updated_at timestamptz not null,
  activated_at timestamptz,
  primary key(profile_id, version),
  constraint agent_profile_versions_profile_fk foreign key(profile_id) references agent_profiles(id) on delete cascade,
  check (version >= 1),
  check (jsonb_typeof(allowed_models) = 'array' and jsonb_typeof(allowed_executors) = 'array')
);

create unique index if not exists agent_profile_versions_active_idx on agent_profile_versions(profile_id) where status='active';

create table if not exists agent_profile_audit (
  id bigserial primary key,
  profile_id text not null,
  version integer not null,
  action text not null check (action in ('draft-saved','activated','rolled-back')),
  actor text not null,
  reason text not null default '',
  prompt_contract_sha256 text not null,
  previous_active_version integer not null default 0,
  created_at timestamptz not null default now()
);

create index if not exists agent_profile_audit_profile_idx on agent_profile_audit(profile_id, id desc);
