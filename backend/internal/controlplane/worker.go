package controlplane

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

type WorkerPoolStatus struct {
	Enabled       bool      `json:"enabled"`
	Running       bool      `json:"running"`
	Workers       int       `json:"workers"`
	Active        int       `json:"active"`
	Lease         string    `json:"lease"`
	PollInterval  string    `json:"poll_interval"`
	LastRecoverAt time.Time `json:"last_recover_at,omitempty"`
	RecoveredRuns int       `json:"recovered_runs"`
	FailedLeases  int       `json:"failed_leases"`
	CompletedRuns int       `json:"completed_runs"`
	FailedRuns    int       `json:"failed_runs"`
	LeaseRenewals int       `json:"lease_renewals"`
	LastError     string    `json:"last_error,omitempty"`
}

type WorkerPool struct {
	store        Store
	runner       *Runner
	workers      int
	lease        time.Duration
	pollInterval time.Duration
	now          func() time.Time

	mu            sync.RWMutex
	running       bool
	active        int
	lastRecoverAt time.Time
	recoveredRuns int
	failedLeases  int
	completedRuns int
	failedRuns    int
	leaseRenewals int
	lastErr       string
	done          chan struct{}
	cancelActive  context.CancelFunc
}

func NewWorkerPool(store Store, runner *Runner, workers int, lease time.Duration, pollInterval time.Duration, now func() time.Time) *WorkerPool {
	if workers < 1 {
		workers = 1
	}
	if lease <= 0 {
		lease = 5 * time.Minute
	}
	if pollInterval <= 0 {
		pollInterval = time.Second
	}
	if now == nil {
		now = time.Now
	}
	return &WorkerPool{store: store, runner: runner, workers: workers, lease: lease, pollInterval: pollInterval, now: now}
}

func (p *WorkerPool) Status() WorkerPoolStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return WorkerPoolStatus{Enabled: true, Running: p.running, Workers: p.workers, Active: p.active, Lease: p.lease.String(), PollInterval: p.pollInterval.String(), LastRecoverAt: p.lastRecoverAt, RecoveredRuns: p.recoveredRuns, FailedLeases: p.failedLeases, CompletedRuns: p.completedRuns, FailedRuns: p.failedRuns, LeaseRenewals: p.leaseRenewals, LastError: p.lastErr}
}

func (p *WorkerPool) Start(ctx context.Context) {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return
	}
	p.running = true
	p.done = make(chan struct{})
	executionCtx, cancelActive := context.WithCancel(context.Background())
	p.cancelActive = cancelActive
	p.mu.Unlock()

	go func() {
		defer func() {
			p.mu.Lock()
			p.running = false
			close(p.done)
			p.mu.Unlock()
			cancelActive()
		}()
		p.recoverLeases()
		var wg sync.WaitGroup
		for i := 0; i < p.workers; i++ {
			workerID := fmt.Sprintf("worker-%d", i+1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				p.loop(ctx, executionCtx, workerID)
			}()
		}
		<-ctx.Done()
		wg.Wait()
	}()
}

func (p *WorkerPool) loop(intakeCtx context.Context, executionCtx context.Context, workerID string) {
	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-intakeCtx.Done():
			return
		default:
		}
		claimed, err := p.store.ClaimNextRun(workerID, p.lease)
		if err == nil {
			p.execute(executionCtx, workerID, claimed)
			continue
		}
		if err != ErrNotFound {
			p.recordError(err)
		}
		select {
		case <-intakeCtx.Done():
			return
		case <-ticker.C:
			p.recoverLeases()
		}
	}
}

func (p *WorkerPool) Wait(ctx context.Context) error {
	p.mu.RLock()
	done := p.done
	p.mu.RUnlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *WorkerPool) CancelActive() {
	p.mu.RLock()
	cancel := p.cancelActive
	p.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
}

func (p *WorkerPool) execute(ctx context.Context, workerID string, run Run) {
	p.mu.Lock()
	p.active++
	p.mu.Unlock()
	execCtx, cancelExecution := context.WithTimeout(ctx, runExecutionTimeout())
	defer cancelExecution()
	stopHeartbeat := p.startHeartbeat(execCtx, workerID, run.ID, cancelExecution)
	defer stopHeartbeat()
	defer func() {
		if recovered := recover(); recovered != nil {
			_, _ = p.store.FinishClaimedRun(run.ID, workerID, RunFailed, fmt.Sprintf("worker panic: %v", recovered))
			p.mu.Lock()
			p.failedRuns++
			p.active--
			p.mu.Unlock()
			return
		}
		p.mu.Lock()
		p.active--
		p.mu.Unlock()
	}()
	finished, err := p.runner.ExecuteClaimedRun(execCtx, run, workerID)
	if err == nil && (finished.Status == RunFailed || finished.Status == RunTimedOut) && finished.AttemptCount < finished.MaxAttempts {
		backoff := runRetryBackoff(finished.AttemptCount)
		if _, retryErr := p.store.RequeueFailedRun(finished.ID, p.now().UTC().Add(backoff), fmt.Sprintf("Retry %d/%d scheduled after %s", finished.AttemptCount+1, finished.MaxAttempts, backoff)); retryErr == nil {
			return
		} else {
			p.recordError(retryErr)
		}
	}
	p.mu.Lock()
	if err != nil || finished.Status == RunFailed || finished.Status == RunCanceled || finished.Status == RunTimedOut {
		p.failedRuns++
	} else {
		p.completedRuns++
	}
	p.mu.Unlock()
	if err != nil {
		p.recordError(err)
	}
}

func runExecutionTimeout() time.Duration {
	if parsed, err := time.ParseDuration(strings.TrimSpace(os.Getenv("SWITCHYARD_RUN_TIMEOUT"))); err == nil && parsed > 0 {
		return parsed
	}
	return 10 * time.Minute
}

func runRetryBackoff(attempt int) time.Duration {
	base := 5 * time.Second
	if parsed, err := time.ParseDuration(strings.TrimSpace(os.Getenv("SWITCHYARD_RUN_RETRY_BACKOFF"))); err == nil && parsed > 0 {
		base = parsed
	}
	if attempt < 1 {
		attempt = 1
	}
	multiplier := 1 << min(attempt-1, 6)
	backoff := time.Duration(multiplier) * base
	maxBackoff := 5 * time.Minute
	if raw := strings.TrimSpace(os.Getenv("SWITCHYARD_RUN_RETRY_MAX_BACKOFF")); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			maxBackoff = parsed
		}
	}
	if backoff > maxBackoff {
		return maxBackoff
	}
	return backoff
}

func (p *WorkerPool) startHeartbeat(ctx context.Context, workerID string, runID string, cancelExecution context.CancelFunc) func() {
	interval := p.lease / 2
	if interval < time.Second {
		interval = time.Second
	}
	heartbeatCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				if _, err := p.store.ExtendRunLease(runID, workerID, p.lease); err != nil {
					p.recordError(err)
					cancelExecution()
					return
				}
				p.mu.Lock()
				p.leaseRenewals++
				p.mu.Unlock()
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func (p *WorkerPool) recoverLeases() {
	requeued, failed, err := p.store.RecoverLeasedRuns(p.now())
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastRecoverAt = p.now().UTC()
	p.recoveredRuns += requeued
	p.failedLeases += failed
	if err != nil {
		p.lastErr = err.Error()
		log.Printf("switchyard worker recovery: %v", err)
	}
}

func (p *WorkerPool) recordError(err error) {
	p.mu.Lock()
	p.lastErr = err.Error()
	p.mu.Unlock()
}
