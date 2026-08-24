package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

type ModelServiceOptions struct {
	QueueDepth             int
	Workers                int
	StaleAfter             time.Duration
	RecoveryInterval       time.Duration
	SingletonCheckInterval time.Duration
	DisableStart           bool
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
	fenceCtx         context.Context
	fenceCancel      context.CancelFunc
	notify           chan struct{}
	mu               sync.Mutex
	operationMu      sync.Mutex
	operations       int
	operationsDone   chan struct{}
	active           map[string]*modelRequestGate
	poisonState      atomic.Pointer[modelServicePoison]
	failure          chan error
	wg               sync.WaitGroup
	stopOnce         sync.Once
	startupErr       error
	singleton        modelServiceSingleton
	releaseOnce      sync.Once
}

type modelServicePoison struct{ err error }

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

const (
	modelServiceSingletonMaxCheckInterval = 250 * time.Millisecond
	modelServiceSingletonHealthTimeout    = 500 * time.Millisecond
	modelServiceSingletonTakeoverGrace    = time.Second
	modelServiceFenceDrainTimeout         = time.Second
)

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
	if options.SingletonCheckInterval <= 0 {
		options.SingletonCheckInterval = modelServiceSingletonMaxCheckInterval
	} else if options.SingletonCheckInterval > modelServiceSingletonMaxCheckInterval {
		options.SingletonCheckInterval = modelServiceSingletonMaxCheckInterval
	}
	ctx, cancel := context.WithCancel(context.Background())
	fenceCtx, fenceCancel := context.WithCancel(context.Background())
	s := &ModelService{store: store, generator: generator, queueDepth: options.QueueDepth, workers: options.Workers, staleAfter: options.StaleAfter, recoveryInterval: options.RecoveryInterval, ctx: ctx, cancel: cancel, fenceCtx: fenceCtx, fenceCancel: fenceCancel, notify: make(chan struct{}, options.Workers), active: map[string]*modelRequestGate{}, failure: make(chan error, 1)}
	if postgres, ok := store.(*PostgresStore); ok {
		lockCtx, lockCancel := context.WithTimeout(context.Background(), 5*time.Second)
		s.singleton, s.startupErr = postgres.acquireModelServiceSingleton(lockCtx)
		lockCancel()
		if s.startupErr != nil {
			s.cancel()
			return s
		}
		s.startSingletonWatchdog(options.SingletonCheckInterval)
		timer := time.NewTimer(modelServiceSingletonTakeoverGrace)
		select {
		case <-timer.C:
		case <-s.ctx.Done():
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
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
	releaseOperation, err := s.beginOperation()
	if err != nil {
		return ModelResponse{}, err
	}
	defer releaseOperation()
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	defer func() { stop(); cancel() }()
	request, release, err := s.arbitrateDispatch(ctx, request)
	if err != nil {
		return ModelResponse{}, err
	}
	defer release()
	return s.generator.Generate(ctx, request)
}

func (s *ModelService) Stream(ctx context.Context, request ModelRequest, emit func(ModelStreamEvent) error) (ModelResponse, error) {
	releaseOperation, err := s.beginOperation()
	if err != nil {
		return ModelResponse{}, err
	}
	defer releaseOperation()
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	defer func() { stop(); cancel() }()
	request, release, err := s.arbitrateDispatch(ctx, request)
	if err != nil {
		return ModelResponse{}, err
	}
	defer release()
	if streamer, ok := s.generator.(ModelStreamer); ok {
		return streamer.Stream(ctx, request, emit)
	}
	response, err := s.generator.Generate(ctx, request)
	if err == nil {
		err = emit(ModelStreamEvent{Type: "content", Content: response.Content, Done: true, CorrelationIDs: response.CorrelationIDs})
	}
	return response, err
}

func (s *ModelService) arbitrateDispatch(ctx context.Context, request ModelRequest) (ModelRequest, func(), error) {
	if err := s.serviceError(); err != nil {
		return ModelRequest{}, func() {}, err
	}
	gate := s.requestGate(request.RequestID)
	if gate == nil {
		request.PreAttempt = s.fencedPreAttempt(request.PreAttempt)
		return request, func() {}, nil
	}
	current, err := s.store.WithContext(ctx).ModelBrokerRequest(request.RequestID)
	if err != nil || current.Status != ModelBrokerRequestRunning {
		if err != nil {
			return ModelRequest{}, func() {}, err
		}
		return ModelRequest{}, func() {}, context.Canceled
	}
	if request.PreAttempt == nil {
		return ModelRequest{}, func() {}, errors.New("active model dispatch requires pre-attempt arbitration")
	}
	preAttempt := request.PreAttempt
	request.PreAttempt = func(decision BackendDecision) error {
		gate.transitionMu.Lock()
		defer gate.transitionMu.Unlock()
		if err := s.serviceError(); err != nil {
			return err
		}
		current, err := s.store.WithContext(ctx).ModelBrokerRequest(request.RequestID)
		if err != nil {
			return err
		}
		if current.Status != ModelBrokerRequestRunning {
			return context.Canceled
		}
		return preAttempt(decision)
	}
	return request, func() {}, nil
}

func (s *ModelService) fencedPreAttempt(preAttempt func(BackendDecision) error) func(BackendDecision) error {
	return func(decision BackendDecision) error {
		if err := s.serviceError(); err != nil {
			return err
		}
		if preAttempt != nil {
			return preAttempt(decision)
		}
		return nil
	}
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
	releaseOperation, err := s.beginOperation()
	if err != nil {
		return ModelBrokerRequest{}, false, err
	}
	defer releaseOperation()
	ctx, cancel := s.operationContext(ctx)
	defer cancel()
	req, admitted, err := s.store.WithContext(ctx).AdmitModelBrokerRequest(input, s.queueDepth)
	if admitted {
		s.signal()
	}
	return req, admitted, err
}

func (s *ModelService) Begin(ctx context.Context, input CreateModelBrokerRequestInput) (ModelBrokerRequest, context.Context, error) {
	releaseOperation, err := s.beginOperation()
	if err != nil {
		return ModelBrokerRequest{}, nil, err
	}
	defer releaseOperation()
	storeCtx, storeCancel := s.operationContext(ctx)
	defer storeCancel()
	requestCtx, cancel := context.WithCancel(ctx)
	gate := &modelRequestGate{cancel: cancel}
	gate.transitionMu.Lock()
	if !s.registerActive(input.ID, gate) {
		gate.transitionMu.Unlock()
		cancel()
		if err := s.serviceError(); err != nil {
			return ModelBrokerRequest{}, nil, err
		}
		req, err := s.store.WithContext(ctx).ModelBrokerRequest(input.ID)
		if err != nil {
			return ModelBrokerRequest{}, nil, err
		}
		return req, nil, ErrConflict
	}
	defer gate.transitionMu.Unlock()
	if err := s.serviceError(); err != nil {
		s.clearActive(input.ID)
		cancel()
		return ModelBrokerRequest{}, nil, err
	}
	store := s.store.WithContext(storeCtx)
	req, admitted, err := store.BeginModelBrokerRequest(input)
	if err != nil {
		s.clearActive(input.ID)
		cancel()
		return ModelBrokerRequest{}, nil, err
	}
	if !admitted {
		s.clearActive(input.ID)
		cancel()
		return req, nil, ErrConflict
	}
	req, err = store.ModelBrokerRequest(req.ID)
	if err != nil || req.Status != ModelBrokerRequestRunning {
		s.clearActive(req.ID)
		cancel()
		if err != nil {
			return ModelBrokerRequest{}, nil, err
		}
		return req, nil, ErrConflict
	}
	return req, requestCtx, nil
}

func (s *ModelService) Finish(ctx context.Context, id, status, responseBody, errorText string, correlation map[string]string) (ModelBrokerRequest, error) {
	releaseOperation, err := s.beginOperation()
	if err != nil {
		return ModelBrokerRequest{}, err
	}
	defer releaseOperation()
	ctx, cancel := s.operationContext(ctx)
	defer cancel()
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
	releaseOperation, err := s.beginOperation()
	if err != nil {
		return ModelBrokerRequest{}, err
	}
	defer releaseOperation()
	ctx, cancelOperation := s.operationContext(ctx)
	defer cancelOperation()
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
	releaseOperation, err := s.beginOperation()
	if err != nil {
		return ModelBrokerRequest{}, err
	}
	defer releaseOperation()
	ctx, cancelOperation := s.operationContext(ctx)
	defer cancelOperation()
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
			return req, err
		}
		if isTerminalModelBrokerRequest(req.Status) {
			if req.Status == ModelBrokerRequestCanceled {
				return store.MergeModelBrokerRequestCorrelationIDs(id, dropAfterCancelEvidence(evidence))
			}
			return req, nil
		}
		correlation := mutationEvidence("drop", evidence)
		if req.CorrelationIDs["audit_action"] == "cancel" || req.CorrelationIDs["audit_action"] == "cancel_all" {
			correlation = mergeStringMaps(correlation, dropAfterCancelEvidence(evidence))
		}
		req, err = store.TransitionModelBrokerRequest(id, TransitionModelBrokerRequestInput{ExpectedStatus: req.Status, Status: ModelBrokerRequestDropped, Error: boundedAuditValue(evidence.Reason), CorrelationIDs: correlation})
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
	releaseOperation, err := s.beginOperation()
	if err != nil {
		return nil, err
	}
	defer releaseOperation()
	ctx, cancel := s.operationContext(ctx)
	defer cancel()
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
	releaseOperation, err := s.beginOperation()
	if err != nil {
		return ModelQueueStatus{}, err
	}
	defer releaseOperation()
	ctx, cancel := s.operationContext(ctx)
	defer cancel()
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
	if s.startupErr != nil {
		return s.startupErr
	}
	var shutdownErr error
	if s.poisonedError() == nil {
		shutdownErr = s.shutdownRequests(ctx)
	}
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		s.fenceCancel()
		return errors.Join(shutdownErr, s.releaseSingleton(ctx))
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
				if s.poisonedError() != nil {
					return
				}
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
		if s.poisonedError() != nil {
			return
		}
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
	gate := &modelRequestGate{cancel: cancel}
	if !s.registerActive(row.ID, gate) {
		cancel()
		return
	}
	defer cancel()
	defer s.clearActive(row.ID)
	response, err := s.Generate(ctx, ModelRequest{Model: model, Messages: messages, RequestID: row.ID, PreAttempt: func(decision BackendDecision) error {
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
		if err := s.serviceError(); err != nil {
			return err
		}
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
	if s.serviceError() != nil {
		return
	}
	s.recoverStatuses([]string{ModelBrokerRequestRunning, ModelBrokerRequestCancelRequested}, func(req ModelBrokerRequest) {
		if s.serviceError() != nil {
			return
		}
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
		if s.serviceError() != nil {
			return
		}
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

func (s *ModelService) registerActive(id string, gate *modelRequestGate) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisonedError() != nil || s.active[id] != nil {
		return false
	}
	s.active[id] = gate
	return true
}

func (s *ModelService) StartupError() error { return s.startupErr }

func (s *ModelService) Failure() <-chan error { return s.failure }

func (s *ModelService) HealthError(ctx context.Context) error {
	if err := s.serviceError(); err != nil {
		return err
	}
	if s.singleton != nil {
		if err := s.singleton.HealthError(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return s.poison(err)
		}
	}
	return nil
}

func (s *ModelService) serviceError() error {
	if s.startupErr != nil {
		return s.startupErr
	}
	return s.poisonedError()
}

func (s *ModelService) poisonedError() error {
	if poison := s.poisonState.Load(); poison != nil {
		return poison.err
	}
	return nil
}

func (s *ModelService) poison(cause error) error {
	candidate := &modelServicePoison{err: fmt.Errorf("%w: %v", ErrModelServiceFenced, cause)}
	s.operationMu.Lock()
	if !s.poisonState.CompareAndSwap(nil, candidate) {
		err := s.poisonState.Load().err
		s.operationMu.Unlock()
		return err
	}
	done := s.operationsDone
	s.operationMu.Unlock()

	s.mu.Lock()
	gates := make([]*modelRequestGate, 0, len(s.active))
	for _, gate := range s.active {
		gates = append(gates, gate)
	}
	s.mu.Unlock()
	for _, gate := range gates {
		gate.closeOutput()
		gate.cancel()
	}
	s.cancel()
	s.fenceCancel()
	if s.singleton != nil {
		s.releaseOnce.Do(func() { _ = s.singleton.Invalidate() })
	}
	select {
	case s.failure <- candidate.err:
	default:
	}
	if done != nil {
		timer := time.NewTimer(modelServiceFenceDrainTimeout)
		select {
		case <-done:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
	}
	return candidate.err
}

func (s *ModelService) beginOperation() (func(), error) {
	s.operationMu.Lock()
	if err := s.serviceError(); err != nil {
		s.operationMu.Unlock()
		return func() {}, err
	}
	if s.operations == 0 {
		s.operationsDone = make(chan struct{})
	}
	s.operations++
	s.operationMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.operationMu.Lock()
			s.operations--
			if s.operations == 0 {
				close(s.operationsDone)
				s.operationsDone = nil
			}
			s.operationMu.Unlock()
		})
	}, nil
}

func (s *ModelService) operationContext(ctx context.Context) (context.Context, func()) {
	operationCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.fenceCtx, cancel)
	return operationCtx, func() {
		stop()
		cancel()
	}
}

func (s *ModelService) startSingletonWatchdog(interval time.Duration) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				checkCtx, cancel := context.WithTimeout(s.ctx, modelServiceSingletonHealthTimeout)
				err := s.singleton.HealthError(checkCtx)
				cancel()
				if err != nil && s.ctx.Err() == nil {
					s.poison(err)
					return
				}
			}
		}
	}()
}

func (s *ModelService) releaseSingleton(ctx context.Context) error {
	var releaseErr error
	s.releaseOnce.Do(func() {
		if s.singleton != nil {
			releaseErr = s.singleton.Close(ctx)
		}
	})
	return releaseErr
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
	releaseOperation, err := s.beginOperation()
	if err != nil {
		return err
	}
	defer releaseOperation()
	ctx, cancel := s.operationContext(ctx)
	defer cancel()
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
	releaseOperation, err := s.beginOperation()
	if err != nil {
		return ModelBrokerRequest{}, err
	}
	defer releaseOperation()
	ctx, cancel := s.operationContext(ctx)
	defer cancel()
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

func dropAfterCancelEvidence(evidence ModelBrokerMutationEvidence) map[string]string {
	return map[string]string{"audit_drop_action": "drop", "audit_drop_actor": boundedAuditValue(evidence.Actor), "audit_drop_reason": boundedAuditValue(evidence.Reason)}
}
