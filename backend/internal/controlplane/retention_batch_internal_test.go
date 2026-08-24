package controlplane

import (
	"context"
	"sync"
	"testing"
	"time"
)

type batchedRetentionStore struct {
	Store
	mu      sync.Mutex
	results []RetentionResult
	drained chan struct{}
}

func (s *batchedRetentionStore) PruneRetention(time.Time) (RetentionResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.results) == 0 {
		return RetentionResult{}, nil
	}
	result := s.results[0]
	s.results = s.results[1:]
	if len(s.results) == 0 {
		close(s.drained)
	}
	return result, nil
}

func TestRetentionManagerImmediatelyDrainsBoundedBatches(t *testing.T) {
	store := &batchedRetentionStore{Store: NewStore(nil), drained: make(chan struct{})}
	for range retentionBatchesPerRun + 1 {
		store.results = append(store.results, RetentionResult{Runs: retentionBatchSize, Artifacts: retentionBatchSize, ArtifactBytes: retentionBatchSize})
	}
	store.results = append(store.results, RetentionResult{Runs: 1, Artifacts: 1, ArtifactBytes: 1})

	ctx, cancel := context.WithCancel(t.Context())
	manager := NewRetentionManager(store, time.Hour, time.Hour, nil)
	manager.Start(ctx)
	select {
	case <-store.drained:
	case <-time.After(time.Second):
		t.Fatal("retention backlog was not drained immediately")
	}
	cancel()
	if err := manager.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionManagerReportsWebhookReceiptPruning(t *testing.T) {
	store := &batchedRetentionStore{Store: NewStore(nil), results: []RetentionResult{{WebhookReceipts: 7}}, drained: make(chan struct{})}
	manager := NewRetentionManager(store, time.Hour, time.Hour, nil)
	manager.prune()
	if got := manager.Status().LastPrune.WebhookReceipts; got != 7 {
		t.Fatalf("webhook receipts=%d", got)
	}
}
