package controlplane

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestModelBrokerRequestTransitions(t *testing.T) {
	allowed := [][2]string{{ModelBrokerRequestQueued, ModelBrokerRequestRunning}, {ModelBrokerRequestQueued, ModelBrokerRequestCanceled}, {ModelBrokerRequestQueued, ModelBrokerRequestDropped}, {ModelBrokerRequestRunning, ModelBrokerRequestCompleted}, {ModelBrokerRequestRunning, ModelBrokerRequestFailed}, {ModelBrokerRequestRunning, ModelBrokerRequestCancelRequested}, {ModelBrokerRequestRunning, ModelBrokerRequestDropped}, {ModelBrokerRequestCancelRequested, ModelBrokerRequestCanceled}, {ModelBrokerRequestCancelRequested, ModelBrokerRequestDropped}}
	for _, transition := range allowed {
		if !ModelBrokerRequestTransitionAllowed(transition[0], transition[1]) {
			t.Errorf("transition %s -> %s rejected", transition[0], transition[1])
		}
	}
	for _, terminal := range []string{ModelBrokerRequestCompleted, ModelBrokerRequestFailed, ModelBrokerRequestCanceled, ModelBrokerRequestDropped} {
		if ModelBrokerRequestTransitionAllowed(terminal, ModelBrokerRequestRunning) {
			t.Errorf("terminal transition from %s allowed", terminal)
		}
	}
	if ModelBrokerRequestTransitionAllowed(ModelBrokerRequestCancelRequested, ModelBrokerRequestCompleted) {
		t.Error("cancel requester lost first-winner semantics")
	}
}

