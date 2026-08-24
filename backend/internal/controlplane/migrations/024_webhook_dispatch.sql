create table if not exists webhook_trigger_auth (
  trigger_id text primary key references triggers(id) on delete cascade,
  revision text not null,
  updated_at timestamptz not null default now()
);

create table if not exists webhook_auth_config (
  singleton boolean primary key default true check (singleton),
  owner text not null,
  generation bigint not null check (generation >= 0),
  digest text not null check (digest = '' or digest ~ '^[0-9a-f]{64}$'),
  receipt_retention_seconds bigint not null check (receipt_retention_seconds between 86400 and 31622400),
  updated_at timestamptz not null default now()
);

insert into webhook_auth_config(singleton,owner,generation,digest,receipt_retention_seconds)
values(true,'',0,'',2592000)
on conflict(singleton) do nothing;

create table if not exists webhook_receipt_metrics (
  singleton boolean primary key default true check (singleton),
  receipts_pruned bigint not null default 0 check (receipts_pruned >= 0)
);

insert into webhook_receipt_metrics(singleton) values(true) on conflict(singleton) do nothing;

create table if not exists webhook_trigger_auth_keys (
  trigger_id text not null references webhook_trigger_auth(trigger_id) on delete cascade,
  key_id text not null,
  not_before timestamptz not null,
  not_after timestamptz not null,
  primary key(trigger_id, key_id),
  check (not_after > not_before)
);

create table if not exists webhook_deliveries (
  trigger_id text not null references triggers(id) on delete cascade,
  delivery_id text not null,
  body_sha256 text not null,
  run_id text not null,
  accepted_at timestamptz not null,
  primary key(trigger_id, delivery_id),
  unique(run_id),
  check (body_sha256 ~ '^[0-9a-f]{64}$')
);

create index if not exists webhook_deliveries_accepted_at_idx on webhook_deliveries(accepted_at);
