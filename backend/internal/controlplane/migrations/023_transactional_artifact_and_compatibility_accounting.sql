-- Replace application-managed artifact accounting with database-owned
-- accounting. Block artifact mutations while capturing the new trustworthy
-- retained floor and installing triggers, then restart lifecycle observation.
lock table artifacts in share row exclusive mode;

alter table artifact_lifecycle_totals add column if not exists observation_retained_floor_count bigint;
alter table artifact_lifecycle_totals add column if not exists observation_retained_floor_bytes bigint;
alter table artifact_lifecycle_totals add column if not exists observation_retained_floor_bytes_unmeasured bigint;
alter table artifact_lifecycle_totals add column if not exists retained_count bigint;
alter table artifact_lifecycle_totals add column if not exists retained_bytes bigint;
alter table artifact_lifecycle_totals add column if not exists retained_bytes_unmeasured bigint;

with retained as (
  select count(*) as count,
         coalesce(sum(byte_size) filter (where storage_class = 'inline-data-uri'), 0) as bytes,
         count(*) filter (where storage_class <> 'inline-data-uri') as unmeasured
  from artifacts
)
update artifact_lifecycle_totals totals
set observation_started_at = clock_timestamp(),
    observation_retained_floor_count = retained.count,
    observation_retained_floor_bytes = retained.bytes,
    observation_retained_floor_bytes_unmeasured = retained.unmeasured,
    retained_count = retained.count,
    retained_bytes = retained.bytes,
    retained_bytes_unmeasured = retained.unmeasured,
    created_count = 0,
    created_bytes = 0,
    pruned_count = 0,
    pruned_bytes = 0,
    pruned_bytes_unmeasured = 0
from retained
where totals.singleton = true;

truncate artifact_lifecycle_daily;

alter table artifact_lifecycle_totals alter column observation_retained_floor_count set not null;
alter table artifact_lifecycle_totals alter column observation_retained_floor_bytes set not null;
alter table artifact_lifecycle_totals alter column observation_retained_floor_bytes_unmeasured set not null;
alter table artifact_lifecycle_totals alter column retained_count set not null;
alter table artifact_lifecycle_totals alter column retained_bytes set not null;
alter table artifact_lifecycle_totals alter column retained_bytes_unmeasured set not null;
alter table artifact_lifecycle_totals add check (observation_retained_floor_count >= 0);
alter table artifact_lifecycle_totals add check (observation_retained_floor_bytes >= 0);
alter table artifact_lifecycle_totals add check (observation_retained_floor_bytes_unmeasured >= 0);
alter table artifact_lifecycle_totals add check (retained_count >= 0);
alter table artifact_lifecycle_totals add check (retained_bytes >= 0);
alter table artifact_lifecycle_totals add check (retained_bytes_unmeasured >= 0);

create or replace function account_artifact_insert() returns trigger language plpgsql as $$
declare
  measured_bytes bigint := case when new.storage_class = 'inline-data-uri' then new.byte_size else 0 end;
  unmeasured bigint := case when new.storage_class = 'inline-data-uri' then 0 else 1 end;
begin
  update artifact_lifecycle_totals
  set retained_count = retained_count + 1,
      retained_bytes = retained_bytes + measured_bytes,
      retained_bytes_unmeasured = retained_bytes_unmeasured + unmeasured
  where singleton = true;
  if current_setting('switchyard.artifact_lifecycle_accounting', true) = 'trigger' then
    update artifact_lifecycle_totals
    set created_count = created_count + 1,
        created_bytes = created_bytes + measured_bytes
    where singleton = true;
    insert into artifact_lifecycle_daily(measurement_date, created_count, created_bytes)
    values((new.created_at at time zone 'UTC')::date, 1, measured_bytes)
    on conflict (measurement_date) do update
    set created_count = artifact_lifecycle_daily.created_count + 1,
        created_bytes = artifact_lifecycle_daily.created_bytes + excluded.created_bytes;
  end if;
  return new;
end;
$$;

create or replace function account_artifact_delete() returns trigger language plpgsql as $$
declare
  measured_bytes bigint := case when old.storage_class = 'inline-data-uri' then old.byte_size else 0 end;
  unmeasured bigint := case when old.storage_class = 'inline-data-uri' then 0 else 1 end;
  pruned_at timestamptz := clock_timestamp();
