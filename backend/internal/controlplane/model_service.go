package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

type ModelServiceOptions struct {
	QueueDepth       int
	Workers          int
	StaleAfter       time.Duration
	RecoveryInterval time.Duration
	DisableStart     bool
}

// ModelService owns durable admission and request execution. EnvModelBroker
// remains the generation adapter and synchronous Generate contract.
type ModelService struct {
	store            Store
	generator        ModelBroker
	queueDepth       int
	workers          int
	staleAfter       time.Duration
	recoveryInterval time.Duration
	ctx              context.Context
	cancel           context.CancelFunc
	notify           chan struct{}
	mu               sync.Mutex
	active           map[string]*modelRequestGate
	wg               sync.WaitGroup
	stopOnce         sync.Once
}

type modelRequestGate struct {
	transitionMu sync.Mutex
	cancel       context.CancelFunc
	output       atomic.Uint32
}

const (
	modelOutputWriting uint32 = 1 << iota
	modelOutputClosed
)

var ErrModelOutputBusy = errors.New("model output write already in progress")

type ModelQueueStatus struct {
	Queued          int      `json:"queued"`
	Running         int      `json:"running"`
	CancelRequested int      `json:"cancel_requested"`
	MaxDepth        int      `json:"max_depth"`
	InFlightIDs     []string `json:"in_flight_request_ids"`
}

type ModelRuntimeControl interface {
	Admit(context.Context, CreateModelBrokerRequestInput) (ModelBrokerRequest, bool, error)
	List(context.Context, ListModelBrokerRequestsFilter) ([]ModelBrokerRequest, error)
	Get(context.Context, string) (ModelBrokerRequest, error)
	CancelWithEvidence(context.Context, string, ModelBrokerMutationEvidence) (ModelBrokerRequest, error)
	DropWithEvidence(context.Context, string, ModelBrokerMutationEvidence) (ModelBrokerRequest, error)
	CancelAllWithEvidence(context.Context, ModelBrokerMutationEvidence) ([]ModelBrokerRequest, error)
	QueueStatus(context.Context) (ModelQueueStatus, error)
}

