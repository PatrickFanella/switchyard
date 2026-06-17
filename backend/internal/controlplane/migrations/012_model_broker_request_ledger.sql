create table if not exists model_broker_requests (
  id text primary key,
  status text not null,
  model text not null,
  path text not null default '',
  client text not null default '',
  request_body jsonb not null default '{}'::jsonb,
  response_body jsonb,
  error text not null default '',
  correlation_ids jsonb not null default '{}'::jsonb,
  created_at timestamptz not null default now(),
  started_at timestamptz,
  finished_at timestamptz,
  updated_at timestamptz not null default now(),
  cancel_requested_at timestamptz
);

create index if not exists idx_model_broker_requests_status_created on model_broker_requests(status, created_at desc);
create index if not exists idx_model_broker_requests_created on model_broker_requests(created_at desc);
