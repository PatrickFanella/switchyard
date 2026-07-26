-- Inline artifacts remain in the control-plane database. Metadata lets
-- operators enforce size, integrity, and retention without dereferencing a URI.
alter table artifacts add column if not exists storage_class text not null default 'legacy-inline';
alter table artifacts add column if not exists content_type text not null default '';
alter table artifacts add column if not exists byte_size integer not null default 0;
alter table artifacts add column if not exists sha256 text not null default '';
alter table artifacts add column if not exists retention_until timestamptz;
create index if not exists artifacts_retention_until_idx on artifacts(retention_until);

-- Count deprecated compatibility attempts by UTC day. This survives event/run
-- retention and is intentionally aggregate-only (no request content).
create table if not exists compatibility_usage_daily (
  usage_date date not null,
  backend text not null,
  attempts bigint not null default 0 check (attempts >= 0),
  first_attempt_at timestamptz not null,
  last_attempt_at timestamptz not null,
  primary key (usage_date, backend)
);
create index if not exists compatibility_usage_daily_backend_date_idx on compatibility_usage_daily(backend, usage_date desc);
