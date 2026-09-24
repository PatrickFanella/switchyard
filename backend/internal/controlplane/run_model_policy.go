package controlplane

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Model sources report where a step's model came from. They are recorded on
// profile-bound run steps as agent_profile_model_source and returned by the
// run-policy views.
const (
	RunModelSourceWorkflow        = "workflow"
	RunModelSourceOverride        = "override"
	RunModelSourceProfileDefault  = "profile-default"
	RunModelSourceExecutorDefault = "executor-default"
	RunModelSourceNone            = "none"
	// RunModelSourceUnrecorded marks a step recorded before model sources
	// existed whose model differs from the current prediction.
	RunModelSourceUnrecorded = "unrecorded"

	RunProfileCapturedSnapshot = "run-snapshot"
	RunProfileCapturedRegistry = "registry"

	RunModelOverrideStatusNone     = "none"
	RunModelOverrideStatusAccepted = "accepted"
	RunModelOverrideStatusRejected = "rejected"

	// RunModelOverrideInput is the run input that carries a per-run model
	// override.
	RunModelOverrideInput = "model"

	runModelOverrideProbe = "switchyard-override-probe"
)

// PipelineStepModelAllowlister is implemented by workflow executors that limit
// which run-level model overrides a step accepts. restricted=false means the
// executor accepts any model name for the step.
type PipelineStepModelAllowlister interface {
	PipelineStepModelAllowlist(workflowID string, step PipelineStep) (models []string, restricted bool)
}

// RunModelPolicyStep is the resolved profile and model policy for one pipeline
// step. For a created run it describes that run's snapshot; for a dispatch
// preview it describes what a new run would capture now.
type RunModelPolicyStep struct {
	StepID                string   `json:"step_id"`
	StepName              string   `json:"step_name"`
	Position              int      `json:"position"`
	Executor              string   `json:"executor"`
	ProfileID             string   `json:"profile_id,omitempty"`
	ProfileName           string   `json:"profile_name,omitempty"`
	ProfileVersion        int      `json:"profile_version"`
	ProfileSource         string   `json:"profile_source,omitempty"`
	ProfileCaptured       string   `json:"profile_captured,omitempty"`
	ProfileContractSHA256 string   `json:"profile_contract_sha256,omitempty"`
	ProfileDefaultModel   string   `json:"profile_default_model,omitempty"`
	ProfileAllowedModels  []string `json:"profile_allowed_models,omitempty"`
	WorkflowModel         string   `json:"workflow_model,omitempty"`
	AcceptsOverride       bool     `json:"accepts_override"`
	OverrideRestricted    bool     `json:"override_restricted"`
	AllowedOverrides      []string `json:"allowed_overrides,omitempty"`
	InheritedModel        string   `json:"inherited_model,omitempty"`
	InheritedModelSource  string   `json:"inherited_model_source"`
	Model                 string   `json:"model,omitempty"`
	ModelSource           string   `json:"model_source"`
	Recorded              bool     `json:"recorded"`
	Error                 string   `json:"error,omitempty"`
	// PromptOwner is workflow-definition for a model step whose prompt is its
	// template input, built-in for a model or opencode step whose executor
	// assembles the prompt in code, and empty for other steps.
	PromptOwner string `json:"prompt_owner,omitempty"`
	// PromptInput names the template input (prompt or template), and
	// PromptTemplateSHA256 hashes the unrendered template in the run snapshot.
	PromptInput          string `json:"prompt_input,omitempty"`
	PromptTemplateSHA256 string `json:"prompt_template_sha256,omitempty"`
}

// RunModelPolicy summarizes the profile and model policy of a run or of a
// prospective run of a workflow, including whether a model override is
// accepted and why it was rejected.
type RunModelPolicy struct {
	WorkflowID      string `json:"workflow_id"`
	PipelineID      string `json:"pipeline_id,omitempty"`
	PipelineVersion string `json:"pipeline_version,omitempty"`
	// DefinitionID and DefinitionVersion identify the workflow-definition
	// version whose step templates the run executes. DefinitionVersion is zero
	// for built-in pipelines and for snapshots created before it was recorded.
	DefinitionID       string               `json:"definition_id,omitempty"`
	DefinitionVersion  int                  `json:"definition_version"`
	Override           string               `json:"override,omitempty"`
	OverrideStatus     string               `json:"override_status"`
	Rejections         []string             `json:"rejections,omitempty"`
	AcceptsOverride    bool                 `json:"accepts_override"`
	OverrideRestricted bool                 `json:"override_restricted"`
	AllowedOverrides   []string             `json:"allowed_overrides,omitempty"`
	Steps              []RunModelPolicyStep `json:"steps"`
}

