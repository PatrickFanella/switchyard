alter table runs add column if not exists queued_at timestamptz;
alter table runs add column if not exists available_at timestamptz;
alter table runs add column if not exists claimed_at timestamptz;
alter table runs add column if not exists claimed_by text not null default '';
alter table runs add column if not exists lease_expires_at timestamptz;
alter table runs add column if not exists attempt_count integer not null default 0;
alter table runs add column if not exists max_attempts integer not null default 1;

update runs set queued_at = created_at where queued_at is null;
update runs set available_at = created_at where available_at is null;

alter table runs alter column queued_at set not null;
alter table runs alter column available_at set not null;

create index if not exists runs_queue_idx on runs(status, available_at, created_at, id);
create index if not exists runs_lease_idx on runs(status, lease_expires_at);
