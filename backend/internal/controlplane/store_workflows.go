package controlplane

import (
	"sort"
	"time"
)

func (s *MemoryStore) UpsertWorkflow(w Workflow) error {
	if w.ID == "" || w.Kind == "" || w.Name == "" {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workflows[w.ID] = w
	return nil
}

func (s *MemoryStore) UpsertWorkflowDefinition(definition WorkflowDefinition) (WorkflowDefinition, error) {
	return s.SaveWorkflowDefinition(definition, nil)
}

func (s *MemoryStore) SaveWorkflowDefinition(definition WorkflowDefinition, expectedVersion *int) (WorkflowDefinition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	definition, err := normalizeWorkflowDefinition(definition, memoryLockedAgentProfileRegistry{store: s})
	if err != nil {
		return WorkflowDefinition{}, err
	}
	existing, exists := s.workflowDefinitions[definition.ID]
	if err := checkWorkflowDefinitionExpectedVersion(definition.ID, existing.Version, expectedVersion); err != nil {
		return WorkflowDefinition{}, err
	}
	return s.appendWorkflowDefinitionVersionLocked(definition, existing, exists), nil
}

// appendWorkflowDefinitionVersionLocked stores definition as the next
// immutable version. Status "active" makes the new version active; a draft
// keeps the existing active version.
func (s *MemoryStore) appendWorkflowDefinitionVersionLocked(definition, existing WorkflowDefinition, exists bool) WorkflowDefinition {
	now := s.now().UTC()
	activeVersion := 0
	if exists {
		definition.CreatedAt = existing.CreatedAt
		definition.Version = existing.Version + 1
		activeVersion = existing.ActiveVersion
	} else {
		definition.CreatedAt = now
		definition.Version = 1
	}
	if isActiveWorkflowDefinition(definition) {
		activeVersion = definition.Version
	}
	definition.UpdatedAt = now
	definition = withWorkflowDefinitionActiveVersion(definition, activeVersion)
	s.workflowDefinitions[definition.ID] = cloneWorkflowDefinition(definition)
	if s.workflowDefinitionVersions[definition.ID] == nil {
		s.workflowDefinitionVersions[definition.ID] = map[int]WorkflowDefinition{}
	}
	s.workflowDefinitionVersions[definition.ID][definition.Version] = cloneWorkflowDefinition(definition)
	return cloneWorkflowDefinition(definition)
}

func (s *MemoryStore) ListWorkflowDefinitionVersions(id string) ([]WorkflowDefinition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	versions, ok := s.workflowDefinitionVersions[id]
	if !ok {
		return nil, ErrNotFound
	}
	activeVersion := s.workflowDefinitions[id].ActiveVersion
	out := make([]WorkflowDefinition, 0, len(versions))
	for _, definition := range versions {
		out = append(out, withWorkflowDefinitionActiveVersion(cloneWorkflowDefinition(definition), activeVersion))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out, nil
}

func (s *MemoryStore) ActivateWorkflowDefinition(id string) (WorkflowDefinition, error) {
	return s.ActivateWorkflowDefinitionVersion(id, 0)
}

func (s *MemoryStore) ActivateWorkflowDefinitionVersion(id string, version int) (WorkflowDefinition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	definition, ok := s.workflowDefinitions[id]
	if !ok {
		return WorkflowDefinition{}, ErrNotFound
	}
	if err := checkWorkflowDefinitionActivationTarget(id, definition.Version, version); err != nil {
		return WorkflowDefinition{}, err
	}
	if definition.ActiveVersion != definition.Version {
		definition = withWorkflowDefinitionActiveVersion(definition, definition.Version)
		definition.UpdatedAt = s.now().UTC()
		s.workflowDefinitions[id] = definition
		s.workflowDefinitionVersions[id][definition.Version] = cloneWorkflowDefinition(definition)
	}
	return cloneWorkflowDefinition(definition), nil
}

func (s *MemoryStore) RollbackWorkflowDefinition(id string, version int) (WorkflowDefinition, error) {
	return s.RollbackWorkflowDefinitionVersion(id, version, nil)
}

func (s *MemoryStore) RollbackWorkflowDefinitionVersion(id string, version int, expectedVersion *int) (WorkflowDefinition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.workflowDefinitions[id]
	if !ok {
		return WorkflowDefinition{}, ErrNotFound
	}
	if err := checkWorkflowDefinitionExpectedVersion(id, current.Version, expectedVersion); err != nil {
		return WorkflowDefinition{}, err
	}
	target, ok := s.workflowDefinitionVersions[id][version]
	if !ok {
		return WorkflowDefinition{}, ErrNotFound
	}
	target = cloneWorkflowDefinition(target)
	target.Status = "active"
	target, err := normalizeWorkflowDefinition(target, memoryLockedAgentProfileRegistry{store: s})
	if err != nil {
		return WorkflowDefinition{}, err
	}
	return s.appendWorkflowDefinitionVersionLocked(target, current, true), nil
}

func (s *MemoryStore) WorkflowDefinition(id string) (WorkflowDefinition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	definition, ok := s.workflowDefinitions[id]
	if !ok {
		return WorkflowDefinition{}, ErrNotFound
	}
	return cloneWorkflowDefinition(definition), nil
}

func (s *MemoryStore) ActiveWorkflowDefinition(id string) (WorkflowDefinition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	definition, ok := s.activeWorkflowDefinitionLocked(id)
	if !ok {
		return WorkflowDefinition{}, ErrNotFound
	}
	return definition, nil
}

func (s *MemoryStore) activeWorkflowDefinitionLocked(id string) (WorkflowDefinition, bool) {
	latest, ok := s.workflowDefinitions[id]
	if !ok || latest.ActiveVersion == 0 {
		return WorkflowDefinition{}, false
	}
	active, ok := s.workflowDefinitionVersions[id][latest.ActiveVersion]
	if !ok {
		return WorkflowDefinition{}, false
	}
	return withWorkflowDefinitionActiveVersion(cloneWorkflowDefinition(active), latest.ActiveVersion), true
}

func (s *MemoryStore) ListWorkflowDefinitions() ([]WorkflowDefinition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]WorkflowDefinition, 0, len(s.workflowDefinitions))
	for _, definition := range s.workflowDefinitions {
		out = append(out, cloneWorkflowDefinition(definition))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *MemoryStore) UpsertTrigger(t Trigger) error {
	if t.ID == "" || t.WorkflowID == "" || t.Type == "" || t.Name == "" {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.workflows[t.WorkflowID]; !ok {
		return ErrNotFound
	}
	now := s.now().UTC()
	if t.UpdatedAt == nil {
		t.UpdatedAt = &now
	}
	s.triggers[t.ID] = t
	return nil
}

func (s *MemoryStore) ListWorkflows() []Workflow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Workflow, 0, len(s.workflows))
	for _, w := range s.workflows {
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *MemoryStore) ListTriggers() []Trigger {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Trigger, 0, len(s.triggers))
	for _, t := range s.triggers {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *MemoryStore) Trigger(id string) (Trigger, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.triggers[id]
	return t, ok
}

func (s *MemoryStore) SetTriggerEnabled(id string, enabled bool) (Trigger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.triggers[id]
	if !ok {
		return Trigger{}, ErrNotFound
	}
	now := s.now().UTC()
	t.Enabled = enabled
	t.UpdatedAt = &now
	s.triggers[id] = t
	return t, nil
}

func (s *MemoryStore) UpdateTriggerScheduleState(id string, lastRunAt *time.Time, nextRunAt *time.Time, lastError string) (Trigger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.triggers[id]
	if !ok {
		return Trigger{}, ErrNotFound
	}
	now := s.now().UTC()
	if lastRunAt != nil {
		last := lastRunAt.UTC()
		t.LastRunAt = &last
	}
	if nextRunAt != nil {
		next := nextRunAt.UTC()
		t.NextRunAt = &next
	}
	t.LastError = lastError
	t.UpdatedAt = &now
	s.triggers[id] = t
	return t, nil
}
