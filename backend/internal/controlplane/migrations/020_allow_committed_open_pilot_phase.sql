alter table open_pilot_issue_states
  drop constraint open_pilot_issue_states_phase_check;

alter table open_pilot_issue_states
  add constraint open_pilot_issue_states_phase_check check (phase in (
    'requested', 'validated', 'branch_prepared', 'agent_completed', 'verified',
    'committed', 'pushed', 'pr_created', 'ai_reviewed', 'ci_passed',
    'merge_ready', 'merged', 'issue_closed', 'failed', 'paused'
  ));
