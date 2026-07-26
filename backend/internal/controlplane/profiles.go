package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

type AgentProfile struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Role         string `json:"role"`
	DefaultModel string `json:"default_model"`
	// AllowedModels is optional. An empty list preserves compatibility with
	// provider/model policies owned by the workflow executor.
	AllowedModels    []string `json:"allowed_models,omitempty"`
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
		"content-writer": {
			ID:               "content-writer",
			Name:             "Content Writer",
			Role:             "writer",
			DefaultModel:     "gpt-5.4",
			PromptContract:   "Write clear, accurate Markdown grounded only in the supplied workflow inputs. Distinguish facts from recommendations.",
			AllowedExecutors: []string{"model"},
		},
		"devlog-summarizer": {
			ID:               "devlog-summarizer",
			Name:             "Devlog Summarizer",
			Role:             "summarizer",
			DefaultModel:     "gpt-5.4",
			PromptContract:   "Produce concise Markdown grounded only in Switchyard run history and artifacts.",
			AllowedExecutors: []string{"model"},
		},
		"open-pilot-executor": {
			ID:               "open-pilot-executor",
			Name:             "Open Pilot Executor",
			Role:             "executor",
			DefaultModel:     "gpt-5.4",
			PromptContract:   "Implement the smallest safe issue-scoped change, respect repository conventions, and report verification with file references.",
			AllowedExecutors: []string{"opencode", "command", "git", "gitea"},
		},
		"operator-reviewer": {
			ID:               "operator-reviewer",
			Name:             "Operator Reviewer",
			Role:             "reviewer",
			DefaultModel:     "gpt-5.4",
			PromptContract:   "Review the completed Open Pilot work for safety, completeness, and operator handoff clarity. Reply exactly with APPROVED: <one paragraph> or CHANGES_REQUESTED: <one paragraph>.",
			AllowedExecutors: []string{"model", "opencode"},
		},
	}
}

func profileAllowsExecutor(profile AgentProfile, executor string) bool {
	executor = strings.TrimSpace(executor)
	for _, allowed := range profile.AllowedExecutors {
		if strings.EqualFold(strings.TrimSpace(allowed), executor) {
			return true
		}
	}
	return false
}

func profileAllowsModel(profile AgentProfile, model string) bool {
	if len(profile.AllowedModels) == 0 {
		return true
	}
	model = strings.TrimSpace(model)
	for _, allowed := range profile.AllowedModels {
		if strings.EqualFold(strings.TrimSpace(allowed), model) {
			return true
		}
	}
	return false
}

func profileContractHash(profile AgentProfile) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(profile.PromptContract)))
	return hex.EncodeToString(sum[:])
}

// applyPipelineProfilePrompt adds the profile contract as an explicit system
// boundary without allowing the workflow prompt to overwrite it.
func applyPipelineProfilePrompt(step PipelineStep, prompt string) string {
	contract := strings.TrimSpace(step.profileContract)
	if contract == "" {
		return prompt
	}
	return "Profile contract:\n" + contract + "\n\nWorkflow request:\n" + prompt
}
