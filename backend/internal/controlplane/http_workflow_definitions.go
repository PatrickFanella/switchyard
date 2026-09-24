package controlplane

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
)

func (a *API) workflowDefinitions(w http.ResponseWriter, r *http.Request) {
	definitions, err := a.store.ListWorkflowDefinitions()
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflow_definitions": workflowDefinitionsOrEmpty(definitions)})
}

func (a *API) workflowDefinitionAction(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusNotFound, "workflow definition not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		a.getWorkflowDefinition(w, r, id)
	case http.MethodPut:
		a.updateWorkflowDefinition(w, r, id)
	default:
		writeError(w, http.StatusNotFound, "workflow definition not found")
	}
}

func (a *API) getWorkflowDefinition(w http.ResponseWriter, r *http.Request, id string) {
	definition, err := a.store.WorkflowDefinition(id)
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflow_definition": definition})
}

func (a *API) workflowDefinitionVersions(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	versions, err := a.store.ListWorkflowDefinitionVersions(id)
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflow_definitions": versions})
}

// activateWorkflowDefinition activates the latest saved version. An optional
// {"version": N} body names the version the caller reviewed; if a newer version
// was saved since, the request fails with 409 instead of activating it.
func (a *API) activateWorkflowDefinition(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	var input WorkflowDefinitionActivationInput
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSONDecodeError(w, err)
		return
	}
	if len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, &input); err != nil {
			writeJSONDecodeError(w, err)
			return
		}
	}
	definition, err := a.store.WorkflowDefinition(id)
	if err != nil {
		writeControlError(w, err)
		return
	}
	if err := checkWorkflowDefinitionActivationTarget(id, definition.Version, input.Version); err != nil {
		writeControlError(w, err)
		return
	}
	if err := validateWorkflowDefinitionForActivation(definition); err != nil {
		writeControlError(w, err)
		return
	}
	// Activate exactly the validated version; a concurrent save makes this 409.
	activated, err := a.store.ActivateWorkflowDefinitionVersion(id, definition.Version)
	if err != nil {
		writeControlError(w, err)
		return
	}
	if err := a.store.UpsertWorkflow(workflowFromDefinition(activated)); err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflow_definition": activated, "previous_active_version": definition.ActiveVersion})
}

func (a *API) rollbackWorkflowDefinition(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	var input WorkflowDefinitionRollbackInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSONDecodeError(w, err)
		return
	}
	versions, err := a.store.ListWorkflowDefinitionVersions(id)
	if err != nil {
		writeControlError(w, err)
		return
	}
	var target *WorkflowDefinition
	for index := range versions {
		if versions[index].Version == input.Version {
			target = &versions[index]
			break
		}
	}
	if target == nil {
		writeControlError(w, ErrNotFound)
		return
	}
	if err := validateWorkflowDefinitionForActivation(*target); err != nil {
		writeControlError(w, err)
		return
	}
	previousActive := versions[0].ActiveVersion
	rolledBack, err := a.store.RollbackWorkflowDefinitionVersion(id, input.Version, input.ExpectedVersion)
	if err != nil {
		writeControlError(w, err)
		return
	}
	if err := a.store.UpsertWorkflow(workflowFromDefinition(rolledBack)); err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflow_definition": rolledBack, "rolled_back_from_version": input.Version, "previous_active_version": previousActive})
}

func workflowFromDefinition(definition WorkflowDefinition) Workflow {
	return Workflow{ID: definition.ID, Kind: WorkflowKind(definition.ID), Name: definition.Name, Description: definition.Description, Owns: append([]string(nil), definition.Owns...), Uses: append([]string(nil), definition.Uses...), Status: definition.Status}
}

func (a *API) updateWorkflowDefinition(w http.ResponseWriter, r *http.Request, id string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSONDecodeError(w, err)
		return
	}
	var definition WorkflowDefinition
	if err := json.Unmarshal(body, &definition); err != nil {
		writeJSONDecodeError(w, err)
		return
	}
	if err := validateRawOutputShapeKeywords(body); err != nil {
		writeControlError(w, err)
		return
	}
	definition.ID = id
	definition.Pipeline.WorkflowID = id
	if err := validateWorkflowDefinitionWrite(definition); err != nil {
		writeControlError(w, err)
		return
	}
	// expected_version is an optional save precondition: 0 creates only, N
	// requires that N is still the latest version. It is not a definition field.
	var precondition struct {
		ExpectedVersion *int `json:"expected_version"`
	}
	if err := json.Unmarshal(body, &precondition); err != nil {
		writeJSONDecodeError(w, err)
		return
	}
	definition.Status = "draft"
	saved, err := a.store.SaveWorkflowDefinition(definition, precondition.ExpectedVersion)
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflow_definition": saved})
}

