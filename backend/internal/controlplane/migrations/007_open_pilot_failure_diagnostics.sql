alter table open_pilot_issue_states
  add column if not exists failing_gate text,
  add column if not exists operator_summary text,
  add column if not exists recommended_next_action text;