func NewModelService(store Store, generator ModelBroker, options ModelServiceOptions) *ModelService {
	if generator == nil {
		generator = NewEnvModelBroker()
	}
	if options.QueueDepth < 1 {
		options.QueueDepth = modelBrokerQueueDepth()
	}
	if options.Workers < 1 {
		options.Workers = modelBrokerQueueWorkers()
	}
	if options.StaleAfter <= 0 {
		options.StaleAfter = modelBrokerRecoveryStaleAfter()
	}
	if options.RecoveryInterval <= 0 {
		options.RecoveryInterval = min(options.StaleAfter/2, time.Minute)
		if options.RecoveryInterval <= 0 {
			options.RecoveryInterval = time.Second
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &ModelService{store: store, generator: generator, queueDepth: options.QueueDepth, workers: options.Workers, staleAfter: options.StaleAfter, recoveryInterval: options.RecoveryInterval, ctx: ctx, cancel: cancel, notify: make(chan struct{}, options.Workers), active: map[string]*modelRequestGate{}}
	if !options.DisableStart {
		s.recover()
		for range s.workers {
			s.wg.Add(1)
			go s.worker()
		}
		s.wg.Add(1)
		go s.recoveryLoop()
		s.signal()
	}
	return s
}

func (s *ModelService) Generate(ctx context.Context, request ModelRequest) (ModelResponse, error) {
	return s.generator.Generate(ctx, request)
}

func (s *ModelService) Stream(ctx context.Context, request ModelRequest, emit func(ModelStreamEvent) error) (ModelResponse, error) {
	if streamer, ok := s.generator.(ModelStreamer); ok {
		return streamer.Stream(ctx, request, emit)
	}
	response, err := s.Generate(ctx, request)
	if err == nil {
		err = emit(ModelStreamEvent{Type: "content", Content: response.Content, Done: true, CorrelationIDs: response.CorrelationIDs})
	}
	return response, err
}

func (s *ModelService) StreamingMode(model string) string {
	if provider, ok := s.generator.(interface{ StreamingMode(string) string }); ok {
		return provider.StreamingMode(model)
	}
	return "buffered"
}

func (s *ModelService) ExecutionDecision(model string) BackendDecision {
	if provider, ok := s.generator.(interface{ ExecutionDecision(string) BackendDecision }); ok {
		return provider.ExecutionDecision(model)
	}
	return BackendDecision{Backend: "switchyard-native"}
}

func (s *ModelService) List(ctx context.Context, filter ListModelBrokerRequestsFilter) ([]ModelBrokerRequest, error) {
	return s.store.WithContext(ctx).ListModelBrokerRequests(filter)
}

func (s *ModelService) Get(ctx context.Context, id string) (ModelBrokerRequest, error) {
	return s.store.WithContext(ctx).ModelBrokerRequest(id)
}

func (s *ModelService) Admit(ctx context.Context, input CreateModelBrokerRequestInput) (ModelBrokerRequest, bool, error) {
	req, admitted, err := s.store.WithContext(ctx).AdmitModelBrokerRequest(input, s.queueDepth)
	if admitted {
		s.signal()
	}
	return req, admitted, err
}

func (s *ModelService) Begin(ctx context.Context, input CreateModelBrokerRequestInput) (ModelBrokerRequest, context.Context, error) {
	store := s.store.WithContext(ctx)
	req, admitted, err := store.BeginModelBrokerRequest(input)
	if err != nil {
		return ModelBrokerRequest{}, nil, err
	}
	if !admitted {
		return req, nil, ErrConflict
	}
	requestCtx, cancel := context.WithCancel(ctx)
	s.setActive(req.ID, cancel)
	return req, requestCtx, nil
}

func (s *ModelService) Finish(ctx context.Context, id, status, responseBody, errorText string, correlation map[string]string) (ModelBrokerRequest, error) {
	defer s.clearActive(id)
	gate := s.requestGate(id)
	if gate != nil {
		gate.transitionMu.Lock()
		defer gate.transitionMu.Unlock()
		gate.closeOutput()
	}
	store := s.store.WithContext(ctx)
	for attempts := 0; attempts < 3; attempts++ {
		req, err := store.ModelBrokerRequest(id)
		if err != nil {
			return ModelBrokerRequest{}, err
		}
		if isTerminalModelBrokerRequest(req.Status) {
			return req, nil
		}
		if req.Status != ModelBrokerRequestRunning && req.Status != ModelBrokerRequestCancelRequested {
			return ModelBrokerRequest{}, ErrConflict
		}
		finalStatus, finalBody := status, responseBody
		if req.Status == ModelBrokerRequestCancelRequested {
			finalStatus, finalBody = ModelBrokerRequestCanceled, ""
		}
		updated, err := store.TransitionModelBrokerRequest(id, TransitionModelBrokerRequestInput{ExpectedStatus: req.Status, Status: finalStatus, ResponseBody: finalBody, Error: errorText, CorrelationIDs: correlation})
		if !errors.Is(err, ErrConflict) {
			return updated, err
		}
	}
	return ModelBrokerRequest{}, ErrConflict
}

func (s *ModelService) Cancel(ctx context.Context, id string) (ModelBrokerRequest, error) {
	return s.CancelWithEvidence(ctx, id, ModelBrokerMutationEvidence{})
}

func (s *ModelService) CancelWithEvidence(ctx context.Context, id string, evidence ModelBrokerMutationEvidence) (ModelBrokerRequest, error) {
	gate := s.requestGate(id)
	if gate != nil {
		gate.transitionMu.Lock()
		defer gate.transitionMu.Unlock()
	}
	store := s.store.WithContext(ctx)
	for {
		if err := ctx.Err(); err != nil {
			return ModelBrokerRequest{}, err
		}
		req, err := store.ModelBrokerRequest(id)
		if err != nil {
			return ModelBrokerRequest{}, err
		}
		switch req.Status {
		case ModelBrokerRequestQueued:
			req, err = store.TransitionModelBrokerRequest(id, TransitionModelBrokerRequestInput{ExpectedStatus: req.Status, Status: ModelBrokerRequestCanceled, Error: "request canceled before execution", CorrelationIDs: mutationEvidence("cancel", evidence)})
		case ModelBrokerRequestRunning:
			req, err = store.TransitionModelBrokerRequest(id, TransitionModelBrokerRequestInput{ExpectedStatus: req.Status, Status: ModelBrokerRequestCancelRequested, CorrelationIDs: mutationEvidence("cancel", evidence)})
		case ModelBrokerRequestCancelRequested:
			err = nil
		default:
			if gate != nil {
				gate.closeOutput()
			}
			return req, nil
		}
		if errors.Is(err, ErrConflict) {
			continue
		}
		if err != nil {
			return ModelBrokerRequest{}, err
		}
		if gate != nil {
			gate.closeOutput()
		}
		if cancel := s.activeCancel(id); cancel != nil {
			cancel()
		}
		return req, nil
	}
}

func (s *ModelService) Drop(ctx context.Context, id, reason string) (ModelBrokerRequest, error) {
	return s.DropWithEvidence(ctx, id, ModelBrokerMutationEvidence{Reason: reason})
}

func (s *ModelService) DropWithEvidence(ctx context.Context, id string, evidence ModelBrokerMutationEvidence) (ModelBrokerRequest, error) {
	gate := s.requestGate(id)
	if gate != nil {
		gate.transitionMu.Lock()
		defer gate.transitionMu.Unlock()
	}
	store := s.store.WithContext(ctx)
	for {
		if err := ctx.Err(); err != nil {
			return ModelBrokerRequest{}, err
		}
		req, err := store.ModelBrokerRequest(id)
		if err != nil || isTerminalModelBrokerRequest(req.Status) {
			return req, err
		}
		req, err = store.TransitionModelBrokerRequest(id, TransitionModelBrokerRequestInput{ExpectedStatus: req.Status, Status: ModelBrokerRequestDropped, Error: boundedAuditValue(evidence.Reason), CorrelationIDs: mutationEvidence("drop", evidence)})
		if errors.Is(err, ErrConflict) {
			continue
		}
		if err != nil {
			return ModelBrokerRequest{}, err
		}
		if gate != nil {
			gate.closeOutput()
		}
		if cancel := s.activeCancel(id); cancel != nil {
			cancel()
		}
		return req, nil
	}
}

func (s *ModelService) CancelAll(ctx context.Context) ([]ModelBrokerRequest, error) {
	return s.CancelAllWithEvidence(ctx, ModelBrokerMutationEvidence{})
}

func (s *ModelService) CancelAllWithEvidence(ctx context.Context, evidence ModelBrokerMutationEvidence) ([]ModelBrokerRequest, error) {
	gates := s.activeGates()
	for _, gate := range gates {
		gate.transitionMu.Lock()
	}
	defer func() {
		for index := len(gates) - 1; index >= 0; index-- {
			gates[index].transitionMu.Unlock()
		}
	}()
	rows, err := s.store.WithContext(ctx).RequestCancelModelBrokerRequests(mutationEvidence("cancel_all", evidence))
	if err != nil {
		return nil, err
	}
	for _, gate := range gates {
		gate.closeOutput()
	}
	for _, req := range rows {
		if gate := s.requestGate(req.ID); gate != nil {
			gate.closeOutput()
			gate.cancel()
		}
	}
	return rows, nil
}

func (s *ModelService) QueueStatus(ctx context.Context) (ModelQueueStatus, error) {
	snapshot, err := s.store.WithContext(ctx).ModelBrokerQueueSnapshot(min(s.queueDepth, 100))
	return ModelQueueStatus{Queued: snapshot.Queued, Running: snapshot.Running, CancelRequested: snapshot.CancelRequested, MaxDepth: s.queueDepth, InFlightIDs: snapshot.InFlightIDs}, err
}

func (s *ModelService) Close(ctx context.Context) error {
	s.Stop()
	return s.Wait(ctx)
}

func (s *ModelService) Stop() {
	s.stopOnce.Do(func() {
		s.cancel()
	})
}

func (s *ModelService) Wait(ctx context.Context) error {
	shutdownErr := s.shutdownRequests(ctx)
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return shutdownErr
	case <-ctx.Done():
		return errors.Join(shutdownErr, ctx.Err())
	}
}

func (s *ModelService) shutdownRequests(ctx context.Context) error {
	_, err := s.CancelAllWithEvidence(ctx, ModelBrokerMutationEvidence{Actor: "switchyard", Reason: "model service shutdown"})
	if err != nil {
		return err
	}
	filter := ListModelBrokerRequestsFilter{Statuses: []string{ModelBrokerRequestCancelRequested}, Limit: 200}
	for {
		rows, err := s.store.WithContext(ctx).ListModelBrokerRequests(filter)
		if err != nil {
			return err
		}
		for _, req := range rows {
			if err := s.terminalizeCanceled(ctx, req.ID, "model service shutdown"); err != nil && !errors.Is(err, ErrConflict) {
				return err
			}
		}
		if len(rows) < filter.Limit {
			return nil
		}
		last := rows[len(rows)-1]
		filter.BeforeCreated, filter.BeforeID = &last.CreatedAt, last.ID
	}
}

func (s *ModelService) signal() {
	for range s.workers {
		select {
		case s.notify <- struct{}{}:
		default:
			return
		}
	}
}

func (s *ModelService) worker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.notify:
		}
		for {
			select {
			case <-s.ctx.Done():
				return
			default:
			}
			row, err := s.store.WithContext(s.ctx).ClaimNextModelBrokerRequest()
			if err != nil {
				break
			}
			if s.ctx.Err() != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = s.terminalizeCanceled(ctx, row.ID, "model service stopped before dispatch")
				cancel()
				return
			}
			s.run(row)
		}
	}
}

