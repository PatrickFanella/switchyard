package dedup

import (
	"bytes"
	"io"
	"net/http"
	"sync"

	"git.subcult.tv/PatrickFanella/llama-line/internal/queue"
)

// Waiter is a subscriber to an in-flight deduplicated request.
// When the primary request completes, its result is fanned out to all waiters.
type Waiter struct {
	ResponseCh chan queue.Result
}

// inflightEntry tracks one in-flight request and all subscribers sharing its result.
type inflightEntry struct {
	waiters []*Waiter
}

// Deduplicator tracks in-flight requests by content hash.
// When a new request arrives with the same hash, it subscribes to the existing
// in-flight request's result channel instead of being enqueued separately.
type Deduplicator struct {
	mu       sync.Mutex
	inflight map[string]*inflightEntry
}

// NewDeduplicator creates an empty Deduplicator.
func NewDeduplicator() *Deduplicator {
	return &Deduplicator{inflight: make(map[string]*inflightEntry)}
}

// Subscribe registers interest in the result for hash.
// If a matching in-flight entry exists, returns a Waiter and true.
// If no entry exists, returns nil, false — the caller should proceed normally and
// call Register to track the new in-flight request.
func (d *Deduplicator) Subscribe(hash string) (*Waiter, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	entry, ok := d.inflight[hash]
	if !ok {
		return nil, false
	}
	w := &Waiter{ResponseCh: make(chan queue.Result, 1)}
	entry.waiters = append(entry.waiters, w)
	return w, true
}

// Register marks hash as in-flight. The caller must call Publish when done.
func (d *Deduplicator) Register(hash string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.inflight[hash]; !ok {
		d.inflight[hash] = &inflightEntry{}
	}
}

// Publish fans out result to all waiters and removes the hash from the in-flight map.
// bodyBytes should be the response body that has already been read by the primary caller.
// If non-nil, each waiter gets a cloned response with that body.
// If result.Err is set, waiters receive the error.
func (d *Deduplicator) Publish(hash string, result queue.Result, bodyBytes []byte) {
	d.mu.Lock()
	entry, ok := d.inflight[hash]
	if !ok {
		d.mu.Unlock()
		return
	}
	waiters := entry.waiters
	delete(d.inflight, hash)
	d.mu.Unlock()

	for _, w := range waiters {
		var r queue.Result
		if result.Err != nil {
			r = result
		} else if bodyBytes != nil && result.Resp != nil {
			r = queue.Result{Resp: cloneResponse(result.Resp, bodyBytes)}
		} else {
			r = result
		}
		select {
		case w.ResponseCh <- r:
		default:
		}
	}
}

// cloneResponse creates a shallow copy of resp with body replaced by bodyBytes.
func cloneResponse(resp *http.Response, body []byte) *http.Response {
	r := *resp
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	return &r
}
