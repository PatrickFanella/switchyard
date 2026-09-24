package controlplane

import "fmt"

// Workflow definitions keep every saved version immutable. The latest stored
// version is what the editor reads and edits; the active version, tracked
// separately, is what new runs snapshot. Saving a draft appends a version
// without touching the active one, so a workflow keeps serving its active
// pipeline until an operator activates the draft.

// checkWorkflowDefinitionExpectedVersion enforces the optimistic concurrency
// precondition shared by saves and rollbacks. latest is zero when the ID is
// unused.
func checkWorkflowDefinitionExpectedVersion(id string, latest int, expected *int) error {
	if expected == nil {
		return nil
	}
	switch {
	case *expected < 0:
		return invalidDefinition("expected_version must be 0 or greater, got %d", *expected)
	case *expected == 0 && latest != 0:
		return fmt.Errorf("%w: workflow definition %q already exists at version %d; expected_version 0 only creates a new definition", ErrConflict, id, latest)
	case *expected != 0 && latest == 0:
		return fmt.Errorf("%w: workflow definition %q does not exist; expected_version was %d", ErrConflict, id, *expected)
	case *expected != latest:
		return fmt.Errorf("%w: workflow definition %q latest version is %d, expected_version was %d; reload and reapply your changes", ErrConflict, id, latest, *expected)
	}
	return nil
}

// checkWorkflowDefinitionActivationTarget allows activating only the latest
// stored version; requested zero means the latest.
func checkWorkflowDefinitionActivationTarget(id string, latest, requested int) error {
	if requested < 0 {
		return invalidDefinition("version must be 1 or greater, got %d", requested)
	}
	if requested != 0 && requested != latest {
		return fmt.Errorf("%w: workflow definition %q version %d is not the latest saved version (latest is %d); only the latest version can be activated, use rollback for an earlier version", ErrConflict, id, requested, latest)
	}
	return nil
}

// withWorkflowDefinitionActiveVersion derives the per-version status from the
// active version pointer so stored status columns cannot disagree with it.
func withWorkflowDefinitionActiveVersion(definition WorkflowDefinition, activeVersion int) WorkflowDefinition {
	definition.ActiveVersion = activeVersion
	if activeVersion != 0 && definition.Version == activeVersion {
		definition.Status = "active"
	} else {
		definition.Status = "draft"
	}
	return definition
}

// activeWorkflowDefinitions returns the active version of every definition that
// has one.
func activeWorkflowDefinitions(store Store) ([]WorkflowDefinition, error) {
	latest, err := store.ListWorkflowDefinitions()
	if err != nil {
		return nil, err
	}
	out := make([]WorkflowDefinition, 0, len(latest))
	for _, definition := range latest {
		switch {
		case definition.ActiveVersion == 0:
			continue
		case definition.ActiveVersion == definition.Version:
			out = append(out, definition)
		default:
			active, err := store.ActiveWorkflowDefinition(definition.ID)
			if err != nil {
				return nil, err
			}
			out = append(out, active)
		}
	}
	return out, nil
}
