package controlplane

import "sort"

type Pipeline struct {
	ID          string         `json:"id"`
	WorkflowID  string         `json:"workflow_id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Steps       []PipelineStep `json:"steps"`
}

type PipelineStep struct {
	ID       string            `json:"id"`
	Position int               `json:"position"`
	Name     string            `json:"name"`
	Executor string            `json:"executor"`
	Profile  string            `json:"profile,omitempty"`
	Model    string            `json:"model,omitempty"`
	Inputs   map[string]string `json:"inputs,omitempty"`
}

type PipelineRegistry interface {
	PipelineForWorkflow(workflowID string) (Pipeline, bool)
	ListPipelines() []Pipeline
}

type StaticPipelineRegistry map[string]Pipeline

func (r StaticPipelineRegistry) PipelineForWorkflow(workflowID string) (Pipeline, bool) {
	pipeline, ok := r[workflowID]
	return pipeline, ok
}

func (r StaticPipelineRegistry) ListPipelines() []Pipeline {
	out := make([]Pipeline, 0, len(r))
	for _, pipeline := range r {
		out = append(out, pipeline)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func DefaultPipelineRegistry() StaticPipelineRegistry {
	return StaticPipelineRegistry{
		string(WorkflowDevlog): {
			ID:          "manual-devlog",
			WorkflowID:  string(WorkflowDevlog),
			Name:        "Manual Devlog",
			Description: "Collect recent run history, generate Markdown, and attach devlog artifact.",
			Steps: []PipelineStep{
				{ID: "collect-history", Position: 1, Name: "devlog.collect-history", Executor: "internal"},
				{ID: "generate-markdown", Position: 2, Name: "devlog.generate-markdown", Executor: "model", Profile: "devlog-summarizer", Model: "${model}"},
				{ID: "attach-artifact", Position: 3, Name: "devlog.attach-artifact", Executor: "internal"},
			},
		},
		string(WorkflowChangemaker): {
			ID:          "changemaker-wrapper",
			WorkflowID:  string(WorkflowChangemaker),
			Name:        "Changemaker Wrapper",
			Description: "Validate and dispatch a Changemaker service run through its HTTP adapter.",
			Steps: []PipelineStep{
				{ID: "dispatch", Position: 1, Name: "changemaker.dispatch", Executor: "http"},
			},
		},
		string(WorkflowOpenPilot): {
			ID:          "open-pilot-prototype",
			WorkflowID:  string(WorkflowOpenPilot),
			Name:        "Open Pilot Prototype",
			Description: "Validate an issue, prepare branch/worktree context, run OpenCode, verify, and prepare a PR.",
			Steps: []PipelineStep{
				{ID: "validate-issue", Position: 1, Name: "open-pilot.validate-issue", Executor: "internal"},
				{ID: "branch-plan", Position: 2, Name: "open-pilot.branch-plan", Executor: "internal"},
				{ID: "prepare-prompt", Position: 3, Name: "open-pilot.prepare-prompt", Executor: "internal"},
				{ID: "agent-work", Position: 4, Name: "open-pilot.agent-work", Executor: "opencode", Profile: "open-pilot-executor", Model: "${model}"},
				{ID: "verify-command", Position: 5, Name: "open-pilot.verify-command", Executor: "command"},
				{ID: "push-branch", Position: 6, Name: "open-pilot.push-branch", Executor: "git"},
				{ID: "pr-plan", Position: 7, Name: "open-pilot.pr-plan", Executor: "gitea"},
				{ID: "ai-review", Position: 8, Name: "open-pilot.ai-review", Executor: "model", Profile: "operator-reviewer", Model: "${model}"},
				{ID: "ci-checks", Position: 9, Name: "open-pilot.ci-checks", Executor: "gitea"},
			},
		},
	}
}
