create table if not exists open_pilot_rollout_control (
    singleton boolean primary key default true check (singleton),
    mode text not null check (mode in ('legacy','disabled','proof','allow')),
    registry_fingerprint text not null default '',
    proof_targets jsonb not null default '[]'::jsonb,
    expires_at timestamptz,
    closed_reason text not null default '',
    generation bigint not null default 0,
    updated_at timestamptz not null default now(),
    check (jsonb_typeof(proof_targets) = 'array')
);

insert into open_pilot_rollout_control(singleton, mode)
values (true, 'legacy')
on conflict (singleton) do nothing;

create or replace function close_failed_open_pilot_proof() returns trigger
language plpgsql as $$
begin
    if new.phase in ('failed', 'paused') then
        update open_pilot_rollout_control control
        set mode='disabled',
            closed_reason='proof target entered ' || new.phase,
            generation=control.generation+1,
            updated_at=clock_timestamp()
        where control.singleton
          and control.mode='proof'
          and exists (
              select 1
              from jsonb_array_elements(control.proof_targets) target
              where target->>'repository_id'=new.repository_id
                and target->>'executor_generation'=new.executor_generation
                and target->>'issue'=new.canonical_issue
          );
    end if;
    return new;
end;
$$;

drop trigger if exists open_pilot_proof_fail_closed on open_pilot_issue_states;
create trigger open_pilot_proof_fail_closed
after insert or update of phase on open_pilot_issue_states
for each row execute function close_failed_open_pilot_proof();
