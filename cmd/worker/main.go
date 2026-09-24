// Command worker is the long-running execution entrypoint. It will consume
// queued work and invoke the executor in a later phase.
package main

import (
	"log"

	"workflow-optimizer/internal/app"
	"workflow-optimizer/internal/config"
)

func main() {
	cfg := config.Load()
	if _, err := app.Bootstrap(cfg); err != nil {
		log.Fatalf("failed to bootstrap worker application: %v", err)
	}
}
