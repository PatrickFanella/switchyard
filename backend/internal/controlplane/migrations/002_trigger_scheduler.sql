alter table triggers add column if not exists last_run_at timestamptz;
alter table triggers add column if not exists updated_at timestamptz;
alter table triggers add column if not exists last_error text not null default '';

create index if not exists triggers_due_idx on triggers(enabled, next_run_at);
