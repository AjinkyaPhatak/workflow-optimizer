package phase10_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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
	"workflow-optimizer/internal/reliability"
	"workflow-optimizer/internal/retry"
	"workflow-optimizer/internal/workflow"
)

// Phase 10 tests run against real PostgreSQL (TEST_DATABASE_URL) and real
// Redis (TEST_REDIS_URL). Each test gets its own schema and Redis keys under
// test:phase10:, removed on cleanup.

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// ---------------------------------------------------------------------------
// Scriptable node
// ---------------------------------------------------------------------------

// step is what the scripted node does on one invocation:
//
//	"ok"            succeed
//	"503"           HTTP 503 (retryable)
//	"429:<dur>"     HTTP 429 with Retry-After (retryable, not applied)
//	"401"           invalid credentials (never retried)
//	"sleep:<dur>"   work for dur, honouring cancellation
//	"block"         block until the test releases the key (ignores ctx),
//	                then succeed: a worker that stopped responding
//	"503-at-end"    wait until the node's context ends, then report a
//	                retryable HTTP 503 instead of the context error
type script struct {
	mu       sync.Mutex
	steps    map[string][]string
	calls    map[string][]node.NodeInput
	release  map[string]chan struct{}
	started  chan string
	pastCall map[string]int
}

func newScript() *script {
	return &script{steps: map[string][]string{}, calls: map[string][]node.NodeInput{},
		release: map[string]chan struct{}{}, started: make(chan string, 1024), pastCall: map[string]int{}}
}

func (s *script) set(key string, steps ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steps[key] = steps
	s.release[key] = make(chan struct{})
}

func (s *script) invocations(key string) []node.NodeInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]node.NodeInput(nil), s.calls[key]...)
}

func (s *script) count(key string) int { return len(s.invocations(key)) }

func (s *script) unblock(key string) {
	s.mu.Lock()
	ch := s.release[key]
	s.mu.Unlock()
	close(ch)
}

func keyOf(in node.NodeInput) string {
	v, _ := in.GetPort("input")
	if m, ok := v.Object(); ok {
		return fmt.Sprint(m["key"])
	}
	return ""
}

type flakyNode struct{ s *script }

func (flakyNode) Type() string { return "test.flaky" }

func (f flakyNode) Execute(ctx context.Context, in node.NodeInput) (node.NodeOutput, error) {
	key := keyOf(in)
	f.s.mu.Lock()
	n := len(f.s.calls[key])
	f.s.calls[key] = append(f.s.calls[key], in)
	steps := f.s.steps[key]
	release := f.s.release[key]
	f.s.mu.Unlock()
	f.s.started <- key
	step := "ok"
	if n < len(steps) {
		step = steps[n]
	} else if len(steps) > 0 {
		step = steps[len(steps)-1]
	}
	kind, arg, _ := strings.Cut(step, ":")
	switch kind {
	case "503":
		return node.NodeOutput{}, node.HTTPStatusError(503, 0, "service unavailable")
	case "429":
		d, _ := time.ParseDuration(arg)
		return node.NodeOutput{}, node.HTTPStatusError(429, d, "rate limited")
	case "401":
		return node.NodeOutput{}, node.HTTPStatusError(401, 0, "invalid credentials")
	case "sleep":
		d, _ := time.ParseDuration(arg)
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return node.NodeOutput{}, ctx.Err()
		}
	case "block":
		<-release
	case "503-at-end":
		<-ctx.Done()
		return node.NodeOutput{}, node.HTTPStatusError(503, 0, "service unavailable")
	}
	out := node.NewNodeOutput(nil)
	v, _ := in.GetPort("input")
	out.SetPort("output", v)
	return out, nil
}

// countNode passes its input through and counts invocations per key: it
// sits before the flaky node, so re-running it would show completed nodes
// being re-executed.
type countNode struct{ s *script }

func (countNode) Type() string { return "test.count" }

func (c countNode) Execute(_ context.Context, in node.NodeInput) (node.NodeOutput, error) {
	c.s.mu.Lock()
	c.s.pastCall[keyOf(in)]++
	c.s.mu.Unlock()
	out := node.NewNodeOutput(nil)
	v, _ := in.GetPort("input")
	out.SetPort("output", v)
	return out, nil
}

func (s *script) preCount(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pastCall[key]
}

