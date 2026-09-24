package controlplane

import (
	"encoding/json"
	"io"
	"net/http"
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

func (a *API) activateWorkflowDefinition(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	definition, err := a.store.WorkflowDefinition(id)
	if err != nil {
		writeControlError(w, err)
		return
	}
	if err := validateWorkflowDefinitionForActivation(definition); err != nil {
		writeControlError(w, err)
		return
	}
	activated, err := a.store.ActivateWorkflowDefinition(id)
	if err != nil {
		writeControlError(w, err)
		return
	}
	if err := a.store.UpsertWorkflow(workflowFromDefinition(activated)); err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflow_definition": activated})
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
	rolledBack, err := a.store.RollbackWorkflowDefinition(id, input.Version)
	if err != nil {
		writeControlError(w, err)
		return
	}
	if err := a.store.UpsertWorkflow(workflowFromDefinition(rolledBack)); err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflow_definition": rolledBack, "rolled_back_from_version": input.Version})
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
	definition.Status = "draft"
	saved, err := a.store.UpsertWorkflowDefinition(definition)
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflow_definition": saved})
}

func validateWorkflowDefinitionWrite(definition WorkflowDefinition) error {
	if definition.Status != "" && !strings.EqualFold(definition.Status, "draft") {
		return ErrInvalid
	}
	return validateWorkflowDefinitionStructure(definition, false)
}
func validateWorkflowDefinitionForActivation(definition WorkflowDefinition) error {
	return validateWorkflowDefinitionStructure(definition, true)
}
func validateWorkflowDefinitionStructure(definition WorkflowDefinition, requireExecutable bool) error {
	seenIDs := map[string]bool{}
	seenPositions := map[int]bool{}
	for _, step := range definition.Pipeline.Steps {
		id := strings.TrimSpace(step.ID)
		if id == "" || seenIDs[id] {
			return ErrInvalid
		}
		seenIDs[id] = true
		if step.Position < 1 || seenPositions[step.Position] {
			return ErrInvalid
		}
		seenPositions[step.Position] = true
		switch strings.TrimSpace(step.Executor) {
		case "internal":
		case "model":
			if requireExecutable && strings.TrimSpace(firstNonEmpty(step.Inputs["prompt"], step.Inputs["template"])) == "" {
				return ErrInvalid
			}
		case "command":
			if requireExecutable && strings.TrimSpace(step.Inputs["command"]) == "" {
				return ErrInvalid
			}
		case "http":
			if requireExecutable && (strings.TrimSpace(step.Inputs["url"]) == "" || !strings.EqualFold(strings.TrimSpace(step.Inputs["allow_http"]), "true")) {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
	}
	for position := 1; position <= len(definition.Pipeline.Steps); position++ {
		if !seenPositions[position] {
			return ErrInvalid
		}
	}
	if definition.InputShape.Type != "" && definition.InputShape.Type != "object" {
		return ErrInvalid
	}
	for _, required := range definition.InputShape.Required {
		if _, ok := definition.InputShape.Properties[required]; !ok {
			return ErrInvalid
		}
	}
	for _, property := range definition.InputShape.Properties {
		switch property.Type {
		case "string", "number", "integer", "boolean", "array", "object":
		default:
			return ErrInvalid
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
