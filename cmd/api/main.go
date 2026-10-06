// Command api is the REST API process (Phase 12). It serves /api/v1 on
// API_ADDR until interrupted (SIGINT/SIGTERM). It never executes workflows:
// it persists executions and enqueues them for cmd/worker. All behaviour
// lives in internal/app, internal/application and internal/api.
package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"workflow-optimizer/internal/app"
	"workflow-optimizer/internal/config"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("api configuration: %v", err)
	}
	runtime, err := app.NewAPI(ctx, cfg, slog.Default())
	if err != nil {
		log.Fatalf("failed to start api: %v", err)
	}
	if err := runtime.Run(ctx); err != nil {
		log.Fatalf("api server: %v", err)
	}
}