func passDefinition(typ string) node.NodeDefinition {
	return node.NodeDefinition{
		Type: typ, Name: typ, Category: node.CategoryUtilities,
		Inputs:  []node.PortDefinition{node.NewPortDefinition("input", node.ValueTypeJSON, true, "")},
		Outputs: []node.PortDefinition{node.NewPortDefinition("output", node.ValueTypeJSON, true, "")},
	}
}

// definition builds input -> pre (count) -> mid (flaky) -> json -> output.
func definition() workflow.Definition {
	pos := &workflow.Position{}
	return workflow.Definition{
		Version: workflow.DefinitionSchemaVersion,
		Nodes: []workflow.Node{
			{ID: "in", Type: "input", Name: "in", Position: pos, Config: map[string]any{}},
			{ID: "pre", Type: "test.count", Name: "pre", Position: pos, Config: map[string]any{}},
			{ID: "mid", Type: "test.flaky", Name: "mid", Position: pos, Config: map[string]any{}},
			{ID: "shape", Type: "json", Name: "shape", Position: pos, Config: map[string]any{}},
			{ID: "out", Type: "output", Name: "out", Position: pos, Config: map[string]any{}},
		},
		Edges: []workflow.Edge{
			{ID: "e1", Source: "in", SourcePort: "data", Target: "pre", TargetPort: "input"},
			{ID: "e2", Source: "pre", SourcePort: "output", Target: "mid", TargetPort: "input"},
			{ID: "e3", Source: "mid", SourcePort: "output", Target: "shape", TargetPort: "input"},
			{ID: "e4", Source: "shape", SourcePort: "output", Target: "out", TargetPort: "value"},
		},
		Settings: map[string]any{},
	}
}

// ---------------------------------------------------------------------------
// Environment
// ---------------------------------------------------------------------------

type env struct {
	dbURL, redisURL string
	store           *postgres.Store
	raw             *pgxpool.Pool
	execs           *postgres.ExecutionRepository
	nodes           *postgres.NodeExecutionRepository
	rel             *postgres.ReliabilityRepository
	app             *app.Application
	s               *script
	redis           *redisinfra.Client
	queue           *redisinfra.Queue
	rawRedis        *goredis.Client
	keyPrefix       string
	wf, v           uuid.UUID
}

// fast is the retry policy used by most tests: no jitter, small delays.
var fast = retry.Policy{MaxAttempts: 3, InitialDelay: 60 * time.Millisecond, MaxDelay: 200 * time.Millisecond, BackoffMultiplier: 2}

func migrationsPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file path")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "migrations")
}

