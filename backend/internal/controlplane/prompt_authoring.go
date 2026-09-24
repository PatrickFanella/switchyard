package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

// Prompt ownership.
//
// Switchyard has exactly two prompt stores, and each prompt has one owner:
//
//   - A workflow step template is the `prompt` (or `template`) input of a
//     model step in a workflow definition. It is versioned, activated, and
//     rolled back with the workflow definition, and a run snapshot binds the
//     definition version it executes.
//   - A profile prompt contract is the `prompt_contract` of an agent profile.
//     It is versioned, activated, and rolled back with the profile, and a run
//     snapshot binds the profile version. The runner prepends it to every model
//     step that uses the profile.
//
// Prompts that the Devlog and Open Pilot executors assemble in Go are
// built-in and change only with the code. The prompt authoring surface is a
// view over these owners; it never stores prompt text itself. Saving an edited
// template writes a workflow-definition draft, which neither activates nor
// runs anything.
const (
	PromptOwnerWorkflowDefinition = "workflow-definition"
	PromptOwnerAgentProfile       = "agent-profile"
	PromptOwnerBuiltIn            = "built-in"

	PromptVariableInput      = "input"
	PromptVariableStepOutput = "step-output"

	PromptProblemError   = "error"
	PromptProblemWarning = "warning"

	maxPromptPreviewTemplateBytes = 64 << 10
	maxPromptPreviewSamples       = 64
	maxPromptPreviewSampleBytes   = 64 << 10
	// A contract shorter than this is not treated as duplicated text; short
	// strings match too easily.
	minDuplicatedContractLength = 16
)

// promptTokenPattern matches every {{...}} token. The runner substitutes run
// inputs only as the exact text {{name}} and step outputs as
// {{steps.<id>.outputs.<name>}}.
var promptTokenPattern = regexp.MustCompile(`\{\{([^{}]*)\}\}`)

// PromptVariable is one variable a template reads.
type PromptVariable struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	StepID   string `json:"step_id,omitempty"`
	Output   string `json:"output,omitempty"`
	Declared bool   `json:"declared"`
	Required bool   `json:"required"`
}

// PromptProblem is a validation finding. Errors describe a template the
// runner cannot render as written; warnings describe likely mistakes.
type PromptProblem struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// PromptProfileConstraints is the profile policy a template runs under.
type PromptProfileConstraints struct {
	ProfileID            string   `json:"profile_id"`
	Name                 string   `json:"name,omitempty"`
	Role                 string   `json:"role,omitempty"`
	Version              int      `json:"version"`
	Source               string   `json:"source,omitempty"`
	Status               string   `json:"status,omitempty"`
	DefaultModel         string   `json:"default_model,omitempty"`
	AllowedModels        []string `json:"allowed_models,omitempty"`
	AllowedExecutors     []string `json:"allowed_executors"`
	PromptContract       string   `json:"prompt_contract"`
	PromptContractSHA256 string   `json:"prompt_contract_sha256"`
}

// PromptTemplate is one workflow step template in the latest stored version
// of a definition.
type PromptTemplate struct {
	Owner                string           `json:"owner"`
	WorkflowID           string           `json:"workflow_id"`
	WorkflowName         string           `json:"workflow_name"`
	Version              int              `json:"version"`
	ActiveVersion        int              `json:"active_version"`
	StepID               string           `json:"step_id"`
	StepName             string           `json:"step_name"`
	Position             int              `json:"position"`
	Executor             string           `json:"executor"`
	ProfileID            string           `json:"profile_id,omitempty"`
	Model                string           `json:"model,omitempty"`
	Input                string           `json:"input"`
	Template             string           `json:"template"`
	TemplateSHA256       string           `json:"template_sha256"`
	ActiveTemplateSHA256 string           `json:"active_template_sha256,omitempty"`
	ChangedSinceActive   bool             `json:"changed_since_active"`
	Variables            []PromptVariable `json:"variables"`
	Problems             []PromptProblem  `json:"problems"`
}

// PromptReference names a workflow step that uses a profile.
type PromptReference struct {
	WorkflowID string `json:"workflow_id"`
	StepID     string `json:"step_id"`
	Version    int    `json:"version"`
}

