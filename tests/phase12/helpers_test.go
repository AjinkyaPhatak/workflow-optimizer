package phase12_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
	"workflow-optimizer/internal/infrastructure/postgres"
)

// Phase 12 tests drive the real API (app.NewAPI, the production
// composition) over HTTP against real PostgreSQL (TEST_DATABASE_URL) and
// Redis (TEST_REDIS_URL), with real workers (app.NewWorker) where an
// execution must run. Each test gets its own schema and Redis keys under
// test:phase12:, removed on cleanup.

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type env struct {
	t        *testing.T
	cfg      config.Config
	raw      *pgxpool.Pool
	rawRedis *goredis.Client
	srv      *httptest.Server
	api      *app.APIRuntime

	mu     sync.Mutex
	bodies []string // every response body, for leak checks
}

func newEnv(t *testing.T) *env {
	t.Helper()
	base, redisURL := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_REDIS_URL")
	if base == "" || redisURL == "" {
		t.Skip("TEST_DATABASE_URL and TEST_REDIS_URL are required for Phase 12 tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := "phase12_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	u, _ := url.Parse(base)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	_, file, _, _ := runtime.Caller(0)
	if err := postgres.ApplyMigrations(u.String(), filepath.Join(filepath.Dir(file), "..", "..", "migrations")); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	raw, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(raw.Close)

	prefix := "test:phase12:" + uuid.NewString()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	rel := config.DefaultReliability()
	rel.DeadLetterQueue = prefix + ":dead-letter"
	rel.SchedulerInterval = 200 * time.Millisecond
	cfg := config.Config{
		DatabaseURL:             u.String(),
		RedisURL:                redisURL,
		RedisQueueName:          prefix + ":executions",
		WorkerCount:             2,
		WorkerShutdownTimeout:   5 * time.Second,
		Reliability:             rel,
		CredentialEncryptionKey: base64.StdEncoding.EncodeToString(key),
		APIAddr:                 "127.0.0.1:0",
		AuthTokenSecret:         base64.StdEncoding.EncodeToString(secret),
		AuthTokenTTL:            time.Hour,
	}
	opts, _ := goredis.ParseURL(redisURL)
	rawRedis := goredis.NewClient(opts)
	t.Cleanup(func() {
		keys, _ := rawRedis.Keys(context.Background(), prefix+"*").Result()
		if len(keys) > 0 {
			_ = rawRedis.Del(context.Background(), keys...).Err()
		}
		_ = rawRedis.Close()
	})
	e := &env{t: t, cfg: cfg, raw: raw, rawRedis: rawRedis}
	e.api, e.srv = e.startAPI(cfg)
	return e
}

func (e *env) startAPI(cfg config.Config) (*app.APIRuntime, *httptest.Server) {
	e.t.Helper()
	rt, err := app.NewAPI(context.Background(), cfg, quiet)
	if err != nil {
		e.t.Fatal(err)
	}
	srv := httptest.NewServer(rt.Handler())
	e.t.Cleanup(func() { srv.Close(); rt.Close() })
	return rt, srv
}

// startWorkers runs a real worker process composition until the test ends.
func (e *env) startWorkers() {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	w, err := app.NewWorker(ctx, e.cfg, quiet)
	if err != nil {
		cancel()
		e.t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = w.Run(ctx) }()
	e.t.Cleanup(func() { cancel(); <-done })
}

// --- HTTP client ------------------------------------------------------------

type resp struct {
	status int
	header http.Header
	body   []byte
}

func (r resp) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.body, &m); err != nil {
		t.Fatalf("decode %s: %v", r.body, err)
	}
	return m
}

func (r resp) errCode(t *testing.T) string {
	t.Helper()
	e, _ := r.json(t)["error"].(map[string]any)
	if e == nil {
		t.Fatalf("not an API error: %d %s", r.status, r.body)
	}
	return e["code"].(string)
}

func (e *env) call(token, method, path string, body any) resp {
	return e.callAt(e.srv, token, method, path, body)
}

