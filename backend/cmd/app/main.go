package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"git.subcult.tv/PatrickFanella/switchyard/internal/app"
	"git.subcult.tv/PatrickFanella/switchyard/internal/controlplane"
)

func main() {
	addr := os.Getenv("SWITCHYARD_LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	store := controlplane.Store(controlplane.NewStore(nil))
	if databaseURL := os.Getenv("DATABASE_URL"); databaseURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		db, err := controlplane.OpenPostgres(ctx, databaseURL)
		if err != nil {
			log.Fatalf("connect postgres: %v", err)
		}
		defer db.Close()
		if err := controlplane.Migrate(ctx, db); err != nil {
			log.Fatalf("migrate postgres: %v", err)
		}
		store = controlplane.NewPostgresStore(db, nil)
		log.Print("switchyard control plane using postgres storage")
	} else {
		log.Print("switchyard control plane using in-memory storage")
	}
	if err := controlplane.SeedDefaults(store); err != nil {
		log.Fatalf("seed control plane: %v", err)
	}
	runner := controlplane.NewRunner(store)
	api := controlplane.NewAPI(store, runner)
	if os.Getenv("SWITCHYARD_SCHEDULER_ENABLED") == "1" || os.Getenv("SWITCHYARD_SCHEDULER_ENABLED") == "true" {
		interval := time.Minute
		if raw := os.Getenv("SWITCHYARD_SCHEDULER_INTERVAL"); raw != "" {
			parsed, err := time.ParseDuration(raw)
			if err != nil {
				log.Fatalf("parse SWITCHYARD_SCHEDULER_INTERVAL: %v", err)
			}
			interval = parsed
		}
		scheduler := controlplane.NewScheduler(store, runner, interval, nil)
		api.SetScheduler(scheduler)
		scheduler.Start(context.Background())
		log.Printf("switchyard scheduler enabled interval=%s", interval)
	}

	fmt.Println(app.Greeting("Switchyard"))
	log.Printf("switchyard control plane listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, api.Handler()))
}
