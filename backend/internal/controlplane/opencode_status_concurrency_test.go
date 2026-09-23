package controlplane

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
)

type statusProbeBarrier struct {
	mu      sync.Mutex
	started int
	release chan struct{}
}

func newStatusProbeBarrier() *statusProbeBarrier {
	return &statusProbeBarrier{release: make(chan struct{})}
}

func (b *statusProbeBarrier) wait(ctx context.Context) bool {
	b.mu.Lock()
	b.started++
	if b.started == 2 {
		close(b.release)
	}
	b.mu.Unlock()
	select {
	case <-b.release:
		return true
	case <-ctx.Done():
		return false
	}
}

type barrierOpenCodeStatusProvider struct{ barrier *statusProbeBarrier }

func (p barrierOpenCodeStatusProvider) Status(ctx context.Context) OpenCodeExecutorStatus {
	healthy := p.barrier.wait(ctx)
	return OpenCodeExecutorStatus{Configured: true, Healthy: healthy, Status: "healthy"}
}

type barrierRepositoryStatusProvider struct{ barrier *statusProbeBarrier }

func (p barrierRepositoryStatusProvider) Status(ctx context.Context) OpenPilotRepositoryStatus {
	healthy := p.barrier.wait(ctx)
	return OpenPilotRepositoryStatus{Configured: true, Healthy: healthy}
}

func TestOpenCodeExecutorStatusProbesProvidersConcurrently(t *testing.T) {
	barrier := newStatusProbeBarrier()
	api := &API{
		openCode:              barrierOpenCodeStatusProvider{barrier: barrier},
		openPilotRepositories: barrierRepositoryStatusProvider{barrier: barrier},
	}
	request := httptest.NewRequest("GET", "/api/opencode-executor/status", nil)
	response := httptest.NewRecorder()

	api.openCodeExecutorStatus(response, request)

	if response.Code != 200 {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	if barrier.started != 2 {
		t.Fatalf("started probes = %d", barrier.started)
	}
}
