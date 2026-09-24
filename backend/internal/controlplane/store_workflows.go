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
	s.mu.Lock()
	defer s.mu.Unlock()
	definition, err := normalizeWorkflowDefinition(definition, memoryLockedAgentProfileRegistry{store: s})
	if err != nil {
		return WorkflowDefinition{}, err
	}
	now := s.now().UTC()
	if existing, ok := s.workflowDefinitions[definition.ID]; ok {
		definition.CreatedAt = existing.CreatedAt
		definition.Version = existing.Version + 1
	} else {
		definition.CreatedAt = now
		definition.Version = 1
	}
	definition.UpdatedAt = now
	s.workflowDefinitions[definition.ID] = cloneWorkflowDefinition(definition)
	if s.workflowDefinitionVersions[definition.ID] == nil {
		s.workflowDefinitionVersions[definition.ID] = map[int]WorkflowDefinition{}
	}
	s.workflowDefinitionVersions[definition.ID][definition.Version] = cloneWorkflowDefinition(definition)
	return cloneWorkflowDefinition(definition), nil
}

func (s *MemoryStore) ListWorkflowDefinitionVersions(id string) ([]WorkflowDefinition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	versions, ok := s.workflowDefinitionVersions[id]
	if !ok {
		return nil, ErrNotFound
	}
	out := make([]WorkflowDefinition, 0, len(versions))
	for _, definition := range versions {
		out = append(out, cloneWorkflowDefinition(definition))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out, nil
}

func (s *MemoryStore) ActivateWorkflowDefinition(id string) (WorkflowDefinition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	definition, ok := s.workflowDefinitions[id]
	if !ok {
		return WorkflowDefinition{}, ErrNotFound
	}
	definition.Status = "active"
	definition.UpdatedAt = s.now().UTC()
	s.workflowDefinitions[id] = definition
	s.workflowDefinitionVersions[id][definition.Version] = definition
	return cloneWorkflowDefinition(definition), nil
}

func (s *MemoryStore) RollbackWorkflowDefinition(id string, version int) (WorkflowDefinition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.workflowDefinitions[id]
	if !ok {
		return WorkflowDefinition{}, ErrNotFound
	}
	target, ok := s.workflowDefinitionVersions[id][version]
	if !ok {
		return WorkflowDefinition{}, ErrNotFound
	}
	target.Version = current.Version + 1
	target.Status = "active"
	target.CreatedAt = current.CreatedAt
	target.UpdatedAt = s.now().UTC()
	s.workflowDefinitions[id] = cloneWorkflowDefinition(target)
	s.workflowDefinitionVersions[id][target.Version] = cloneWorkflowDefinition(target)
	return cloneWorkflowDefinition(target), nil
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
