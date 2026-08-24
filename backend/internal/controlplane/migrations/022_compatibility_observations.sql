-- Zero-use evidence must be an explicit, revision-bound observation. Positive
-- attempt rows from migration 019 remain usage but do not become coverage.
alter table compatibility_usage_daily alter column first_attempt_at drop not null;
alter table compatibility_usage_daily alter column last_attempt_at drop not null;
alter table compatibility_usage_daily add column if not exists source_revision text not null default '';
alter table compatibility_usage_daily add column if not exists deployment_id text not null default '';
alter table compatibility_usage_daily add column if not exists observation_started_at timestamptz;
alter table compatibility_usage_daily add column if not exists observation_completed_at timestamptz;
alter table compatibility_usage_daily add column if not exists observation_segment text not null default '';
alter table compatibility_usage_daily add column if not exists closed_at timestamptz;

create table if not exists compatibility_observer_fence (
    singleton boolean primary key default true check (singleton),
    deployment_id text not null,
    segment_id text not null,
    fence bigint not null
);
