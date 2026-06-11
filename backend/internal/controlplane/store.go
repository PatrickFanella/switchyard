package controlplane

import (
	"errors"
	"sort"
	"sync"
	"time"
)

var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid input")
)

type Store struct {
	mu        sync.RWMutex
	now       func() time.Time
	seq       int64
	workflows map[string]Workflow
	triggers  map[string]Trigger
	runs      map[string]Run
	events    map[string][]RunEvent
	artifacts map[string][]Artifact
}

func NewStore(now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{
		now:       now,
		workflows: map[string]Workflow{},
		triggers:  map[string]Trigger{},
		runs:      map[string]Run{},
		events:    map[string][]RunEvent{},
		artifacts: map[string][]Artifact{},
	}
}

func (s *Store) nextID(prefix string) string {
	s.seq++
	return prefix + "-" + time.Unix(0, s.seq).UTC().Format("150405.000000000")
}

func (s *Store) UpsertWorkflow(w Workflow) error {
	if w.ID == "" || w.Kind == "" || w.Name == "" {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workflows[w.ID] = w
	return nil
}

func (s *Store) UpsertTrigger(t Trigger) error {
	if t.ID == "" || t.WorkflowID == "" || t.Type == "" || t.Name == "" {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.workflows[t.WorkflowID]; !ok {
		return ErrNotFound
	}
	s.triggers[t.ID] = t
	return nil
}

func (s *Store) ListWorkflows() []Workflow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Workflow, 0, len(s.workflows))
	for _, w := range s.workflows {
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *Store) ListTriggers() []Trigger {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Trigger, 0, len(s.triggers))
	for _, t := range s.triggers {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *Store) Trigger(id string) (Trigger, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.triggers[id]
	return t, ok
}

func (s *Store) CreateRun(input CreateRunInput) (Run, error) {
	if input.WorkflowID == "" {
		return Run{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.workflows[input.WorkflowID]; !ok {
		return Run{}, ErrNotFound
	}
	if input.TriggerID != "" {
		if _, ok := s.triggers[input.TriggerID]; !ok {
			return Run{}, ErrNotFound
		}
	}
	now := s.now().UTC()
	r := Run{ID: s.nextID("run"), WorkflowID: input.WorkflowID, TriggerID: input.TriggerID, Status: RunQueued, Inputs: cloneMap(input.Inputs), CreatedAt: now}
	s.runs[r.ID] = r
	s.events[r.ID] = append(s.events[r.ID], RunEvent{ID: s.nextID("evt"), RunID: r.ID, Type: "run.created", Message: "Run queued", CreatedAt: now})
	return r, nil
}

func (s *Store) StartRun(id string) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		return Run{}, ErrNotFound
	}
	now := s.now().UTC()
	r.Status = RunRunning
	r.StartedAt = &now
	s.runs[id] = r
	s.events[id] = append(s.events[id], RunEvent{ID: s.nextID("evt"), RunID: id, Type: "run.started", Message: "Run started", CreatedAt: now})
	return r, nil
}

func (s *Store) FinishRun(id string, status RunStatus, summary string) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		return Run{}, ErrNotFound
	}
	now := s.now().UTC()
	r.Status = status
	r.FinishedAt = &now
	r.Summary = summary
	s.runs[id] = r
	s.events[id] = append(s.events[id], RunEvent{ID: s.nextID("evt"), RunID: id, Type: "run." + string(status), Message: summary, CreatedAt: now})
	return r, nil
}

func (s *Store) AddEvent(runID, typ, message string) (RunEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.runs[runID]; !ok {
		return RunEvent{}, ErrNotFound
	}
	e := RunEvent{ID: s.nextID("evt"), RunID: runID, Type: typ, Message: message, CreatedAt: s.now().UTC()}
	s.events[runID] = append(s.events[runID], e)
	return e, nil
}

func (s *Store) ListRuns() []Run {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Run, 0, len(s.runs))
	for _, r := range s.runs {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func (s *Store) RunDetail(id string) (RunDetail, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.runs[id]
	if !ok {
		return RunDetail{}, ErrNotFound
	}
	return RunDetail{Run: r, Events: append([]RunEvent(nil), s.events[id]...), Artifacts: append([]Artifact(nil), s.artifacts[id]...)}, nil
}

func cloneMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
