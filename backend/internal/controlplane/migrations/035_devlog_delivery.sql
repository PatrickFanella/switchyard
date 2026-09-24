-- Reviewed Devlog delivery (issue #86). Approvals and receipts reference runs
-- and artifacts by id without foreign keys so delivery evidence survives run
-- retention. One receipt row exists per idempotency key.
create table if not exists devlog_delivery_approvals (
  id text primary key,
  run_id text not null,
  artifact_id text not null,
  artifact_sha256 text not null check (artifact_sha256 ~ '^[0-9a-f]{64}$'),
  adapter text not null,
  destination text not null,
  decision text not null check (decision in ('approved','rejected')),
  actor text not null,
  reason text not null default '',
  created_at timestamptz not null,
  expires_at timestamptz not null,
  check (expires_at > created_at)
);

create index if not exists devlog_delivery_approvals_run_idx on devlog_delivery_approvals(run_id, created_at desc, id desc);

create table if not exists devlog_delivery_receipts (
  id text primary key,
  idempotency_key text not null,
  run_id text not null,
  artifact_id text not null,
  artifact_sha256 text not null check (artifact_sha256 ~ '^[0-9a-f]{64}$'),
  adapter text not null,
  destination text not null,
  approval_id text not null,
  status text not null check (status in ('sending','delivered','failed')),
  attempts integer not null default 1 check (attempts >= 1),
  reconciled boolean not null default false,
  claim_token text not null default '',
  external_id text not null default '',
  external_url text not null default '',
  last_error text not null default '',
  requested_by text not null,
  created_at timestamptz not null,
  updated_at timestamptz not null,
  delivered_at timestamptz,
  constraint devlog_delivery_receipts_idempotency_key unique (idempotency_key),
  check ((status = 'delivered') = (delivered_at is not null)),
  check ((status = 'sending') = (claim_token <> ''))
);

create index if not exists devlog_delivery_receipts_run_idx on devlog_delivery_receipts(run_id, created_at desc, id desc);
