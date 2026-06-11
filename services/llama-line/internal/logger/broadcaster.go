package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
)

// LogRecord is a structured copy of a single slog record, suitable for
// streaming to subscribers (admin logs SSE) and for the in-memory ring buffer.
//
// Raw is the canonical JSON-encoded form (the same bytes emitted to stdout).
// Fields duplicates the parsed top-level structure so the UI can filter
// without re-parsing.
type LogRecord struct {
	Time    string         `json:"time"`
	Level   string         `json:"level"`
	Msg     string         `json:"msg"`
	Fields  map[string]any `json:"fields,omitempty"`
	RawJSON string         `json:"raw"`
}

// Broadcaster fans out every log record to all live subscribers and keeps a
// bounded ring buffer for back-fill on connect. It is safe for concurrent use.
type Broadcaster struct {
	mu          sync.RWMutex
	ring        []LogRecord
	size        int
	head        int
	count       int
	subscribers map[chan LogRecord]struct{}
	seq         atomic.Uint64
}

// NewBroadcaster creates a broadcaster keeping the last `size` records.
func NewBroadcaster(size int) *Broadcaster {
	if size <= 0 {
		size = 500
	}
	return &Broadcaster{
		ring:        make([]LogRecord, size),
		size:        size,
		subscribers: make(map[chan LogRecord]struct{}),
	}
}

// publish stores the record and fans it out. Slow subscribers whose buffered
// channel is full are dropped silently for this record (never blocks logging).
func (b *Broadcaster) publish(r LogRecord) {
	b.mu.Lock()
	idx := (b.head + b.count) % b.size
	if b.count == b.size {
		b.ring[b.head] = r
		b.head = (b.head + 1) % b.size
	} else {
		b.ring[idx] = r
		b.count++
	}
	// Snapshot subscriber channels under the lock; deliver outside.
	chans := make([]chan LogRecord, 0, len(b.subscribers))
	for c := range b.subscribers {
		chans = append(chans, c)
	}
	b.mu.Unlock()
	for _, c := range chans {
		select {
		case c <- r:
		default:
			// Slow consumer; skip this record for them.
		}
	}
}

// Subscribe returns a channel that receives every future record and an
// unsubscribe func that must be called when the subscriber goes away.
// Buffer is sized so brief bursts don't drop events.
func (b *Broadcaster) Subscribe() (<-chan LogRecord, func()) {
	ch := make(chan LogRecord, 256)
	b.mu.Lock()
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		if _, ok := b.subscribers[ch]; ok {
			delete(b.subscribers, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
}

// Recent returns up to n most recent records, oldest first.
// If n <= 0 or n > count, returns all stored records.
func (b *Broadcaster) Recent(n int) []LogRecord {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if n <= 0 || n > b.count {
		n = b.count
	}
	out := make([]LogRecord, n)
	start := b.count - n
	for i := 0; i < n; i++ {
		out[i] = b.ring[(b.head+start+i)%b.size]
	}
	return out
}

// ─── slog.Handler implementation ─────────────────────────────────────────────

// teeHandler writes every record to the underlying JSON handler (e.g. stdout)
// and publishes a structured copy to the broadcaster.
type teeHandler struct {
	json  *slog.JSONHandler
	buf   *bytes.Buffer
	bufMu *sync.Mutex
	b     *Broadcaster
	attrs []slog.Attr
	group string
}

func newTeeHandler(level slog.Level, w io.Writer, b *Broadcaster) *teeHandler {
	buf := &bytes.Buffer{}
	mw := &teeWriter{primary: w, capture: buf, mu: &sync.Mutex{}}
	h := slog.NewJSONHandler(mw, &slog.HandlerOptions{Level: level})
	return &teeHandler{json: h, buf: buf, bufMu: mw.mu, b: b}
}

// teeWriter writes to primary and also into capture buffer under mu so the
// caller can read the most-recent JSON record line out of capture.
type teeWriter struct {
	primary io.Writer
	capture *bytes.Buffer
	mu      *sync.Mutex
}

func (t *teeWriter) Write(p []byte) (int, error) {
	// The capture buffer is managed by teeHandler.Handle which holds mu
	// across the underlying handler call; we just append.
	t.capture.Write(p)
	return t.primary.Write(p)
}

func (h *teeHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.json.Enabled(ctx, l)
}

func (h *teeHandler) Handle(ctx context.Context, r slog.Record) error {
	// Serialise across the entire write+capture so the captured bytes match
	// the record we just emitted (the underlying handler can be called
	// concurrently from many goroutines).
	h.bufMu.Lock()
	h.buf.Reset()
	err := h.json.Handle(ctx, r)
	raw := append([]byte(nil), h.buf.Bytes()...)
	h.bufMu.Unlock()
	if err != nil {
		return err
	}
	if h.b == nil || len(raw) == 0 {
		return nil
	}
	// Trim trailing newline before storing as RawJSON string.
	trimmed := bytes.TrimRight(raw, "\n")
	rec := LogRecord{
		Time:    r.Time.Format("2006-01-02T15:04:05.000Z07:00"),
		Level:   r.Level.String(),
		Msg:     r.Message,
		RawJSON: string(trimmed),
	}
	// Parse fields from the raw JSON for filtering convenience.
	var parsed map[string]any
	if err := json.Unmarshal(trimmed, &parsed); err == nil {
		// Remove duplicates of top-level fields we already promote.
		delete(parsed, "time")
		delete(parsed, "level")
		delete(parsed, "msg")
		if len(parsed) > 0 {
			rec.Fields = parsed
		}
	}
	h.b.publish(rec)
	return nil
}

func (h *teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &teeHandler{
		json:  h.json.WithAttrs(attrs).(*slog.JSONHandler),
		buf:   h.buf,
		bufMu: h.bufMu,
		b:     h.b,
	}
}

func (h *teeHandler) WithGroup(name string) slog.Handler {
	return &teeHandler{
		json:  h.json.WithGroup(name).(*slog.JSONHandler),
		buf:   h.buf,
		bufMu: h.bufMu,
		b:     h.b,
	}
}
