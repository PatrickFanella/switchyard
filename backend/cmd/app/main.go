package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"git.subcult.tv/PatrickFanella/switchyard/internal/app"
	"git.subcult.tv/PatrickFanella/switchyard/internal/controlplane"
	"git.subcult.tv/PatrickFanella/switchyard/internal/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tp, shutdown, err := telemetry.NewTracerProviderFromEnv(ctx)
	if err != nil {
		return err
	}
	if tp != nil {
		otel.SetTracerProvider(tp)
		otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
			log.Printf("switchyard telemetry export error: %v", err)
		}))
		_, span := otel.Tracer("git.subcult.tv/PatrickFanella/switchyard/cmd/app").Start(ctx, "switchyard.startup", trace.WithAttributes(attribute.String("service", "switchyard")))
		span.End()
		log.Print("switchyard telemetry tracing enabled")
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdown(shutdownCtx)
	}()

	addr := os.Getenv("SWITCHYARD_LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}

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
		log.Print("switchyard control plane using postgres storage")
	} else {
		log.Print("switchyard control plane using in-memory storage")
	}
	if err := controlplane.SeedDefaults(store); err != nil {
		return fmt.Errorf("seed control plane: %w", err)
	}
	for _, warning := range controlplane.NewEnvOperatorSafetyStatus().Warnings {
		log.Printf("switchyard operator safety warning: %s", warning)
	}
	runner := controlplane.NewRunner(store)
	api := controlplane.NewAPI(store, runner)
	var workersPool *controlplane.WorkerPool
	workerEnabled := envEnabledByDefault("SWITCHYARD_WORKER_ENABLED")
	if workerEnabled {
		workers := 1
		if raw := os.Getenv("SWITCHYARD_WORKER_COUNT"); raw != "" {
			if _, err := fmt.Sscanf(raw, "%d", &workers); err != nil {
				return fmt.Errorf("parse SWITCHYARD_WORKER_COUNT: %w", err)
			}
		}
		lease := 5 * time.Minute
		if raw := os.Getenv("SWITCHYARD_WORKER_LEASE"); raw != "" {
			parsed, err := time.ParseDuration(raw)
			if err != nil {
				return fmt.Errorf("parse SWITCHYARD_WORKER_LEASE: %w", err)
			}
			lease = parsed
		}
		poll := time.Second
		if raw := os.Getenv("SWITCHYARD_WORKER_POLL_INTERVAL"); raw != "" {
			parsed, err := time.ParseDuration(raw)
			if err != nil {
				return fmt.Errorf("parse SWITCHYARD_WORKER_POLL_INTERVAL: %w", err)
			}
			poll = parsed
		}
		workersPool = controlplane.NewWorkerPool(store, runner, workers, lease, poll, nil)
		api.SetWorkerPool(workersPool)
		api.SetAsyncDispatch(true)
		workersPool.Start(ctx)
		log.Printf("switchyard worker pool enabled workers=%d lease=%s poll=%s", workers, lease, poll)
	}
	var scheduler *controlplane.Scheduler
	if envEnabledByDefault("SWITCHYARD_SCHEDULER_ENABLED") {
		interval := time.Minute
		if raw := os.Getenv("SWITCHYARD_SCHEDULER_INTERVAL"); raw != "" {
			parsed, err := time.ParseDuration(raw)
			if err != nil {
				return fmt.Errorf("parse SWITCHYARD_SCHEDULER_INTERVAL: %w", err)
			}
			interval = parsed
		}
		scheduler = controlplane.NewScheduler(store, runner, interval, nil)
		if workerEnabled {
			scheduler.SetAsyncDispatch(true)
		}
		api.SetScheduler(scheduler)
		scheduler.Start(ctx)
		log.Printf("switchyard scheduler enabled interval=%s", interval)
	}

	fmt.Println(app.Greeting("Switchyard"))
	log.Printf("switchyard control plane listening on %s", addr)
	servers := []*http.Server{{Addr: addr, Handler: api.ControlPlaneHandler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}}
	if modelAddr := strings.TrimSpace(os.Getenv("LLAMA_LINE_LISTEN_ADDR")); modelAddr != "" && modelAddr != addr {
		servers = append(servers, &http.Server{Addr: modelAddr, Handler: api.ModelIngressHandler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute})
		log.Printf("switchyard model ingress listening on %s", modelAddr)
	}
	errCh := make(chan error, len(servers))
	for _, server := range servers {
		server := server
		go func() {
			errCh <- server.ListenAndServe()
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
	shutdownRuntime := func() error {
		grace := 30 * time.Second
		if raw := strings.TrimSpace(os.Getenv("SWITCHYARD_SHUTDOWN_GRACE")); raw != "" {
			parsed, err := time.ParseDuration(raw)
			if err != nil {
				return fmt.Errorf("parse SWITCHYARD_SHUTDOWN_GRACE: %w", err)
			}
			if parsed <= 0 {
				return fmt.Errorf("SWITCHYARD_SHUTDOWN_GRACE must be positive")
			}
			grace = parsed
		}
		drainCtx, cancel := context.WithTimeout(context.Background(), grace)
		defer cancel()
		var runtimeErr error
		if scheduler != nil {
			runtimeErr = errors.Join(runtimeErr, scheduler.Wait(drainCtx))
		}
		if workersPool != nil {
			if err := workersPool.Wait(drainCtx); err != nil {
				workersPool.CancelActive()
				forceCtx, forceCancel := context.WithTimeout(context.Background(), 5*time.Second)
				runtimeErr = errors.Join(runtimeErr, err, workersPool.Wait(forceCtx))
				forceCancel()
			}
		}
		runtimeErr = errors.Join(runtimeErr, api.Shutdown(drainCtx))
		return runtimeErr
	}

	select {
	case err := <-errCh:
		stop()
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return shutdownRuntime()
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = shutdownServers(shutdownCtx)
		return errors.Join(err, shutdownRuntime())
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shutdownErr := shutdownServers(shutdownCtx)
		for range servers {
			if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
				shutdownErr = errors.Join(shutdownErr, err)
			}
		}
		return errors.Join(shutdownErr, shutdownRuntime())
	}
}

func envEnabledByDefault(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}
