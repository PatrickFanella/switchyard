package server

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/trace"

	"git.subcult.tv/PatrickFanella/llama-line/internal/config"
	"git.subcult.tv/PatrickFanella/llama-line/internal/dedup"
	"git.subcult.tv/PatrickFanella/llama-line/internal/keystore"
	"git.subcult.tv/PatrickFanella/llama-line/internal/logger"
	"git.subcult.tv/PatrickFanella/llama-line/internal/metrics"
	"git.subcult.tv/PatrickFanella/llama-line/internal/persistence"
	"git.subcult.tv/PatrickFanella/llama-line/internal/proxy"
	"git.subcult.tv/PatrickFanella/llama-line/internal/queue"
	"git.subcult.tv/PatrickFanella/llama-line/internal/router"
	"git.subcult.tv/PatrickFanella/llama-line/internal/ui"
)

// upstreamEntry holds the per-upstream queue, dispatcher, and forwarder.
type upstreamEntry struct {
	cfg        config.UpstreamConfig
	queue      *queue.Queue
	dispatcher *queue.Dispatcher
	forwarder  *proxy.Forwarder
	healthMu   sync.RWMutex
	health     upstreamHealthState
}

type upstreamHealthState struct {
	Healthy             *bool
	LastProbeAt         time.Time
	LastProbeMS         int64
	LastProbeError      string
	ConsecutiveFailures int
}

// Server wires together config, queue, forwarder, and HTTP routing.
type Server struct {
	cfg              *config.Config
	queue            *queue.Queue    // primary queue (upstreams[0]) — kept for admin/status compat
	upstreamEntries  []upstreamEntry // one per configured upstream
	router           *router.Router  // model → upstream index
	keys             *keystore.Store
	forwarder        *proxy.Forwarder // primary forwarder (upstreams[0]) — kept for pass-through compat
	met              *metrics.Metrics // nil if metrics disabled
	alerter          *metrics.Alerter // nil if alerting disabled
	history          *ui.RingBuffer
	log              *slog.Logger
	auditLog         *slog.Logger // nil if audit logging disabled
	logBroadcaster   *logger.Broadcaster
	tracerResolver   interface{ TracerForClient(string) trace.Tracer }
	store            *persistence.Store
	embedder         persistence.Embedder
	connectedClients atomic.Int32

	// Dedup / cache
	deduplicator *dedup.Deduplicator
	cacheMu      sync.Mutex
	caches       map[string]*dedup.Cache // keyed by client name
}

func newForwarderForUpstream(u config.UpstreamConfig, retryMaxSecs int, log *slog.Logger) *proxy.Forwarder {
	fwd := proxy.New(u.URL, retryMaxSecs, log)
	fwd.SetOutboundAuth(u.Auth)
	return fwd
}

// New creates a Server with a single upstream (legacy mode).
func New(cfg *config.Config, q *queue.Queue, f *proxy.Forwarder, log *slog.Logger) *Server {
	resolved := cfg.ResolvedUpstreams()
	entries := make([]upstreamEntry, len(resolved))
	for i, u := range resolved {
		maxDepth := cfg.MaxQueueDepth
		if u.MaxQueueDepth > 0 {
			maxDepth = u.MaxQueueDepth
		}
		if i == 0 {
			f.SetOutboundAuth(u.Auth)
			// Reuse the provided queue and forwarder for upstream 0.
			entries[i] = upstreamEntry{cfg: u, queue: q, forwarder: f}
		} else {
			entries[i] = upstreamEntry{
				cfg:       u,
				queue:     queue.New(maxDepth),
				forwarder: newForwarderForUpstream(u, cfg.OllamaRetryMaxSecs, log),
			}
		}
	}
	return &Server{
		cfg:             cfg,
		queue:           q,
		upstreamEntries: entries,
		router:          router.New(resolved),
		keys:            keystore.New(cfg.Keys),
		forwarder:       f,
		history:         ui.NewRingBuffer(200),
		log:             log,
		deduplicator:    dedup.NewDeduplicator(),
		caches:          make(map[string]*dedup.Cache),
	}
}

