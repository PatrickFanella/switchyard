package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Agent profile status and source values. Static defaults are compiled into the
// binary and never change at runtime; persisted versions move draft -> active ->
// superseded and are never rewritten once created.
const (
	AgentProfileStatusStatic     = "static"
	AgentProfileStatusDraft      = "draft"
	AgentProfileStatusActive     = "active"
	AgentProfileStatusSuperseded = "superseded"

	AgentProfileSourceStatic    = "static"
	AgentProfileSourcePersisted = "persisted"

	AgentProfileOriginStaticDefault = "static-default"
	AgentProfileOriginOperator      = "operator"
	AgentProfileOriginRollback      = "rollback"

	AgentProfileAuditDraftSaved = "draft-saved"
	AgentProfileAuditActivated  = "activated"
	AgentProfileAuditRolledBack = "rolled-back"

	maxAgentProfileNameLength     = 128
	maxAgentProfileModelLength    = 128
	maxAgentProfileContractLength = 16384
	maxAgentProfileNoteLength     = 1024
	defaultAgentProfileAuditLimit = 100
)

var agentProfileIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// knownPipelineExecutors is the closed set of executor kinds a profile may
// authorize. It mirrors the executors dispatched by the runner and pipelines.
var knownPipelineExecutors = map[string]bool{"internal": true, "model": true, "opencode": true, "command": true, "http": true, "git": true, "gitea": true}

// AgentProfile is the effective policy body resolved for a pipeline step. The
// versioning metadata identifies where the policy came from: static defaults
// report Source "static" and Version 0; persisted versions report their
// immutable version number and provenance.
type AgentProfile struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Role         string `json:"role"`
	DefaultModel string `json:"default_model"`
	// AllowedModels is optional. An empty list preserves compatibility with
	// provider/model policies owned by the workflow executor.
	AllowedModels        []string                `json:"allowed_models,omitempty"`
	PromptContract       string                  `json:"prompt_contract"`
	AllowedExecutors     []string                `json:"allowed_executors"`
	Version              int                     `json:"version"`
	Status               string                  `json:"status"`
	Source               string                  `json:"source"`
	PromptContractSHA256 string                  `json:"prompt_contract_sha256"`
	Provenance           *AgentProfileProvenance `json:"provenance,omitempty"`
}

// AgentProfileProvenance records how a persisted version came to exist.
type AgentProfileProvenance struct {
	Origin                string `json:"origin"`
	Actor                 string `json:"actor,omitempty"`
	Note                  string `json:"note,omitempty"`
	DerivedFromVersion    int    `json:"derived_from_version,omitempty"`
	RolledBackFromVersion int    `json:"rolled_back_from_version,omitempty"`
	// StaticContractSHA256 is the prompt-contract hash of the shipped default
	// at the time the version was written, when a shipped default exists.
	StaticContractSHA256 string `json:"static_contract_sha256,omitempty"`
}

// AgentProfileVersion is one immutable persisted policy body.
type AgentProfileVersion struct {
	AgentProfile
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ActivatedAt *time.Time `json:"activated_at,omitempty"`
}