// PromptContractEntry is one effective profile prompt contract.
type PromptContractEntry struct {
	Owner string `json:"owner"`
	PromptProfileConstraints
	ReferencedBy []PromptReference `json:"referenced_by"`
}

// PromptBuiltInStep is a step whose prompt an executor assembles in code.
type PromptBuiltInStep struct {
	Owner      string `json:"owner"`
	WorkflowID string `json:"workflow_id"`
	PipelineID string `json:"pipeline_id"`
	StepID     string `json:"step_id"`
	StepName   string `json:"step_name"`
	Executor   string `json:"executor"`
	ProfileID  string `json:"profile_id,omitempty"`
	Note       string `json:"note"`
}

// PromptInventory is the response of GET /api/prompts.
type PromptInventory struct {
	Templates []PromptTemplate      `json:"templates"`
	Contracts []PromptContractEntry `json:"contracts"`
	BuiltIn   []PromptBuiltInStep   `json:"built_in"`
}

// PromptPreviewInput is the body of POST /api/prompts/preview.
type PromptPreviewInput struct {
	WorkflowID string `json:"workflow_id"`
	StepID     string `json:"step_id"`
	// Version selects a stored version; zero means the latest.
	Version int `json:"version,omitempty"`
	// Template, when set, replaces the stored template: an unsaved draft.
	Template    *string                      `json:"template,omitempty"`
	Inputs      map[string]string            `json:"inputs,omitempty"`
	StepOutputs map[string]map[string]string `json:"step_outputs,omitempty"`
}

// PromptMissingVariable is a variable the preview could not fill.
type PromptMissingVariable struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// PromptPreview is the rendered result. Nothing is executed: no model call,
// no run, no stored change.
type PromptPreview struct {
	WorkflowID       string                    `json:"workflow_id"`
	Version          int                       `json:"version"`
	ActiveVersion    int                       `json:"active_version"`
	StepID           string                    `json:"step_id"`
	StepName         string                    `json:"step_name"`
	Input            string                    `json:"input"`
	Draft            bool                      `json:"draft"`
	TemplateSHA256   string                    `json:"template_sha256"`
	Profile          *PromptProfileConstraints `json:"profile,omitempty"`
	Variables        []PromptVariable          `json:"variables"`
	Problems         []PromptProblem           `json:"problems"`
	MissingVariables []PromptMissingVariable   `json:"missing_variables"`
	OK               bool                      `json:"ok"`
	WorkflowRequest  string                    `json:"workflow_request"`
	Rendered         string                    `json:"rendered"`
	Redacted         bool                      `json:"redacted"`
}

// promptTemplateInput returns the input the model executor reads: `prompt`
// when it is non-empty, otherwise `template`.
func promptTemplateInput(inputs map[string]string) (string, string) {
	if strings.TrimSpace(inputs["prompt"]) != "" {
		return "prompt", inputs["prompt"]
	}
	if strings.TrimSpace(inputs["template"]) != "" {
		return "template", inputs["template"]
	}
	return "prompt", ""
}

func promptTemplateSHA256(template string) string {
	sum := sha256.Sum256([]byte(template))
	return hex.EncodeToString(sum[:])
}

// isPromptStep reports whether a step's prompt comes from its template.
func isPromptStep(executor string) bool {
	return strings.EqualFold(strings.TrimSpace(executor), "model")
}