// SetMetrics attaches a Metrics instance to the server.
// Must be called before ListenAndServe.
func (s *Server) SetMetrics(m *metrics.Metrics) {
	s.met = m
}

// SetAlerter attaches an Alerter to the server.
// Must be called before ListenAndServe.
func (s *Server) SetAlerter(a *metrics.Alerter) {
	s.alerter = a
}

// SetAuditLog attaches an audit logger to the server.
// When set, every non-streaming request/response body is written to this logger.
// Must be called before ListenAndServe.
func (s *Server) SetAuditLog(l *slog.Logger) {
	s.auditLog = l
}

// SetLogBroadcaster attaches a log broadcaster used by /admin/logs endpoints.
func (s *Server) SetLogBroadcaster(b *logger.Broadcaster) {
	s.logBroadcaster = b
}

// SetStore attaches optional Postgres persistence.
func (s *Server) SetStore(store *persistence.Store) {
	s.store = store
}

// SetEmbedder attaches an embedder used by semantic search endpoints.
func (s *Server) SetEmbedder(embedder persistence.Embedder) {
	s.embedder = embedder
}

// SetTracer attaches an OTEL tracer to the server.
func (s *Server) SetTracer(tracer trace.Tracer) {
	s.tracerResolver = fixedTracerResolver{tracer: tracer}
}

type fixedTracerResolver struct{ tracer trace.Tracer }

func (f fixedTracerResolver) TracerForClient(string) trace.Tracer { return f.tracer }

// SetTracerResolver attaches a client-aware OTEL tracer resolver to the server.
func (s *Server) SetTracerResolver(r interface{ TracerForClient(string) trace.Tracer }) {
	s.tracerResolver = r
}

// UpstreamEntry returns the i-th upstream entry (for wiring metrics/alerters in main).
func (s *Server) UpstreamCount() int { return len(s.upstreamEntries) }

// StartDispatchers starts a dispatch loop for each upstream. Call before ListenAndServe.
func (s *Server) StartDispatchers(ctx context.Context, requestTimeout, queueWaitTimeout time.Duration) {
	for i := range s.upstreamEntries {
		e := &s.upstreamEntries[i]
		d := queue.NewDispatcher(e.queue, e.forwarder.Forward, requestTimeout, queueWaitTimeout)
		e.dispatcher = d
		go d.Run(ctx)
	}
}

// StartHealthProbes starts a background health probe loop for each upstream.
func (s *Server) StartHealthProbes(ctx context.Context, interval, timeout time.Duration) {
	if interval <= 0 || timeout <= 0 || len(s.upstreamEntries) == 0 {
		return
	}
	for i := range s.upstreamEntries {
		e := &s.upstreamEntries[i]
		go func(entry *upstreamEntry) {
			probe := func() {
				probeCtx, cancel := context.WithTimeout(ctx, timeout)
				defer cancel()
				entry.setHealth(entry.forwarder.HealthCheck(probeCtx, entry.cfg.HealthPath))
			}

			probe()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					probe()
				}
			}
		}(e)
	}
}

// SetDispatcherMetrics attaches metrics to all per-upstream dispatchers.
// Must be called after StartDispatchers.
func (s *Server) SetDispatcherMetrics(m *metrics.Metrics) {
	for i := range s.upstreamEntries {
		if s.upstreamEntries[i].dispatcher != nil {
			s.upstreamEntries[i].dispatcher.SetMetrics(m)
		}
	}
}

// SetForwarderMetrics attaches metrics and alerter to all per-upstream forwarders.
func (s *Server) SetForwarderMetrics(m *metrics.Metrics) {
	for i := range s.upstreamEntries {
		s.upstreamEntries[i].forwarder.SetMetrics(m)
	}
}

// SetForwarderAlerter attaches alerter to all per-upstream forwarders.
func (s *Server) SetForwarderAlerter(a *metrics.Alerter) {
	for i := range s.upstreamEntries {
		s.upstreamEntries[i].forwarder.SetAlerter(a)
	}
}

