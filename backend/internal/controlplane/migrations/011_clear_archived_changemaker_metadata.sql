update workflows
set uses = '[]'::jsonb
where id = 'changemaker' and status = 'archived';
