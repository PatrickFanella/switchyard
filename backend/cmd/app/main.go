package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"git.subcult.tv/PatrickFanella/switchyard/internal/app"
	"git.subcult.tv/PatrickFanella/switchyard/internal/controlplane"
	"git.subcult.tv/PatrickFanella/switchyard/internal/runtimeconfig"
	"git.subcult.tv/PatrickFanella/switchyard/internal/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	webhookReadTimeout    = 9 * time.Second
	webhookWriteTimeout   = 10 * time.Second
	webhookResponseMargin = time.Second
	webhookStartupTimeout = 10 * time.Second
	webhookCleanupMargin  = time.Second
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	if err := run(); err != nil {
		slog.Error("switchyard stopped", "error", controlplane.RedactForLog(err.Error()))
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if _, err := controlplane.ReverseProxyAuthFromEnv(); err != nil {
		return fmt.Errorf("invalid reverse proxy authentication configuration: %w", err)
	}

	tp, shutdown, err := telemetry.NewTracerProviderFromEnv(ctx)
	if err != nil {
		return err
	}
	if tp != nil {
		otel.SetTracerProvider(tp)
		otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
			slog.Error("telemetry export failed", "error", controlplane.RedactForLog(err.Error()))
		}))
		_, span := otel.Tracer("git.subcult.tv/PatrickFanella/switchyard/cmd/app").Start(ctx, "switchyard.startup", trace.WithAttributes(attribute.String("service", "switchyard")))
		span.End()
		slog.Info("telemetry tracing enabled")
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdown(shutdownCtx)
	}()

	addr := os.Getenv("SWITCHYARD_LISTEN_ADDR")
	if addr == "" {
		addr = runtimeconfig.String("SWITCHYARD_LISTEN_ADDR")
	}
	modelAddr := strings.TrimSpace(os.Getenv("LLAMA_LINE_LISTEN_ADDR"))

	store := controlplane.Store(controlplane.NewStore(nil))
	if databaseURL := os.Getenv("DATABASE_URL"); databaseURL != "" {
		dbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		db, err := controlplane.OpenPostgres(dbCtx, databaseURL)
		if err != nil {
			return fmt.Errorf("connect postgres: %w", err)
		}
		defer db.Close()
		if err := controlplane.Migrate(dbCtx, db); err != nil {
			return fmt.Errorf("migrate postgres: %w", err)
		}
		store = controlplane.NewPostgresStore(db, nil)
		slog.Info("persistence configured", "backend", "postgres")
	} else {
		slog.Warn("persistence configured", "backend", "memory", "durable", false)
	}
	if err := controlplane.SeedDefaults(store); err != nil {
		return fmt.Errorf("seed control plane: %w", err)
	}
	deploymentID := strings.TrimSpace(os.Getenv("SWITCHYARD_DEPLOYMENT_ID"))
	const compatibilityHeartbeatInterval = time.Minute
	sourceRevision := strings.TrimSpace(os.Getenv("SWITCHYARD_SOURCE_REVISION"))
	var compatibilityObserver *controlplane.CompatibilityObserver
	if !envEnabledByDefault("SWITCHYARD_COMPATIBILITY_OBSERVER_ENABLED") {
		slog.Info("compatibility observation disabled by configuration")
	} else if sourceRevision == "" || strings.EqualFold(sourceRevision, "unknown") {
		slog.Warn("compatibility observation disabled", "reason", "SWITCHYARD_SOURCE_REVISION must identify the deployed revision")
	} else if deploymentID == "" {
		return fmt.Errorf("SWITCHYARD_DEPLOYMENT_ID is required when compatibility observation is enabled")
	} else {
		identity := controlplane.CompatibilityObservationIdentity{SourceRevision: sourceRevision, DeploymentID: deploymentID}
		var observerLease *controlplane.CompatibilityObserverLease
		if postgres, ok := store.(*controlplane.PostgresStore); ok {
			observerLease, identity, err = postgres.AcquireCompatibilityObserverLease(ctx, identity)
			if err != nil {
				return fmt.Errorf("acquire compatibility observer lease: %w", err)
			}
			defer observerLease.Close()
		}
		compatibilityObserver, err = controlplane.NewCompatibilityObserver(store, identity, compatibilityHeartbeatInterval)
		if err != nil {
			return fmt.Errorf("configure compatibility observation: %w", err)
		}
		if err := compatibilityObserver.Tick(ctx, time.Now().UTC()); err != nil {
			return fmt.Errorf("record compatibility observation startup heartbeat: %w", err)
		}
		go func() {
			if err := compatibilityObserver.Run(ctx, compatibilityHeartbeatInterval); err != nil {
				slog.Error("compatibility observation stopped", "error", controlplane.RedactForLog(err.Error()))
			}
		}()
	}
	for _, warning := range controlplane.NewEnvOperatorSafetyStatus().Warnings {
		slog.Warn("operator safety", "warning", controlplane.RedactForLog(warning))
	}
	modelService := controlplane.NewModelService(store, controlplane.NewEnvModelBroker(), controlplane.ModelServiceOptions{})
	if err := modelService.StartupError(); err != nil {
		return fmt.Errorf("start model service: %w", err)
	}
	runner := controlplane.NewRunner(store, controlplane.NewEnvWorkflowExecutorsWithModel(modelService, store))
	api := controlplane.NewAPIWithModelService(store, runner, modelService)
	api.SetCompatibilityObserver(compatibilityObserver)
	retentionWindow := runtimeconfig.Duration("SWITCHYARD_RETENTION")
	if raw := strings.TrimSpace(os.Getenv("SWITCHYARD_RETENTION")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("parse SWITCHYARD_RETENTION: %w", err)
		}
		if parsed <= 0 {
			return fmt.Errorf("SWITCHYARD_RETENTION must be positive")
		}
		retentionWindow = parsed
	}
	var workersPool *controlplane.WorkerPool
	var workerCount int
	var workerLease, workerPoll time.Duration
	workerEnabled := envEnabledByDefault("SWITCHYARD_WORKER_ENABLED")
	if workerEnabled {
		workers := runtimeconfig.Int("SWITCHYARD_WORKER_COUNT")
		if raw := os.Getenv("SWITCHYARD_WORKER_COUNT"); raw != "" {
			if _, err := fmt.Sscanf(raw, "%d", &workers); err != nil {
				return fmt.Errorf("parse SWITCHYARD_WORKER_COUNT: %w", err)
			}
		}
		lease := runtimeconfig.Duration("SWITCHYARD_WORKER_LEASE")
		if raw := os.Getenv("SWITCHYARD_WORKER_LEASE"); raw != "" {
			parsed, err := time.ParseDuration(raw)
			if err != nil {
				return fmt.Errorf("parse SWITCHYARD_WORKER_LEASE: %w", err)
			}
			lease = parsed
		}
		poll := runtimeconfig.Duration("SWITCHYARD_WORKER_POLL_INTERVAL")
		if raw := os.Getenv("SWITCHYARD_WORKER_POLL_INTERVAL"); raw != "" {
			parsed, err := time.ParseDuration(raw)
			if err != nil {
				return fmt.Errorf("parse SWITCHYARD_WORKER_POLL_INTERVAL: %w", err)
			}
			poll = parsed
		}
		workersPool = controlplane.NewWorkerPool(store, runner, workers, lease, poll, nil)
		if err := workerAdmissionError(workersPool); err != nil {
			return fmt.Errorf("configure worker admission: %w", err)
		}
		workerCount, workerLease, workerPoll = workers, lease, poll
		api.SetWorkerPool(workersPool)
		api.SetAsyncDispatch(true)
	}
	var scheduler *controlplane.Scheduler
	var schedulerInterval time.Duration
	var schedulerLocation *time.Location
	if envEnabledByDefault("SWITCHYARD_SCHEDULER_ENABLED") {
		interval := runtimeconfig.Duration("SWITCHYARD_SCHEDULER_INTERVAL")
		if raw := os.Getenv("SWITCHYARD_SCHEDULER_INTERVAL"); raw != "" {
			parsed, err := time.ParseDuration(raw)
			if err != nil {
				return fmt.Errorf("parse SWITCHYARD_SCHEDULER_INTERVAL: %w", err)
			}
			interval = parsed
		}
		scheduler = controlplane.NewScheduler(store, runner, interval, nil)
		scheduler.SetOpenPilotAssigner(controlplane.NewEnvOpenPilotAssigner(store, runner, controlplane.NewEnvGiteaClient()))
		scheduler.SetOpenPilotOverseer(api.OpenPilotOverseer())
		timezone := strings.TrimSpace(os.Getenv("SWITCHYARD_SCHEDULER_TIMEZONE"))
		if timezone == "" {
			timezone = runtimeconfig.String("SWITCHYARD_SCHEDULER_TIMEZONE")
		}
		location, err := time.LoadLocation(timezone)
		if err != nil {
			return fmt.Errorf("load SWITCHYARD_SCHEDULER_TIMEZONE: %w", err)
		}
		scheduler.SetLocation(location)
		schedulerInterval, schedulerLocation = interval, location
		if workerEnabled {
			scheduler.SetAsyncDispatch(true)
		}
		api.SetScheduler(scheduler)
	}
	retentionInterval := runtimeconfig.Duration("SWITCHYARD_RETENTION_INTERVAL")
	if raw := strings.TrimSpace(os.Getenv("SWITCHYARD_RETENTION_INTERVAL")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("parse SWITCHYARD_RETENTION_INTERVAL: %w", err)
		}
		if parsed <= 0 {
			return fmt.Errorf("SWITCHYARD_RETENTION_INTERVAL must be positive")
		}
		retentionInterval = parsed
	}
	retention := controlplane.NewRetentionManager(store, retentionWindow, retentionInterval, nil)
	api.SetRetentionManager(retention)
	shutdownGrace := 30 * time.Second
	if raw := strings.TrimSpace(os.Getenv("SWITCHYARD_SHUTDOWN_GRACE")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("parse SWITCHYARD_SHUTDOWN_GRACE: %w", err)
		}
		if parsed <= 0 {
			return fmt.Errorf("SWITCHYARD_SHUTDOWN_GRACE must be positive")
		}
		shutdownGrace = parsed
	}

	var webhookDispatcher *controlplane.WebhookDispatcher
	webhookAddr := strings.TrimSpace(os.Getenv("SWITCHYARD_WEBHOOK_LISTEN_ADDR"))
	if webhookAddr == "" {
		webhookAddr = runtimeconfig.String("SWITCHYARD_WEBHOOK_LISTEN_ADDR")
	}
	if webhookAddr != "" {
		if listenerAddressesOverlap(webhookAddr, addr) || (modelAddr != "" && listenerAddressesOverlap(webhookAddr, modelAddr)) {
			return fmt.Errorf("webhook listener must not share the control or model address")
		}
		webhookTimeout, err := webhookDurationEnv("SWITCHYARD_WEBHOOK_TIMEOUT", webhookWriteTimeout-webhookResponseMargin)
		if err != nil {
			return err
		}
		if err := validateWebhookAdmissionTimeout(webhookTimeout); err != nil {
			return err
		}
		webhookConcurrency, err := webhookIntEnv("SWITCHYARD_WEBHOOK_MAX_CONCURRENCY", 1, 1024)
		if err != nil {
			return err
		}
		webhookRate, err := webhookIntEnv("SWITCHYARD_WEBHOOK_RATE_LIMIT_PER_SECOND", 1, 10000)
		if err != nil {
			return err
		}
		webhookDispatcher, err = controlplane.PrepareWebhookDispatcher(
			strings.TrimSpace(os.Getenv("SWITCHYARD_WEBHOOK_CONFIG_FILE")),
			strings.TrimSpace(os.Getenv("SWITCHYARD_WEBHOOK_SECRET_DIR")),
			store,
			controlplane.WebhookDispatcherOptions{
				Timeout:        webhookTimeout,
				MaxConcurrency: webhookConcurrency,
				RatePerSecond:  webhookRate,
				RunRetention:   retentionWindow,
				Owner:          deploymentID,
			},
		)
		if err != nil {
			return fmt.Errorf("configure webhook ingress: %w", err)
		}
	}

	servers := []*http.Server{{Addr: addr, Handler: api.ControlPlaneHandler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}}
	if modelAddr != "" && modelAddr != addr {
		servers = append(servers, &http.Server{Addr: modelAddr, Handler: api.ModelIngressHandler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute})
	}
	if webhookDispatcher != nil {
		servers = append(servers, &http.Server{Addr: webhookAddr, Handler: webhookDispatcher.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: webhookReadTimeout, WriteTimeout: webhookWriteTimeout, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10})
	}
	var webhookRuntime webhookLifecycle
	var webhookDeactivation webhookDeactivator
	if webhookDispatcher != nil {
		webhookRuntime = webhookDispatcher
		webhookDeactivation = webhookDispatcher
	}
	startupCtx, cancelStartup := context.WithTimeout(ctx, webhookStartupTimeout)
	listeners, err := bindAndActivateServers(startupCtx, servers, webhookRuntime, func(ctx context.Context, network, address string) (net.Listener, error) {
		return (&net.ListenConfig{}).Listen(ctx, network, address)
	})
	cancelStartup()
	if err != nil {
		return err
	}

	if workersPool != nil {
		workersPool.Start(ctx)
		slog.Info("worker pool enabled", "workers", workerCount, "lease", workerLease.String(), "poll_interval", workerPoll.String())
	}
	if scheduler != nil {
		scheduler.Start(ctx)
		slog.Info("scheduler enabled", "interval", schedulerInterval.String(), "timezone", schedulerLocation.String())
	}
	retention.Start(ctx)
	fmt.Println(app.Greeting("Switchyard"))
	for index, server := range servers {
		slog.Info("listener ready", "address", listeners[index].Addr().String(), "configured_address", server.Addr)
	}
	errCh := make(chan error, len(servers))
	for index, server := range servers {
		server := server
		listener := listeners[index]
		go func() {
			errCh <- server.Serve(listener)
		}()
	}
	shutdownServers := func(shutdownCtx context.Context) error {
		var shutdownErr error
		for _, server := range servers {
			if err := server.Shutdown(shutdownCtx); err != nil {
				shutdownErr = errors.Join(shutdownErr, err)
			}
		}
		return shutdownErr
	}
	shutdownRuntime := func(drainCtx context.Context) error {
		var runtimeErr error
		modelService.Stop()
		if scheduler != nil {
			runtimeErr = errors.Join(runtimeErr, scheduler.Wait(drainCtx))
		}
		runtimeErr = errors.Join(runtimeErr, retention.Wait(drainCtx))
		if workersPool != nil {
			if err := workersPool.Wait(drainCtx); err != nil {
				workersPool.CancelActive()
				runtimeErr = errors.Join(runtimeErr, err, workersPool.Wait(drainCtx))
			}
		}
		runtimeErr = errors.Join(runtimeErr, modelService.Wait(drainCtx))
		return runtimeErr
	}
	shutdownAll := func(cause error) error {
		stop()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		return errors.Join(cause, shutdownApplication(shutdownCtx, webhookDeactivation, shutdownServers, shutdownRuntime))
	}

	select {
	case err := <-modelService.Failure():
		return shutdownAll(err)
	case err := <-errCh:
		return shutdownAll(err)
	case <-ctx.Done():
		shutdownErr := shutdownAll(nil)
		for range servers {
			if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
				shutdownErr = errors.Join(shutdownErr, err)
			}
		}
		return shutdownErr
	}
}

