package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"git.subcult.tv/PatrickFanella/llama-line/internal/config"
	"git.subcult.tv/PatrickFanella/llama-line/internal/logger"
	"git.subcult.tv/PatrickFanella/llama-line/internal/metrics"
	"git.subcult.tv/PatrickFanella/llama-line/internal/persistence"
	"git.subcult.tv/PatrickFanella/llama-line/internal/proxy"
	"git.subcult.tv/PatrickFanella/llama-line/internal/queue"
	"git.subcult.tv/PatrickFanella/llama-line/internal/server"
	"git.subcult.tv/PatrickFanella/llama-line/internal/tracer"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "add-app" {
		if err := runAddApp(os.Args[2:], os.Stdin, os.Stdout, os.Stderr); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	configPath := flag.String("config", "config.yaml", "Path to config file")
	logLevel := flag.String("log-level", "", "Override log level (debug|info|warn|error)")
	version := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *version {
		fmt.Println("llama-line v0.4.0")
		os.Exit(0)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	config.ApplyEnv(cfg)
	config.ApplyCLI(cfg, config.CLIFlags{ConfigPath: *configPath, LogLevel: *logLevel})

	logBroadcaster := logger.NewBroadcaster(1000)
	log := logger.NewWithBroadcaster(cfg.LogLevel, logBroadcaster)

	// Resolve upstreams (supports both legacy ollama_url and new upstreams list).
	upstreams := cfg.ResolvedUpstreams()
	if len(upstreams) == 0 {
		fmt.Fprintln(os.Stderr, "error: no upstreams configured (set ollama_url or upstreams list)")
		os.Exit(1)
	}
	log.Info("llama-line starting",
		"listen_addr", cfg.ListenAddr,
		"upstreams", len(upstreams),
		"primary_url", upstreams[0].URL,
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, err := persistence.Open(ctx, cfg.Database)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error opening database: %v\n", err)
		os.Exit(1)
	}
	if store != nil {
		defer store.Close()
		log.Info("database persistence enabled", "app_logs", cfg.Database.PersistAppLogs, "request_bodies", cfg.Database.PersistRequestBodies)
	}
	stopLogPersistence := startAppLogPersistence(ctx, store, logBroadcaster)
	defer stopLogPersistence()
	var embedder persistence.Embedder
	var stopEmbeddingWorkers func()

	// Build primary queue and forwarder (server.New wires additional upstreams internally).
	q := queue.New(primaryQueueMaxDepth(cfg, upstreams))
	fwd := proxy.New(upstreams[0].URL, cfg.OllamaRetryMaxSecs, log)
	requestTimeout := time.Duration(cfg.RequestTimeoutSecs) * time.Second
	queueWaitTimeout := time.Duration(cfg.QueueWaitTimeoutSecs) * time.Second

	srv := server.New(cfg, q, fwd, log)
	srv.SetLogBroadcaster(logBroadcaster)
	srv.SetStore(store)
	if tracerManager, err := tracer.NewManager(ctx, cfg.Phoenix); err != nil {
		fmt.Fprintf(os.Stderr, "error initializing phoenix tracer: %v\n", err)
		os.Exit(1)
	} else {
		defer tracerManager.Shutdown()
		if cfg.Phoenix.Enabled {
			srv.SetTracerResolver(tracerManager)
			log.Info("phoenix tracing enabled", "endpoint", cfg.Phoenix.OTLPEndpoint, "project_mode", cfg.Phoenix.ProjectMode, "project_name", cfg.Phoenix.ProjectName)
		}
	}
	if store != nil && cfg.Embeddings.Enabled {
		if cfg.Embeddings.Dimension != persistence.EmbeddingDimension {
			fmt.Fprintf(os.Stderr, "error: embeddings.dimension must be %d for current schema\n", persistence.EmbeddingDimension)
			os.Exit(1)
		}
		if err := store.MigrateEmbeddings(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "error applying embedding migrations: %v\n", err)
			os.Exit(1)
		}
		store.SetEmbeddingsEnabled(true)
		embedder = &persistence.OllamaEmbedder{BaseURL: upstreams[0].URL, Dimension: cfg.Embeddings.Dimension}
		stopEmbeddingWorkers = store.StartEmbeddingWorkers(ctx, cfg.Embeddings, embedder)
		defer stopEmbeddingWorkers()
		srv.SetEmbedder(embedder)
		log.Info("semantic indexing enabled", "model", cfg.Embeddings.Model, "dimension", cfg.Embeddings.Dimension, "workers", cfg.Embeddings.WorkerCount)
	}

	// Wire metrics and alerting if enabled.
	if cfg.MetricsEnabled {
		met := metrics.New(prometheus.DefaultRegisterer)
		srv.SetMetrics(met)
		srv.SetDispatcherMetrics(met)
		srv.SetForwarderMetrics(met)
		log.Info("metrics enabled", "path", cfg.MetricsPath)
	}

	if cfg.AlertWebhookURL != "" {
		alerter := metrics.NewAlerter(cfg.AlertWebhookURL, log)
		srv.SetAlerter(alerter)
		srv.SetForwarderAlerter(alerter)
		log.Info("alerting enabled", "webhook", cfg.AlertWebhookURL)
	}

	// Wire audit log if configured.
	if cfg.AuditLogPath != "" {
		auditLogger, err := logger.NewFile(cfg.AuditLogPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error opening audit log: %v\n", err)
			os.Exit(1)
		}
		srv.SetAuditLog(auditLogger)
		log.Info("audit logging enabled", "path", cfg.AuditLogPath)
	}

	// Start one dispatcher goroutine per upstream.
	srv.StartDispatchers(ctx, requestTimeout, queueWaitTimeout)
	srv.StartHealthProbes(
		ctx,
		time.Duration(cfg.UpstreamHealthProbeIntervalSecs)*time.Second,
		time.Duration(cfg.UpstreamHealthProbeTimeoutSecs)*time.Second,
	)

	if err := srv.ListenAndServe(ctx); err != nil {
		log.Error("server error", "err", err)
		os.Exit(1)
	}

	log.Info("llama-line shutting down")
}

func startAppLogPersistence(ctx context.Context, store *persistence.Store, b *logger.Broadcaster) func() {
	if store == nil || b == nil {
		return func() {}
	}
	ch, unsubscribe := b.Subscribe()
	done := make(chan struct{})
	stop := make(chan struct{})
	persist := func(rec logger.LogRecord) {
		tm, _ := time.Parse(time.RFC3339Nano, rec.Time)
		if tm.IsZero() {
			tm = time.Now()
		}
		store.EnqueueAppLog(persistence.AppLogEvent{
			Time:    tm,
			Level:   rec.Level,
			Msg:     rec.Msg,
			Fields:  rec.Fields,
			RawJSON: rec.RawJSON,
		})
	}
	drain := func() {
		for rec := range ch {
			persist(rec)
		}
	}
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				unsubscribe()
				drain()
				return
			case <-ctx.Done():
				unsubscribe()
				drain()
				return
			case rec, ok := <-ch:
				if !ok {
					return
				}
				persist(rec)
			}
		}
	}()
	return func() { close(stop); <-done }
}

func primaryQueueMaxDepth(cfg *config.Config, upstreams []config.UpstreamConfig) int {
	if len(upstreams) > 0 && upstreams[0].MaxQueueDepth > 0 {
		return upstreams[0].MaxQueueDepth
	}
	return cfg.MaxQueueDepth
}
