create extension if not exists vector;

create table if not exists broker.request_embedding_job (
  request_event_id uuid primary key references broker.request_event(id) on delete cascade,
  status text not null default 'pending',
  attempts integer not null default 0,
  last_error text,
  available_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create table if not exists broker.request_embedding (
  id bigserial primary key,
  request_event_id uuid not null references broker.request_event(id) on delete cascade,
  ts timestamptz not null default now(),
  chunk_ix integer not null,
  role text not null,
  content text not null,
  embedding_model text not null,
  embedding_dim integer not null,
  embedding vector(768) not null,
  meta jsonb not null default '{}'::jsonb,
  unique (request_event_id, chunk_ix, embedding_model)
);

create index if not exists request_embedding_event_idx on broker.request_embedding (request_event_id);
create index if not exists request_embedding_ts_idx on broker.request_embedding (ts desc);
create index if not exists request_embedding_hnsw_idx on broker.request_embedding using hnsw (embedding vector_cosine_ops);