func (s *ModelService) run(row ModelBrokerRequest) {
	if s.ctx.Err() != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = s.terminalizeCanceled(ctx, row.ID, "model service stopped before dispatch")
		cancel()
		return
	}
	model, messages, ok := decodeModelBrokerRequestPayload(row)
	if !ok {
		_, _ = s.Finish(s.ctx, row.ID, ModelBrokerRequestFailed, "", "invalid queued request payload", nil)
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.setActive(row.ID, cancel)
	defer cancel()
	defer s.clearActive(row.ID)
	current, err := s.store.ModelBrokerRequest(row.ID)
	if err != nil || current.Status != ModelBrokerRequestRunning {
		return
	}
	response, err := s.generator.Generate(ctx, ModelRequest{Model: model, Messages: messages, RequestID: row.ID, PreAttempt: func(decision BackendDecision) error {
		_, mergeErr := s.store.MergeModelBrokerRequestCorrelationIDs(row.ID, decision.CorrelationIDs())
		return mergeErr
	}})
	correlation := mergeStringMaps(response.CorrelationIDs, row.CorrelationIDs)
	if err != nil {
		status := ModelBrokerRequestFailed
		if errors.Is(err, context.Canceled) {
			finishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = s.terminalizeCanceled(finishCtx, row.ID, err.Error())
			cancel()
			return
		}
		_, _ = s.Finish(context.Background(), row.ID, status, `{}`, err.Error(), correlation)
		return
	}
	response.CorrelationIDs = correlation
	body, _ := json.Marshal(map[string]any{"content": response.Content, "correlation_ids": correlation})
	_, _ = s.Finish(context.Background(), row.ID, ModelBrokerRequestCompleted, string(body), "", correlation)
}

