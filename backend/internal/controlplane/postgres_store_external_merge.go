package controlplane

func (s *PostgresStore) ReconcileOpenPilotExternalMerge(expected OpenPilotIssueState, pr GiteaPullRequest, issue GiteaIssue) (OpenPilotIssueState, error) {
	ctx := s.operationContext()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OpenPilotIssueState{}, err
	}
	defer tx.Rollback()
	current, err := scanOpenPilotIssueStateRow(tx.QueryRowContext(ctx, `select `+openPilotIssueStateColumns+` from open_pilot_issue_states where repo=$1 and issue=$2 for update`, expected.Repo, expected.Issue))
	if err != nil {
		return OpenPilotIssueState{}, mapScanErr(err)
	}
	run, err := scanRunRow(tx.QueryRowContext(ctx, `select `+postgresRunColumns+` from runs where id=$1 for update`, current.RunID))
	if err != nil {
		return OpenPilotIssueState{}, mapScanErr(err)
	}
	var active bool
	if err := tx.QueryRowContext(ctx, `select exists(select 1 from runs where workflow_id='open-pilot' and lower(inputs->>'repo')=lower($1) and inputs->>'issue'=$2 and status in ('queued','running'))`, current.Repo, current.Issue).Scan(&active); err != nil {
		return OpenPilotIssueState{}, err
	}
	if active {
		return OpenPilotIssueState{}, ErrConflict
	}
	now := s.now().UTC()
	state, message, err := reconciledOpenPilotExternalMerge(current, expected, run, pr, issue, now)
	if err != nil {
		return OpenPilotIssueState{}, err
	}
	if message == "" {
		return state, tx.Commit()
	}
	gateJSON, err := marshalOpenPilotJSONMap(map[string]string(state.GateStatus))
	if err != nil {
		return OpenPilotIssueState{}, err
	}
	resumeJSON, err := marshalOpenPilotJSONMap(state.ResumeInputs)
	if err != nil {
		return OpenPilotIssueState{}, err
	}
	saved, err := scanOpenPilotIssueStateRow(tx.QueryRowContext(ctx, `update open_pilot_issue_states set phase=$3, merge_commit_sha=$4, issue_closed=$5, gate_status=$6::jsonb, resume_inputs=$7::jsonb, last_error='', failing_gate=null, operator_summary=$8, recommended_next_action=$9, updated_at=$10 where repo=$1 and issue=$2 returning `+openPilotIssueStateColumns, state.Repo, state.Issue, state.Phase, state.MergeCommitSHA, state.IssueClosed, string(gateJSON), string(resumeJSON), state.OperatorSummary, state.RecommendedNextAction, now))
	if err != nil {
		return OpenPilotIssueState{}, mapScanErr(err)
	}
	if _, err := tx.ExecContext(ctx, `insert into run_events(id,run_id,type,message,created_at) values($1,$2,$3,$4,$5)`, newID("evt"), run.ID, "open-pilot.external-merge.reconciled", message, now); err != nil {
		return OpenPilotIssueState{}, err
	}
	if err := tx.Commit(); err != nil {
		return OpenPilotIssueState{}, err
	}
	return saved, nil
}
