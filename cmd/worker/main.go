// Command worker is the long-running background execution process. It
// consumes execution jobs from the Redis queue and runs them with a pool of
// WORKER_COUNT workers until interrupted (SIGINT/SIGTERM), then shuts down
// gracefully. All behaviour lives in internal/app and internal/worker.
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
		log.Fatalf("worker configuration: %v", err)
	}
	runtime, err := app.NewWorker(ctx, cfg, slog.Default())
	if err != nil {
		log.Fatalf("failed to start worker runtime: %v", err)
	}
	if err := runtime.Run(ctx); err != nil {
		log.Fatalf("worker runtime: %v", err)
	}
}