func TestModelServiceAdmissionIsAtomic(t *testing.T) {
	store := NewStore(nil)
	service := NewModelService(store, nil, ModelServiceOptions{QueueDepth: 1, DisableStart: true})
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{"one", "two"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := service.Admit(t.Context(), CreateModelBrokerRequestInput{ID: id, Model: "test", RequestBody: `{}`})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	admitted, dropped := 0, 0
	for err := range results {
		if err == nil {
			admitted++
		} else if errors.Is(err, ErrQueueFull) {
			dropped++
		} else {
			t.Fatal(err)
		}
	}
	if admitted != 1 || dropped != 1 {
		t.Fatalf("admitted=%d dropped=%d", admitted, dropped)
	}
}

func TestMemoryModelBrokerCancelSemantics(t *testing.T) {
	store := NewStore(nil)
	queued, _ := store.CreateModelBrokerRequest(CreateModelBrokerRequestInput{ID: "queued", Model: "test"})
	queued, err := store.CancelModelBrokerRequest(queued.ID)
	if err != nil || queued.Status != ModelBrokerRequestCanceled || queued.CancelRequestedAt == nil || queued.FinishedAt == nil {
		t.Fatalf("queued cancel=%+v err=%v", queued, err)
	}
	terminal, err := store.CancelModelBrokerRequest(queued.ID)
	if err != nil || terminal.Status != ModelBrokerRequestCanceled {
		t.Fatalf("terminal cancel=%+v err=%v", terminal, err)
	}
	running, _ := store.CreateModelBrokerRequest(CreateModelBrokerRequestInput{ID: "running", Model: "test"})
	running, _ = store.StartModelBrokerRequest(running.ID)
	running, err = store.CancelModelBrokerRequest(running.ID)
	if err != nil || running.Status != ModelBrokerRequestCancelRequested || running.FinishedAt != nil {
		t.Fatalf("running cancel=%+v err=%v", running, err)
	}
}

func TestMemoryCancelPreservesFirstRequestedAt(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	store := NewStore(func() time.Time { return now })
	request, _, _ := store.BeginModelBrokerRequest(CreateModelBrokerRequestInput{ID: "first-cancel", Model: "test"})
	request, _ = store.CancelModelBrokerRequest(request.ID)
	first := *request.CancelRequestedAt
	now = now.Add(time.Hour)
	request, _ = store.CancelModelBrokerRequest(request.ID)
	if request.CancelRequestedAt == nil || !request.CancelRequestedAt.Equal(first) {
		t.Fatalf("cancel_requested_at=%v want=%v", request.CancelRequestedAt, first)
	}
}

func TestModelAdmissionQueueFullReplayIsIdempotent(t *testing.T) {
	store := NewStore(nil)
	service := NewModelService(store, nil, ModelServiceOptions{QueueDepth: 1, DisableStart: true})
	_, _, _ = service.Admit(t.Context(), CreateModelBrokerRequestInput{ID: "accepted", Model: "test"})
	first, admitted, err := service.Admit(t.Context(), CreateModelBrokerRequestInput{ID: "rejected", Model: "test"})
	if !errors.Is(err, ErrQueueFull) || admitted {
		t.Fatalf("first=%+v admitted=%v err=%v", first, admitted, err)
	}
	replayed, admitted, err := service.Admit(t.Context(), CreateModelBrokerRequestInput{ID: "rejected", Model: "different"})
	if !errors.Is(err, ErrQueueFull) || admitted || replayed.ID != first.ID || replayed.Status != first.Status || replayed.Model != first.Model {
		t.Fatalf("replay=%+v admitted=%v err=%v", replayed, admitted, err)
	}
}

func TestModelQueueStatusUsesExactCountsAndBoundedIDs(t *testing.T) {
	store := NewStore(nil)
	service := NewModelService(store, nil, ModelServiceOptions{QueueDepth: 1, DisableStart: true})
	for _, id := range []string{"running-a", "running-b", "running-c"} {
		if _, _, err := service.Begin(t.Context(), CreateModelBrokerRequestInput{ID: id, Model: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	status, err := service.QueueStatus(t.Context())
	if err != nil || status.Running != 3 || len(status.InFlightIDs) != 1 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

type cancelAwareGenerator struct {
	started chan struct{}
	release chan struct{}
}

type lateGenerator struct {
	started chan struct{}
	release chan struct{}
}

type finishConflictStore struct {
	Store
	once sync.Once
}

type queuedRunningConflictStore struct {
	Store
	once sync.Once
}

func (s *queuedRunningConflictStore) WithContext(context.Context) Store { return s }

func (s *queuedRunningConflictStore) TransitionModelBrokerRequest(id string, input TransitionModelBrokerRequestInput) (ModelBrokerRequest, error) {
	conflict := false
	s.once.Do(func() {
		if input.ExpectedStatus == ModelBrokerRequestQueued {
			_, _ = s.Store.TransitionModelBrokerRequest(id, TransitionModelBrokerRequestInput{ExpectedStatus: ModelBrokerRequestQueued, Status: ModelBrokerRequestRunning})
			conflict = true
		}
	})
	if conflict {
		return ModelBrokerRequest{}, ErrConflict
	}
	return s.Store.TransitionModelBrokerRequest(id, input)
}

func (s *finishConflictStore) WithContext(context.Context) Store { return s }

func (s *finishConflictStore) TransitionModelBrokerRequest(id string, input TransitionModelBrokerRequestInput) (ModelBrokerRequest, error) {
	conflict := false
	s.once.Do(func() {
		if input.ExpectedStatus == ModelBrokerRequestRunning {
			_, _ = s.Store.TransitionModelBrokerRequest(id, TransitionModelBrokerRequestInput{ExpectedStatus: ModelBrokerRequestRunning, Status: ModelBrokerRequestCancelRequested})
			conflict = true
		}
	})
	if conflict {
		return ModelBrokerRequest{}, ErrConflict
	}
	return s.Store.TransitionModelBrokerRequest(id, input)
}

func TestModelServiceFinishReloadsAfterCancelConflict(t *testing.T) {
	memory := NewStore(nil)
	req, _, err := memory.BeginModelBrokerRequest(CreateModelBrokerRequestInput{ID: "cas-cancel", Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	service := NewModelService(&finishConflictStore{Store: memory}, nil, ModelServiceOptions{DisableStart: true})
	finished, err := service.Finish(t.Context(), req.ID, ModelBrokerRequestCompleted, `{"content":"late"}`, "", nil)
	if err != nil || finished.Status != ModelBrokerRequestCanceled || strings.Contains(finished.ResponseBody, "late") {
		t.Fatalf("finished=%+v err=%v", finished, err)
	}
}

func TestModelServiceMutationsReloadQueuedRunningConflict(t *testing.T) {
	for _, test := range []struct {
		name string
		want string
		run  func(*ModelService, string) (ModelBrokerRequest, error)
	}{
		{name: "cancel", want: ModelBrokerRequestCancelRequested, run: func(service *ModelService, id string) (ModelBrokerRequest, error) {
			return service.Cancel(t.Context(), id)
		}},
		{name: "drop", want: ModelBrokerRequestDropped, run: func(service *ModelService, id string) (ModelBrokerRequest, error) {
			return service.Drop(t.Context(), id, "race")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			memory := NewStore(nil)
			req, err := memory.CreateModelBrokerRequest(CreateModelBrokerRequestInput{ID: "race-" + test.name, Model: "test"})
			if err != nil {
				t.Fatal(err)
			}
			service := NewModelService(&queuedRunningConflictStore{Store: memory}, nil, ModelServiceOptions{DisableStart: true})
			updated, err := test.run(service, req.ID)
			if err != nil || updated.Status != test.want {
				t.Fatalf("updated=%+v err=%v", updated, err)
			}
		})
	}
}

func TestModelServiceRecoveryScansBeyondQueueDepth(t *testing.T) {
	store := NewStore(nil)
	for _, id := range []string{"invalid-a", "invalid-b", "invalid-c"} {
		if _, err := store.CreateModelBrokerRequest(CreateModelBrokerRequestInput{ID: id, Model: "test", RequestBody: `{}`}); err != nil {
			t.Fatal(err)
		}
	}
	service := NewModelService(store, nil, ModelServiceOptions{QueueDepth: 1, DisableStart: true})
	service.recover()
	for _, id := range []string{"invalid-a", "invalid-b", "invalid-c"} {
		req, _ := store.ModelBrokerRequest(id)
		if req.Status != ModelBrokerRequestDropped {
			t.Fatalf("%s status=%s", id, req.Status)
		}
	}
}

func TestModelServicePeriodicRecoveryClosesFreshOrphan(t *testing.T) {
	store := NewStore(nil)
	if _, _, err := store.BeginModelBrokerRequest(CreateModelBrokerRequestInput{ID: "orphan", Model: "test"}); err != nil {
		t.Fatal(err)
	}
	service := NewModelService(store, nil, ModelServiceOptions{QueueDepth: 1, Workers: 1, StaleAfter: 10 * time.Millisecond, RecoveryInterval: 5 * time.Millisecond})
	t.Cleanup(func() { _ = service.Close(context.Background()) })
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		req, _ := store.ModelBrokerRequest("orphan")
		if req.Status == ModelBrokerRequestFailed {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("periodic recovery did not close orphan")
}

func TestModelServicePeriodicRecoverySkipsLiveRequest(t *testing.T) {
	store := NewStore(nil)
	generator := &lateGenerator{started: make(chan struct{}), release: make(chan struct{})}
	service := NewModelService(store, generator, ModelServiceOptions{QueueDepth: 1, Workers: 1, StaleAfter: 5 * time.Millisecond, RecoveryInterval: 2 * time.Millisecond})
	t.Cleanup(func() { _ = service.Close(context.Background()) })
	if _, _, err := service.Admit(t.Context(), CreateModelBrokerRequestInput{ID: "live", Model: "test", RequestBody: `{"model":"test","messages":[{"role":"user","content":"hi"}]}`}); err != nil {
		t.Fatal(err)
	}
	<-generator.started
	time.Sleep(20 * time.Millisecond)
	req, _ := service.Get(t.Context(), "live")
	if req.Status != ModelBrokerRequestRunning {
		t.Fatalf("live request recovered as orphan: %+v", req)
	}
	close(generator.release)
}

func TestModelServiceCancelDoesNotWaitForStalledEmit(t *testing.T) {
	store := NewStore(nil)
	service := NewModelService(store, nil, ModelServiceOptions{DisableStart: true})
	_, _, err := service.Begin(t.Context(), CreateModelBrokerRequestInput{ID: "stream", Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	emitting, release := make(chan struct{}), make(chan struct{})
	emitted := make(chan error, 1)
	go func() {
		emitted <- service.Emit(t.Context(), "stream", ModelStreamEvent{Content: "first"}, func(ModelStreamEvent) error {
			close(emitting)
			<-release
			return nil
		})
	}()
	<-emitting
	canceled := make(chan error, 1)
	go func() {
		_, err := service.Cancel(t.Context(), "stream")
		canceled <- err
	}()
	select {
	case err := <-canceled:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel blocked on stalled output")
	}
	if err := service.Emit(t.Context(), "stream", ModelStreamEvent{Content: "late"}, func(ModelStreamEvent) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("concurrent late emit error=%v", err)
	}
	close(release)
	if err := <-emitted; err != nil {
		t.Fatal(err)
	}
	if err := service.Emit(t.Context(), "stream", ModelStreamEvent{Content: "late"}, func(ModelStreamEvent) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("late emit error=%v", err)
	}
}

type claimCountingStore struct {
	Store
	mu     sync.Mutex
	claims int
	called chan struct{}
}

func (s *claimCountingStore) WithContext(context.Context) Store { return s }

func (s *claimCountingStore) ClaimNextModelBrokerRequest() (ModelBrokerRequest, error) {
	s.mu.Lock()
	s.claims++
	s.mu.Unlock()
	select {
	case s.called <- struct{}{}:
	default:
	}
	return s.Store.ClaimNextModelBrokerRequest()
}

type stopAfterClaimStore struct {
	Store
	service **ModelService
	claimed chan struct{}
}

func (s *stopAfterClaimStore) WithContext(context.Context) Store { return s }
func (s *stopAfterClaimStore) ClaimNextModelBrokerRequest() (ModelBrokerRequest, error) {
	req, err := s.Store.ClaimNextModelBrokerRequest()
	if err == nil {
		(*s.service).Stop()
		close(s.claimed)
	}
	return req, err
}

type recordingGenerator struct{ called chan struct{} }

func (g recordingGenerator) Generate(context.Context, ModelRequest) (ModelResponse, error) {
	close(g.called)
	return ModelResponse{}, nil
}

func TestModelServiceShutdownAfterClaimSkipsDispatch(t *testing.T) {
	memory := NewStore(nil)
	var service *ModelService
	store := &stopAfterClaimStore{Store: memory, service: &service, claimed: make(chan struct{})}
	called := make(chan struct{})
	service = NewModelService(store, recordingGenerator{called: called}, ModelServiceOptions{QueueDepth: 1, Workers: 1})
	if _, _, err := service.Admit(t.Context(), CreateModelBrokerRequestInput{ID: "claimed-at-shutdown", Model: "test", RequestBody: `{"model":"test","messages":[{"role":"user","content":"hi"}]}`}); err != nil {
		t.Fatal(err)
	}
	<-store.claimed
	if err := service.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-called:
		t.Fatal("request dispatched after shutdown")
	default:
	}
	req, err := memory.ModelBrokerRequest("claimed-at-shutdown")
	if err != nil || req.Status != ModelBrokerRequestCanceled {
		t.Fatalf("request=%+v err=%v", req, err)
	}
}

func TestModelServiceShutdownStopsBeforeAnotherClaim(t *testing.T) {
	memory := NewStore(nil)
	store := &claimCountingStore{Store: memory, called: make(chan struct{}, 4)}
	generator := &cancelAwareGenerator{started: make(chan struct{}), release: make(chan struct{})}
	service := NewModelService(store, generator, ModelServiceOptions{QueueDepth: 2, Workers: 1})
	<-store.called
	if _, _, err := service.Admit(t.Context(), CreateModelBrokerRequestInput{ID: "shutdown", Model: "test", RequestBody: `{"model":"test","messages":[{"role":"user","content":"hi"}]}`}); err != nil {
		t.Fatal(err)
	}
	<-generator.started
	if err := service.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.claims != 2 {
		t.Fatalf("claims=%d, want initial empty claim plus request claim", store.claims)
	}
	req, err := memory.ModelBrokerRequest("shutdown")
	if err != nil || req.Status != ModelBrokerRequestCanceled {
		t.Fatalf("shutdown request=%+v err=%v", req, err)
	}
}

func (g *lateGenerator) Generate(context.Context, ModelRequest) (ModelResponse, error) {
	close(g.started)
	<-g.release
	return ModelResponse{Content: "late output"}, nil
}

func (g *cancelAwareGenerator) Generate(ctx context.Context, _ ModelRequest) (ModelResponse, error) {
	close(g.started)
	select {
	case <-ctx.Done():
		return ModelResponse{}, ctx.Err()
	case <-g.release:
		return ModelResponse{Content: "late output"}, nil
	}
}

func TestModelServiceCancelWinsOverLateOutput(t *testing.T) {
	store := NewStore(nil)
	generator := &cancelAwareGenerator{started: make(chan struct{}), release: make(chan struct{})}
	service := NewModelService(store, generator, ModelServiceOptions{QueueDepth: 2, Workers: 1})
	t.Cleanup(func() { _ = service.Close(context.Background()) })
	_, _, err := service.Admit(t.Context(), CreateModelBrokerRequestInput{ID: "cancel-me", Model: "test", RequestBody: `{"model":"test","messages":[{"role":"user","content":"hi"}]}`})
	if err != nil {
		t.Fatal(err)
	}
	<-generator.started
	req, err := service.Cancel(t.Context(), "cancel-me")
	if err != nil || req.Status != ModelBrokerRequestCancelRequested {
		t.Fatalf("cancel=%+v err=%v", req, err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		req, _ = service.Get(t.Context(), "cancel-me")
		if req.Status == ModelBrokerRequestCanceled {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if req.Status != ModelBrokerRequestCanceled || strings.Contains(req.ResponseBody, "late output") {
		t.Fatalf("late output won: %+v", req)
	}
	terminal, err := service.Cancel(t.Context(), "cancel-me")
	if err != nil || terminal.Status != ModelBrokerRequestCanceled {
		t.Fatalf("terminal cancel not idempotent: %+v %v", terminal, err)
	}
}

func TestModelServiceDropWinsWhenGeneratorIgnoresCancellation(t *testing.T) {
	store := NewStore(nil)
	generator := &lateGenerator{started: make(chan struct{}), release: make(chan struct{})}
	service := NewModelService(store, generator, ModelServiceOptions{QueueDepth: 2, Workers: 1})
	t.Cleanup(func() { _ = service.Close(context.Background()) })
	_, _, err := service.Admit(t.Context(), CreateModelBrokerRequestInput{ID: "drop-me", Model: "test", RequestBody: `{"model":"test","messages":[{"role":"user","content":"hi"}]}`})
	if err != nil {
		t.Fatal(err)
	}
	<-generator.started
	req, err := service.Drop(t.Context(), "drop-me", "operator drop")
	if err != nil || req.Status != ModelBrokerRequestDropped {
		t.Fatalf("drop=%+v err=%v", req, err)
	}
	close(generator.release)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		req, _ = service.Get(t.Context(), "drop-me")
		if req.Status == ModelBrokerRequestDropped && req.Error == "operator drop" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if req.Status != ModelBrokerRequestDropped || strings.Contains(req.ResponseBody, "late output") {
		t.Fatalf("late output overwrote drop: %+v", req)
	}
}
