package queue

import (
	"container/heap"
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

// Sentinel errors.
var (
	ErrQueueFull        = errors.New("queue full")
	ErrQueueWaitTimeout = errors.New("queue wait timeout")
	ErrRequestTimeout   = errors.New("request timeout")
)

// StatusUpdate is an SSE payload sent to a waiting client.
// Status is always one of "queued", "ollama_unavailable", or "dropped_by_admin".
// When Status is "ollama_unavailable", Message carries the human-readable detail.
// RequestID allows clients to correlate events to a specific in-flight request.
type StatusUpdate struct {
	RequestID   string `json:"request_id,omitempty"`
	Position    int    `json:"position,omitempty"`
	WaitSeconds int    `json:"wait_seconds,omitempty"`
	Status      string `json:"status,omitempty"` // "queued" | "ollama_unavailable"
	Message     string `json:"message,omitempty"`
}

// Result is the outcome of a forwarded request.
type Result struct {
	Resp *http.Response
	Err  error
	// Cancel releases resources held by the per-request timeout context.
	// The consumer of Resp.Body (the handler) MUST call Cancel after fully
	// reading the body; calling it sooner cancels the body stream.
	Cancel context.CancelFunc
}

// Request represents a single queued inference request.
// Priority controls ordering: higher value = served sooner.
// Requests with equal priority are served FIFO (ordered by EnqueuedAt).
type Request struct {
	ID         string
	ClientName string
	Priority   int
	EnqueuedAt time.Time
	// StartedAt is set by the dispatcher immediately before calling the forward
	// function. Zero until the request leaves the queue.
	StartedAt  time.Time
	Ctx        context.Context
	Cancel     context.CancelFunc
	ResponseCh chan Result
	StatusCh   chan StatusUpdate
	HTTPReq    *http.Request

	// index is maintained by the heap.Interface implementation.
	index int
}

// QueueSnapshot is a point-in-time view of queue state.
type QueueSnapshot struct {
	Depth              int
	MaxDepth           int
	InFlightClient     string // empty if none
	InFlightID         string // empty if none
	InFlightPriority   int
	InFlightEnqueuedAt time.Time
	InFlightStartedAt  time.Time
	Items              []QueueItem
}

// QueueItem is a single entry in a QueueSnapshot.
type QueueItem struct {
	ID         string    `json:"id"`
	ClientName string    `json:"client"`
	Priority   int       `json:"priority"`
	Position   int       `json:"position"`
	EnqueuedAt time.Time `json:"enqueued_at"`
}

// --- heap.Interface implementation ---

// requestHeap is a max-heap ordered by Priority (desc), then EnqueuedAt (asc).
type requestHeap []*Request

func (h requestHeap) Len() int { return len(h) }

func (h requestHeap) Less(i, j int) bool {
	if h[i].Priority != h[j].Priority {
		return h[i].Priority > h[j].Priority // higher priority first
	}
	return h[i].EnqueuedAt.Before(h[j].EnqueuedAt) // earlier enqueue first
}

func (h requestHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *requestHeap) Push(x any) {
	r := x.(*Request)
	r.index = len(*h)
	*h = append(*h, r)
}

func (h *requestHeap) Pop() any {
	old := *h
	n := len(old)
	r := old[n-1]
	old[n-1] = nil
	r.index = -1
	*h = old[:n-1]
	return r
}

// --- Queue ---

// Queue is a concurrency-safe priority queue of inference requests.
// Higher-priority requests are dequeued before lower-priority ones.
// Equal-priority requests are served FIFO.
type Queue struct {
	mu       sync.Mutex
	items    requestHeap
	maxDepth int
	inFlight *Request
	notify   chan struct{}
}

// New creates a Queue with the given maximum depth.
func New(maxDepth int) *Queue {
	h := make(requestHeap, 0, maxDepth)
	heap.Init(&h)
	return &Queue{
		maxDepth: maxDepth,
		items:    h,
		notify:   make(chan struct{}, 1),
	}
}

// Enqueue adds r to the queue. Returns ErrQueueFull if at capacity.
func (q *Queue) Enqueue(r *Request) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) >= q.maxDepth {
		return ErrQueueFull
	}
	heap.Push(&q.items, r)
	// Wake Dequeue if it is blocking.
	select {
	case q.notify <- struct{}{}:
	default:
	}
	return nil
}

