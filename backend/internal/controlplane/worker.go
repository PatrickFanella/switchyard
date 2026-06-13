package controlplane

import (
	"context"
	"fmt"
	"log"
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
	p.mu.Unlock()

	go func() {
		defer func() {
			p.mu.Lock()
			p.running = false
			p.mu.Unlock()
		}()
		p.recoverLeases()
		var wg sync.WaitGroup
		for i := 0; i < p.workers; i++ {
			workerID := fmt.Sprintf("worker-%d", i+1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				p.loop(ctx, workerID)
			}()
		}
		<-ctx.Done()
		wg.Wait()
	}()
}

func (p *WorkerPool) loop(ctx context.Context, workerID string) {
	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		claimed, err := p.store.ClaimNextRun(workerID, p.lease)
		if err == nil {
			p.execute(ctx, workerID, claimed)
			continue
		}
		if err != ErrNotFound {
			p.recordError(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.recoverLeases()
		}
	}
}

func (p *WorkerPool) execute(ctx context.Context, workerID string, run Run) {
	p.mu.Lock()
	p.active++
	p.mu.Unlock()
	execCtx, cancelExecution := context.WithCancel(ctx)
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
	p.mu.Lock()
	if err != nil || finished.Status == RunFailed || finished.Status == RunCanceled {
		p.failedRuns++
	} else {
		p.completedRuns++
	}
	p.mu.Unlock()
	if err != nil {
		p.recordError(err)
	}
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
