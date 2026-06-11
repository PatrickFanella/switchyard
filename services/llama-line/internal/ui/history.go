package ui

import (
	"sync"
	"time"
)

// HistoryEntry records the outcome of a completed request.
type HistoryEntry struct {
	ID          string    `json:"id"`
	Client      string    `json:"client"`
	Model       string    `json:"model"`
	Status      string    `json:"status"` // "success" | "error" | "timeout" | "dropped"
	DurationMs  int64     `json:"duration_ms"`
	QueueMs     int64     `json:"queue_ms"`
	UpstreamMs  int64     `json:"upstream_ms"`
	CompletedAt time.Time `json:"completed_at"`
}

// RingBuffer is a fixed-size circular buffer of HistoryEntry values.
// It is safe for concurrent use.
type RingBuffer struct {
	mu      sync.Mutex
	entries []HistoryEntry
	size    int
	head    int
	count   int
}

// NewRingBuffer creates a RingBuffer with the given capacity.
func NewRingBuffer(size int) *RingBuffer {
	if size <= 0 {
		size = 100
	}
	return &RingBuffer{
		entries: make([]HistoryEntry, size),
		size:    size,
	}
}

// Add appends an entry, overwriting the oldest when full.
func (r *RingBuffer) Add(e HistoryEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	idx := (r.head + r.count) % r.size
	if r.count == r.size {
		// Overwrite oldest: advance head.
		r.entries[r.head] = e
		r.head = (r.head + 1) % r.size
	} else {
		r.entries[idx] = e
		r.count++
	}
}

// Snapshot returns entries in order from oldest to newest.
func (r *RingBuffer) Snapshot() []HistoryEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]HistoryEntry, r.count)
	for i := 0; i < r.count; i++ {
		out[i] = r.entries[(r.head+i)%r.size]
	}
	return out
}