func newEnv(t *testing.T) *env {
	t.Helper()
	base, redisURL := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_REDIS_URL")
	if base == "" || redisURL == "" {
		t.Skip("TEST_DATABASE_URL and TEST_REDIS_URL are required for Phase 10 tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := "phase10_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	u, _ := url.Parse(base)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	if err := postgres.ApplyMigrations(u.String(), migrationsPath(t)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := postgres.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	e := &env{dbURL: u.String(), redisURL: redisURL, store: store, raw: store.Pool,
		execs: postgres.NewExecutionRepository(store), nodes: postgres.NewNodeExecutionRepository(store),
		rel: postgres.NewReliabilityRepository(store), s: newScript()}
	e.app, err = app.Bootstrap(config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []node.Node{flakyNode{e.s}, countNode{e.s}} {
		if err := e.app.NodeRegistry.RegisterNode(n, passDefinition(n.Type())); err != nil {
			t.Fatal(err)
		}
	}
	if e.redis, err = redisinfra.Open(ctx, redisURL); err != nil {
		t.Fatal(err)
	}
	e.keyPrefix = "test:phase10:" + uuid.NewString()
	if e.queue, err = redisinfra.NewQueue(e.redis, e.keyPrefix+":executions", 0); err != nil {
		t.Fatal(err)
	}
	opts, _ := goredis.ParseURL(redisURL)
	e.rawRedis = goredis.NewClient(opts)
	t.Cleanup(func() {
		keys, _ := e.rawRedis.Keys(context.Background(), e.keyPrefix+"*").Result()
		if len(keys) > 0 {
			_ = e.rawRedis.Del(context.Background(), keys...).Err()
		}
		_ = e.rawRedis.Close()
		_ = e.redis.Close()
	})
	e.wf, e.v = e.seed(t)
	return e
}

func (e *env) mustExec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.raw.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec: %v", err)
	}
}

func (e *env) seed(t *testing.T) (uuid.UUID, uuid.UUID) {
	t.Helper()
	def, _ := json.Marshal(definition())
	user, ws, project, wf, v := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	e.mustExec(t, "INSERT INTO users (id, email, name, password_hash) VALUES ($1, $2, 'U', 'h')", user, user.String()+"@example.test")
	e.mustExec(t, "INSERT INTO workspaces (id, name, owner_id) VALUES ($1, 'W', $2)", ws, user)
	e.mustExec(t, "INSERT INTO projects (id, workspace_id, name) VALUES ($1, $2, 'P')", project, ws)
	e.mustExec(t, "INSERT INTO workflows (id, project_id, name) VALUES ($1, $2, 'F')", wf, project)
	e.mustExec(t, "INSERT INTO workflow_versions (id, workflow_id, version_number, definition, status) VALUES ($1, $2, 1, $3, 'PUBLISHED')", v, wf, def)
	return wf, v
}

func (e *env) service(maxAttempts int, timeout time.Duration) *execution.LifecycleService {
	return execution.NewLifecycleService(e.execs).
		WithDefaults(execution.ExecutionDefaults{MaxAttempts: maxAttempts, Timeout: timeout}).
		WithCancellations(e.rel)
}

// create makes a PENDING execution scripted by steps; it returns its ID and
// script key.
func (e *env) create(t *testing.T, maxAttempts int, timeout time.Duration, steps ...string) (uuid.UUID, string) {
	t.Helper()
	key := uuid.NewString()
	e.s.set(key, steps...)
	ex, err := e.service(maxAttempts, timeout).Create(context.Background(), e.wf, e.v, map[string]any{"key": key})
	if err != nil {
		t.Fatal(err)
	}
	return ex.ID, key
}

func (e *env) get(t *testing.T, id uuid.UUID) execution.Execution {
	t.Helper()
	ex, err := e.execs.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return ex
}

// keeper is a LeaseKeeper that never renews: a worker that stopped
// heartbeating (crashed, frozen, partitioned).
type deadKeeper struct{}

func (deadKeeper) Keep(ctx context.Context, _, _ uuid.UUID) (context.Context, func()) {
	return ctx, func() {}
}

type runnerOpts struct {
	backoff     execution.Backoff
	nodeRetries int
	nodeTimeout time.Duration
	keeper      execution.LeaseKeeper
	owner       string
	lease       time.Duration
}

func (e *env) runner(t *testing.T, o runnerOpts) *execution.Runner {
	t.Helper()
	cfg := execution.RunnerConfig{
		Executions: e.execs, NodeExecutions: e.nodes,
		Definitions: postgres.NewWorkflowVersionRepository(e.store),
		Validator:   workflow.NewValidator(e.app.NodeRegistry),
		Graph:       execution.NewGraphExecutor(e.app.NodeRegistry),
		Backoff:     o.backoff, NodeTimeout: o.nodeTimeout,
	}
	if o.nodeRetries > 0 {
		cfg.NodeRetry = execution.NodeRetry{MaxInvocations: o.nodeRetries, Backoff: fast, MaxInlineDelay: time.Second}
	}
	if o.keeper != nil {
		if o.owner == "" {
			o.owner = "worker-" + uuid.NewString()[:6]
		}
		if o.lease == 0 {
			o.lease = time.Second
		}
		cfg.Leases, cfg.Lease = o.keeper, &execution.LeaseGrant{Owner: o.owner, Duration: o.lease}
	}
	r, err := execution.NewRunner(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (e *env) heartbeat(t *testing.T, lease, interval time.Duration) *reliability.Heartbeat {
	t.Helper()
	h, err := reliability.NewHeartbeat(e.rel, lease, interval, quiet)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func (e *env) history(t *testing.T, id uuid.UUID) []string {
	t.Helper()
	h, err := e.execs.History(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, x := range h {
		from := "NULL"
		if x.From != nil {
			from = string(*x.From)
		}
		out = append(out, fmt.Sprintf("%s->%s@%d", from, x.To, x.Attempt))
	}
	return out
}

func (e *env) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.raw.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(15 * time.Millisecond)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
