package controlplane

import (
	"context"
	"sort"
	"strings"
	"time"
)

func (s *MemoryStore) CreateModelBrokerRequest(input CreateModelBrokerRequestInput) (ModelBrokerRequest, error) {
	req, _, err := s.admitModelBrokerRequest(input, ModelBrokerRequestQueued, 0)
	return req, err
}

func (s *MemoryStore) BeginModelBrokerRequest(input CreateModelBrokerRequestInput) (ModelBrokerRequest, bool, error) {
	return s.admitModelBrokerRequest(input, ModelBrokerRequestRunning, 0)
}

func (s *MemoryStore) AdmitModelBrokerRequest(input CreateModelBrokerRequestInput, maxActive int) (ModelBrokerRequest, bool, error) {
	return s.admitModelBrokerRequest(input, ModelBrokerRequestQueued, maxActive)
}

func (s *MemoryStore) admitModelBrokerRequest(input CreateModelBrokerRequestInput, requestedStatus string, maxActive int) (ModelBrokerRequest, bool, error) {
	if strings.TrimSpace(input.ID) == "" || strings.TrimSpace(input.Model) == "" || (maxActive < 0) {
		return ModelBrokerRequest{}, false, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.modelBrokerRequests[input.ID]; ok {
		err := error(nil)
		if existing.Status == ModelBrokerRequestDropped && existing.Error == "queue capacity exceeded" {
			err = ErrQueueFull
		}
		return cloneModelBrokerRequest(existing), false, err
	}
	active := 0
	for _, req := range s.modelBrokerRequests {
		if req.Status == ModelBrokerRequestQueued || req.Status == ModelBrokerRequestRunning || req.Status == ModelBrokerRequestCancelRequested {
			active++
		}
	}
	now := s.now().UTC()
	status := requestedStatus
	if maxActive > 0 && active >= maxActive {
		status = ModelBrokerRequestDropped
	}
	req := ModelBrokerRequest{ID: input.ID, Status: status, Model: input.Model, Path: input.Path, Client: input.Client, ClientPriority: input.Priority, RequestBody: redactPersistedJSON(input.RequestBody), CorrelationIDs: map[string]string{}, CreatedAt: now, UpdatedAt: now}
	if status == ModelBrokerRequestRunning {
		req.StartedAt = &now
	}
	if status == ModelBrokerRequestDropped {
		req.Error = "queue capacity exceeded"
		req.FinishedAt = &now
	}
	s.modelBrokerRequests[req.ID] = req
	if status == ModelBrokerRequestDropped {
		return cloneModelBrokerRequest(req), false, ErrQueueFull
	}
	return cloneModelBrokerRequest(req), true, nil
}

func (s *MemoryStore) TransitionModelBrokerRequest(id string, input TransitionModelBrokerRequestInput) (ModelBrokerRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	req, ok := s.modelBrokerRequests[id]
	if !ok {
		return ModelBrokerRequest{}, ErrNotFound
	}
	if req.Status != input.ExpectedStatus || !ModelBrokerRequestTransitionAllowed(req.Status, input.Status) {
		return ModelBrokerRequest{}, ErrConflict
	}
	now := s.now().UTC()
	req.Status = input.Status
	req.UpdatedAt = now
	req.CorrelationIDs = mergeModelCorrelationIDs(req.CorrelationIDs, input.CorrelationIDs)
	if input.Status == ModelBrokerRequestRunning {
		req.StartedAt = &now
	}
	if input.Status == ModelBrokerRequestCancelRequested && req.CancelRequestedAt == nil {
		req.CancelRequestedAt = &now
	}
	if input.Status == ModelBrokerRequestCanceled && req.CancelRequestedAt == nil {
		req.CancelRequestedAt = &now
	}
	if isTerminalModelBrokerRequest(input.Status) {
		req.ResponseBody = redactPersistedJSON(input.ResponseBody)
		req.Error = embeddedSecretPattern.ReplaceAllString(input.Error, `${1}${2}${3}[REDACTED]`)
		req.FinishedAt = &now
	}
	s.modelBrokerRequests[id] = req
	return cloneModelBrokerRequest(req), nil
}

func (s *MemoryStore) StartModelBrokerRequest(id string) (ModelBrokerRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	req, ok := s.modelBrokerRequests[id]
	if !ok {
		return ModelBrokerRequest{}, ErrNotFound
	}
	if req.Status != ModelBrokerRequestQueued {
		return ModelBrokerRequest{}, ErrNotFound
	}
	now := s.now().UTC()
	req.Status = ModelBrokerRequestRunning
	req.StartedAt = &now
	req.UpdatedAt = now
	s.modelBrokerRequests[id] = req
	return cloneModelBrokerRequest(req), nil
}

func (s *MemoryStore) ClaimNextModelBrokerRequest() (ModelBrokerRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var selected *ModelBrokerRequest
	for _, candidate := range s.modelBrokerRequests {
		if candidate.Status != ModelBrokerRequestQueued {
			continue
		}
		if selected == nil || candidate.ClientPriority > selected.ClientPriority || (candidate.ClientPriority == selected.ClientPriority && candidate.CreatedAt.Before(selected.CreatedAt)) || (candidate.ClientPriority == selected.ClientPriority && candidate.CreatedAt.Equal(selected.CreatedAt) && candidate.ID < selected.ID) {
			copy := candidate
			selected = &copy
		}
	}
	if selected == nil {
		return ModelBrokerRequest{}, ErrNotFound
	}
	now := s.now().UTC()
	selected.Status = ModelBrokerRequestRunning
	selected.StartedAt = &now
	selected.UpdatedAt = now
	s.modelBrokerRequests[selected.ID] = *selected
	return cloneModelBrokerRequest(*selected), nil
}

func (s *MemoryStore) FinishModelBrokerRequest(id string, input FinishModelBrokerRequestInput) (ModelBrokerRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	req, ok := s.modelBrokerRequests[id]
	if !ok {
		return ModelBrokerRequest{}, ErrNotFound
	}
	now := s.now().UTC()
	status := strings.TrimSpace(input.Status)
	if status == "" {
		status = ModelBrokerRequestCompleted
	}
	req.Status = status
	req.ResponseBody = redactPersistedJSON(input.ResponseBody)
	req.Error = embeddedSecretPattern.ReplaceAllString(input.Error, `${1}${2}${3}[REDACTED]`)
	req.CorrelationIDs = mergeModelCorrelationIDs(req.CorrelationIDs, input.CorrelationIDs)
	req.FinishedAt = &now
	req.UpdatedAt = now
	s.modelBrokerRequests[id] = req
	return cloneModelBrokerRequest(req), nil
}

func (s *MemoryStore) MergeModelBrokerRequestCorrelationIDs(id string, correlationIDs map[string]string) (ModelBrokerRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	req, ok := s.modelBrokerRequests[id]
	if !ok {
		return ModelBrokerRequest{}, ErrNotFound
	}
	req.CorrelationIDs = mergeModelCorrelationIDs(req.CorrelationIDs, correlationIDs)
	req.UpdatedAt = s.now().UTC()
	if backend := strings.TrimSpace(correlationIDs[CorrelationDeprecatedBackend]); backend != "" {
		if err := s.recordCompatibilityUsageLocked(backend, req.UpdatedAt); err != nil {
			return ModelBrokerRequest{}, err
		}
	}
	s.modelBrokerRequests[id] = req
	return cloneModelBrokerRequest(req), nil
}

func (s *MemoryStore) CancelModelBrokerRequest(id string) (ModelBrokerRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	req, ok := s.modelBrokerRequests[id]
	if !ok {
		return ModelBrokerRequest{}, ErrNotFound
	}
	if isTerminalModelBrokerRequest(req.Status) {
		return cloneModelBrokerRequest(req), nil
	}
	now := s.now().UTC()
	if req.Status == ModelBrokerRequestQueued {
		req.Status, req.Error, req.FinishedAt = ModelBrokerRequestCanceled, "request canceled before execution", &now
	} else {
		req.Status = ModelBrokerRequestCancelRequested
	}
	if req.CancelRequestedAt == nil {
		req.CancelRequestedAt = &now
	}
	req.UpdatedAt = now
	s.modelBrokerRequests[id] = req
	return cloneModelBrokerRequest(req), nil
}

func (s *MemoryStore) ModelBrokerQueueSnapshot(idLimit int) (ModelBrokerQueueSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshot := ModelBrokerQueueSnapshot{InFlightIDs: []string{}}
	ids := []string{}
	for _, req := range s.modelBrokerRequests {
		switch req.Status {
		case ModelBrokerRequestQueued:
			snapshot.Queued++
		case ModelBrokerRequestRunning:
			snapshot.Running++
			ids = append(ids, req.ID)
		case ModelBrokerRequestCancelRequested:
			snapshot.CancelRequested++
			ids = append(ids, req.ID)
		}
	}
	sort.Strings(ids)
	if idLimit >= 0 && len(ids) > idLimit {
		ids = ids[:idLimit]
	}
	snapshot.InFlightIDs = ids
	return snapshot, nil
}

func (s *MemoryStore) CancelAllModelBrokerRequests() (int, error) {
	requests, err := s.RequestCancelModelBrokerRequests(nil)
	return len(requests), err
}

func (s *MemoryStore) RequestCancelModelBrokerRequests(correlationIDs map[string]string) ([]ModelBrokerRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	out := []ModelBrokerRequest{}
	for id, req := range s.modelBrokerRequests {
		switch req.Status {
		case ModelBrokerRequestQueued:
			req.Status, req.Error, req.FinishedAt = ModelBrokerRequestCanceled, "request canceled before execution", &now
			req.CancelRequestedAt = &now
		case ModelBrokerRequestRunning:
			req.Status, req.CancelRequestedAt = ModelBrokerRequestCancelRequested, &now
		case ModelBrokerRequestCancelRequested:
			continue
		default:
			continue
		}
		req.UpdatedAt = now
		req.CorrelationIDs = mergeModelCorrelationIDs(req.CorrelationIDs, correlationIDs)
		s.modelBrokerRequests[id] = req
		out = append(out, cloneModelBrokerRequest(req))
	}
	return out, nil
}

func mergeModelCorrelationIDs(base, updates map[string]string) map[string]string {
	merged := mergeStringMaps(base, updates)
	if base["audit_action"] != "" {
		for _, key := range []string{"audit_action", "audit_actor", "audit_reason"} {
			if value, ok := base[key]; ok {
				merged[key] = value
			} else {
				delete(merged, key)
			}
		}
	}
	return merged
}

func (s *MemoryStore) ListModelBrokerRequests(filter ListModelBrokerRequestsFilter) ([]ModelBrokerRequest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	statuses := map[string]bool{}
	for _, status := range filter.Statuses {
		statuses[status] = true
	}
	query := strings.ToLower(strings.TrimSpace(filter.Query))
	out := []ModelBrokerRequest{}
	for _, req := range s.modelBrokerRequests {
		if len(statuses) > 0 && !statuses[req.Status] {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(req.ID+" "+req.Model+" "+req.Path+" "+req.Client+" "+req.RequestBody+" "+req.ResponseBody+" "+req.Error), query) {
			continue
		}
		if filter.BeforeCreated != nil && (req.CreatedAt.After(*filter.BeforeCreated) || (req.CreatedAt.Equal(*filter.BeforeCreated) && req.ID >= filter.BeforeID)) {
			continue
		}
		if filter.SinceCreated != nil && req.CreatedAt.Before(*filter.SinceCreated) {
			continue
		}
		out = append(out, cloneModelBrokerRequest(req))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	limit := filter.Limit
	if limit <= 0 || limit > 201 {
		limit = 200
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MemoryStore) ListModelBrokerRequestsContext(_ context.Context, filter ListModelBrokerRequestsFilter) ([]ModelBrokerRequest, error) {
	return s.ListModelBrokerRequests(filter)
}

func (s *MemoryStore) ModelBrokerRequest(id string) (ModelBrokerRequest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	req, ok := s.modelBrokerRequests[id]
	if !ok {
		return ModelBrokerRequest{}, ErrNotFound
	}
	return cloneModelBrokerRequest(req), nil
}

func (s *MemoryStore) ModelBrokerRequestContext(_ context.Context, id string) (ModelBrokerRequest, error) {
	return s.ModelBrokerRequest(id)
}

func (s *MemoryStore) AddModelBrokerFeedback(input CreateModelBrokerFeedbackInput) (ModelBrokerFeedback, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(input.RequestID) == "" || input.Rating < -1 || input.Rating > 1 || len(input.Comment) > 4096 {
		return ModelBrokerFeedback{}, ErrInvalid
	}
	if _, ok := s.modelBrokerRequests[input.RequestID]; !ok {
		return ModelBrokerFeedback{}, ErrNotFound
	}
	feedback := ModelBrokerFeedback{ID: newID("feedback"), RequestID: input.RequestID, Rating: input.Rating, Comment: embeddedSecretPattern.ReplaceAllString(input.Comment, `${1}${2}${3}[REDACTED]`), CreatedAt: s.now().UTC()}
	s.modelBrokerFeedback = append(s.modelBrokerFeedback, feedback)
	return feedback, nil
}

func (s *MemoryStore) ListModelBrokerFeedback(limit int) ([]ModelBrokerFeedback, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit < 1 || limit > 200 {
		return nil, ErrInvalid
	}
	start := max(0, len(s.modelBrokerFeedback)-limit)
	out := append([]ModelBrokerFeedback(nil), s.modelBrokerFeedback[start:]...)
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *MemoryStore) PruneRetention(before time.Time) (RetentionResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := RetentionResult{}
	for id, run := range s.runs {
		if !isTerminalRunStatus(run.Status) || !run.CreatedAt.Before(before) {
			continue
		}
		delete(s.runs, id)
		delete(s.events, id)
		for _, artifact := range s.artifacts[id] {
			result.Artifacts++
			result.ArtifactBytes += int64(artifact.ByteSize)
		}
		delete(s.artifacts, id)
		for stepID, step := range s.steps {
			if step.RunID == id {
				delete(s.steps, stepID)
			}
		}
		result.Runs++
	}
	for id, req := range s.modelBrokerRequests {
		if !isTerminalModelBrokerRequest(req.Status) || !req.CreatedAt.Before(before) {
			continue
		}
		delete(s.modelBrokerRequests, id)
		keptFeedback := s.modelBrokerFeedback[:0]
		for _, feedback := range s.modelBrokerFeedback {
			if feedback.RequestID != id {
				keptFeedback = append(keptFeedback, feedback)
			}
		}
		s.modelBrokerFeedback = keptFeedback
		result.ModelRequests++
	}
	return result, nil
}

func (s *MemoryStore) RuntimeMetrics() (RuntimeMetrics, error) {
	return s.RuntimeMetricsSince(context.Background(), time.Time{})
}

func (s *MemoryStore) RuntimeMetricsSince(_ context.Context, since time.Time) (RuntimeMetrics, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	metrics := RuntimeMetrics{RunStatusCounts: map[string]int{}, ModelRequestStatusCounts: map[string]int{}}
	for _, run := range s.runs {
		metrics.RunStatusCounts[string(run.Status)]++
	}
	for _, request := range s.modelBrokerRequests {
		if !since.IsZero() && request.CreatedAt.Before(since) {
			continue
		}
		metrics.ModelRequestStatusCounts[request.Status]++
		if request.StartedAt != nil && request.FinishedAt != nil {
			metrics.ModelLatencyCount++
			milliseconds := float64(request.FinishedAt.Sub(*request.StartedAt).Microseconds()) / 1000
			metrics.ModelLatencyMilliseconds += milliseconds
			if milliseconds > metrics.ModelLatencyMaxMilliseconds {
				metrics.ModelLatencyMaxMilliseconds = milliseconds
			}
		}
	}
	return metrics, nil
}