// Dequeue blocks until a request is available or ctx is cancelled.
// Returns nil if ctx is cancelled.
func (q *Queue) Dequeue(ctx context.Context) *Request {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			r := heap.Pop(&q.items).(*Request)
			q.inFlight = r
			q.mu.Unlock()
			return r
		}
		q.mu.Unlock()

		select {
		case <-ctx.Done():
			return nil
		case <-q.notify:
			// retry
		}
	}
}

// Drop removes the request with the given ID from the queue (not in-flight).
// It returns the dropped request, or nil if not found.
func (q *Queue) Drop(id string) *Request {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, r := range q.items {
		if r.ID == id {
			return heap.Remove(&q.items, i).(*Request)
		}
	}
	return nil
}

// Complete clears the in-flight request.
func (q *Queue) Complete() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.inFlight = nil
}

// CancelInFlight cancels the current in-flight request, if any.
// The dispatcher will call Complete() after the forward function returns.
// Returns true if a request was cancelled.
func (q *Queue) CancelInFlight() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.inFlight == nil {
		return false
	}
	q.inFlight.Cancel()
	return true
}

// InFlight returns a copy of the in-flight request metadata, or nil if none.
func (q *Queue) InFlight() *Request {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.inFlight
}

// Drain removes all queued (not in-flight) requests and returns them.
// Callers should send a final status event to each dropped request.
func (q *Queue) Drain() []*Request {
	q.mu.Lock()
	defer q.mu.Unlock()
	dropped := make([]*Request, len(q.items))
	copy(dropped, q.items)
	q.items = q.items[:0]
	heap.Init(&q.items)
	return dropped
}

// Depth returns the number of queued (not in-flight) requests.
func (q *Queue) Depth() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// Snapshot returns a point-in-time view of queue state.
// Items are ordered by dequeue priority (highest priority, then FIFO).
func (q *Queue) Snapshot() QueueSnapshot {
	q.mu.Lock()
	defer q.mu.Unlock()

	// Build a sorted copy without mutating the heap.
	sorted := prioritySorted(q.items)

	items := make([]QueueItem, len(sorted))
	for i, r := range sorted {
		items[i] = QueueItem{
			ID:         r.ID,
			ClientName: r.ClientName,
			Priority:   r.Priority,
			Position:   i + 1,
			EnqueuedAt: r.EnqueuedAt,
		}
	}

	s := QueueSnapshot{
		Depth:    len(q.items),
		MaxDepth: q.maxDepth,
		Items:    items,
	}
	if q.inFlight != nil {
		s.InFlightClient = q.inFlight.ClientName
		s.InFlightID = q.inFlight.ID
		s.InFlightPriority = q.inFlight.Priority
		s.InFlightEnqueuedAt = q.inFlight.EnqueuedAt
		s.InFlightStartedAt = q.inFlight.StartedAt
	}
	return s
}

// Positions returns a map of request ID to 1-based queue position (priority order).
func (q *Queue) Positions() map[string]int {
	q.mu.Lock()
	defer q.mu.Unlock()
	sorted := prioritySorted(q.items)
	m := make(map[string]int, len(sorted))
	for i, r := range sorted {
		m[r.ID] = i + 1
	}
	return m
}

// broadcastPositions sends non-blocking StatusUpdate to all queued requests.
// Caller must NOT hold q.mu.
func (q *Queue) broadcastPositions() {
	q.mu.Lock()
	sorted := prioritySorted(q.items)
	q.mu.Unlock()

	for i, r := range sorted {
		update := StatusUpdate{
			RequestID:   r.ID,
			Position:    i + 1,
			WaitSeconds: int(time.Since(r.EnqueuedAt).Seconds()),
			Status:      "queued",
		}
		select {
		case r.StatusCh <- update:
		default:
		}
	}
}

// prioritySorted returns a copy of items sorted by dequeue order
// (highest priority first, then FIFO by EnqueuedAt).
// Caller must hold q.mu.
func prioritySorted(items requestHeap) []*Request {
	if len(items) == 0 {
		return nil
	}
	// Copy the heap and drain it into sorted order.
	tmp := make(requestHeap, len(items))
	copy(tmp, items)
	// Fix indices on the copy.
	for i, r := range tmp {
		r2 := *r
		r2.index = i
		tmp[i] = &r2
	}
	heap.Init(&tmp)

	sorted := make([]*Request, 0, len(tmp))
	for len(tmp) > 0 {
		sorted = append(sorted, heap.Pop(&tmp).(*Request))
	}
	return sorted
}
