package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

func clonePipeline(pipeline Pipeline) Pipeline {
	out := pipeline
	out.Steps = append([]PipelineStep(nil), pipeline.Steps...)
	for i := range out.Steps {
		out.Steps[i].Inputs = cloneMap(out.Steps[i].Inputs)
	}
	if pipeline.Profiles != nil {
		out.Profiles = make(map[string]AgentProfile, len(pipeline.Profiles))
		for id, profile := range pipeline.Profiles {
			out.Profiles[id] = cloneAgentProfile(profile)
		}
	}
	return out
}

// pipelineVersion identifies the pipeline shape. The profile policy snapshot
// is deliberately excluded so runs of the same pipeline share a version even
// when the effective profile version differs; profile identity is recorded on
// run-step correlation IDs instead.
func pipelineVersion(pipeline Pipeline) string {
	pipeline.Profiles = nil
	encoded, _ := json.Marshal(pipeline)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:8])
}

type Pipeline struct {
	ID          string         `json:"id"`
	WorkflowID  string         `json:"workflow_id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Steps       []PipelineStep `json:"steps"`
	// Profiles holds the effective agent-profile policy captured when a run
	// snapshot is created. Registry pipelines leave it empty.
	Profiles map[string]AgentProfile `json:"profiles,omitempty"`
}

type PipelineStep struct {
	ID              string            `json:"id"`
	Position        int               `json:"position"`
	Name            string            `json:"name"`
	Executor        string            `json:"executor"`
	Profile         string            `json:"profile,omitempty"`
	Model           string            `json:"model,omitempty"`
	Inputs          map[string]string `json:"inputs,omitempty"`
	profileContract string
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

type StoreBackedPipelineRegistry struct {
	store    Store
	fallback PipelineRegistry
}

func NewStoreBackedPipelineRegistry(store Store, fallback PipelineRegistry) StoreBackedPipelineRegistry {
	return StoreBackedPipelineRegistry{store: store, fallback: fallback}
}

func (r StoreBackedPipelineRegistry) PipelineForWorkflow(workflowID string) (Pipeline, bool) {
	if r.store != nil {
		if definition, err := r.store.WorkflowDefinition(workflowID); err == nil && isActiveWorkflowDefinition(definition) {
			return pipelineFromDefinition(definition), true
		}
	}
	if r.fallback == nil {
		return Pipeline{}, false
	}
	return r.fallback.PipelineForWorkflow(workflowID)
}

func (r StoreBackedPipelineRegistry) ListPipelines() []Pipeline {
	seen := map[string]struct{}{}
	out := make([]Pipeline, 0)
	if r.store != nil {
		if definitions, err := r.store.ListWorkflowDefinitions(); err == nil {
			for _, definition := range definitions {
				if !isActiveWorkflowDefinition(definition) {
					continue
				}
				pipeline := pipelineFromDefinition(definition)
				out = append(out, pipeline)
				seen[pipeline.WorkflowID] = struct{}{}
			}
		}
	}
	if r.fallback != nil {
		for _, pipeline := range r.fallback.ListPipelines() {
			if _, ok := seen[pipeline.WorkflowID]; ok {
				continue
			}
			out = append(out, pipeline)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func pipelineFromDefinition(definition WorkflowDefinition) Pipeline {
	steps := make([]PipelineStep, 0, len(definition.Pipeline.Steps))
	for _, step := range definition.Pipeline.Steps {
		steps = append(steps, PipelineStep{ID: step.ID, Position: step.Position, Name: step.Name, Executor: step.Executor, Profile: step.Profile, Model: step.Model, Inputs: cloneMap(step.Inputs)})
	}
	sort.Slice(steps, func(i, j int) bool { return steps[i].Position < steps[j].Position })
	workflowID := definition.Pipeline.WorkflowID
	if workflowID == "" {
		workflowID = definition.ID
	}
	return Pipeline{ID: definition.Pipeline.ID, WorkflowID: workflowID, Name: definition.Pipeline.Name, Description: definition.Pipeline.Description, Steps: steps}
}

func isActiveWorkflowDefinition(definition WorkflowDefinition) bool {
	return strings.EqualFold(strings.TrimSpace(definition.Status), "active")
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
				{ID: "commit-changes", Position: 6, Name: "open-pilot.commit-changes", Executor: "git"},
				{ID: "push-branch", Position: 7, Name: "open-pilot.push-branch", Executor: "git"},
				{ID: "pr-plan", Position: 8, Name: "open-pilot.pr-plan", Executor: "gitea"},
				{ID: "ai-review", Position: 9, Name: "open-pilot.ai-review", Executor: "model", Profile: "operator-reviewer", Model: "${model}"},
				{ID: "ci-checks", Position: 10, Name: "open-pilot.ci-checks", Executor: "gitea"},
				{ID: "merge-pr", Position: 11, Name: "open-pilot.merge-pr", Executor: "gitea"},
				{ID: "close-issue", Position: 12, Name: "open-pilot.close-issue", Executor: "gitea"},
			},
		},
	}
}