// analyzePromptTemplate lists the variables a template reads and reports
// problems. steps are the definition steps of the same version; contract is
// the step profile's prompt contract, if any.
func analyzePromptTemplate(steps []PipelineStepDefinition, stepID, template string, inputShape JSONShape, contract string) ([]PromptVariable, []PromptProblem) {
	variables := []PromptVariable{}
	problems := []PromptProblem{}
	seen := map[string]bool{}
	positions := make(map[string]int, len(steps))
	current := 0
	for _, step := range steps {
		positions[step.ID] = step.Position
		if step.ID == stepID {
			current = step.Position
		}
	}
	required := map[string]bool{}
	for _, name := range inputShape.Required {
		required[name] = true
	}
	addProblem := func(severity, format string, args ...any) {
		problems = append(problems, PromptProblem{Severity: severity, Message: fmt.Sprintf(format, args...)})
	}
	if strings.TrimSpace(template) == "" {
		addProblem(PromptProblemError, "the model step has no prompt or template input; activation requires one")
	}
	for _, match := range promptTokenPattern.FindAllStringSubmatch(template, -1) {
		token, body := match[0], match[1]
		trimmed := strings.TrimSpace(body)
		if strings.HasPrefix(trimmed, stepOutputReferencePrefix) {
			references, err := parseStepOutputReferences(token)
			if err != nil || len(references) != 1 {
				addProblem(PromptProblemError, "%s is not a valid step output reference; use {{steps.<step_id>.outputs.<name>}}", token)
				continue
			}
			reference := references[0]
			name := reference.String()
			if seen[name] {
				continue
			}
			seen[name] = true
			variables = append(variables, PromptVariable{Name: name, Kind: PromptVariableStepOutput, StepID: reference.StepID, Output: reference.Name, Declared: true, Required: true})
			position, ok := positions[reference.StepID]
			switch {
			case reference.StepID == stepID:
				addProblem(PromptProblemError, "%s: a step cannot reference its own outputs", token)
			case !ok:
				addProblem(PromptProblemError, "%s references unknown step %q", token, reference.StepID)
			case current != 0 && position >= current:
				addProblem(PromptProblemError, "%s references step %q, which does not run before this step", token, reference.StepID)
			}
			continue
		}
		if trimmed == "" {
			addProblem(PromptProblemError, "%s is empty; name a run input such as {{topic}}", token)
			continue
		}
		if trimmed != body {
			addProblem(PromptProblemError, "%s contains spaces; the runner substitutes only {{%s}} exactly", token, trimmed)
		}
		if seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		_, declared := inputShape.Properties[trimmed]
		variables = append(variables, PromptVariable{Name: trimmed, Kind: PromptVariableInput, Declared: declared, Required: required[trimmed]})
		if !declared && trimmed != RunModelOverrideInput {
			addProblem(PromptProblemWarning, "run input %q is not declared in the workflow input_shape; a run that omits it leaves {{%s}} in the prompt", trimmed, trimmed)
		}
	}
	if contract = strings.TrimSpace(contract); len(contract) >= minDuplicatedContractLength && strings.Contains(template, contract) {
		addProblem(PromptProblemError, "the template repeats the step profile's prompt contract; the runner already prepends it, so keep that text only in the profile")
	}
	if RedactForLog(template) != template {
		addProblem(PromptProblemWarning, "the template contains credential-shaped text; keep secrets in runtime configuration, not prompts")
	}
	return variables, problems
}

func promptProfileConstraints(profile AgentProfile) *PromptProfileConstraints {
	return &PromptProfileConstraints{
		ProfileID:            profile.ID,
		Name:                 profile.Name,
		Role:                 profile.Role,
		Version:              profile.Version,
		Source:               firstNonEmpty(profile.Source, AgentProfileSourceStatic),
		Status:               profile.Status,
		DefaultModel:         profile.DefaultModel,
		AllowedModels:        append([]string(nil), profile.AllowedModels...),
		AllowedExecutors:     append([]string{}, profile.AllowedExecutors...),
		PromptContract:       RedactForLog(profile.PromptContract),
		PromptContractSHA256: firstNonEmpty(profile.PromptContractSHA256, profileContractHash(profile)),
	}
}

// promptStepProfile resolves the step's profile and adds problems for an
// unknown profile or one that forbids the step's executor or model.
func promptStepProfile(registry AgentProfileRegistry, step PipelineStepDefinition) (AgentProfile, bool, []PromptProblem) {
	id := strings.TrimSpace(step.Profile)
	if id == "" || registry == nil {
		return AgentProfile{}, false, nil
	}
	profile, ok := registry.Profile(id)
	if !ok {
		return AgentProfile{}, false, []PromptProblem{{Severity: PromptProblemError, Message: fmt.Sprintf("unknown agent profile %q", id)}}
	}
	var problems []PromptProblem
	if !profileAllowsExecutor(profile, step.Executor) {
		problems = append(problems, PromptProblem{Severity: PromptProblemError, Message: fmt.Sprintf("agent profile %q does not allow executor %q", id, step.Executor)})
	}
	if model := strings.TrimSpace(step.Model); model != "" && model != "${model}" && !profileAllowsModel(profile, model) {
		problems = append(problems, PromptProblem{Severity: PromptProblemError, Message: fmt.Sprintf("agent profile %q does not allow model %q", id, model)})
	}
	return profile, true, problems
}

