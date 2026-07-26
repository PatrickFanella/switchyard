alter table runs add column if not exists pipeline_id text not null default '';
alter table runs add column if not exists pipeline_version text not null default '';
alter table runs add column if not exists pipeline_snapshot jsonb;

create index if not exists runs_pipeline_version_idx on runs(pipeline_id, pipeline_version);
