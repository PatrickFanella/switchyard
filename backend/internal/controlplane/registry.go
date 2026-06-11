package controlplane

func SeedDefaults(store Store) error {
	workflows := []Workflow{
		{ID: "open-pilot", Kind: WorkflowOpenPilot, Name: "Open Pilot", Description: "Gitea issue selection, branch/PR policy, and agent run workflow.", Owns: []string{"gitea labels", "branches", "pull requests"}, Uses: []string{"llama-line", "ocq", "blacktower"}, Status: "planned"},
		{ID: "changemaker", Kind: WorkflowChangemaker, Name: "Changemaker", Description: "Timed activity summarization and publication workflow.", Owns: []string{"change windows", "post generation", "publishing"}, Uses: []string{"llama-line"}, Status: "wrapped"},
		{ID: "devlog", Kind: WorkflowDevlog, Name: "Devlog", Description: "Repository/activity narrative workflow for periodic development logs.", Owns: []string{"activity collection", "devlog prompts", "draft artifacts"}, Uses: []string{"llama-line", "gitea"}, Status: "planned"},
	}
	for _, w := range workflows {
		if err := store.UpsertWorkflow(w); err != nil {
			return err
		}
	}
	triggers := []Trigger{
		{ID: "manual-open-pilot", WorkflowID: "open-pilot", Type: TriggerManual, Name: "Run Open Pilot now", Description: "Manual trigger for issue-to-PR automation.", Enabled: true},
		{ID: "daily-changemaker", WorkflowID: "changemaker", Type: TriggerCron, Name: "Daily Changemaker", Description: "Daily content workflow trigger owned by Switchyard, not Changemaker.", Enabled: false, Schedule: "0 9 * * *"},
		{ID: "gitea-open-pilot-poll", WorkflowID: "open-pilot", Type: TriggerPoll, Name: "Gitea ready-for-agent poll", Description: "Poll Gitea for work ready to become Open Pilot runs.", Enabled: false, Schedule: "*/10 * * * *"},
		{ID: "manual-devlog", WorkflowID: "devlog", Type: TriggerManual, Name: "Generate devlog", Description: "Manual devlog draft generation.", Enabled: true},
	}
	for _, t := range triggers {
		if err := store.UpsertTrigger(t); err != nil {
			return err
		}
	}
	return nil
}