func workerAdmissionError(pool *controlplane.WorkerPool) error {
	if pool == nil {
		return nil
	}
	return pool.AdmissionError()
}

func envEnabledByDefault(name string) bool {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		if fallback, ok := runtimeconfig.Lookup(name); ok {
			raw = fallback
		}
	}
	switch strings.ToLower(raw) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func webhookIntEnv(name string, minimum, maximum int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		raw = runtimeconfig.String(name)
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be an integer from %d to %d", name, minimum, maximum)
	}
	return value, nil
}

func webhookDurationEnv(name string, maximum time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		raw = runtimeconfig.String(name)
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 || value > maximum {
		return 0, fmt.Errorf("%s must be a positive duration no greater than %s", name, maximum)
	}
	return value, nil
}

func validateWebhookAdmissionTimeout(value time.Duration) error {
	maximum := webhookWriteTimeout - webhookResponseMargin
	if value <= 0 || value > maximum {
		return fmt.Errorf("SWITCHYARD_WEBHOOK_TIMEOUT must be positive and no greater than %s, reserving %s below webhook write timeout", maximum, webhookResponseMargin)
	}
	return nil
}

type webhookActivator interface {
	Activate(context.Context) error
}

type webhookDeactivator interface {
	Deactivate(context.Context) error
}