func (e *env) callAt(srv *httptest.Server, token, method, path string, body any) resp {
	e.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			e.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, srv.URL+path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	e.mu.Lock()
	e.bodies = append(e.bodies, string(b))
	e.mu.Unlock()
	return resp{status: r.StatusCode, header: r.Header, body: b}
}

func (e *env) must(r resp, status int) map[string]any {
	e.t.Helper()
	if r.status != status {
		e.t.Fatalf("status %d, want %d: %s", r.status, status, r.body)
	}
	if status == http.StatusNoContent {
		return nil
	}
	return r.json(e.t)
}

// --- fixtures ---------------------------------------------------------------

type account struct {
	token     string
	userID    string
	workspace string
}

func (e *env) register(name string) account {
	e.t.Helper()
	m := e.must(e.call("", "POST", "/api/v1/auth/register", map[string]any{
		"email": strings.ToLower(name) + "-" + uuid.NewString()[:8] + "@example.test", "name": name, "password": "password-" + name,
	}), 201)
	return account{token: m["access_token"].(string), userID: m["user"].(map[string]any)["id"].(string),
		workspace: m["workspace"].(map[string]any)["id"].(string)}
}

func (e *env) project(a account) string {
	e.t.Helper()
	return e.must(e.call(a.token, "POST", "/api/v1/projects", map[string]any{"workspace_id": a.workspace, "name": "Project"}), 201)["id"].(string)
}

func (e *env) workflow(a account, project, name string) string {
	e.t.Helper()
	return e.must(e.call(a.token, "POST", "/api/v1/workflows", map[string]any{"project_id": project, "name": name}), 201)["id"].(string)
}

// validDefinition is input -> json -> output, built only from the canonical
// node registry.
func validDefinition() map[string]any {
	pos := map[string]any{"x": 0, "y": 0}
	return map[string]any{
		"version": 1,
		"nodes": []any{
			map[string]any{"id": "in", "type": "input", "name": "In", "position": pos, "config": map[string]any{}},
			map[string]any{"id": "shape", "type": "json", "name": "Shape", "position": pos, "config": map[string]any{}},
			map[string]any{"id": "out", "type": "output", "name": "Out", "position": pos, "config": map[string]any{}},
		},
		"edges": []any{
			map[string]any{"id": "e1", "source": "in", "source_port": "data", "target": "shape", "target_port": "input"},
			map[string]any{"id": "e2", "source": "shape", "source_port": "output", "target": "out", "target_port": "value"},
		},
		"settings": map[string]any{},
	}
}

func (e *env) version(a account, wf string, def map[string]any) map[string]any {
	e.t.Helper()
	return e.must(e.call(a.token, "POST", "/api/v1/workflows/"+wf+"/versions", map[string]any{"definition": def}), 201)
}

// published creates a workflow with a published valid version.
func (e *env) published(a account) (wf, version string) {
	e.t.Helper()
	wf = e.workflow(a, e.project(a), "Published")
	version = e.version(a, wf, validDefinition())["id"].(string)
	e.must(e.call(a.token, "POST", "/api/v1/workflows/"+wf+"/versions/"+version+"/publish", nil), 200)
	return wf, version
}

// waitStatus polls the API until the execution reaches a terminal status.
func (e *env) waitStatus(a account, id string, timeout time.Duration) (map[string]any, []string) {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	var seen []string
	for {
		m := e.must(e.call(a.token, "GET", "/api/v1/executions/"+id, nil), 200)
		s := m["status"].(string)
		if len(seen) == 0 || seen[len(seen)-1] != s {
			seen = append(seen, s)
		}
		if s == "COMPLETED" || s == "FAILED" || s == "CANCELLED" {
			return m, seen
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("execution %s still %s after %s", id, s, timeout)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (e *env) history(id string) []string {
	e.t.Helper()
	rows, err := e.raw.Query(context.Background(),
		`SELECT COALESCE(from_status, 'NULL') || '->' || to_status FROM execution_status_history WHERE execution_id = $1 ORDER BY created_at, id`, id)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func (e *env) sqlString(query string, args ...any) string {
	e.t.Helper()
	var s string
	if err := e.raw.QueryRow(context.Background(), query, args...).Scan(&s); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
	return s
}
