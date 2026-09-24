package phase9_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"

	"workflow-optimizer/internal/app"
	"workflow-optimizer/internal/config"
	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/infrastructure/postgres"
	redisinfra "workflow-optimizer/internal/infrastructure/redis"
	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/worker"
	"workflow-optimizer/internal/workflow"
)

// Phase 9 end-to-end tests run against real PostgreSQL (TEST_DATABASE_URL)
// and real Redis (TEST_REDIS_URL). Each test gets its own PostgreSQL schema
// and its own Redis key under test:phase9:, both removed on cleanup.

// counter records how often the test.count node ran for each execution
// (keyed by the execution input's "key"). One graph run executes the node
// exactly once, so this is the number of GraphExecutor invocations.
type counter struct {
	mu sync.Mutex
	n  map[string]int
}

func (c *counter) inc(key string) {
	c.mu.Lock()
	c.n[key]++
	c.mu.Unlock()
}

func (c *counter) get(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n[key]
}

func (c *counter) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := 0
	for _, v := range c.n {
		t += v
	}
	return t
}

func inputKey(in node.NodeInput) string {
	v, _ := in.GetPort("input")
	if m, ok := v.Object(); ok {
		return fmt.Sprint(m["key"])
	}
	return ""
}

type countNode struct {
	c     *counter
	delay time.Duration
}

func (countNode) Type() string { return "test.count" }
func (n countNode) Execute(ctx context.Context, in node.NodeInput) (node.NodeOutput, error) {
	n.c.inc(inputKey(in))
	if n.delay > 0 {
		select {
		case <-time.After(n.delay):
		case <-ctx.Done():
			return node.NodeOutput{}, ctx.Err()
		}
	}
	out := node.NewNodeOutput(nil)
	v, _ := in.GetPort("input")
	out.SetPort("output", v)
	return out, nil
}

type failNode struct{ c *counter }

func (failNode) Type() string { return "test.fail" }
func (n failNode) Execute(_ context.Context, in node.NodeInput) (node.NodeOutput, error) {
	n.c.inc(inputKey(in))
	return node.NodeOutput{}, node.NewNodeError(node.ErrCodeRateLimited, "provider throttled", true)
}

// blockNode signals that it started, then waits for cancellation.
type blockNode struct{ started chan string }

func (blockNode) Type() string { return "test.block" }
func (n blockNode) Execute(ctx context.Context, in node.NodeInput) (node.NodeOutput, error) {
	n.started <- inputKey(in)
	<-ctx.Done()
	return node.NodeOutput{}, ctx.Err()
}

func passDefinition(typ string) node.NodeDefinition {
	return node.NodeDefinition{
		Type: typ, Name: typ, Category: node.CategoryUtilities,
		Inputs:  []node.PortDefinition{node.NewPortDefinition("input", node.ValueTypeJSON, true, "")},
		Outputs: []node.PortDefinition{node.NewPortDefinition("output", node.ValueTypeJSON, true, "")},
	}
}

// definition builds input -> <middle type> -> json -> output.
func definition(middleType string) workflow.Definition {
	pos := &workflow.Position{}
	return workflow.Definition{
		Version: workflow.DefinitionSchemaVersion,
		Nodes: []workflow.Node{
			{ID: "in", Type: "input", Name: "in", Position: pos, Config: map[string]any{}},
			{ID: "mid", Type: middleType, Name: "mid", Position: pos, Config: map[string]any{}},
			{ID: "shape", Type: "json", Name: "shape", Position: pos, Config: map[string]any{}},
			{ID: "out", Type: "output", Name: "out", Position: pos, Config: map[string]any{}},
		},
		Edges: []workflow.Edge{
			{ID: "e1", Source: "in", SourcePort: "data", Target: "mid", TargetPort: "input"},
			{ID: "e2", Source: "mid", SourcePort: "output", Target: "shape", TargetPort: "input"},
			{ID: "e3", Source: "shape", SourcePort: "output", Target: "out", TargetPort: "value"},
		},
		Settings: map[string]any{},
	}
}

type env struct {
	dbURL    string
	redisURL string
	store    *postgres.Store
	raw      *pgxpool.Pool
	execs    *postgres.ExecutionRepository
	svc      *execution.LifecycleService
	app      *app.Application
	counts   *counter
	started  chan string
	redis    *redisinfra.Client
	queue    *redisinfra.Queue
	rawRedis *goredis.Client
}

func requireEnv(t *testing.T) (dbURL, redisURL string) {
	t.Helper()
	dbURL, redisURL = os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_REDIS_URL")
	if dbURL == "" || redisURL == "" {
		t.Skip("TEST_DATABASE_URL and TEST_REDIS_URL are required for Phase 9 end-to-end tests")
	}
	return dbURL, redisURL
}

func migrationsPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file path")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "migrations")
}

