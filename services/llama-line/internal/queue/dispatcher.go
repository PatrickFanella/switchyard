package queue

import (
	"context"
	"time"
)

// ForwardFn is the function the dispatcher calls to execute a request against ollama.
// It must respect ctx cancellation.
type ForwardFn func(ctx context.Context, r *Request) Result

// MetricsRecorder is an optional interface for recording dispatcher metrics.
// Implemented by internal/metrics.Metrics to avoid a circular import.
type MetricsRecorder interface {
	RecordQueueWait(client string, secs float64)
	RecordInferenceDuration(client string, secs float64)
	RecordOllamaUnavailable()
	SetQueueDepth(depth float64)
}

// Dispatcher serialises inference requests through the queue one at a time.
type Dispatcher struct {
	queue            *Queue
	forward          ForwardFn
	requestTimeout   time.Duration
	queueWaitTimeout time.Duration
	met              MetricsRecorder // nil if metrics disabled
}

// NewDispatcher creates a Dispatcher.
func NewDispatcher(q *Queue, fn ForwardFn, requestTimeout, queueWaitTimeout time.Duration) *Dispatcher {
	return &Dispatcher{
		queue:            q,
		forward:          fn,
		requestTimeout:   requestTimeout,
		queueWaitTimeout: queueWaitTimeout,
	}
}

// SetMetrics attaches a MetricsRecorder to the dispatcher.
func (d *Dispatcher) SetMetrics(m MetricsRecorder) {
	d.met = m
}

// Run starts the dispatch loop. Blocks until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context) {
	for {
		r := d.queue.Dequeue(ctx)
		if r == nil {
			// ctx cancelled
			return
		}

		if d.met != nil {
			d.met.SetQueueDepth(float64(d.queue.Depth()))
		}

		// Check queue wait timeout.
		waitSecs := time.Since(r.EnqueuedAt).Seconds()
		if time.Duration(waitSecs*float64(time.Second)) > d.queueWaitTimeout {
			r.ResponseCh <- Result{Err: ErrQueueWaitTimeout}
			d.queue.Complete()
			d.queue.broadcastPositions()
			if d.met != nil {
				d.met.SetQueueDepth(float64(d.queue.Depth()))
			}
			continue
		}

		if d.met != nil {
			d.met.RecordQueueWait(r.ClientName, waitSecs)
		}

		// Execute with per-request timeout, also respecting client disconnect.
		start := time.Now()
		r.StartedAt = start
		childCtx, cancel := context.WithTimeout(r.Ctx, d.requestTimeout)
		result := d.forward(childCtx, r)
		// IMPORTANT: do NOT cancel here. The response body returned by forward()
		// is still being streamed from ollama; cancelling childCtx now kills the
		// in-flight body reads. The handler is responsible for calling result.Cancel
		// after PipeResponse drains the body (or immediately on Err).
		if result.Err != nil {
			cancel()
		} else {
			result.Cancel = cancel
		}

		if d.met != nil {
			d.met.RecordInferenceDuration(r.ClientName, time.Since(start).Seconds())
		}

		r.ResponseCh <- result
		d.queue.Complete()
		d.queue.broadcastPositions()
		if d.met != nil {
			d.met.SetQueueDepth(float64(d.queue.Depth()))
		}
	}
}
