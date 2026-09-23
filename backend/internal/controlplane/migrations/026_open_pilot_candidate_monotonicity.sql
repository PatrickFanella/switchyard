create or replace function switchyard_open_pilot_identity_shadow() returns trigger
language plpgsql as $$
declare
  lock_candidates text[];
  old_candidates text[];
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
    if new.repo ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,99}/[A-Za-z0-9][A-Za-z0-9._-]{0,99}$' then new.canonical_repo := lower(new.repo); end if;
  end if;
  if new.repository_id is null or new.canonical_issue is null then
    new.canonical_issue := null;
    if new.issue ~ '^[0-9]+$' and new.issue !~ '^0+$' then
      begin
        new.canonical_issue := (regexp_replace(new.issue, '^0+', '')::bigint)::text;
        if new.canonical_issue::bigint <= 0 then new.canonical_issue := null; end if;
      exception when numeric_value_out_of_range then new.canonical_issue := null;
      end;
    end if;
  end if;

  if new.repository_id is not null and new.canonical_repo is not null then
    select coalesce(array_agg(distinct lower(value) order by lower(value)), array[]::text[]) into old_candidates
      from unnest(case when tg_op = 'UPDATE' then coalesce(old.canonical_candidates,array[]::text[]) else array[]::text[] end) value;
    if tg_op = 'UPDATE' and exists (
      select 1 from unnest(old_candidates) value
      where not exists (select 1 from unnest(coalesce(new.canonical_candidates,array[]::text[])) new_value where lower(new_value) = lower(value))
    ) then
      raise check_violation using constraint = 'open_pilot_canonical_candidates_monotonic_check';
    end if;
    if exists (
      select 1 from unnest(coalesce(new.canonical_candidates,array[]::text[]) || old_candidates || lower(new.canonical_repo)) value
      where value !~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,99}/[A-Za-z0-9][A-Za-z0-9._-]{0,99}$'
    ) then
      raise check_violation using constraint = 'open_pilot_canonical_candidates_check';
    end if;
    select array_agg(distinct lower(value) order by lower(value)) into lock_candidates
      from unnest(coalesce(new.canonical_candidates,array[]::text[]) || old_candidates || lower(new.canonical_repo)) value;
    new.canonical_candidates := lock_candidates;
  else
    new.canonical_candidates := null;
    if new.canonical_repo is not null then lock_candidates := array[lower(new.canonical_repo)]; end if;
  end if;

  if new.canonical_issue is not null then
    foreach candidate in array coalesce(lock_candidates,array[]::text[]) loop
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

update open_pilot_issue_states set repo = repo;
