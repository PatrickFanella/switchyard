update workflows
set uses = '["opencode", "model-broker", "blacktower"]'::jsonb
where id = 'open-pilot';

update workflows
set uses = '["model-broker", "gitea"]'::jsonb
where id = 'devlog';

update workflows
set uses = '["model-broker"]'::jsonb
where id = 'changemaker-daily-digest';
