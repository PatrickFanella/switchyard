package queue_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"git.subcult.tv/PatrickFanella/llama-line/internal/queue"
)

func makeRequestWithPriority(id, client string, priority int) *queue.Request {
	ctx, cancel := context.WithCancel(context.Background())
	return &queue.Request{
		ID:         id,
		ClientName: client,
		Priority:   priority,
		EnqueuedAt: time.Now(),
		Ctx:        ctx,
		Cancel:     cancel,
		ResponseCh: make(chan queue.Result, 1),
		StatusCh:   make(chan queue.StatusUpdate, 8),
	}
}

func TestPriorityQueue_HighPriorityDequeuesFirst(t *testing.T) {
	q := queue.New(5)
	// Enqueue low-priority first, then high-priority.
	low := makeRequestWithPriority("low", "client-low", 0)
	high := makeRequestWithPriority("high", "client-high", 10)
	q.Enqueue(low)
	q.Enqueue(high)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	first := q.Dequeue(ctx)
	if first.ID != "high" {
		t.Errorf("first dequeued = %q, want high", first.ID)
	}
	q.Complete()

	second := q.Dequeue(ctx)
	if second.ID != "low" {
		t.Errorf("second dequeued = %q, want low", second.ID)
	}
	q.Complete()
}

func TestPriorityQueue_EqualPriorityFIFO(t *testing.T) {
	q := queue.New(5)
	r1 := makeRequestWithPriority("1", "a", 5)
	time.Sleep(time.Millisecond) // ensure distinct EnqueuedAt
	r2 := makeRequestWithPriority("2", "b", 5)
	q.Enqueue(r1)
	q.Enqueue(r2)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	first := q.Dequeue(ctx)
	if first.ID != "1" {
		t.Errorf("first = %q, want 1 (FIFO)", first.ID)
	}
	q.Complete()
}

func TestPriorityQueue_PositionsReflectPriorityOrder(t *testing.T) {
	q := queue.New(5)
	q.Enqueue(makeRequestWithPriority("low", "a", 0))
	q.Enqueue(makeRequestWithPriority("high", "b", 10))
	q.Enqueue(makeRequestWithPriority("mid", "c", 5))

	pos := q.Positions()
	if pos["high"] != 1 {
		t.Errorf("high priority position = %d, want 1", pos["high"])
	}
	if pos["mid"] != 2 {
		t.Errorf("mid priority position = %d, want 2", pos["mid"])
	}
	if pos["low"] != 3 {
		t.Errorf("low priority position = %d, want 3", pos["low"])
	}
}

// makeRequest creates a Request with default priority 0.
func makeRequest(id, client string) *queue.Request {
	return makeRequestWithPriority(id, client, 0)
}

// --- Queue tests ---

func TestEnqueue_ReturnsErrQueueFull(t *testing.T) {
	q := queue.New(2)
	if err := q.Enqueue(makeRequest("1", "a")); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(makeRequest("2", "b")); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(makeRequest("3", "c")); err != queue.ErrQueueFull {
		t.Errorf("expected ErrQueueFull, got %v", err)
	}
}

func TestDrop_RemovesCorrectItem(t *testing.T) {
	q := queue.New(5)
	q.Enqueue(makeRequest("a", "client-a"))
	q.Enqueue(makeRequest("b", "client-b"))
	q.Enqueue(makeRequest("c", "client-c"))

	q.Drop("b")

	if q.Depth() != 2 {
		t.Errorf("depth = %d, want 2", q.Depth())
	}
	pos := q.Positions()
	if _, ok := pos["b"]; ok {
		t.Error("dropped item 'b' still in positions")
	}
	if pos["a"] != 1 || pos["c"] != 2 {
		t.Errorf("positions = %v, want a=1 c=2", pos)
	}
}

func TestDrop_NoopForMissingID(t *testing.T) {
	q := queue.New(5)
	q.Enqueue(makeRequest("a", "client-a"))
	q.Drop("nonexistent")
	if q.Depth() != 1 {
		t.Errorf("depth = %d, want 1", q.Depth())
	}
}

