update workflows
set status = 'active'
where id in ('open-pilot', 'devlog')
  and status = 'planned';