begin
  update artifact_lifecycle_totals
  set retained_count = retained_count - 1,
      retained_bytes = retained_bytes - measured_bytes,
      retained_bytes_unmeasured = retained_bytes_unmeasured - unmeasured
  where singleton = true;
  if current_setting('switchyard.artifact_lifecycle_accounting', true) = 'trigger' then
    update artifact_lifecycle_totals
    set pruned_count = pruned_count + 1,
        pruned_bytes = pruned_bytes + measured_bytes,
        pruned_bytes_unmeasured = pruned_bytes_unmeasured + unmeasured
    where singleton = true;
    insert into artifact_lifecycle_daily(measurement_date, pruned_count, pruned_bytes, pruned_bytes_unmeasured)
    values((pruned_at at time zone 'UTC')::date, 1, measured_bytes, unmeasured)
    on conflict (measurement_date) do update
    set pruned_count = artifact_lifecycle_daily.pruned_count + 1,
        pruned_bytes = artifact_lifecycle_daily.pruned_bytes + excluded.pruned_bytes,
        pruned_bytes_unmeasured = artifact_lifecycle_daily.pruned_bytes_unmeasured + excluded.pruned_bytes_unmeasured;
  end if;
  return old;
end;
$$;

create or replace function account_artifact_update() returns trigger language plpgsql as $$
declare
  old_measured_bytes bigint := case when old.storage_class = 'inline-data-uri' then old.byte_size else 0 end;
  new_measured_bytes bigint := case when new.storage_class = 'inline-data-uri' then new.byte_size else 0 end;
  old_unmeasured bigint := case when old.storage_class = 'inline-data-uri' then 0 else 1 end;
  new_unmeasured bigint := case when new.storage_class = 'inline-data-uri' then 0 else 1 end;
begin
  update artifact_lifecycle_totals
  set retained_bytes = retained_bytes - old_measured_bytes + new_measured_bytes,
      retained_bytes_unmeasured = retained_bytes_unmeasured - old_unmeasured + new_unmeasured
  where singleton = true;
  return new;
end;
$$;

drop trigger if exists artifacts_account_insert on artifacts;
create trigger artifacts_account_insert after insert on artifacts for each row execute function account_artifact_insert();
drop trigger if exists artifacts_account_delete on artifacts;
create trigger artifacts_account_delete after delete on artifacts for each row execute function account_artifact_delete();
drop trigger if exists artifacts_account_update on artifacts;
create trigger artifacts_account_update after update of storage_class, byte_size on artifacts for each row execute function account_artifact_update();

-- Preserve migration 019's non-null positive-use reader contract. Copy explicit
-- coverage from the old combined table, remove zero-use rows, then return the
-- usage table to its pre-022 shape.
create table if not exists compatibility_observation_daily (
    usage_date date not null,
    backend text not null,
    source_revision text not null,
    deployment_id text not null,
    observation_started_at timestamptz not null,
    observation_completed_at timestamptz not null,
    observation_segment text not null,
    closed_at timestamptz,
    primary key (usage_date, backend)
);

insert into compatibility_observation_daily(usage_date, backend, source_revision, deployment_id, observation_started_at, observation_completed_at, observation_segment, closed_at)
select usage_date, backend, source_revision, deployment_id, observation_started_at, observation_completed_at, observation_segment, closed_at
from compatibility_usage_daily
where observation_started_at is not null and observation_completed_at is not null
on conflict (usage_date, backend) do update
set source_revision = excluded.source_revision,
    deployment_id = excluded.deployment_id,
    observation_started_at = excluded.observation_started_at,
    observation_completed_at = excluded.observation_completed_at,
    observation_segment = excluded.observation_segment,
    closed_at = excluded.closed_at;

delete from compatibility_usage_daily where attempts = 0;
alter table compatibility_usage_daily alter column first_attempt_at set not null;
alter table compatibility_usage_daily alter column last_attempt_at set not null;
alter table compatibility_usage_daily drop column if exists source_revision;
alter table compatibility_usage_daily drop column if exists deployment_id;
alter table compatibility_usage_daily drop column if exists observation_started_at;
alter table compatibility_usage_daily drop column if exists observation_completed_at;
alter table compatibility_usage_daily drop column if exists observation_segment;
alter table compatibility_usage_daily drop column if exists closed_at;

create index if not exists compatibility_observation_daily_identity_date_idx
    on compatibility_observation_daily(source_revision, deployment_id, usage_date);
