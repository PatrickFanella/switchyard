package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"git.subcult.tv/PatrickFanella/switchyard/internal/app"
	"git.subcult.tv/PatrickFanella/switchyard/internal/controlplane"
)

func main() {
	addr := os.Getenv("SWITCHYARD_LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	store := controlplane.NewStore(nil)
	if err := controlplane.SeedDefaults(store); err != nil {
		log.Fatalf("seed control plane: %v", err)
	}
	runner := controlplane.NewRunner(store)
	api := controlplane.NewAPI(store, runner)

	fmt.Println(app.Greeting("Switchyard"))
	log.Printf("switchyard control plane listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, api.Handler()))
}
