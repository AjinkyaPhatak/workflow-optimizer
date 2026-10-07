package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"workflow-optimizer/internal/api"
	"workflow-optimizer/internal/api/handlers"
	"workflow-optimizer/internal/application"
	"workflow-optimizer/internal/auth"
	"workflow-optimizer/internal/config"
	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/infrastructure/postgres"
	redisinfra "workflow-optimizer/internal/infrastructure/redis"
	"workflow-optimizer/internal/observability"
	"workflow-optimizer/internal/queue"
	"workflow-optimizer/internal/workflow"
)

// APIShutdownTimeout bounds graceful shutdown of the HTTP server.
const APIShutdownTimeout = 15 * time.Second

// APIRuntime is an API process's composition (Phase 12): one PostgreSQL pool,
// one Redis client, the application services over the existing engine
// components, and the HTTP router. It never runs workflows: executions are
// persisted PENDING and enqueued for the worker processes.
type APIRuntime struct {
	cfg     config.Config
	store   *postgres.Store
	redis   *redisinfra.Client
	handler http.Handler
	logger  *slog.Logger
}

// NewAPI validates cfg, connects to PostgreSQL and Redis, and wires:
//
//	HTTP -> handlers -> application services -> repositories / queue.Submitter
//	                                         -> graph validator (node registry)
//	                                         -> credential.Service (AES-256-GCM)
func NewAPI(ctx context.Context, cfg config.Config, logger *slog.Logger) (*APIRuntime, error) {
	if err := cfg.ValidateAPI(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	enc, err := credentialEncryptor(cfg)
	if err != nil {
		return nil, err
	}
	if enc == nil {
		logger.Warn("CREDENTIAL_ENCRYPTION_KEY is not set: credentials cannot be created (503)")
	}
	tokens, err := auth.NewHMACTokenService([]byte(cfg.AuthTokenSecret), cfg.AuthTokenTTL, nil)
	if err != nil {
		return nil, fmt.Errorf("app: %w", err)
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
	rt := &APIRuntime{cfg: cfg, store: store, redis: redisClient, logger: logger}
	if err := rt.build(enc, tokens); err != nil {
		rt.Close()
		return nil, err
	}
	return rt, nil
}

func (rt *APIRuntime) build(enc credential.SecretEncryptor, tokens auth.TokenService) error {
	pricing, err := observability.ParsePricing(rt.cfg.ModelPricing)
	if err != nil {
		return fmt.Errorf("app: %w", err)
	}
	// The API applies cancellations of RUNNING executions itself (Phase 10
	// RequestCancel); it reports them as events like the workers do.
	recorder := observability.NewRecorder(postgres.NewExecutionEventRepository(rt.store), rt.logger, observability.NewMetrics())
	credentialRepo := postgres.NewCredentialRepository(rt.store)
	credentials, err := credential.NewService(credentialRepo, enc)
	if err != nil {
		return err
	}
	// The same bootstrap as the worker: the API validates against exactly the
	// node registry the workers execute with.
	engine, err := BootstrapWith(rt.cfg, Dependencies{Credentials: credentials})
	if err != nil {
		return err
	}
	q, err := redisinfra.NewQueue(rt.redis, rt.cfg.RedisQueueName, 0)
	if err != nil {
		return err
	}
	dispatcher, err := queue.NewDispatcher(q)
	if err != nil {
		return err
	}
	rel := rt.cfg.Reliability.OrDefaults()
	lifecycle := execution.NewLifecycleService(postgres.NewExecutionRepository(rt.store)).
		WithDefaults(execution.ExecutionDefaults{MaxAttempts: rel.MaxAttempts, Timeout: rel.ExecutionTimeout}).
		WithCancellations(postgres.NewReliabilityRepository(rt.store)).
		WithObserver(recorder)
	submitter, err := queue.NewSubmitter(lifecycle, dispatcher)
	if err != nil {
		return err
	}

	identities := postgres.NewIdentityRepository(rt.store)
	access := application.NewAccess(identities)
	workflows := application.NewWorkflowService(access, postgres.NewWorkflowRepository(rt.store),
		workflow.NewValidator(engine.NodeRegistry))
	executions := application.NewExecutionService(application.ExecutionDeps{
		Workflows:  workflows,
		Submitter:  submitter,
		Executions: postgres.NewExecutionRepository(rt.store),
		Nodes:      postgres.NewNodeExecutionRepository(rt.store),
		Canceller:  lifecycle,
		Workspaces: postgres.NewWorkflowVersionRepository(rt.store),
		Logger:     rt.logger,
	})
	policy := application.ObservabilityPolicy{ExposeNodeInputs: rt.cfg.ExposeNodeData, ExposeNodeOutputs: rt.cfg.ExposeNodeData}
	h := &handlers.Handlers{
		Auth:       application.NewAuthService(identities, tokens),
		Workflows:  workflows,
		Executions: executions,
		Observability: application.NewObservabilityService(executions, postgres.NewExecutionEventRepository(rt.store),
			postgres.NewExecutionListRepository(rt.store), pricing, policy),
		Credentials: application.NewCredentialService(access, credentials, credentialRepo, engine.ProviderRegistry, enc != nil),
		Nodes:       engine.NodeRegistry,
		Ready: []handlers.Check{
			{Name: "postgres", Check: rt.store.Pool.Ping},
			{Name: "redis", Check: rt.redis.Ping},
		},
		Logger: rt.logger,
	}
	rt.handler = api.NewRouter(h, tokens, rt.logger)
	return nil
}

// Handler is the API's HTTP handler (for tests and embedding).
func (rt *APIRuntime) Handler() http.Handler { return rt.handler }

// Run serves HTTP on API_ADDR until ctx is cancelled, then shuts down
// gracefully and closes PostgreSQL and Redis.
func (rt *APIRuntime) Run(ctx context.Context) error {
	defer rt.Close()
	ln, err := net.Listen("tcp", rt.cfg.APIAddr)
	if err != nil {
		return fmt.Errorf("app: listen on %s: %w", rt.cfg.APIAddr, err)
	}
	return rt.Serve(ctx, ln)
}

// Serve serves HTTP on ln until ctx is cancelled.
func (rt *APIRuntime) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           rt.handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
		BaseContext:       func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}
	errs := make(chan error, 1)
	go func() { errs <- srv.Serve(ln) }()
	rt.logger.Info("api server listening", "addr", ln.Addr().String())
	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), APIShutdownTimeout)
	defer cancel()
	err := srv.Shutdown(shutdown)
	if serveErr := <-errs; serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) && err == nil {
		err = serveErr
	}
	rt.logger.Info("api server stopped", "error", err)
	return err
}

// Close releases Redis and PostgreSQL. It is safe to call more than once.
func (rt *APIRuntime) Close() {
	if rt.redis != nil {
		if err := rt.redis.Close(); err != nil {
			rt.logger.Error("closing Redis", "error", err)
		}
	}
	if rt.store != nil {
		rt.store.Close()
	}
}
