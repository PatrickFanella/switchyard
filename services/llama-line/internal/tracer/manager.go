package tracer

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel/trace"

	"git.subcult.tv/PatrickFanella/llama-line/internal/config"
)

// Resolver returns a tracer for a llama-line client name.
type Resolver interface {
	TracerForClient(client string) trace.Tracer
	Shutdown()
}

// Manager owns Phoenix tracer providers for static or per-client projects.
type Manager struct {
	cfg       config.PhoenixConfig
	mu        sync.Mutex
	tracers   map[string]trace.Tracer
	shutdowns []func()
}

// NewManager creates a tracer manager. Static mode eagerly creates one tracer;
// client mode lazily creates one provider per observed client project.
func NewManager(ctx context.Context, cfg config.PhoenixConfig) (*Manager, error) {
	m := &Manager{cfg: cfg, tracers: map[string]trace.Tracer{}}
	if !cfg.Enabled {
		return m, nil
	}
	if cfg.ProjectMode == "" || cfg.ProjectMode == "static" {
		tr, shutdown, err := New(ctx, cfg)
		if err != nil {
			return nil, err
		}
		m.tracers[cfg.ProjectName] = tr
		m.shutdowns = append(m.shutdowns, shutdown)
	}
	return m, nil
}

// TracerForClient returns the static tracer or a per-client Phoenix project tracer.
func (m *Manager) TracerForClient(client string) trace.Tracer {
	if m == nil || !m.cfg.Enabled {
		return nil
	}
	project := m.cfg.ProjectName
	if m.cfg.ProjectMode == "client" && client != "" {
		project = client
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if tr, ok := m.tracers[project]; ok {
		return tr
	}
	cfg := m.cfg
	cfg.ProjectName = project
	tr, shutdown, err := New(context.Background(), cfg)
	if err != nil {
		return nil
	}
	m.tracers[project] = tr
	m.shutdowns = append(m.shutdowns, shutdown)
	return tr
}

// Shutdown flushes and stops all owned tracer providers.
func (m *Manager) Shutdown() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, shutdown := range m.shutdowns {
		shutdown()
	}
}
