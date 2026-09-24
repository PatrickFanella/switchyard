package controlplane

func SeedDefaults(store Store) error {
	workflows := []Workflow{
		{ID: "open-pilot", Kind: WorkflowOpenPilot, Name: "Open Pilot", Description: "Gitea issue selection, branch/PR policy, and agent run workflow.", Owns: []string{"gitea labels", "branches", "pull requests"}, Uses: []string{"opencode", "model-broker", "blacktower"}, Status: "active"},
		changemakerDailyDigestWorkflow(),
		{ID: "devlog", Kind: WorkflowDevlog, Name: "Devlog", Description: "Repository/activity narrative workflow for periodic development logs.", Owns: []string{"activity collection", "devlog prompts", "draft artifacts"}, Uses: []string{"model-broker", "gitea"}, Status: "active"},
	}
	existingWorkflows := map[string]struct{}{}
	for _, workflow := range store.ListWorkflows() {
		existingWorkflows[workflow.ID] = struct{}{}
	}
	for _, w := range workflows {
		if _, ok := existingWorkflows[w.ID]; ok {
			continue
		}
		if err := store.UpsertWorkflow(w); err != nil {
			return err
		}
	}
	triggers := []Trigger{
		{ID: "manual-open-pilot", WorkflowID: "open-pilot", Type: TriggerManual, Name: "Run Open Pilot now", Description: "Manual trigger for issue-to-PR automation.", Enabled: true},
		{ID: "daily-changemaker-native", WorkflowID: "changemaker-daily-digest", Type: TriggerCron, Name: "Daily Changemaker Native", Description: "Native workflow-creator Changemaker template.", Enabled: false, Schedule: "0 9 * * *", Inputs: map[string]string{"topic": "daily project activity", "prompt": "Write a concise narrative digest of notable project activity. Include what changed, why it matters, and suggested next steps."}},
		{ID: "gitea-open-pilot-poll", WorkflowID: "open-pilot", Type: TriggerPoll, Name: "Gitea ready-for-agent poll", Description: "Poll Gitea for work ready to become Open Pilot runs.", Enabled: false, Schedule: "*/10 * * * *"},
		{ID: "manual-devlog", WorkflowID: "devlog", Type: TriggerManual, Name: "Generate devlog", Description: "Manual devlog draft generation.", Enabled: true},
	}
	for _, t := range triggers {
		if _, ok := store.Trigger(t.ID); ok {
			continue
		}
		if err := store.UpsertTrigger(t); err != nil {
			return err
		}
	}
	if err := seedChangemakerDailyDigest(store); err != nil {
		return err
	}
	return nil
}