type webhookLifecycle interface {
	webhookActivator
	webhookDeactivator
}

type listenFunc func(context.Context, string, string) (net.Listener, error)

func bindAndActivateServers(ctx context.Context, servers []*http.Server, webhook webhookLifecycle, listen listenFunc) ([]net.Listener, error) {
	listeners := make([]net.Listener, 0, len(servers))
	closeListeners := func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}
	for _, server := range servers {
		listener, err := listen(ctx, "tcp", server.Addr)
		if err != nil {
			closeListeners()
			return nil, fmt.Errorf("bind %s: %w", server.Addr, err)
		}
		listeners = append(listeners, listener)
		if err := ctx.Err(); err != nil {
			closeListeners()
			return nil, err
		}
	}
	if webhook != nil {
		activateCtx, cancel := webhookActivationContext(ctx)
		err := webhook.Activate(activateCtx)
		cancel()
		if err != nil {
			closeListeners()
			return nil, errors.Join(err, deactivateWebhook(ctx, webhook))
		}
		if err := ctx.Err(); err != nil {
			closeListeners()
			return nil, errors.Join(err, deactivateWebhook(ctx, webhook))
		}
	}
	return listeners, nil
}

func webhookActivationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return context.WithCancel(ctx)
	}
	remaining := time.Until(deadline)
	reserve := min(webhookCleanupMargin, remaining/2)
	return context.WithDeadline(ctx, deadline.Add(-reserve))
}