func definitionStep(definition WorkflowDefinition, stepID string) (PipelineStepDefinition, bool) {
	for _, step := range definition.Pipeline.Steps {
		if step.ID == stepID {
			return step, true
		}
	}
	return PipelineStepDefinition{}, false
}

func sortedDefinitionSteps(definition WorkflowDefinition) []PipelineStepDefinition {
	steps := append([]PipelineStepDefinition(nil), definition.Pipeline.Steps...)
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].Position < steps[j].Position })
	return steps
}

// BuildPromptInventory lists every workflow step template, every effective
// profile contract with the steps that use it, and every built-in prompt step.
// Template and contract text is redacted like other control-plane evidence.
func BuildPromptInventory(store Store, registry AgentProfileRegistry, pipelines []Pipeline) (PromptInventory, error) {
	inventory := PromptInventory{Templates: []PromptTemplate{}, Contracts: []PromptContractEntry{}, BuiltIn: []PromptBuiltInStep{}}
	definitions, err := store.ListWorkflowDefinitions()
	if err != nil {
		return inventory, err
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].ID < definitions[j].ID })
	references := map[string][]PromptReference{}
	for _, definition := range definitions {
		var active *WorkflowDefinition
		if definition.ActiveVersion != 0 && definition.ActiveVersion != definition.Version {
			if stored, err := store.ActiveWorkflowDefinition(definition.ID); err == nil {
				active = &stored
			}
		}
		for _, step := range sortedDefinitionSteps(definition) {
			if id := strings.TrimSpace(step.Profile); id != "" {
				references[id] = append(references[id], PromptReference{WorkflowID: definition.ID, StepID: step.ID, Version: definition.Version})
			}
			if !isPromptStep(step.Executor) {
				continue
			}
			input, template := promptTemplateInput(step.Inputs)
			profile, hasProfile, profileProblems := promptStepProfile(registry, step)
			contract := ""
			if hasProfile {
				contract = profile.PromptContract
			}
			variables, problems := analyzePromptTemplate(definition.Pipeline.Steps, step.ID, template, definition.InputShape, contract)
			entry := PromptTemplate{
				Owner:          PromptOwnerWorkflowDefinition,
				WorkflowID:     definition.ID,
				WorkflowName:   definition.Name,
				Version:        definition.Version,
				ActiveVersion:  definition.ActiveVersion,
				StepID:         step.ID,
				StepName:       step.Name,
				Position:       step.Position,
				Executor:       step.Executor,
				ProfileID:      step.Profile,
				Model:          step.Model,
				Input:          input,
				Template:       RedactForLog(template),
				TemplateSHA256: promptTemplateSHA256(template),
				Variables:      variables,
				Problems:       append(profileProblems, problems...),
			}
			switch {
			case definition.ActiveVersion == definition.Version:
				entry.ActiveTemplateSHA256 = entry.TemplateSHA256
			case active != nil:
				if activeStep, ok := definitionStep(*active, step.ID); ok {
					_, activeTemplate := promptTemplateInput(activeStep.Inputs)
					entry.ActiveTemplateSHA256 = promptTemplateSHA256(activeTemplate)
				}
			}
			entry.ChangedSinceActive = entry.ActiveTemplateSHA256 != entry.TemplateSHA256
			inventory.Templates = append(inventory.Templates, entry)
		}
	}
	for _, pipeline := range pipelines {
		if pipeline.DefinitionVersion != 0 {
			continue
		}
		steps := append([]PipelineStep(nil), pipeline.Steps...)
		sort.SliceStable(steps, func(i, j int) bool { return steps[i].Position < steps[j].Position })
		for _, step := range steps {
			if id := strings.TrimSpace(step.Profile); id != "" {
				references[id] = append(references[id], PromptReference{WorkflowID: pipeline.WorkflowID, StepID: step.ID})
			}
			if step.Executor != "model" && step.Executor != "opencode" {
				continue
			}
			inventory.BuiltIn = append(inventory.BuiltIn, PromptBuiltInStep{
				Owner:      PromptOwnerBuiltIn,
				WorkflowID: pipeline.WorkflowID,
				PipelineID: pipeline.ID,
				StepID:     step.ID,
				StepName:   step.Name,
				Executor:   step.Executor,
				ProfileID:  step.Profile,
				Note:       fmt.Sprintf("The %s executor assembles this prompt in code; it changes only with a code change. Its profile contract is still versioned with the profile.", pipeline.WorkflowID),
			})
		}
	}
	if registry != nil {
		for _, profile := range registry.ListProfiles() {
			refs := references[profile.ID]
			if refs == nil {
				refs = []PromptReference{}
			}
			inventory.Contracts = append(inventory.Contracts, PromptContractEntry{Owner: PromptOwnerAgentProfile, PromptProfileConstraints: *promptProfileConstraints(profile), ReferencedBy: refs})
		}
		sort.Slice(inventory.Contracts, func(i, j int) bool { return inventory.Contracts[i].ProfileID < inventory.Contracts[j].ProfileID })
	}
	return inventory, nil
}

