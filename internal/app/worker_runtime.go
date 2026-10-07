package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/google/uuid"

	"workflow-optimizer/internal/config"
	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/infrastructure/encryption"
	"workflow-optimizer/internal/infrastructure/postgres"
	redisinfra "workflow-optimizer/internal/infrastructure/redis"
	"workflow-optimizer/internal/observability"
	"workflow-optimizer/internal/queue"
	"workflow-optimizer/internal/reliability"
	"workflow-optimizer/internal/retry"
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

	// Phase 10.
	policy    retry.Policy
	rel       *postgres.ReliabilityRepository
	scheduler *reliability.Scheduler
	reaper    *reliability.Reaper
	owner     string

	// Phase 14: execution events, structured execution logs and metrics.
	recorder *observability.Recorder
	metrics  *observability.Metrics
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
	return newWorkerRuntime(ctx, cfg, application, store, logger)
}

// NewWorker is the production worker composition (Phase 11): it opens
// PostgreSQL, builds the credential chain
//
//	AES-256-GCM (CREDENTIAL_ENCRYPTION_KEY) -> CredentialRepository -> credential.Service
//
// bootstraps the application with it (provider registry -> OpenAI provider
// -> LLM node -> node registry), and starts the worker runtime on the same
// pool.
func NewWorker(ctx context.Context, cfg config.Config, logger *slog.Logger) (*WorkerRuntime, error) {
	return NewWorkerWith(ctx, cfg, logger, Extensions{})
}

// NewWorkerWith is NewWorker with optional extensions (OAuth providers,
// integrations).
func NewWorkerWith(ctx context.Context, cfg config.Config, logger *slog.Logger, ext Extensions) (*WorkerRuntime, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if err := cfg.ValidateWorker(); err != nil {
		return nil, err
	}
	enc, err := credentialEncryptor(cfg)
	if err != nil {
		return nil, err
	}
	if enc == nil {
		logger.Warn("CREDENTIAL_ENCRYPTION_KEY is not set: credentials cannot be resolved; provider nodes will fail with CREDENTIAL_DECRYPTION_FAILED")
	}
	store, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("app: open PostgreSQL: %w", err)
	}
	chain, err := newCredentialChain(cfg, store, enc, ext, logger)
	if err != nil {
		store.Close()
		return nil, err
	}
	application, err := BootstrapWith(cfg, chain.dependencies(ext))
	if err != nil {
		store.Close()
		return nil, err
	}
	return newWorkerRuntime(ctx, cfg, application, store, logger)
}

// credentialEncryptor builds the AES-256-GCM encryptor from
// CREDENTIAL_ENCRYPTION_KEY (nil, nil when the key is not configured).
func credentialEncryptor(cfg config.Config) (credential.SecretEncryptor, error) {
	if cfg.CredentialEncryptionKey == "" {
		return nil, nil
	}
	key, err := encryption.ParseKey(cfg.CredentialEncryptionKey)
	if err != nil {
		return nil, fmt.Errorf("app: %w", err)
	}
	defer clear(key)
	enc, err := encryption.NewAESGCM(key)
	if err != nil {
		return nil, fmt.Errorf("app: %w", err)
	}
	return enc, nil
}

