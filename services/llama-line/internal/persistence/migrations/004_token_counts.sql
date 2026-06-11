alter table if exists broker.request_event
  add column if not exists prompt_tokens integer not null default 0,
  add column if not exists completion_tokens integer not null default 0;
