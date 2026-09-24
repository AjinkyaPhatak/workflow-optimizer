// Command api is the transport entrypoint. It will submit execution requests to
// a queue in a later phase; it does not execute workflows itself.
package main

import (
	"log"

	"workflow-optimizer/internal/app"
	"workflow-optimizer/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("api configuration: %v", err)
	}
	if _, err := app.Bootstrap(cfg); err != nil {
		log.Fatalf("failed to bootstrap api application: %v", err)
	}
}
