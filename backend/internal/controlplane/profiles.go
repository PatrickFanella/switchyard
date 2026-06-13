package controlplane

import "sort"

type AgentProfile struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Role             string   `json:"role"`
	DefaultModel     string   `json:"default_model"`
	PromptContract   string   `json:"prompt_contract"`
	AllowedExecutors []string `json:"allowed_executors"`
}

type AgentProfileRegistry interface {
	Profile(id string) (AgentProfile, bool)
	ListProfiles() []AgentProfile
}

type StaticAgentProfileRegistry map[string]AgentProfile

func (r StaticAgentProfileRegistry) Profile(id string) (AgentProfile, bool) {
	profile, ok := r[id]
	return profile, ok
}

func (r StaticAgentProfileRegistry) ListProfiles() []AgentProfile {
	out := make([]AgentProfile, 0, len(r))
	for _, profile := range r {
		out = append(out, profile)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func DefaultAgentProfileRegistry() StaticAgentProfileRegistry {
	return StaticAgentProfileRegistry{
		"devlog-summarizer": {
			ID:               "devlog-summarizer",
			Name:             "Devlog Summarizer",
			Role:             "summarizer",
			DefaultModel:     "opencode:gpt-5.5",
			PromptContract:   "Produce concise Markdown grounded only in Switchyard run history and artifacts.",
			AllowedExecutors: []string{"model"},
		},
		"open-pilot-executor": {
			ID:               "open-pilot-executor",
			Name:             "Open Pilot Executor",
			Role:             "executor",
			DefaultModel:     "opencode:gpt-5.5",
			PromptContract:   "Implement the smallest safe issue-scoped change, respect repository conventions, and report verification with file references.",
			AllowedExecutors: []string{"opencode", "command", "git", "gitea"},
		},
		"operator-reviewer": {
			ID:               "operator-reviewer",
			Name:             "Operator Reviewer",
			Role:             "reviewer",
			DefaultModel:     "opencode:gpt-5.5",
			PromptContract:   "Review workflow output for safety, completeness, and operator handoff clarity.",
			AllowedExecutors: []string{"model", "opencode"},
		},
	}
}
