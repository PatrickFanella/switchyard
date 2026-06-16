delete from triggers where id = 'daily-changemaker';

update workflows
set status = 'archived',
    description = 'Archived legacy Changemaker wrapper. Use changemaker-daily-digest workflow definitions instead.'
where id = 'changemaker';
