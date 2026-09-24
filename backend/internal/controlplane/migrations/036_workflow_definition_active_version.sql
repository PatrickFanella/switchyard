-- Track the version new runs use separately from the latest saved version, so
-- saving a draft appends an immutable version without taking the active
-- pipeline out of service. Existing active definitions keep their current
-- version active; definitions stored as drafts stay without an active version.
alter table workflow_definitions add column if not exists active_version integer;

update workflow_definitions
set active_version = version
where status = 'active' and active_version is null;

alter table workflow_definitions drop constraint if exists workflow_definitions_active_version_check;
alter table workflow_definitions add constraint workflow_definitions_active_version_check
  check (active_version is null or (active_version >= 1 and active_version <= version));