// resolveStepModel mirrors the runner's model selection for a step and reports
// the source of the result. It does not apply the profile allowlist.
func resolveStepModel(workflowID string, inputs map[string]string, step PipelineStep, executor WorkflowExecutor, profile AgentProfile, hasProfile bool) (string, string, error) {
	model := strings.TrimSpace(step.Model)
	source := ""
	if model != "" && model != "${model}" {
		source = RunModelSourceWorkflow
	}
	override := strings.TrimSpace(inputs[RunModelOverrideInput])
	if hasProfile && model == "" {
		if override != "" {
			model, source = override, RunModelSourceOverride
		} else if strings.TrimSpace(profile.DefaultModel) != "" && (step.Executor == "model" || step.Executor == "opencode") {
			model, source = strings.TrimSpace(profile.DefaultModel), RunModelSourceProfileDefault
		}
	}
	if model == "" || model == "${model}" {
		if resolver, ok := executor.(PipelineStepModelResolver); ok {
			resolved, err := resolver.ResolvePipelineStepModel(workflowID, step, inputs)
			if err != nil {
				return "", "", err
			}
			model = strings.TrimSpace(resolved)
			switch {
			case model == "":
				source = RunModelSourceNone
			case override != "" && model == override:
				source = RunModelSourceOverride
			default:
				source = RunModelSourceExecutorDefault
			}
		}
	}
	if source == "" {
		if model == "" {
			source = RunModelSourceNone
		} else {
			source = RunModelSourceWorkflow
		}
	}
	return model, source, nil
}

func profileModelRejection(profile AgentProfile, model string) error {
	return fmt.Errorf("agent profile %q does not allow model %q", profile.ID, model)
}