// AgentProfileIdentity is the stable identity record for a profile ID.
// ActiveVersion 0 means the shipped static default (if any) serves requests.
type AgentProfileIdentity struct {
	ID            string    `json:"id"`
	ActiveVersion int       `json:"active_version"`
	DraftVersion  int       `json:"draft_version"`
	LatestVersion int       `json:"latest_version"`
	StaticDefault bool      `json:"static_default"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// AgentProfileDraftInput creates a new draft version. ExpectedVersion must
// equal the identity's latest version (0 for a profile that has never been
// persisted); a mismatch is reported as ErrConflict.
type AgentProfileDraftInput struct {
	ExpectedVersion  int      `json:"expected_version"`
	Name             string   `json:"name"`
	Role             string   `json:"role"`
	DefaultModel     string   `json:"default_model"`
	AllowedModels    []string `json:"allowed_models,omitempty"`
	AllowedExecutors []string `json:"allowed_executors"`
	PromptContract   string   `json:"prompt_contract"`
	Note             string   `json:"note,omitempty"`
}

// AgentProfileActivationInput selects the version to activate or roll back to.
type AgentProfileActivationInput struct {
	Version int    `json:"version"`
	Reason  string `json:"reason,omitempty"`
}

// AgentProfileAuditEntry is an append-only record of a control-plane action.
type AgentProfileAuditEntry struct {
	ID                    string    `json:"id"`
	ProfileID             string    `json:"profile_id"`
	Version               int       `json:"version"`
	Action                string    `json:"action"`
	Actor                 string    `json:"actor"`
	Reason                string    `json:"reason,omitempty"`
	PromptContractSHA256  string    `json:"prompt_contract_sha256"`
	PreviousActiveVersion int       `json:"previous_active_version"`
	CreatedAt             time.Time `json:"created_at"`
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

// StoreBackedAgentProfileRegistry resolves the active persisted version first
// and falls back to the shipped static defaults, so existing profile references
// keep working when nothing has been persisted.
type StoreBackedAgentProfileRegistry struct {
	store    Store
	fallback AgentProfileRegistry
}

func NewStoreBackedAgentProfileRegistry(store Store, fallback AgentProfileRegistry) StoreBackedAgentProfileRegistry {
	if fallback == nil {
		fallback = DefaultAgentProfileRegistry()
	}
	return StoreBackedAgentProfileRegistry{store: store, fallback: fallback}
}

func (r StoreBackedAgentProfileRegistry) Profile(id string) (AgentProfile, bool) {
	if r.store != nil {
		if version, err := r.store.ActiveAgentProfile(id); err == nil {
			return version.AgentProfile, true
		}
	}
	if r.fallback == nil {
		return AgentProfile{}, false
	}
	return r.fallback.Profile(id)
}

func (r StoreBackedAgentProfileRegistry) ListProfiles() []AgentProfile {
	var fallback []AgentProfile
	if r.fallback != nil {
		fallback = r.fallback.ListProfiles()
	}
	if r.store == nil {
		return fallback
	}
	active, err := r.store.ListActiveAgentProfiles()
	if err != nil {
		return fallback
	}
	return mergeAgentProfiles(active, fallback)
}

func mergeAgentProfiles(active []AgentProfileVersion, fallback []AgentProfile) []AgentProfile {
	out := make([]AgentProfile, 0, len(active)+len(fallback))
	seen := map[string]struct{}{}
	for _, version := range active {
		out = append(out, version.AgentProfile)
		seen[version.ID] = struct{}{}
	}
	for _, profile := range fallback {
		if _, ok := seen[profile.ID]; ok {
			continue
		}
		out = append(out, profile)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func DefaultAgentProfileRegistry() StaticAgentProfileRegistry {
	registry := StaticAgentProfileRegistry{
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
	for id, profile := range registry {
		registry[id] = withStaticAgentProfileMetadata(profile)
	}
	return registry
}

func withStaticAgentProfileMetadata(profile AgentProfile) AgentProfile {
	profile.Version = 0
	profile.Status = AgentProfileStatusStatic
	profile.Source = AgentProfileSourceStatic
	profile.PromptContractSHA256 = profileContractHash(profile)
	profile.Provenance = nil
	return profile
}

func cloneAgentProfile(profile AgentProfile) AgentProfile {
	profile.AllowedModels = append([]string(nil), profile.AllowedModels...)
	profile.AllowedExecutors = append([]string(nil), profile.AllowedExecutors...)
	if profile.Provenance != nil {
		provenance := *profile.Provenance
		profile.Provenance = &provenance
	}
	return profile
}

func cloneAgentProfileVersion(version AgentProfileVersion) AgentProfileVersion {
	version.AgentProfile = cloneAgentProfile(version.AgentProfile)
	if version.ActivatedAt != nil {
		activated := *version.ActivatedAt
		version.ActivatedAt = &activated
	}
	return version
}

// normalizeAgentProfileDraft validates and canonicalizes a draft body. It
// returns ErrInvalid-wrapped errors so the control API reports 400.
func normalizeAgentProfileDraft(id string, input AgentProfileDraftInput) (AgentProfile, error) {
	id = strings.TrimSpace(id)
	if !agentProfileIDPattern.MatchString(id) {
		return AgentProfile{}, fmt.Errorf("%w: agent profile id must match %s", ErrInvalid, agentProfileIDPattern.String())
	}
	profile := AgentProfile{ID: id, Name: strings.TrimSpace(input.Name), Role: strings.TrimSpace(input.Role), DefaultModel: strings.TrimSpace(input.DefaultModel), PromptContract: strings.TrimSpace(input.PromptContract)}
	if profile.Name == "" || len(profile.Name) > maxAgentProfileNameLength {
		return AgentProfile{}, fmt.Errorf("%w: agent profile name is required and must be at most %d characters", ErrInvalid, maxAgentProfileNameLength)
	}
	if profile.Role == "" || len(profile.Role) > maxAgentProfileNameLength {
		return AgentProfile{}, fmt.Errorf("%w: agent profile role is required and must be at most %d characters", ErrInvalid, maxAgentProfileNameLength)
	}
	if profile.PromptContract == "" || len(profile.PromptContract) > maxAgentProfileContractLength {
		return AgentProfile{}, fmt.Errorf("%w: agent profile prompt_contract is required and must be at most %d bytes", ErrInvalid, maxAgentProfileContractLength)
	}
	if len(strings.TrimSpace(input.Note)) > maxAgentProfileNoteLength {
		return AgentProfile{}, fmt.Errorf("%w: agent profile note must be at most %d characters", ErrInvalid, maxAgentProfileNoteLength)
	}
	if err := validateAgentProfileModelName(profile.DefaultModel, true); err != nil {
		return AgentProfile{}, err
	}
	executors, err := normalizeAgentProfileExecutors(input.AllowedExecutors)
	if err != nil {
		return AgentProfile{}, err
	}
	profile.AllowedExecutors = executors
	models, err := normalizeAgentProfileModels(input.AllowedModels)
	if err != nil {
		return AgentProfile{}, err
	}
	profile.AllowedModels = models
	if len(models) > 0 && profile.DefaultModel != "" && !profileAllowsModel(profile, profile.DefaultModel) {
		return AgentProfile{}, fmt.Errorf("%w: agent profile default_model %q is not in allowed_models", ErrInvalid, profile.DefaultModel)
	}
	profile.PromptContractSHA256 = profileContractHash(profile)
	profile.Source = AgentProfileSourcePersisted
	return profile, nil
}

func normalizeAgentProfileExecutors(executors []string) ([]string, error) {
	out := make([]string, 0, len(executors))
	seen := map[string]bool{}
	for _, executor := range executors {
		executor = strings.ToLower(strings.TrimSpace(executor))
		if executor == "" {
			continue
		}
		if !knownPipelineExecutors[executor] {
			return nil, fmt.Errorf("%w: agent profile executor %q is not a known pipeline executor", ErrInvalid, executor)
		}
		if seen[executor] {
			continue
		}
		seen[executor] = true
		out = append(out, executor)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: agent profile allowed_executors must name at least one executor", ErrInvalid)
	}
	return out, nil
}

func normalizeAgentProfileModels(models []string) ([]string, error) {
	out := make([]string, 0, len(models))
	seen := map[string]bool{}
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if err := validateAgentProfileModelName(model, false); err != nil {
			return nil, err
		}
		key := strings.ToLower(model)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, model)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func validateAgentProfileModelName(model string, allowEmpty bool) error {
	if model == "" {
		if allowEmpty {
			return nil
		}
		return fmt.Errorf("%w: agent profile model names must not be empty", ErrInvalid)
	}
	if len(model) > maxAgentProfileModelLength || strings.ContainsAny(model, " \t\r\n") || model == "${model}" {
		return fmt.Errorf("%w: agent profile model %q must be a single token of at most %d characters", ErrInvalid, model, maxAgentProfileModelLength)
	}
	return nil
}

// validateAgentProfileConsumers rejects activating a policy body that would
// make an active workflow definition or a shipped default pipeline invalid.
// Profiles may only be narrowed when every current consumer still fits.
func validateAgentProfileConsumers(store Store, profile AgentProfile) error {
	check := func(owner, stepName, executor, model string) error {
		if !profileAllowsExecutor(profile, executor) {
			return fmt.Errorf("%w: agent profile %q version would not allow executor %q used by %s step %s", ErrInvalid, profile.ID, executor, owner, stepName)
		}
		if model != "" && model != "${model}" && !profileAllowsModel(profile, model) {
			return fmt.Errorf("%w: agent profile %q version would not allow model %q used by %s step %s", ErrInvalid, profile.ID, model, owner, stepName)
		}
		return nil
	}
	for _, pipeline := range DefaultPipelineRegistry().ListPipelines() {
		for _, step := range pipeline.Steps {
			if step.Profile != profile.ID {
				continue
			}
			if err := check("pipeline "+pipeline.ID, step.Name, step.Executor, step.Model); err != nil {
				return err
			}
		}
	}
	if store == nil {
		return nil
	}
	definitions, err := store.ListWorkflowDefinitions()
	if err != nil {
		return err
	}
	for _, definition := range definitions {
		if !isActiveWorkflowDefinition(definition) {
			continue
		}
		for _, step := range definition.Pipeline.Steps {
			if strings.TrimSpace(step.Profile) != profile.ID {
				continue
			}
			if err := check("workflow definition "+definition.ID, step.Name, step.Executor, step.Model); err != nil {
				return err
			}
		}
	}
	return nil
}

// attachPipelineProfileSnapshot copies the effective policy for every profile
// the pipeline references into the snapshot so a run keeps its meaning after
// later activations or rollbacks.
func attachPipelineProfileSnapshot(pipeline *Pipeline, registry AgentProfileRegistry) {
	if pipeline == nil || registry == nil {
		return
	}
	for _, step := range pipeline.Steps {
		id := strings.TrimSpace(step.Profile)
		if id == "" {
			continue
		}
		if _, ok := pipeline.Profiles[id]; ok {
			continue
		}
		profile, ok := registry.Profile(id)
		if !ok {
			continue
		}
		if pipeline.Profiles == nil {
			pipeline.Profiles = map[string]AgentProfile{}
		}
		pipeline.Profiles[id] = cloneAgentProfile(profile)
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