func (e *upstreamEntry) setHealth(result proxy.HealthCheckResult) {
	healthy := result.Healthy
	e.healthMu.Lock()
	defer e.healthMu.Unlock()
	e.health.LastProbeAt = result.CheckedAt
	e.health.LastProbeMS = result.Duration.Milliseconds()
	e.health.LastProbeError = result.Error
	e.health.Healthy = &healthy
	if result.Healthy {
		e.health.ConsecutiveFailures = 0
		return
	}
	e.health.ConsecutiveFailures++
}

func (e *upstreamEntry) healthSnapshot() upstreamHealthState {
	e.healthMu.RLock()
	defer e.healthMu.RUnlock()
	snap := e.health
	if snap.Healthy != nil {
		healthy := *snap.Healthy
		snap.Healthy = &healthy
	}
	return snap
}

// primaryEntry returns the primary (index 0) upstream entry.
func (s *Server) primaryEntry() *upstreamEntry {
	if len(s.upstreamEntries) == 0 {
		return nil
	}
	return &s.upstreamEntries[0]
}

// clientCache returns the cache for clientName, creating it on first use with the
// given TTL and max entries.  Returns nil when caching is disabled for this client.
func (s *Server) clientCache(clientName string, ttlSecs, maxEntries int) *dedup.Cache {
	if ttlSecs <= 0 || maxEntries <= 0 {
		// Use global default max entries if per-key is 0.
		if maxEntries <= 0 {
			maxEntries = s.cfg.CacheMaxEntries
		}
		if ttlSecs <= 0 || maxEntries <= 0 {
			return nil
		}
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if c, ok := s.caches[clientName]; ok {
		return c
	}
	c := dedup.NewCache(maxEntries, time.Duration(ttlSecs)*time.Second)
	s.caches[clientName] = c
	return c
}

// ListenAndServe starts the HTTP server and blocks until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	mux := http.NewServeMux()

	mux.HandleFunc("/broker/status", s.HandleBrokerStatus)

	// Prometheus metrics endpoint (optional).
	if s.cfg.MetricsEnabled {
		path := s.cfg.MetricsPath
		if path == "" {
			path = "/metrics"
		}
		mux.Handle(path, promhttp.Handler())
	}

	// Admin API — protected by admin key.
	mux.Handle("/admin/", s.AdminHandler())

	// Web UI — unauthenticated (admin key is applied per-action in JS).
	uiHandler := ui.New((*uiQuerier)(s), uiKeyLister{s.keys}, s.history)
	mux.Handle("/ui/", uiHandler)
	mux.Handle("/ui", http.RedirectHandler("/ui/", http.StatusMovedPermanently))

	authed := AuthMiddleware(s.keys, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if IsInferenceEndpoint(r.URL.Path) {
			s.HandleInference(w, r)
		} else {
			s.HandlePassThrough(w, r)
		}
	}))
	mux.Handle("/", authed)

	srv := &http.Server{
		Addr:    s.cfg.ListenAddr,
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		srv.Shutdown(shutCtx)
	}()

	s.log.Info("listening", "addr", s.cfg.ListenAddr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

// ─── UI adapter types ────────────────────────────────────────────────────────

// uiQuerier adapts *Server to ui.Querier.
type uiQuerier Server

func (u *uiQuerier) Snapshot() ui.QueueSnapshot {
	snap := (*Server)(u).queue.Snapshot()
	items := make([]ui.QueueItem, len(snap.Items))
	for i, it := range snap.Items {
		items[i] = ui.QueueItem{
			ID:         it.ID,
			ClientName: it.ClientName,
			Priority:   it.Priority,
			Position:   it.Position,
			EnqueuedAt: it.EnqueuedAt,
		}
	}
	return ui.QueueSnapshot{
		Depth:          snap.Depth,
		MaxDepth:       snap.MaxDepth,
		InFlightClient: snap.InFlightClient,
		InFlightID:     snap.InFlightID,
		Items:          items,
	}
}

// uiKeyLister adapts *keystore.Store to ui.KeyLister.
type uiKeyLister struct{ s *keystore.Store }

func (u uiKeyLister) List() []ui.KeyMeta {
	raw := u.s.List()
	out := make([]ui.KeyMeta, len(raw))
	for i, k := range raw {
		out[i] = ui.KeyMeta{Name: k.Name, Priority: k.Priority, CreatedAt: k.CreatedAt}
	}
	return out
}
