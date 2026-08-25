package controlplane

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type preAttemptStallingGenerator struct{ started chan struct{} }

func (g preAttemptStallingGenerator) Generate(ctx context.Context, _ ModelRequest) (ModelResponse, error) {
	close(g.started)
	<-ctx.Done()
	return ModelResponse{}, ctx.Err()
}

func TestModelServiceCancelDoesNotWaitForPreAttempt(t *testing.T) {
	store := NewStore(nil)
	started := make(chan struct{})
	service := NewModelService(store, preAttemptStallingGenerator{started: started}, ModelServiceOptions{DisableStart: true})
	request, requestCtx, err := service.Begin(t.Context(), CreateModelBrokerRequestInput{ID: "before-pre-attempt", Model: "test", RequestBody: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, generateErr := service.Generate(requestCtx, ModelRequest{Model: "test", RequestID: request.ID, PreAttempt: func(BackendDecision) error { return nil }})
		done <- generateErr
	}()
	<-started
	cancelCtx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := service.Cancel(cancelCtx, request.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("generate error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("generation remained blocked before pre-attempt")
	}
}

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

type beginTerminalStore struct {
	Store
	status string
}

type blockingMutationStore struct {
	Store
	status  string
	entered chan struct{}
	release chan struct{}
}

type testModelBrokerFunc func(context.Context, ModelRequest) (ModelResponse, error)

type callbackModelStreamer struct{}

func (callbackModelStreamer) Generate(context.Context, ModelRequest) (ModelResponse, error) {
	return ModelResponse{}, nil
}

func (callbackModelStreamer) Stream(_ context.Context, _ ModelRequest, emit func(ModelStreamEvent) error) (ModelResponse, error) {
	return ModelResponse{}, emit(ModelStreamEvent{Type: "content"})
}

type fakeModelServiceSingleton struct {
	checked chan struct{}
	lost    chan error
}

func (s *fakeModelServiceSingleton) HealthError(ctx context.Context) error {
	select {
	case s.checked <- struct{}{}:
	default:
	}
	select {
	case err := <-s.lost:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (*fakeModelServiceSingleton) Close(context.Context) error { return nil }
func (*fakeModelServiceSingleton) Invalidate() error           { return nil }

func (f testModelBrokerFunc) Generate(ctx context.Context, request ModelRequest) (ModelResponse, error) {
	return f(ctx, request)
}

func (s *blockingMutationStore) WithContext(context.Context) Store { return s }

func (s *blockingMutationStore) TransitionModelBrokerRequest(id string, input TransitionModelBrokerRequestInput) (ModelBrokerRequest, error) {
	if input.Status == s.status {
		close(s.entered)
		<-s.release
	}
	return s.Store.TransitionModelBrokerRequest(id, input)
}

func (s *beginTerminalStore) WithContext(context.Context) Store { return s }

func (s *beginTerminalStore) BeginModelBrokerRequest(input CreateModelBrokerRequestInput) (ModelBrokerRequest, bool, error) {
	req, admitted, err := s.Store.BeginModelBrokerRequest(input)
	if err != nil || !admitted {
		return req, admitted, err
	}
	if s.status == ModelBrokerRequestCanceled {
		_, err = s.Store.TransitionModelBrokerRequest(req.ID, TransitionModelBrokerRequestInput{ExpectedStatus: ModelBrokerRequestRunning, Status: ModelBrokerRequestCancelRequested})
		if err == nil {
			_, err = s.Store.TransitionModelBrokerRequest(req.ID, TransitionModelBrokerRequestInput{ExpectedStatus: ModelBrokerRequestCancelRequested, Status: ModelBrokerRequestCanceled})
		}
	} else {
		_, err = s.Store.TransitionModelBrokerRequest(req.ID, TransitionModelBrokerRequestInput{ExpectedStatus: ModelBrokerRequestRunning, Status: s.status})
	}
	return req, admitted, err
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

func TestModelServiceBeginRejectsTerminalAdmissionRace(t *testing.T) {
	for _, status := range []string{ModelBrokerRequestCanceled, ModelBrokerRequestDropped} {
		t.Run(status, func(t *testing.T) {
			memory := NewStore(nil)
			service := NewModelService(&beginTerminalStore{Store: memory, status: status}, nil, ModelServiceOptions{DisableStart: true})
			req, requestCtx, err := service.Begin(t.Context(), CreateModelBrokerRequestInput{ID: "begin-" + status, Model: "test"})
			if !errors.Is(err, ErrConflict) || requestCtx != nil || req.Status != status {
				t.Fatalf("request=%+v context=%v err=%v", req, requestCtx, err)
			}
			if service.requestGate(req.ID) != nil {
				t.Fatal("terminal request remained active after Begin")
			}
		})
	}
}

func TestModelServiceMutationBeforeDispatchNeverInvokesGenerator(t *testing.T) {
	for _, test := range []struct {
		name   string
		status string
		mutate func(*ModelService, string) (ModelBrokerRequest, error)
	}{
		{name: "cancel", status: ModelBrokerRequestCancelRequested, mutate: func(service *ModelService, id string) (ModelBrokerRequest, error) {
			return service.Cancel(t.Context(), id)
		}},
		{name: "drop", status: ModelBrokerRequestDropped, mutate: func(service *ModelService, id string) (ModelBrokerRequest, error) {
			return service.Drop(t.Context(), id, "race")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			memory := NewStore(nil)
			store := &blockingMutationStore{Store: memory, status: test.status, entered: make(chan struct{}), release: make(chan struct{})}
			called := make(chan struct{}, 1)
			preAttemptEntered := make(chan struct{})
			service := NewModelService(store, testModelBrokerFunc(func(_ context.Context, request ModelRequest) (ModelResponse, error) {
				close(preAttemptEntered)
				if err := request.PreAttempt(BackendDecision{Backend: "test"}); err != nil {
					return ModelResponse{}, err
				}
				called <- struct{}{}
				return ModelResponse{}, nil
			}), ModelServiceOptions{DisableStart: true})
			request, requestCtx, err := service.Begin(t.Context(), CreateModelBrokerRequestInput{ID: "dispatch-race-" + test.name, Model: "test"})
			if err != nil {
				t.Fatal(err)
			}
			mutated := make(chan error, 1)
			go func() {
				_, err := test.mutate(service, request.ID)
				mutated <- err
			}()
			<-store.entered
			dispatched := make(chan error, 1)
			go func() {
				_, err := service.Generate(requestCtx, ModelRequest{Model: "test", RequestID: request.ID, PreAttempt: func(BackendDecision) error { return nil }})
				dispatched <- err
			}()
			<-preAttemptEntered
			close(store.release)
			if err := <-mutated; err != nil {
				t.Fatal(err)
			}
			if err := <-dispatched; !errors.Is(err, context.Canceled) {
				t.Fatalf("dispatch error=%v", err)
			}
			select {
			case <-called:
				t.Fatal("generator invoked after mutation won dispatch arbitration")
			default:
			}
		})
	}
}

func TestModelServiceStartupErrorRejectsAdmission(t *testing.T) {
	startupErr := errors.New("singleton unavailable")
	service := NewModelService(NewStore(nil), nil, ModelServiceOptions{DisableStart: true})
	service.startupErr = startupErr
	if !errors.Is(service.StartupError(), startupErr) {
		t.Fatalf("startup error=%v", service.StartupError())
	}
	if _, admitted, err := service.Admit(t.Context(), CreateModelBrokerRequestInput{ID: "not-ready", Model: "test"}); !errors.Is(err, startupErr) || admitted {
		t.Fatalf("admitted=%v err=%v", admitted, err)
	}
	if _, requestCtx, err := service.Begin(t.Context(), CreateModelBrokerRequestInput{ID: "not-ready-sync", Model: "test"}); !errors.Is(err, startupErr) || requestCtx != nil {
		t.Fatalf("context=%v err=%v", requestCtx, err)
	}
	if err := service.Wait(t.Context()); !errors.Is(err, startupErr) {
		t.Fatalf("wait error=%v", err)
	}
}

func TestModelServiceLockLossPermanentlyPoisonsRuntime(t *testing.T) {
	store := NewStore(nil)
	called := make(chan struct{}, 1)
	service := NewModelService(store, testModelBrokerFunc(func(context.Context, ModelRequest) (ModelResponse, error) {
		called <- struct{}{}
		return ModelResponse{}, nil
	}), ModelServiceOptions{DisableStart: true})
	_, activeCtx, err := service.Begin(t.Context(), CreateModelBrokerRequestInput{ID: "active-before-fence", Model: "test"})
	if err != nil {
		t.Fatal(err)
	}
	fence := &fakeModelServiceSingleton{checked: make(chan struct{}, 1), lost: make(chan error, 1)}
	service.singleton = fence
	service.startSingletonWatchdog(time.Nanosecond)
	<-fence.checked
	fence.lost <- errors.New("dedicated postgres session terminated")
	select {
	case <-activeCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("active request context was not canceled after lock loss")
	}
	poison := service.HealthError(t.Context())
	if !errors.Is(poison, ErrModelServiceFenced) {
		t.Fatalf("health error=%v", poison)
	}

	assertPoison := func(name string, err error) {
		t.Helper()
		if err != poison {
			t.Errorf("%s error=%v, want stable poison %v", name, err, poison)
		}
	}
	_, _, err = service.Admit(t.Context(), CreateModelBrokerRequestInput{ID: "admit-after-fence", Model: "test"})
	assertPoison("Admit", err)
	_, _, err = service.Begin(t.Context(), CreateModelBrokerRequestInput{ID: "begin-after-fence", Model: "test"})
	assertPoison("Begin", err)
	_, err = service.QueueStatus(t.Context())
	assertPoison("QueueStatus", err)
	_, err = service.Generate(t.Context(), ModelRequest{Model: "test"})
	assertPoison("Generate", err)
	_, err = service.Stream(t.Context(), ModelRequest{Model: "test"}, func(ModelStreamEvent) error { return nil })
	assertPoison("Stream", err)
	_, err = service.Finish(t.Context(), "active-before-fence", ModelBrokerRequestCompleted, `{}`, "", nil)
	assertPoison("Finish", err)
	_, err = service.Cancel(t.Context(), "active-before-fence")
	assertPoison("Cancel", err)
	_, err = service.Drop(t.Context(), "active-before-fence", "test")
	assertPoison("Drop", err)
	_, err = service.CancelAll(t.Context())
	assertPoison("CancelAll", err)
	err = service.Emit(t.Context(), "active-before-fence", ModelStreamEvent{}, func(ModelStreamEvent) error { return nil })
	assertPoison("Emit", err)
	_, err = service.MergeCorrelation(t.Context(), "active-before-fence", map[string]string{"test": "value"})
	assertPoison("MergeCorrelation", err)
	select {
	case <-called:
		t.Fatal("generator called after service was fenced")
	default:
	}
	if err := service.Close(t.Context()); err != nil {
		t.Fatalf("close poisoned service: %v", err)
	}
}

func TestModelServicePoisonDuringCallbackDoesNotDeadlock(t *testing.T) {
	for _, test := range []struct {
		name string
		call func(*ModelService, context.Context, string) error
	}{
		{name: "merge correlation", call: func(service *ModelService, ctx context.Context, id string) error {
			_, err := service.MergeCorrelation(ctx, id, map[string]string{"callback": "true"})
			return err
		}},
		{name: "emit", call: func(service *ModelService, ctx context.Context, id string) error {
			return service.Emit(ctx, id, ModelStreamEvent{Type: "content"}, func(ModelStreamEvent) error { return nil })
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := NewModelService(NewStore(nil), callbackModelStreamer{}, ModelServiceOptions{DisableStart: true})
			_, activeCtx, err := service.Begin(t.Context(), CreateModelBrokerRequestInput{ID: "callback-request", Model: "test"})
			if err != nil {
				t.Fatal(err)
			}
			entered := make(chan struct{})
			resume := make(chan struct{})
			streamDone := make(chan error, 1)
			go func() {
				_, err := service.Stream(activeCtx, ModelRequest{Model: "test"}, func(ModelStreamEvent) error {
					close(entered)
					<-resume
					return test.call(service, activeCtx, "callback-request")
				})
				streamDone <- err
			}()
			<-entered
			poisonDone := make(chan error, 1)
			go func() { poisonDone <- service.poison(errors.New("watchdog fence loss")) }()
			deadline := time.Now().Add(time.Second)
			for service.poisonedError() == nil && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if service.poisonedError() == nil {
				t.Fatal("service was not poisoned")
			}
			close(resume)
			select {
			case err := <-streamDone:
				if !errors.Is(err, ErrModelServiceFenced) {
					t.Fatalf("callback error=%v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("callback deadlocked while poison waited for in-flight operations")
			}
			select {
			case err := <-poisonDone:
				if !errors.Is(err, ErrModelServiceFenced) {
					t.Fatalf("poison error=%v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("poison did not finish after callback returned")
			}
		})
	}
}

func TestModelServicePoisonBeforeUngatedPreAttemptPreventsDispatch(t *testing.T) {
	preAttemptReady := make(chan struct{})
	resume := make(chan struct{})
	dispatched := make(chan struct{}, 1)
	service := NewModelService(NewStore(nil), testModelBrokerFunc(func(_ context.Context, request ModelRequest) (ModelResponse, error) {
		close(preAttemptReady)
		<-resume
		if err := request.PreAttempt(BackendDecision{Backend: "test"}); err != nil {
			return ModelResponse{}, err
		}
		dispatched <- struct{}{}
		return ModelResponse{}, nil
	}), ModelServiceOptions{DisableStart: true})
	generateDone := make(chan error, 1)
	go func() {
		_, err := service.Generate(t.Context(), ModelRequest{Model: "test"})
		generateDone <- err
	}()
	<-preAttemptReady
	poisonDone := make(chan error, 1)
	go func() { poisonDone <- service.poison(errors.New("watchdog fence loss")) }()
	deadline := time.Now().Add(time.Second)
	for service.poisonedError() == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if service.poisonedError() == nil {
		t.Fatal("service was not poisoned")
	}
	close(resume)
	if err := <-generateDone; !errors.Is(err, ErrModelServiceFenced) {
		t.Fatalf("generate error=%v", err)
	}
	if err := <-poisonDone; !errors.Is(err, ErrModelServiceFenced) {
		t.Fatalf("poison error=%v", err)
	}
	select {
	case <-dispatched:
		t.Fatal("provider dispatched after service poison")
	default:
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
	req, err := s.Store.ClaimNextModelBrokerRequest()
	select {
	case s.called <- struct{}{}:
	default:
	}
	return req, err
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

func (g *lateGenerator) Generate(_ context.Context, request ModelRequest) (ModelResponse, error) {
	if err := request.PreAttempt(BackendDecision{Backend: "test"}); err != nil {
		return ModelResponse{}, err
	}
	close(g.started)
	<-g.release
	return ModelResponse{Content: "late output"}, nil
}

func (g *cancelAwareGenerator) Generate(ctx context.Context, request ModelRequest) (ModelResponse, error) {
	if err := request.PreAttempt(BackendDecision{Backend: "test"}); err != nil {
		return ModelResponse{}, err
	}
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

func TestModelServiceDropAfterCancelPreservesFirstDropAudit(t *testing.T) {
	store := NewStore(nil)
	service := NewModelService(store, nil, ModelServiceOptions{DisableStart: true})
	if _, _, err := service.Begin(t.Context(), CreateModelBrokerRequestInput{ID: "drop-after-cancel", Model: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CancelWithEvidence(t.Context(), "drop-after-cancel", ModelBrokerMutationEvidence{Actor: "cancel-actor", Reason: "cancel reason"}); err != nil {
		t.Fatal(err)
	}
	first, err := service.DropWithEvidence(t.Context(), "drop-after-cancel", ModelBrokerMutationEvidence{Actor: "drop-actor", Reason: "drop reason"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.DropWithEvidence(t.Context(), "drop-after-cancel", ModelBrokerMutationEvidence{Actor: "other-actor", Reason: "other reason"})
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []ModelBrokerRequest{first, second} {
		if req.Status != ModelBrokerRequestDropped || req.CorrelationIDs["audit_action"] != "cancel" || req.CorrelationIDs["audit_actor"] != "cancel-actor" || req.CorrelationIDs["audit_reason"] != "cancel reason" || req.CorrelationIDs["audit_drop_action"] != "drop" || req.CorrelationIDs["audit_drop_actor"] != "drop-actor" || req.CorrelationIDs["audit_drop_reason"] != "drop reason" {
			t.Fatalf("audit evidence=%+v status=%s", req.CorrelationIDs, req.Status)
		}
	}
}

func TestPostgresStoreModelServiceSingletonRejectsSecondAndReleasesIntegration(t *testing.T) {
	databaseURL := os.Getenv("SWITCHYARD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set SWITCHYARD_TEST_DATABASE_URL to run PostgreSQL model service integration tests")
	}
	if os.Getenv("SWITCHYARD_TEST_POSTGRES_ISOLATED") != "1" {
		t.Skip("run PostgreSQL model service integration tests through make postgres-test")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	db, err := OpenPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := NewPostgresStore(db, nil)
	first := NewModelService(store, nil, ModelServiceOptions{DisableStart: true})
	if err := first.StartupError(); err != nil {
		t.Fatal(err)
	}
	if err := first.HealthError(ctx); err != nil {
		t.Fatalf("first health error=%v", err)
	}
	requestID := "postgres-drop-after-cancel-" + shortRandomID()
	if _, _, err := first.Begin(ctx, CreateModelBrokerRequestInput{ID: requestID, Model: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := first.CancelWithEvidence(ctx, requestID, ModelBrokerMutationEvidence{Actor: "cancel-actor", Reason: "cancel reason"}); err != nil {
		t.Fatal(err)
	}
	dropped, err := first.DropWithEvidence(ctx, requestID, ModelBrokerMutationEvidence{Actor: "drop-actor", Reason: "drop reason"})
	if err != nil || dropped.Status != ModelBrokerRequestDropped || dropped.CorrelationIDs["audit_action"] != "cancel" || dropped.CorrelationIDs["audit_drop_action"] != "drop" {
		t.Fatalf("Postgres drop-after-cancel=%+v err=%v", dropped, err)
	}
	t.Cleanup(func() { _ = first.Close(context.Background()) })
	second := NewModelService(store, nil, ModelServiceOptions{DisableStart: true})
	if !errors.Is(second.StartupError(), ErrModelServiceAlreadyRunning) {
		t.Fatalf("second startup error=%v", second.StartupError())
	}
	if !errors.Is(second.HealthError(ctx), ErrModelServiceAlreadyRunning) {
		t.Fatalf("second health error=%v", second.HealthError(ctx))
	}
	if err := first.Close(ctx); err != nil {
		t.Fatal(err)
	}
	third := NewModelService(store, nil, ModelServiceOptions{DisableStart: true})
	if err := third.StartupError(); err != nil {
		t.Fatalf("singleton was not released: %v", err)
	}
	if err := third.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreModelServiceLockLossFencesOldOwnerAfterTakeoverIntegration(t *testing.T) {
	databaseURL := os.Getenv("SWITCHYARD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set SWITCHYARD_TEST_DATABASE_URL to run PostgreSQL model service integration tests")
	}
	if os.Getenv("SWITCHYARD_TEST_POSTGRES_ISOLATED") != "1" {
		t.Skip("run PostgreSQL model service integration tests through make postgres-test")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	db, err := OpenPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := NewPostgresStore(db, nil)
	oldDispatch := make(chan struct{}, 1)
	old := NewModelService(store, testModelBrokerFunc(func(context.Context, ModelRequest) (ModelResponse, error) {
		oldDispatch <- struct{}{}
		return ModelResponse{}, nil
	}), ModelServiceOptions{DisableStart: true, SingletonCheckInterval: 5 * time.Millisecond})
	if err := old.StartupError(); err != nil {
		t.Fatal(err)
	}
	oldLock := old.singleton.(*postgresModelServiceSingleton)
	oldPID := oldLock.pid
	var terminated bool
	if err := db.QueryRowContext(ctx, `select pg_terminate_backend($1)`, oldPID).Scan(&terminated); err != nil || !terminated {
		t.Fatalf("terminate old singleton session: terminated=%v err=%v", terminated, err)
	}
	var replacement *ModelService
	for {
		replacement = NewModelService(store, nil, ModelServiceOptions{DisableStart: true})
		err := replacement.StartupError()
		if err == nil {
			break
		}
		if !errors.Is(err, ErrModelServiceAlreadyRunning) {
			t.Fatalf("replacement did not acquire singleton after old session ended: %v", err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("replacement did not acquire singleton after old session ended: %v", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Cleanup(func() { _ = replacement.Close(context.Background()) })
	poison := old.poisonedError()
	if !errors.Is(poison, ErrModelServiceFenced) {
		t.Fatalf("old owner was not fenced before replacement startup completed: %v", poison)
	}
	if _, admitted, err := old.Admit(ctx, CreateModelBrokerRequestInput{ID: "old-owner-admit-" + shortRandomID(), Model: "test"}); err != poison || admitted {
		t.Fatalf("old owner admitted=%v err=%v, want poison=%v", admitted, err, poison)
	}
	if _, err := old.Generate(ctx, ModelRequest{Model: "test"}); err != poison {
		t.Fatalf("old owner dispatch error=%v, want poison=%v", err, poison)
	}
	select {
	case <-oldDispatch:
		t.Fatal("old owner dispatched after replacement acquired singleton")
	default:
	}
	if _, admitted, err := replacement.Admit(ctx, CreateModelBrokerRequestInput{ID: "replacement-admit-" + shortRandomID(), Model: "test"}); err != nil || !admitted {
		t.Fatalf("replacement admitted=%v err=%v", admitted, err)
	}
	if err := old.Close(ctx); err != nil {
		t.Fatalf("close fenced old owner: %v", err)
	}
}

func TestPostgresStoreModelServicePoisonInvalidatesSessionForImmediateTakeoverIntegration(t *testing.T) {
	databaseURL := os.Getenv("SWITCHYARD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set SWITCHYARD_TEST_DATABASE_URL to run PostgreSQL model service integration tests")
	}
	if os.Getenv("SWITCHYARD_TEST_POSTGRES_ISOLATED") != "1" {
		t.Skip("run PostgreSQL model service integration tests through make postgres-test")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	db, err := OpenPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := NewPostgresStore(db, nil)
	old := NewModelService(store, nil, ModelServiceOptions{DisableStart: true})
	if err := old.StartupError(); err != nil {
		t.Fatal(err)
	}
	poison := old.poison(errors.New("watchdog detected lock ownership loss"))
	if !errors.Is(poison, ErrModelServiceFenced) {
		t.Fatalf("poison error=%v", poison)
	}
	replacement := NewModelService(store, nil, ModelServiceOptions{DisableStart: true})
	if err := replacement.StartupError(); err != nil {
		t.Fatalf("replacement blocked after poison invalidated singleton session: %v", err)
	}
	t.Cleanup(func() { _ = replacement.Close(context.Background()) })
	if old.poisonedError() != poison {
		t.Fatalf("old service poison changed: got %v want %v", old.poisonedError(), poison)
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