func newWorkerRuntime(ctx context.Context, cfg config.Config, application *Application, store *postgres.Store, logger *slog.Logger) (*WorkerRuntime, error) {
	if logger == nil {
		logger = slog.Default()
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
	rel := rt.cfg.Reliability.OrDefaults()
	rt.policy = retry.Policy{
		MaxAttempts:       rel.MaxAttempts,
		InitialDelay:      rel.InitialRetryDelay,
		MaxDelay:          rel.MaxRetryDelay,
		BackoffMultiplier: rel.BackoffMultiplier,
		Jitter:            retry.DefaultJitter,
	}
	if err := rt.policy.Validate(); err != nil {
		return err
	}
	rt.rel = postgres.NewReliabilityRepository(rt.store)
	heartbeat, err := reliability.NewHeartbeat(rt.rel, rel.WorkerLeaseDuration, rel.WorkerHeartbeatInterval, rt.logger)
	if err != nil {
		return err
	}
	rt.owner = workerOwner()
	rt.metrics = observability.NewMetrics()
	rt.recorder = observability.NewRecorder(postgres.NewExecutionEventRepository(rt.store), rt.logger, rt.metrics)
	executions := postgres.NewExecutionRepository(rt.store)
	runner, err := execution.NewRunner(execution.RunnerConfig{
		Executions:     executions,
		NodeExecutions: postgres.NewNodeExecutionRepository(rt.store),
		Definitions:    postgres.NewWorkflowVersionRepository(rt.store),
		Validator:      workflow.NewValidator(application.NodeRegistry),
		Graph:          execution.NewGraphExecutor(application.NodeRegistry),
		Backoff:        rt.policy,
		NodeRetry: execution.NodeRetry{
			MaxInvocations: rel.NodeMaxAttempts,
			Backoff:        rt.policy,
			MaxInlineDelay: execution.DefaultMaxInlineNodeDelay,
		},
		NodeTimeout: rel.NodeTimeout,
		Leases:      heartbeat,
		Lease:       &execution.LeaseGrant{Owner: rt.owner, Duration: rel.WorkerLeaseDuration},
		// Each run is scoped to its workflow's workspace (credential isolation).
		Workspaces: postgres.NewWorkflowVersionRepository(rt.store),
		// Events are recorded after each durable change; a recording failure
		// is logged and never affects the run.
		Observer: rt.recorder,
	})
	if err != nil {
		return err
	}
	processor, err := worker.NewExecutionProcessor(executions, runner)
	if err != nil {
		return err
	}
	deadLetters, err := redisinfra.NewList(rt.redis, rel.DeadLetterQueue)
	if err != nil {
		return err
	}
	notify := reliability.DeadLetterNotifier(deadLetters, rt.logger)
	pool, err := worker.NewPool(rt.cfg.WorkerCount, q, processor, worker.Options{
		Logger: rt.logger,
		OnDeadLetter: func(ctx context.Context, res worker.Result) {
			notify(ctx, reliability.DeadLetterNotice{ExecutionID: res.ExecutionID, Attempt: res.DeadLetterAttempt,
				Reason: res.DeadLetter, Code: execution.ErrorFromExecution(res.Err).Code})
		},
	})
	if err != nil {
		return err
	}
	// A dispatched job still unclaimed after one lease duration is assumed
	// lost and dispatched again (duplicates are harmless).
	rt.scheduler, err = reliability.NewScheduler(rt.rel, q, rel.SchedulerInterval, rel.WorkerLeaseDuration, rt.logger)
	if err != nil {
		return err
	}
	rt.reaper, err = reliability.NewReaper(rt.rel, rt.policy, rel.ReaperInterval, rt.logger)
	if err != nil {
		return err
	}
	rt.reaper.Notify = notify
	rt.reaper.Observer = rt.recorder
	rt.queue, rt.pool = q, pool
	return nil
}

// workerOwner identifies this worker process in leases.
func workerOwner() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown-host"
	}
	return fmt.Sprintf("%s/%d/%s", host, os.Getpid(), uuid.NewString()[:8])
}

// Run starts the workers, blocks until ctx is cancelled, shuts down within
// WorkerShutdownTimeout, and closes Redis and PostgreSQL.
func (rt *WorkerRuntime) Run(ctx context.Context) error {
	defer rt.Close()
	rt.logger.Info("worker runtime starting",
		"workers", rt.cfg.WorkerCount, "queue", rt.cfg.RedisQueueName, "owner", rt.owner)
	// The retry scheduler and the reaper run next to the workers. Every
	// worker process runs them; concurrent instances are safe (atomic claims
	// and fenced recovery in PostgreSQL).
	var background sync.WaitGroup
	background.Add(2)
	go func() { defer background.Done(); rt.scheduler.Run(ctx) }()
	go func() { defer background.Done(); rt.reaper.Run(ctx) }()
	err := rt.pool.Run(ctx, rt.cfg.WorkerShutdownTimeout)
	background.Wait()
	rt.logger.Info("worker metrics", rt.metrics.Snapshot().LogAttrs()...)
	rt.logger.Info("worker runtime stopped", "error", err)
	return err
}

// Scheduler and Reaper expose the background components (for tests and
// operational tooling that drives them directly).
func (rt *WorkerRuntime) Scheduler() *reliability.Scheduler { return rt.scheduler }
func (rt *WorkerRuntime) Reaper() *reliability.Reaper       { return rt.reaper }

// Metrics exposes the worker's execution metrics (Phase 14).
func (rt *WorkerRuntime) Metrics() *observability.Metrics { return rt.metrics }

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
	rel := rt.cfg.Reliability.OrDefaults()
	service := execution.NewLifecycleService(postgres.NewExecutionRepository(rt.store)).
		WithDefaults(execution.ExecutionDefaults{MaxAttempts: rel.MaxAttempts, Timeout: rel.ExecutionTimeout}).
		WithCancellations(rt.rel).
		WithObserver(rt.recorder)
	return queue.NewSubmitter(service, dispatcher)
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