// newEnv creates an isolated schema, migrates it, opens PostgreSQL and Redis
// on a unique queue key, and bootstraps the canonical node registry plus the
// test nodes.
func newEnv(t *testing.T, countDelay time.Duration) *env {
	t.Helper()
	base, redisURL := requireEnv(t)
	ctx := context.Background()

	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(admin.Close)
	schema := "phase9_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	scoped := u.String()
	if err := postgres.ApplyMigrations(scoped, migrationsPath(t)); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	store, err := postgres.Open(ctx, scoped)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(store.Close)

	e := &env{
		dbURL: scoped, redisURL: redisURL, store: store, raw: store.Pool,
		execs:   postgres.NewExecutionRepository(store),
		counts:  &counter{n: map[string]int{}},
		started: make(chan string, 64),
	}
	e.svc = execution.NewLifecycleService(e.execs)
	e.app = e.bootstrap(t, countDelay)

	rc, err := redisinfra.Open(ctx, redisURL)
	if err != nil {
		t.Fatalf("open redis: %v", err)
	}
	e.redis = rc
	key := "test:phase9:" + uuid.NewString()
	e.queue, err = redisinfra.NewQueue(rc, key, 0)
	if err != nil {
		t.Fatal(err)
	}
	opts, err := goredis.ParseURL(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	e.rawRedis = goredis.NewClient(opts)
	t.Cleanup(func() {
		_ = e.rawRedis.Del(context.Background(), key).Err()
		_ = e.rawRedis.Close()
		_ = rc.Close()
	})
	return e
}

func (e *env) bootstrap(t *testing.T, countDelay time.Duration) *app.Application {
	t.Helper()
	a, err := app.Bootstrap(config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []node.Node{countNode{c: e.counts, delay: countDelay}, failNode{c: e.counts}, blockNode{started: e.started}} {
		if err := a.NodeRegistry.RegisterNode(n, passDefinition(n.Type())); err != nil {
			t.Fatal(err)
		}
	}
	return a
}

func (e *env) mustExec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := e.raw.Exec(context.Background(), query, args...); err != nil {
		t.Fatalf("exec: %v", err)
	}
}

// seed inserts user -> workspace -> project -> workflow -> published v1.
func (e *env) seed(t *testing.T, middleType string) (uuid.UUID, uuid.UUID) {
	t.Helper()
	defJSON, err := json.Marshal(definition(middleType))
	if err != nil {
		t.Fatal(err)
	}
	userID, wsID, projectID, wf, v := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	e.mustExec(t, "INSERT INTO users (id, email, name, password_hash) VALUES ($1, $2, 'U', 'h')", userID, userID.String()+"@example.test")
	e.mustExec(t, "INSERT INTO workspaces (id, name, owner_id) VALUES ($1, 'W', $2)", wsID, userID)
	e.mustExec(t, "INSERT INTO projects (id, workspace_id, name) VALUES ($1, $2, 'P')", projectID, wsID)
	e.mustExec(t, "INSERT INTO workflows (id, project_id, name) VALUES ($1, $2, 'F')", wf, projectID)
	e.mustExec(t, "INSERT INTO workflow_versions (id, workflow_id, version_number, definition, status) VALUES ($1, $2, 1, $3, 'PUBLISHED')", v, wf, defJSON)
	return wf, v
}

// createPending creates a PENDING execution whose input key is its own tag.
func (e *env) createPending(t *testing.T, wf, v uuid.UUID) (execution.Execution, string) {
	t.Helper()
	key := uuid.NewString()
	ex, err := e.svc.Create(context.Background(), wf, v, map[string]any{"key": key})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return ex, key
}

func (e *env) get(t *testing.T, id uuid.UUID) execution.Execution {
	t.Helper()
	ex, err := e.execs.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return ex
}

func (e *env) historyCount(t *testing.T, id uuid.UUID, to execution.ExecutionStatus) int {
	t.Helper()
	var n int
	if err := e.raw.QueryRow(context.Background(),
		"SELECT count(*) FROM execution_status_history WHERE execution_id = $1 AND to_status = $2", id, string(to)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *env) nodeRows(t *testing.T, id uuid.UUID, nodeID string) int {
	t.Helper()
	var n int
	if err := e.raw.QueryRow(context.Background(),
		"SELECT count(*) FROM node_executions WHERE execution_id = $1 AND node_id = $2", id, nodeID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *env) queueLen(t *testing.T) int64 {
	t.Helper()
	n, err := e.rawRedis.LLen(context.Background(), e.queue.Name()).Result()
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *env) runner(t *testing.T) *execution.Runner {
	t.Helper()
	r, err := execution.NewRunner(execution.RunnerConfig{
		Executions:     e.execs,
		NodeExecutions: postgres.NewNodeExecutionRepository(e.store),
		Definitions:    postgres.NewWorkflowVersionRepository(e.store),
		Validator:      workflow.NewValidator(e.app.NodeRegistry),
		Graph:          execution.NewGraphExecutor(e.app.NodeRegistry),
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (e *env) processor(t *testing.T) *worker.ExecutionProcessor {
	t.Helper()
	p, err := worker.NewExecutionProcessor(e.execs, e.runner(t))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// startPool starts n workers on the Redis queue and stops them on cleanup.
func (e *env) startPool(t *testing.T, n int) *worker.Pool {
	t.Helper()
	pool, err := worker.NewPool(n, e.queue, e.processor(t), worker.Options{ErrorPause: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := pool.Stop(ctx); err != nil {
			t.Errorf("stop pool: %v", err)
		}
	})
	return pool
}

func (e *env) enqueue(t *testing.T, ids ...uuid.UUID) {
	t.Helper()
	d := dispatcher(t, e)
	for _, id := range ids {
		if err := d.Dispatch(context.Background(), id); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	}
}

// outcomes sums every worker's outcome counters.
func outcomes(pool *worker.Pool) map[worker.Outcome]int {
	out := map[worker.Outcome]int{}
	for _, w := range pool.Status().Workers {
		for k, v := range w.Stats.Outcomes {
			out[k] += v
		}
	}
	return out
}

func processed(pool *worker.Pool) int {
	n := 0
	for _, v := range outcomes(pool) {
		n += v
	}
	return n
}

// eventually polls cond until it holds or the timeout expires.
func eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
