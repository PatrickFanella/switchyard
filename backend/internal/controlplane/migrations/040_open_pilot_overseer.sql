create table if not exists open_pilot_overseer (
  singleton boolean primary key default true check (singleton),
  revision bigint not null default 0,
  state jsonb not null,
  updated_at timestamptz not null default clock_timestamp()
);
insert into open_pilot_overseer(singleton, state) values (true,
  '{"config":{"enabled":false,"queue_enabled":true,"recovery_enabled":true,"max_open_lanes":3,"interval_seconds":900},"ready_seen":{},"incidents":{},"history":[]}'::jsonb)
on conflict (singleton) do nothing;
