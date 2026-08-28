alter table open_pilot_issue_states
  add column if not exists repository_id text,
  add column if not exists executor_generation text,
  add column if not exists canonical_repo text,
  add column if not exists canonical_issue text,
  add column if not exists canonical_candidates text[],
  add column if not exists legacy_repo text,
  add column if not exists legacy_issue text;

alter table open_pilot_issue_states
  drop constraint if exists open_pilot_issue_states_affinity_check;
alter table open_pilot_issue_states
  add constraint open_pilot_issue_states_affinity_check check (
    (repository_id is null and executor_generation is null) or
    (repository_id is not null and executor_generation is not null and
     repository_id ~ '^[a-z0-9][a-z0-9-]{0,31}$' and executor_generation ~ '^[a-z0-9][a-z0-9-]{0,31}$')
  );

create or replace function switchyard_open_pilot_identity_shadow() returns trigger
language plpgsql as $$
declare
  lock_candidates text[];
  candidate text;
begin
  if tg_op = 'UPDATE' and old.repository_id is not null and
     (new.repository_id is distinct from old.repository_id or new.executor_generation is distinct from old.executor_generation) then
    raise unique_violation using constraint = 'open_pilot_canonical_reservation';
  end if;
  if new.legacy_repo is null then new.legacy_repo := new.repo; end if;
  if new.legacy_issue is null then new.legacy_issue := new.issue; end if;

  if new.repository_id is null or new.canonical_repo is null then
    new.canonical_repo := null;
    if new.repo ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,99}/[A-Za-z0-9][A-Za-z0-9._-]{0,99}$' then
      new.canonical_repo := lower(new.repo);
    end if;
  end if;
  if new.repository_id is null or new.canonical_issue is null then
    new.canonical_issue := null;
    if new.issue ~ '^[0-9]+$' and new.issue !~ '^0+$' then
      begin
        new.canonical_issue := (regexp_replace(new.issue, '^0+', '')::bigint)::text;
        if new.canonical_issue::bigint <= 0 then new.canonical_issue := null; end if;
      exception when numeric_value_out_of_range then
        new.canonical_issue := null;
      end;
    end if;
  end if;

  if new.repository_id is not null and new.canonical_repo is not null then
    if new.canonical_candidates is null then
      new.canonical_candidates := array[lower(new.canonical_repo)];
    end if;
    if exists (
      select 1 from unnest(new.canonical_candidates || lower(new.canonical_repo)) value
      where value !~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,99}/[A-Za-z0-9][A-Za-z0-9._-]{0,99}$'
    ) then
      raise check_violation using constraint = 'open_pilot_canonical_candidates_check';
    end if;
    select array_agg(distinct lower(value) order by lower(value)) into lock_candidates
      from unnest(new.canonical_candidates || lower(new.canonical_repo)) value;
    new.canonical_candidates := lock_candidates;
  else
    new.canonical_candidates := null;
    if new.canonical_repo is not null then
      lock_candidates := array[lower(new.canonical_repo)];
    end if;
  end if;

  if new.canonical_issue is not null then
    foreach candidate in array coalesce(lock_candidates, array[]::text[]) loop
      perform pg_advisory_xact_lock(hashtextextended(lower(candidate) || '#' || new.canonical_issue, 0));
    end loop;
  end if;

  if new.canonical_issue is not null and cardinality(lock_candidates) > 0 and exists (
    select 1 from open_pilot_issue_states existing
    where existing.id <> new.id and existing.canonical_issue = new.canonical_issue and (
      (new.repository_id is not null and lower(existing.canonical_repo) = any(lock_candidates)) or
      (new.repository_id is not null and existing.canonical_candidates && lock_candidates) or
      (new.repository_id is null and existing.repository_id is not null and existing.canonical_candidates && lock_candidates)
    )
  ) then
    raise unique_violation using constraint = 'open_pilot_canonical_reservation';
  end if;
  return new;
end;
$$;

drop trigger if exists open_pilot_issue_states_identity_shadow on open_pilot_issue_states;
create trigger open_pilot_issue_states_identity_shadow
before insert or update of repo, issue, repository_id, executor_generation, legacy_repo, legacy_issue, canonical_repo, canonical_issue, canonical_candidates
on open_pilot_issue_states for each row execute function switchyard_open_pilot_identity_shadow();

update open_pilot_issue_states set repo = repo;

create index if not exists open_pilot_issue_states_canonical_candidate_idx
  on open_pilot_issue_states(canonical_repo, canonical_issue)
  where canonical_repo is not null and canonical_issue is not null;

create index if not exists open_pilot_issue_states_canonical_candidates_idx
  on open_pilot_issue_states using gin(canonical_candidates)
  where repository_id is not null;
