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
		log.Fatal(err)
	}
	if err := app.RunOrchestrator(ctx, cfg, slog.Default()); err != nil {
		log.Fatal(err)
	}
}
