-- Durable aggregate measurement starts when this migration is applied. Existing
-- artifacts appear only in the retained snapshot; lifecycle totals start at zero.
create table if not exists artifact_lifecycle_totals (
  singleton boolean primary key default true check (singleton),
  observation_started_at timestamptz not null,
  created_count bigint not null default 0 check (created_count >= 0),
  created_bytes bigint not null default 0 check (created_bytes >= 0),
  pruned_count bigint not null default 0 check (pruned_count >= 0),
  pruned_bytes bigint not null default 0 check (pruned_bytes >= 0),
  pruned_bytes_unmeasured bigint not null default 0 check (pruned_bytes_unmeasured >= 0)
);

insert into artifact_lifecycle_totals(singleton, observation_started_at)
values(true, now())
on conflict (singleton) do nothing;

create table if not exists artifact_lifecycle_daily (
  measurement_date date primary key,
  created_count bigint not null default 0 check (created_count >= 0),
  created_bytes bigint not null default 0 check (created_bytes >= 0),
  pruned_count bigint not null default 0 check (pruned_count >= 0),
  pruned_bytes bigint not null default 0 check (pruned_bytes >= 0),
  pruned_bytes_unmeasured bigint not null default 0 check (pruned_bytes_unmeasured >= 0)
);