func (s *ModelService) terminalizeCanceled(ctx context.Context, id, errorText string) error {
	store := s.store.WithContext(ctx)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		req, err := store.ModelBrokerRequest(id)
		if err != nil {
			return err
		}
		if isTerminalModelBrokerRequest(req.Status) {
			return nil
		}
		next := ModelBrokerRequestCancelRequested
		if req.Status == ModelBrokerRequestCancelRequested {
			next = ModelBrokerRequestCanceled
		} else if req.Status != ModelBrokerRequestRunning {
			return ErrConflict
		}
		_, err = store.TransitionModelBrokerRequest(id, TransitionModelBrokerRequestInput{ExpectedStatus: req.Status, Status: next, Error: errorText})
		if errors.Is(err, ErrConflict) {
			continue
		}
		if err != nil {
			return err
		}
		if next == ModelBrokerRequestCanceled {
			return nil
		}
	}
}

func (s *ModelService) recover() {
	s.recoverStatuses([]string{ModelBrokerRequestRunning, ModelBrokerRequestCancelRequested}, func(req ModelBrokerRequest) {
		if s.requestGate(req.ID) != nil {
			return
		}
		now := time.Now().UTC()
		if now.Sub(req.UpdatedAt) < s.staleAfter {
			return
		}
		status, errorText := ModelBrokerRequestFailed, "recovered stale running request after API restart"
		if req.Status == ModelBrokerRequestCancelRequested {
			status, errorText = ModelBrokerRequestCanceled, "recovered canceled request after API restart"
		}
		_, _ = s.store.TransitionModelBrokerRequest(req.ID, TransitionModelBrokerRequestInput{ExpectedStatus: req.Status, Status: status, Error: errorText, CorrelationIDs: req.CorrelationIDs})
	})
	s.recoverStatuses([]string{ModelBrokerRequestQueued}, func(req ModelBrokerRequest) {
		if _, _, ok := decodeModelBrokerRequestPayload(req); !ok {
			_, _ = s.store.TransitionModelBrokerRequest(req.ID, TransitionModelBrokerRequestInput{ExpectedStatus: req.Status, Status: ModelBrokerRequestDropped, Error: "invalid recovered request payload"})
		}
	})
}

