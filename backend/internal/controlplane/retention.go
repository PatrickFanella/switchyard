package controlplane

import (
	"context"
	"errors"
	"sync"
	"time"
)

const retentionBatchesPerRun = 20

type RetentionStatus struct {
	Running   bool            `json:"running"`
	Window    string          `json:"window"`
	Interval  string          `json:"interval"`
	LastRunAt time.Time       `json:"last_run_at,omitempty"`
	LastPrune RetentionResult `json:"last_prune"`
	LastError string          `json:"last_error,omitempty"`
}

type RetentionManager struct {
	store    Store
	window   time.Duration
	interval time.Duration
	now      func() time.Time
	mu       sync.RWMutex
	status   RetentionStatus
	done     chan struct{}
}

func NewRetentionManager(store Store, window, interval time.Duration, now func() time.Time) *RetentionManager {
	if window <= 0 {
		window = 30 * 24 * time.Hour
	}
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	if now == nil {
		now = time.Now
	}
	return &RetentionManager{store: store, window: window, interval: interval, now: now, status: RetentionStatus{Window: window.String(), Interval: interval.String()}}
}

func (m *RetentionManager) Start(ctx context.Context) {
	m.mu.Lock()
	if m.status.Running {
		m.mu.Unlock()
		return
	}
	m.status.Running = true
	m.done = make(chan struct{})
	m.mu.Unlock()
	go func() {
		defer func() {
			m.mu.Lock()
			m.status.Running = false
			close(m.done)
			m.mu.Unlock()
		}()
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		for {
			more := m.prune()
			if more {
				select {
				case <-ctx.Done():
					return
				default:
					continue
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (m *RetentionManager) prune() bool {
	now := m.now().UTC()
	result := RetentionResult{}
	var pruneErr error
	more := false
	for range retentionBatchesPerRun {
		batch, err := m.store.PruneRetention(now.Add(-m.window))
		result.Runs += batch.Runs
		result.ModelRequests += batch.ModelRequests
		result.Artifacts += batch.Artifacts
		result.ArtifactBytes += batch.ArtifactBytes
		result.ArtifactBytesUnmeasured += batch.ArtifactBytesUnmeasured
		if err != nil {
			pruneErr = errors.Join(pruneErr, err)
			break
		}
		more = batch.Runs >= retentionBatchSize || batch.ModelRequests >= retentionBatchSize
		if !more {
			break
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.LastRunAt = now
	m.status.LastPrune = result
	m.status.LastError = ""
	if pruneErr != nil {
		m.status.LastError = pruneErr.Error()
	}
	return more && pruneErr == nil
}

func (m *RetentionManager) Status() RetentionStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}

func (m *RetentionManager) Wait(ctx context.Context) error {
	m.mu.RLock()
	done := m.done
	m.mu.RUnlock()
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
