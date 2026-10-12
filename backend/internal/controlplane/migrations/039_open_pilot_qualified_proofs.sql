-- Qualification survives lifecycle cleanup and run retention. No foreign key to
-- disposable run/state rows; only the successful proof-to-allow transaction writes.
create table if not exists open_pilot_qualified_proofs (
    repository_id text not null,
    executor_generation text not null,
    canonical_issue text not null,
    repository_slug text not null,
    qualified_at timestamptz not null default clock_timestamp(),
    primary key (repository_id, executor_generation, canonical_issue, repository_slug)
);
