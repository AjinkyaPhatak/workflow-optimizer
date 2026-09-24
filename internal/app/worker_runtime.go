package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"workflow-optimizer/internal/config"
	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/infrastructure/postgres"
	redisinfra "workflow-optimizer/internal/infrastructure/redis"
	"workflow-optimizer/internal/queue"
	"workflow-optimizer/internal/worker"
	"workflow-optimizer/internal/workflow"
)

// WorkerRuntime is a worker process's composition: one PostgreSQL pool, one
// Redis client, the Redis job queue, and a pool of workers that claim and run
// executions through the Phase 8 Runner and the Phase 7 GraphExecutor.
type WorkerRuntime struct {
	cfg    config.Config
	store  *postgres.Store
	redis  *redisinfra.Client
	queue  *redisinfra.Queue
	pool   *worker.Pool
	logger *slog.Logger
}

// NewWorkerRuntime validates cfg, connects to PostgreSQL and Redis, and builds
// the worker pool from the application's canonical node registry. Every
// resource opened here is closed by Close (or by Run on return).
func NewWorkerRuntime(ctx context.Context, cfg config.Config, application *Application, logger *slog.Logger) (*WorkerRuntime, error) {
	if application == nil || application.NodeRegistry == nil {
		return nil, errors.New("app: worker runtime requires a bootstrapped application")
	}
	if err := cfg.ValidateWorker(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	store, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("app: open PostgreSQL: %w", err)
	}
	redisClient, err := redisinfra.Open(ctx, cfg.RedisURL)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("app: open Redis: %w", err)
	}
	rt := &WorkerRuntime{cfg: cfg, store: store, redis: redisClient, logger: logger}
	if err := rt.build(application); err != nil {
		rt.Close()
		return nil, err
	}
	return rt, nil
}

func (rt *WorkerRuntime) build(application *Application) error {
	q, err := redisinfra.NewQueue(rt.redis, rt.cfg.RedisQueueName, 0)
	if err != nil {
		return err
	}
	executions := postgres.NewExecutionRepository(rt.store)
	runner, err := execution.NewRunner(execution.RunnerConfig{
		Executions:     executions,
		NodeExecutions: postgres.NewNodeExecutionRepository(rt.store),
		Definitions:    postgres.NewWorkflowVersionRepository(rt.store),
		Validator:      workflow.NewValidator(application.NodeRegistry),
		Graph:          execution.NewGraphExecutor(application.NodeRegistry),
	})
	if err != nil {
		return err
	}
	processor, err := worker.NewExecutionProcessor(executions, runner)
	if err != nil {
		return err
	}
	pool, err := worker.NewPool(rt.cfg.WorkerCount, q, processor, worker.Options{Logger: rt.logger})
	if err != nil {
		return err
	}
	rt.queue, rt.pool = q, pool
	return nil
}

// Run starts the workers, blocks until ctx is cancelled, shuts down within
// WorkerShutdownTimeout, and closes Redis and PostgreSQL.
func (rt *WorkerRuntime) Run(ctx context.Context) error {
	defer rt.Close()
	rt.logger.Info("worker runtime starting",
		"workers", rt.cfg.WorkerCount, "queue", rt.cfg.RedisQueueName)
	err := rt.pool.Run(ctx, rt.cfg.WorkerShutdownTimeout)
	rt.logger.Info("worker runtime stopped", "error", err)
	return err
}

// Health reports whether Redis is reachable and how many workers run.
func (rt *WorkerRuntime) Health(ctx context.Context) worker.Health {
	return worker.CheckHealth(ctx, rt.redis, rt.pool)
}

// Submitter returns the request-path entry point (create PENDING, then
// enqueue) bound to this runtime's PostgreSQL and Redis resources.
func (rt *WorkerRuntime) Submitter() (*queue.Submitter, error) {
	dispatcher, err := queue.NewDispatcher(rt.queue)
	if err != nil {
		return nil, err
	}
	return queue.NewSubmitter(execution.NewLifecycleService(postgres.NewExecutionRepository(rt.store)), dispatcher)
}

// Close releases Redis and PostgreSQL. It is safe to call more than once.
func (rt *WorkerRuntime) Close() {
	if rt.redis != nil {
		if err := rt.redis.Close(); err != nil {
			rt.logger.Error("closing Redis", "error", err)
		}
	}
	if rt.store != nil {
		rt.store.Close()
	}
}
