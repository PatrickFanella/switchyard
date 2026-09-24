-- Changemaker review, revision history, and opt-in publication (issue #88).
-- Rows reference runs and artifacts by id without foreign keys so review and
-- publication evidence survives run retention. A thread is identified by the
-- run id of its version 1.
create table if not exists changemaker_draft_versions (
  id text primary key,
  thread_id text not null,
  version integer not null check (version >= 1 and version <= 10),
  run_id text not null,
  parent_version integer,
  sources_run_id text not null,
  source_mode text not null check (source_mode in ('original','same','recollected')),
  revision_notes text not null default '',
  requested_by text not null default '',
  created_at timestamptz not null,
  constraint changemaker_draft_versions_thread_version unique (thread_id, version),
  constraint changemaker_draft_versions_run unique (run_id),
  check ((version = 1) = (parent_version is null)),
  check (parent_version is null or (parent_version >= 1 and parent_version < version)),
  check ((version = 1) = (source_mode = 'original')),
  check (version > 1 or thread_id = run_id)
);

create table if not exists changemaker_review_decisions (
  id text primary key,
  thread_id text not null,
  version integer not null check (version >= 1),
  run_id text not null,
  artifact_id text not null,
  draft_sha256 text not null check (draft_sha256 ~ '^[0-9a-f]{64}$'),
  evidence_sha256 text not null check (evidence_sha256 ~ '^[0-9a-f]{64}$'),
  decision text not null check (decision in ('approved','rejected','revision-requested')),
  notes text not null default '',
  actor text not null,
  adapter text not null,
  destination text not null default '',
  revision_run_id text not null default '',
  created_at timestamptz not null,
  expires_at timestamptz not null,
  check (expires_at > created_at),
  check ((decision = 'revision-requested') = (revision_run_id <> ''))
);

create index if not exists changemaker_review_decisions_thread_idx on changemaker_review_decisions(thread_id, created_at desc, id desc);

create table if not exists changemaker_publication_receipts (
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
  constraint changemaker_publication_receipts_idempotency_key unique (idempotency_key),
  check ((status = 'delivered') = (delivered_at is not null)),
  check ((status = 'sending') = (claim_token <> ''))
);

create index if not exists changemaker_publication_receipts_run_idx on changemaker_publication_receipts(run_id, created_at desc, id desc);