func TestDepth_Accuracy(t *testing.T) {
	q := queue.New(5)
	if q.Depth() != 0 {
		t.Errorf("initial depth = %d, want 0", q.Depth())
	}
	q.Enqueue(makeRequest("1", "a"))
	q.Enqueue(makeRequest("2", "b"))
	if q.Depth() != 2 {
		t.Errorf("depth = %d, want 2", q.Depth())
	}
	q.Drop("1")
	if q.Depth() != 1 {
		t.Errorf("depth after drop = %d, want 1", q.Depth())
	}
}

func TestDequeue_ReturnsNilOnContextCancel(t *testing.T) {
	q := queue.New(5)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	r := q.Dequeue(ctx)
	if r != nil {
		t.Errorf("expected nil, got %+v", r)
	}
}

func TestSnapshot_ReflectsInFlight(t *testing.T) {
	q := queue.New(5)
	req := makeRequest("x", "my-client")
	q.Enqueue(req)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	got := q.Dequeue(ctx) // marks in-flight
	if got == nil {
		t.Fatal("expected request, got nil")
	}

	snap := q.Snapshot()
	if snap.InFlightClient != "my-client" {
		t.Errorf("InFlightClient = %q, want my-client", snap.InFlightClient)
	}
	if snap.Depth != 0 {
		t.Errorf("Depth = %d, want 0", snap.Depth)
	}

	q.Complete()
	snap = q.Snapshot()
	if snap.InFlightClient != "" {
		t.Errorf("InFlightClient after Complete = %q, want empty", snap.InFlightClient)
	}
}

// --- Dispatcher tests ---

func TestDispatcher_QueueWaitTimeout(t *testing.T) {
	q := queue.New(5)
	req := makeRequest("old", "client")
	req.EnqueuedAt = time.Now().Add(-10 * time.Minute) // already expired
	q.Enqueue(req)

	forward := func(ctx context.Context, r *queue.Request) queue.Result {
		t.Error("forward should not be called for timed-out request")
		return queue.Result{Resp: &http.Response{}}
	}

	d := queue.NewDispatcher(q, forward, 30*time.Second, 5*time.Minute)
	ctx, cancel := context.WithCancel(context.Background())

	go d.Run(ctx)

	select {
	case result := <-req.ResponseCh:
		if result.Err != queue.ErrQueueWaitTimeout {
			t.Errorf("err = %v, want ErrQueueWaitTimeout", result.Err)
		}
	case <-time.After(time.Second):
		t.Error("timed out waiting for result")
	}
	cancel()
}

func TestDispatcher_SerialExecution(t *testing.T) {
	q := queue.New(5)
	r1 := makeRequest("1", "client-1")
	r2 := makeRequest("2", "client-2")
	q.Enqueue(r1)
	q.Enqueue(r2)

	var order []string
	done := make(chan struct{})

	forward := func(ctx context.Context, r *queue.Request) queue.Result {
		order = append(order, r.ID)
		if len(order) == 2 {
			close(done)
		}
		return queue.Result{Resp: &http.Response{}}
	}

	d := queue.NewDispatcher(q, forward, 30*time.Second, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)

	// Drain both results
	<-r1.ResponseCh
	<-r2.ResponseCh

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for both requests")
	}

	if len(order) != 2 || order[0] != "1" || order[1] != "2" {
		t.Errorf("execution order = %v, want [1 2]", order)
	}
}

func TestDispatcher_PositionBroadcastAfterComplete(t *testing.T) {
	q := queue.New(5)
	r1 := makeRequest("1", "client-1")
	r2 := makeRequest("2", "client-2")
	q.Enqueue(r1)
	q.Enqueue(r2)

	block := make(chan struct{})
	forward := func(ctx context.Context, r *queue.Request) queue.Result {
		if r.ID == "1" {
			<-block // hold until we're ready
		}
		return queue.Result{Resp: &http.Response{}}
	}

	d := queue.NewDispatcher(q, forward, 30*time.Second, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)

	// Unblock r1 and wait for r2 to receive a position update
	close(block)
	<-r1.ResponseCh

	select {
	case update := <-r2.StatusCh:
		if update.Position != 1 {
			t.Errorf("position = %d, want 1", update.Position)
		}
	case <-time.After(time.Second):
		t.Error("timed out waiting for position update on r2")
	}
}