// modelPolicy evaluates every step of pipeline against inputs. recorded steps,
// when present, supply the values the runner actually used.
func (r *Runner) modelPolicy(workflowID string, pipeline Pipeline, inputs map[string]string, recorded []RunStep) RunModelPolicy {
	policy := RunModelPolicy{WorkflowID: workflowID, PipelineID: pipeline.ID, DefinitionID: pipeline.DefinitionID, DefinitionVersion: pipeline.DefinitionVersion, OverrideStatus: RunModelOverrideStatusNone, Steps: []RunModelPolicyStep{}}
	var executor WorkflowExecutor
	if r.executors != nil {
		executor = r.executors.ExecutorFor(workflowID)
	}
	override := strings.TrimSpace(inputs[RunModelOverrideInput])
	withoutOverride := make(map[string]string, len(inputs))
	probe := make(map[string]string, len(inputs)+1)
	for key, value := range inputs {
		if key != RunModelOverrideInput {
			withoutOverride[key] = value
			probe[key] = value
		}
	}
	probe[RunModelOverrideInput] = runModelOverrideProbe

	recordedByPosition := map[int]RunStep{}
	for _, step := range recorded {
		recordedByPosition[step.Position] = step
	}
	steps := append([]PipelineStep(nil), pipeline.Steps...)
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].Position < steps[j].Position })
	var allowed []string
	for _, step := range steps {
		entry := RunModelPolicyStep{StepID: step.ID, StepName: step.Name, Position: step.Position, Executor: step.Executor, WorkflowModel: strings.TrimSpace(step.Model)}
		applyStepPromptPolicy(&entry, step)
		profile, hasProfile := AgentProfile{}, false
		if id := strings.TrimSpace(step.Profile); id != "" {
			entry.ProfileID = id
			if snapshot, ok := pipeline.Profiles[id]; ok {
				profile, hasProfile, entry.ProfileCaptured = snapshot, true, RunProfileCapturedSnapshot
			} else if current, ok := r.effectiveAgentProfiles().Profile(id); ok {
				profile, hasProfile, entry.ProfileCaptured = current, true, RunProfileCapturedRegistry
			} else {
				entry.Error = fmt.Sprintf("unknown agent profile %q", id)
			}
			if hasProfile {
				entry.ProfileName = profile.Name
				entry.ProfileVersion = profile.Version
				entry.ProfileSource = firstNonEmpty(profile.Source, AgentProfileSourceStatic)
				entry.ProfileContractSHA256 = firstNonEmpty(profile.PromptContractSHA256, profileContractHash(profile))
				entry.ProfileDefaultModel = profile.DefaultModel
				entry.ProfileAllowedModels = append([]string(nil), profile.AllowedModels...)
				if !profileAllowsExecutor(profile, step.Executor) {
					entry.Error = fmt.Sprintf("agent profile %q does not allow executor %q", id, step.Executor)
					hasProfile = false
				}
			}
		}
		if entry.Error == "" {
			inherited, inheritedSource, inheritedErr := resolveStepModel(workflowID, withoutOverride, step, executor, profile, hasProfile)
			if inheritedErr == nil {
				entry.InheritedModel, entry.InheritedModelSource = inherited, inheritedSource
			} else {
				entry.InheritedModelSource = RunModelSourceNone
			}
			_, probeSource, probeErr := resolveStepModel(workflowID, probe, step, executor, profile, hasProfile)
			entry.AcceptsOverride = probeSource == RunModelSourceOverride || (probeErr != nil && inheritedErr == nil)
			if entry.AcceptsOverride {
				entry.AllowedOverrides, entry.OverrideRestricted = stepOverrideAllowlist(workflowID, step, executor, profile, hasProfile)
			}
			model, source, err := resolveStepModel(workflowID, inputs, step, executor, profile, hasProfile)
			if err == nil && hasProfile && model != "" && !profileAllowsModel(profile, model) {
				err = profileModelRejection(profile, model)
			}
			entry.Model, entry.ModelSource = model, source
			if err != nil {
				entry.Error = err.Error()
				entry.ModelSource = RunModelSourceNone
			}
		} else {
			entry.InheritedModelSource, entry.ModelSource = RunModelSourceNone, RunModelSourceNone
		}
		if step, ok := recordedByPosition[step.Position]; ok && step.Name == entry.StepName {
			applyRecordedStepPolicy(&entry, step)
		}
		if entry.AcceptsOverride {
			policy.AcceptsOverride = true
			if entry.OverrideRestricted {
				if !policy.OverrideRestricted {
					allowed = append([]string(nil), entry.AllowedOverrides...)
				} else {
					allowed = intersectModelNames(allowed, entry.AllowedOverrides)
				}
				policy.OverrideRestricted = true
			}
		}
		policy.Steps = append(policy.Steps, entry)
	}
	if policy.OverrideRestricted {
		policy.AllowedOverrides = allowed
		if policy.AllowedOverrides == nil {
			policy.AllowedOverrides = []string{}
		}
	}
	if override != "" {
		policy.Override = override
		policy.Rejections = modelOverrideRejections(workflowID, override, policy)
		if len(policy.Rejections) > 0 {
			policy.OverrideStatus = RunModelOverrideStatusRejected
		} else {
			policy.OverrideStatus = RunModelOverrideStatusAccepted
		}
	}
	return policy
}

// applyStepPromptPolicy reports which store owns a step's prompt and, for a
// template, the hash of the unrendered text captured in the snapshot.
func applyStepPromptPolicy(entry *RunModelPolicyStep, step PipelineStep) {
	switch {
	case isPromptStep(step.Executor):
		input, template := promptTemplateInput(step.Inputs)
		if strings.TrimSpace(template) == "" {
			entry.PromptOwner = PromptOwnerBuiltIn
			return
		}
		entry.PromptOwner, entry.PromptInput, entry.PromptTemplateSHA256 = PromptOwnerWorkflowDefinition, input, promptTemplateSHA256(template)
	case step.Executor == "opencode":
		entry.PromptOwner = PromptOwnerBuiltIn
	}
}

func applyRecordedStepPolicy(entry *RunModelPolicyStep, step RunStep) {
	entry.Recorded = true
	correlation := step.CorrelationIDs
	if version, err := strconv.Atoi(correlation["agent_profile_version"]); err == nil && correlation["agent_profile_id"] != "" {
		entry.ProfileID = correlation["agent_profile_id"]
		entry.ProfileVersion = version
		entry.ProfileSource = firstNonEmpty(correlation["agent_profile_source"], entry.ProfileSource)
		entry.ProfileContractSHA256 = firstNonEmpty(correlation["agent_profile_contract_sha256"], entry.ProfileContractSHA256)
	}
	recordedModel := firstNonEmpty(strings.TrimSpace(step.Model), correlation["agent_profile_model"])
	if recordedModel == "" {
		return
	}
	switch source := correlation["agent_profile_model_source"]; {
	case source != "":
		entry.ModelSource = source
	case recordedModel != entry.Model:
		entry.ModelSource = RunModelSourceUnrecorded
	}
	entry.Model = recordedModel
	entry.Error = ""
}