func validateWorkflowDefinitionWrite(definition WorkflowDefinition) error {
	if definition.Status != "" && !strings.EqualFold(definition.Status, "draft") {
		return invalidDefinition("saving writes a draft; status must be empty or \"draft\", got %q (activate a saved draft separately)", definition.Status)
	}
	return validateWorkflowDefinitionStructure(definition, false)
}
func validateWorkflowDefinitionForActivation(definition WorkflowDefinition) error {
	return validateWorkflowDefinitionStructure(definition, true)
}

// supportedDefinitionExecutors are the executors a generic workflow definition
// step may use; first-party executors (opencode, git, gitea) stay in code.
var supportedDefinitionExecutors = []string{"internal", "model", "command", "http"}

func validateWorkflowDefinitionStructure(definition WorkflowDefinition, requireExecutable bool) error {
	seenIDs := map[string]bool{}
	seenPositions := map[int]bool{}
	for _, step := range definition.Pipeline.Steps {
		id := strings.TrimSpace(step.ID)
		if id == "" {
			return invalidDefinition("step at position %d: id is required", step.Position)
		}
		if seenIDs[id] {
			return invalidDefinition("step %q: id is used by more than one step", id)
		}
		seenIDs[id] = true
		if step.Position < 1 {
			return invalidDefinition("step %q: position must be 1 or greater, got %d", id, step.Position)
		}
		if seenPositions[step.Position] {
			return invalidDefinition("step %q: position %d is used by more than one step", id, step.Position)
		}
		seenPositions[step.Position] = true
		switch strings.TrimSpace(step.Executor) {
		case "internal":
		case "model":
			if requireExecutable && strings.TrimSpace(firstNonEmpty(step.Inputs["prompt"], step.Inputs["template"])) == "" {
				return invalidDefinition("step %q: model executor needs a prompt or template input", id)
			}
		case "command":
			if requireExecutable && strings.TrimSpace(step.Inputs["command"]) == "" {
				return invalidDefinition("step %q: command executor needs a command input", id)
			}
		case "http":
			if requireExecutable && strings.TrimSpace(step.Inputs["url"]) == "" {
				return invalidDefinition("step %q: http executor needs a url input", id)
			}
			if requireExecutable && !strings.EqualFold(strings.TrimSpace(step.Inputs["allow_http"]), "true") {
				return invalidDefinition("step %q: http executor needs allow_http set to \"true\"", id)
			}
		case "source":
			// Source steps only read through a named adapter; they carry no
			// agent profile and name a supported adapter even as drafts.
			if strings.TrimSpace(step.Profile) != "" || strings.TrimSpace(step.Inputs["adapter"]) != SourceAdapterGiteaActivity {
				return ErrInvalid
			}
		default:
			return invalidDefinition("step %q: executor %q is not supported; use one of %s", id, step.Executor, strings.Join(supportedDefinitionExecutors, ", "))
		}
	}
	for position := 1; position <= len(definition.Pipeline.Steps); position++ {
		if !seenPositions[position] {
			return invalidDefinition("step positions must run 1 to %d without gaps; position %d is missing", len(definition.Pipeline.Steps), position)
		}
	}
	if definition.InputShape.Type != "" && definition.InputShape.Type != "object" {
		return invalidDefinition("input_shape.type must be \"object\", got %q", definition.InputShape.Type)
	}
	for _, required := range definition.InputShape.Required {
		if _, ok := definition.InputShape.Properties[required]; !ok {
			return invalidDefinition("input_shape.required names %q, which is not a declared input field", required)
		}
	}
	names := make([]string, 0, len(definition.InputShape.Properties))
	for name := range definition.InputShape.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		switch definition.InputShape.Properties[name].Type {
		case "string", "number", "integer", "boolean", "array", "object":
		default:
			return invalidDefinition("input_shape.properties.%s.type must be one of string, number, integer, boolean, array, object; got %q", name, definition.InputShape.Properties[name].Type)
		}
	}
	// Versions saved before output contracts were enforced may carry shapes
	// outside the supported subset; they cannot be activated or rolled back to.
	return validatePipelineStepOutputShapes(definition.Pipeline.Steps)
}

func workflowsOrEmpty(items []Workflow) []Workflow {
	if items == nil {
		return []Workflow{}
	}
	return items
}
func workflowDefinitionsOrEmpty(items []WorkflowDefinition) []WorkflowDefinition {
	if items == nil {
		return []WorkflowDefinition{}
	}
	return items
}
