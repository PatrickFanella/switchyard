create extension if not exists pgcrypto;
create extension if not exists pg_trgm;

create table if not exists broker.app_log (
  id bigserial primary key,
  ts timestamptz not null,
  level text not null,
  msg text not null,
  fields jsonb not null default '{}'::jsonb,
  raw_json text not null,
  client text generated always as (fields->>'client') stored,
  request_id text generated always as (fields->>'id') stored,
  model text generated always as (fields->>'model') stored,
  path text generated always as (fields->>'path') stored,
  search_tsv tsvector generated always as (
    to_tsvector('simple', coalesce(level,'') || ' ' || coalesce(msg,'') || ' ' || coalesce(raw_json,''))
  ) stored
);

create table if not exists broker.request_event (
  id uuid primary key default gen_random_uuid(),
  request_id text not null unique,
  ts timestamptz not null,
  client text not null,
  model text not null,
  path text not null,
  upstream text,
  status text not null,
  streaming boolean not null default false,
  duration_ms integer not null default 0,
  queue_ms integer not null default 0,
  upstream_ms integer not null default 0,
  request_body jsonb,
  response_body jsonb,
  request_body_text text,
  response_body_text text,
  request_body_bytes integer not null default 0,
  response_body_bytes integer not null default 0,
  response_json_valid boolean,
  request_message_count integer not null default 0,
  tool_names text[] not null default '{}',
  tool_error_count integer not null default 0,
  tool_errors text[] not null default '{}',
  raw_audit jsonb not null default '{}'::jsonb,
  search_tsv tsvector generated always as (
    to_tsvector('simple', coalesce(client,'') || ' ' || coalesce(model,'') || ' ' || coalesce(path,'') || ' ' || coalesce(status,'') || ' ' || coalesce(request_body_text,'') || ' ' || coalesce(response_body_text,''))
  ) stored
);

create table if not exists broker.request_stats_1m (
  bucket_min timestamptz not null,
  client text not null,
  model text not null,
  path text not null,
  status text not null,
  cnt bigint not null,
  error_count bigint not null,
  tool_error_count bigint not null,
  malformed_count bigint not null,
  cache_hit_count bigint not null,
  p50_ms integer,
  p95_ms integer,
  p99_ms integer,
  queue_p95_ms integer,
  upstream_p95_ms integer,
  primary key (bucket_min, client, model, path, status)
);

create index if not exists app_log_ts_idx on broker.app_log (ts desc);
create index if not exists app_log_level_ts_idx on broker.app_log (level, ts desc);
create index if not exists app_log_client_ts_idx on broker.app_log (client, ts desc) where client is not null;
create index if not exists app_log_request_id_idx on broker.app_log (request_id) where request_id is not null;
create index if not exists app_log_search_idx on broker.app_log using gin (search_tsv);
create index if not exists app_log_raw_trgm_idx on broker.app_log using gin (raw_json gin_trgm_ops);

create index if not exists request_event_ts_idx on broker.request_event (ts desc);
create index if not exists request_event_client_ts_idx on broker.request_event (client, ts desc);
create index if not exists request_event_model_ts_idx on broker.request_event (model, ts desc);
create index if not exists request_event_status_ts_idx on broker.request_event (status, ts desc);
create index if not exists request_event_tool_error_ts_idx on broker.request_event (ts desc) where tool_error_count > 0;
create index if not exists request_event_malformed_ts_idx on broker.request_event (ts desc) where response_json_valid = false;
create index if not exists request_event_search_idx on broker.request_event using gin (search_tsv);
create index if not exists request_event_request_body_idx on broker.request_event using gin (request_body jsonb_path_ops) where request_body is not null;
create index if not exists request_event_response_body_idx on broker.request_event using gin (response_body jsonb_path_ops) where response_body is not null;

create index if not exists request_stats_1m_bucket_idx on broker.request_stats_1m (bucket_min desc);
