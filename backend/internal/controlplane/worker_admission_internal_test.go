package controlplane

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type admissionRecoveryStore struct {
	Store
	recoverCalls atomic.Int32
}

func (s *admissionRecoveryStore) RecoverLeasedRuns(time.Time, ...*OpenPilotAdmissionGate) (int, int, error) {
	s.recoverCalls.Add(1)
	return 0, 0, nil
}

func TestWorkerPoolGateFailureRefusesStartAndRecovery(t *testing.T) {
	base := NewStore(nil)
	store := &admissionRecoveryStore{Store: base}
	runner := NewRunner(store, nil)
	runner.openPilotGateError = errors.New("admission unavailable")
	pool := NewWorkerPool(store, runner, 1, time.Minute, time.Millisecond, nil)
	pool.Start(t.Context())
	pool.recoverLeases()
	status := pool.Status()
	if status.Running || !strings.Contains(status.LastError, "admission unavailable") {
		t.Fatalf("status = %+v", status)
	}
	if store.recoverCalls.Load() != 0 {
		t.Fatalf("recovery store calls = %d", store.recoverCalls.Load())
	}
}