func (s *ModelService) recoverStatuses(statuses []string, recoverOne func(ModelBrokerRequest)) {
	filter := ListModelBrokerRequestsFilter{Statuses: statuses, Limit: 200}
	for {
		rows, err := s.store.ListModelBrokerRequests(filter)
		if err != nil || len(rows) == 0 {
			return
		}
		for _, req := range rows {
			recoverOne(req)
		}
		if len(rows) < filter.Limit {
			return
		}
		last := rows[len(rows)-1]
		filter.BeforeCreated, filter.BeforeID = &last.CreatedAt, last.ID
	}
}

func (s *ModelService) recoveryLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.recoveryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.recover()
			s.signal()
		}
	}
}

func (s *ModelService) setActive(id string, cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active[id] = &modelRequestGate{cancel: cancel}
}

func (s *ModelService) clearActive(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.active, id)
}

func (s *ModelService) activeCancel(id string) context.CancelFunc {
	s.mu.Lock()
	defer s.mu.Unlock()
	if gate := s.active[id]; gate != nil {
		return gate.cancel
	}
	return nil
}

func (s *ModelService) requestGate(id string) *modelRequestGate {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[id]
}

func (s *ModelService) activeGates() []*modelRequestGate {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.active))
	for id := range s.active {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	gates := make([]*modelRequestGate, 0, len(ids))
	for _, id := range ids {
		gates = append(gates, s.active[id])
	}
	return gates
}

func (s *ModelService) Emit(ctx context.Context, id string, event ModelStreamEvent, emit func(ModelStreamEvent) error) error {
	gate := s.requestGate(id)
	if gate == nil {
		return context.Canceled
	}
	if err := gate.reserveOutput(); err != nil {
		return err
	}
	defer gate.releaseOutput()
	req, err := s.store.WithContext(ctx).ModelBrokerRequest(id)
	if err != nil || req.Status != ModelBrokerRequestRunning {
		return context.Canceled
	}
	return emit(event)
}

func (g *modelRequestGate) reserveOutput() error {
	for {
		state := g.output.Load()
		if state&modelOutputClosed != 0 {
			return context.Canceled
		}
		if state&modelOutputWriting != 0 {
			return ErrModelOutputBusy
		}
		if g.output.CompareAndSwap(state, state|modelOutputWriting) {
			return nil
		}
	}
}

func (g *modelRequestGate) releaseOutput() {
	g.output.And(^uint32(modelOutputWriting))
}

func (g *modelRequestGate) closeOutput() {
	g.output.Or(modelOutputClosed)
}

func (s *ModelService) MergeCorrelation(ctx context.Context, id string, correlation map[string]string) (ModelBrokerRequest, error) {
	return s.store.WithContext(ctx).MergeModelBrokerRequestCorrelationIDs(id, correlation)
}

func boundedAuditValue(value string) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.ToValidUTF8(strings.TrimSpace(value), "�"))
	if len(value) > 256 {
		value = value[:256]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}

func mutationEvidence(action string, evidence ModelBrokerMutationEvidence) map[string]string {
	return map[string]string{"audit_action": action, "audit_actor": boundedAuditValue(evidence.Actor), "audit_reason": boundedAuditValue(evidence.Reason)}
}