func stepOverrideAllowlist(workflowID string, step PipelineStep, executor WorkflowExecutor, profile AgentProfile, hasProfile bool) ([]string, bool) {
	var executorModels []string
	executorRestricted := false
	if allowlister, ok := executor.(PipelineStepModelAllowlister); ok {
		executorModels, executorRestricted = allowlister.PipelineStepModelAllowlist(workflowID, step)
	}
	profileRestricted := hasProfile && len(profile.AllowedModels) > 0
	switch {
	case profileRestricted && executorRestricted:
		return intersectModelNames(executorModels, profile.AllowedModels), true
	case profileRestricted:
		return append([]string(nil), profile.AllowedModels...), true
	case executorRestricted:
		return append([]string(nil), executorModels...), true
	default:
		return nil, false
	}
}

// intersectModelNames keeps the names in left that appear in right, compared
// case-insensitively like profileAllowsModel.
func intersectModelNames(left, right []string) []string {
	out := []string{}
	for _, candidate := range left {
		for _, other := range right {
			if strings.EqualFold(strings.TrimSpace(candidate), strings.TrimSpace(other)) {
				out = append(out, candidate)
				break
			}
		}
	}
	return out
}

func modelOverrideRejections(workflowID, override string, policy RunModelPolicy) []string {
	if len(override) > maxAgentProfileModelLength || strings.ContainsAny(override, " \t\r\n") || override == "${model}" {
		return []string{fmt.Sprintf("model override must be a single token of at most %d characters", maxAgentProfileModelLength)}
	}
	if !policy.AcceptsOverride {
		return []string{fmt.Sprintf("workflow %s has no step that accepts a run-level model override; each step fixes its model or selects none", workflowID)}
	}
	var reasons []string
	for _, step := range policy.Steps {
		if !step.AcceptsOverride || step.Error == "" {
			continue
		}
		reason := fmt.Sprintf("step %q: %s", step.StepName, step.Error)
		if step.OverrideRestricted {
			if len(step.AllowedOverrides) == 0 {
				reason += "; no override is allowed for this step"
			} else {
				reason += "; allowed: " + strings.Join(step.AllowedOverrides, ", ")
			}
		}
		reasons = append(reasons, reason)
	}
	return reasons
}

// validateRunModelOverride rejects a per-run model override that the run's
// snapshot policy would not accept, before the run is created.
func (r *Runner) validateRunModelOverride(input CreateRunInput) error {
	if input.PipelineSnapshot == nil || strings.TrimSpace(input.Inputs[RunModelOverrideInput]) == "" {
		return nil
	}
	policy := r.modelPolicy(input.WorkflowID, *input.PipelineSnapshot, input.Inputs, nil)
	if policy.OverrideStatus != RunModelOverrideStatusRejected {
		return nil
	}
	return fmt.Errorf("%w: model override %q rejected: %s", ErrInvalid, policy.Override, strings.Join(policy.Rejections, "; "))
}

// WorkflowModelPolicy previews the policy a new run of workflowID would
// capture now, evaluating override when it is non-empty. ok is false when the
// workflow has no pipeline.
func (r *Runner) WorkflowModelPolicy(workflowID, override string) (RunModelPolicy, bool) {
	if r.pipelines == nil {
		return RunModelPolicy{}, false
	}
	pipeline, ok := r.pipelines.PipelineForWorkflow(workflowID)
	if !ok {
		return RunModelPolicy{}, false
	}
	snapshot := clonePipeline(pipeline)
	attachPipelineProfileSnapshot(&snapshot, r.effectiveAgentProfiles())
	inputs := map[string]string{}
	if override = strings.TrimSpace(override); override != "" {
		inputs[RunModelOverrideInput] = override
	}
	policy := r.modelPolicy(workflowID, snapshot, inputs, nil)
	policy.PipelineVersion = pipelineVersion(snapshot)
	return policy, true
}

// RunModelPolicy reports the resolved profile and model policy of a created
// run from its immutable snapshot and recorded steps. It returns nil for runs
// without a pipeline snapshot.
func (r *Runner) RunModelPolicy(run Run, steps []RunStep) *RunModelPolicy {
	if r == nil || run.PipelineSnapshot == nil {
		return nil
	}
	policy := r.modelPolicy(run.WorkflowID, *run.PipelineSnapshot, run.Inputs, steps)
	policy.PipelineVersion = run.PipelineVersion
	return &policy
}