func invalidPromptPreview(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// PreviewPrompt renders a stored or draft template with sample values the way
// the runner would: step output references first, then run inputs, then the
// profile contract prefix. It never calls a model or changes stored state.
func PreviewPrompt(store Store, registry AgentProfileRegistry, input PromptPreviewInput) (PromptPreview, error) {
	workflowID, stepID := strings.TrimSpace(input.WorkflowID), strings.TrimSpace(input.StepID)
	if workflowID == "" || stepID == "" {
		return PromptPreview{}, invalidPromptPreview("workflow_id and step_id are required")
	}
	if input.Version < 0 {
		return PromptPreview{}, invalidPromptPreview("version must be 0 or greater")
	}
	if input.Template != nil && len(*input.Template) > maxPromptPreviewTemplateBytes {
		return PromptPreview{}, invalidPromptPreview("template exceeds %d bytes", maxPromptPreviewTemplateBytes)
	}
	samples := len(input.Inputs)
	for _, outputs := range input.StepOutputs {
		samples += len(outputs)
	}
	if samples > maxPromptPreviewSamples {
		return PromptPreview{}, invalidPromptPreview("at most %d sample values are allowed", maxPromptPreviewSamples)
	}
	for name, value := range input.Inputs {
		if len(value) > maxPromptPreviewSampleBytes {
			return PromptPreview{}, invalidPromptPreview("sample input %q exceeds %d bytes", name, maxPromptPreviewSampleBytes)
		}
	}
	for step, outputs := range input.StepOutputs {
		for name, value := range outputs {
			if len(value) > maxPromptPreviewSampleBytes {
				return PromptPreview{}, invalidPromptPreview("sample output %s.%s exceeds %d bytes", step, name, maxPromptPreviewSampleBytes)
			}
		}
	}

	definition, err := store.WorkflowDefinition(workflowID)
	if err != nil {
		return PromptPreview{}, err
	}
	if input.Version != 0 && input.Version != definition.Version {
		versions, err := store.ListWorkflowDefinitionVersions(workflowID)
		if err != nil {
			return PromptPreview{}, err
		}
		found := false
		for _, version := range versions {
			if version.Version == input.Version {
				activeVersion := definition.ActiveVersion
				definition, found = version, true
				definition.ActiveVersion = activeVersion
				break
			}
		}
		if !found {
			return PromptPreview{}, fmt.Errorf("%w: workflow definition %q has no version %d", ErrNotFound, workflowID, input.Version)
		}
	}
	step, ok := definitionStep(definition, stepID)
	if !ok {
		return PromptPreview{}, fmt.Errorf("%w: workflow definition %q version %d has no step %q", ErrNotFound, workflowID, definition.Version, stepID)
	}
	if !isPromptStep(step.Executor) {
		return PromptPreview{}, invalidPromptPreview("step %q uses executor %q; only model steps have prompt templates", stepID, step.Executor)
	}
	inputName, template := promptTemplateInput(step.Inputs)
	if input.Template != nil {
		template = *input.Template
	}

	preview := PromptPreview{WorkflowID: definition.ID, Version: definition.Version, ActiveVersion: definition.ActiveVersion, StepID: step.ID, StepName: step.Name, Input: inputName, Draft: input.Template != nil, TemplateSHA256: promptTemplateSHA256(template), MissingVariables: []PromptMissingVariable{}}
	profile, hasProfile, profileProblems := promptStepProfile(registry, step)
	contract := ""
	if hasProfile {
		contract = profile.PromptContract
		preview.Profile = promptProfileConstraints(profile)
	}
	variables, problems := analyzePromptTemplate(definition.Pipeline.Steps, step.ID, template, definition.InputShape, contract)
	preview.Variables = variables
	preview.Problems = append(profileProblems, problems...)

	sampleInputs := make(map[string]string, len(input.Inputs))
	redacted := false
	for name, value := range input.Inputs {
		if sensitiveJSONKey(name) {
			value, redacted = "[REDACTED]", true
		}
		sampleInputs[name] = value
	}
	outputs := newPipelineStepOutputs()
	for id, values := range input.StepOutputs {
		outputs.set(id, values)
	}
	for _, variable := range variables {
		switch variable.Kind {
		case PromptVariableStepOutput:
			if _, ok := outputs.lookup(StepOutputReference{StepID: variable.StepID, Name: variable.Output}); !ok {
				preview.MissingVariables = append(preview.MissingVariables, PromptMissingVariable{Name: variable.Name, Kind: variable.Kind, Message: fmt.Sprintf("missing variable {{%s}}: no sample output supplied; a run without this recorded output pauses before the model call", variable.Name)})
			}
		default:
			if _, ok := sampleInputs[variable.Name]; !ok {
				preview.MissingVariables = append(preview.MissingVariables, PromptMissingVariable{Name: variable.Name, Kind: variable.Kind, Message: fmt.Sprintf("missing variable {{%s}}: no sample run input supplied; a run without it sends the literal token to the model", variable.Name)})
			}
		}
	}

	// Mirror the runner: step output references resolve first, then the
	// executor substitutes run inputs.
	request := stepOutputReferencePattern.ReplaceAllStringFunc(template, func(token string) string {
		references, err := parseStepOutputReferences(token)
		if err != nil || len(references) != 1 {
			return token
		}
		if sensitiveJSONKey(references[0].Name) {
			redacted = true
			return "[REDACTED]"
		}
		value, ok := outputs.lookup(references[0])
		if !ok {
			return token
		}
		return value
	})
	request = renderTemplateValues(request, sampleInputs)
	rendered := applyPipelineProfilePrompt(PipelineStep{profileContract: contract}, strings.TrimSpace(request))
	preview.WorkflowRequest = RedactForLog(request)
	preview.Rendered = RedactForLog(rendered)
	preview.Redacted = redacted || preview.Rendered != rendered || preview.WorkflowRequest != request
	preview.OK = len(preview.MissingVariables) == 0
	for _, problem := range preview.Problems {
		if problem.Severity == PromptProblemError {
			preview.OK = false
		}
	}
	return preview, nil
}

func (a *API) promptInventory(w http.ResponseWriter, r *http.Request) {
	var pipelines []Pipeline
	if a.runner != nil {
		pipelines = a.runner.Pipelines()
	}
	inventory, err := BuildPromptInventory(a.store, a.effectiveAgentProfiles(), pipelines)
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, inventory)
}

// previewPrompt is a POST because a draft template and its samples can be
// large, so like every non-GET control route it needs the admin role. It is a
// dry run: it neither stores nor executes anything.
func (a *API) previewPrompt(w http.ResponseWriter, r *http.Request) {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input PromptPreviewInput
	if err := decoder.Decode(&input); err != nil {
		writeJSONDecodeError(w, err)
		return
	}
	preview, err := PreviewPrompt(a.store, a.effectiveAgentProfiles(), input)
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]PromptPreview{"preview": preview})
}
