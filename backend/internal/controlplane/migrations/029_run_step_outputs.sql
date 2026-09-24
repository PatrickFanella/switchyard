-- Durable named step outputs. Values are bounded, credential-shaped names are
-- rejected before persistence, and the map is cleared whenever a step attempt
-- restarts so only succeeded attempts publish outputs.
alter table run_steps add column if not exists outputs jsonb not null default '{}'::jsonb;

alter table run_steps drop constraint if exists run_steps_outputs_object;
alter table run_steps add constraint run_steps_outputs_object check (jsonb_typeof(outputs) = 'object');