func deactivateWebhook(ctx context.Context, webhook webhookDeactivator) error {
	if webhook == nil {
		return nil
	}
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err = webhook.Deactivate(ctx); err == nil {
			return nil
		}
		if attempt < 2 {
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return fmt.Errorf("revoke webhook authentication: %w", errors.Join(err, ctx.Err()))
			}
		}
	}
	return fmt.Errorf("revoke webhook authentication: %w", err)
}

func shutdownApplication(ctx context.Context, webhook webhookDeactivator, shutdownServers, shutdownRuntime func(context.Context) error) error {
	results := make(chan error, 3)
	go func() { results <- deactivateWebhook(ctx, webhook) }()
	go func() { results <- shutdownServers(ctx) }()
	go func() { results <- shutdownRuntime(ctx) }()
	var shutdownErr error
	for range 3 {
		select {
		case err := <-results:
			shutdownErr = errors.Join(shutdownErr, err)
		case <-ctx.Done():
			return errors.Join(shutdownErr, ctx.Err())
		}
	}
	return shutdownErr
}

func listenerAddressesOverlap(first, second string) bool {
	firstHost, firstPort, firstErr := net.SplitHostPort(first)
	secondHost, secondPort, secondErr := net.SplitHostPort(second)
	if firstErr != nil || secondErr != nil || firstPort != secondPort {
		return first == second
	}
	if firstHost == secondHost {
		return true
	}
	isWildcard := func(host string) bool { return host == "" || host == "0.0.0.0" || host == "::" }
	return isWildcard(firstHost) || isWildcard(secondHost)
}
