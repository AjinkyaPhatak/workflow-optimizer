package phase14_test

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

// Phase 14 tests drive the debugger API of the production composition
// (app.NewAPI, app.NewWorker) against real PostgreSQL and Redis. Only the
// OpenAI API is replaced, by a scriptable mock: the prompt (the workflow
// input rendered as JSON) selects its behaviour.

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

const providerSecret = "sk-phase14-PROVIDER-SECRET-0099"

// mockOpenAI answers like Chat Completions. Prompts containing
//
//	"fail-once"  fail with 503 on their first call, then succeed
//	"always-503" always fail with 503
//	"slow"       wait 4s (or until the client gives up)
//
// and succeed otherwise, reporting usage 7/5/12.
type mockOpenAI struct {
	srv   *httptest.Server
	mu    sync.Mutex
	calls map[string]int
	auth  []string
}

func newMockOpenAI(t *testing.T) *mockOpenAI {
	m := &mockOpenAI{calls: map[string]int{}}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		prompt := ""
		if len(req.Messages) > 0 {
			prompt = req.Messages[len(req.Messages)-1].Content
		}
		m.mu.Lock()
		m.calls[prompt]++
		n := m.calls[prompt]
		m.auth = append(m.auth, r.Header.Get("Authorization"))
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(prompt, "always-503"), strings.Contains(prompt, "fail-once") && n == 1:
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"error":{"type":"server_error","message":"overloaded; key ` + providerSecret + `"}}`))
			return
		case strings.Contains(prompt, "slow"):
			select {
			case <-time.After(4 * time.Second):
			case <-r.Context().Done():
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":   req.Model + "-2025",
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "answer to " + prompt}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 7, "completion_tokens": 5, "total_tokens": 12},
		})
	}))
	t.Cleanup(m.srv.Close)
	return m
}

type env struct {
	t        *testing.T
	cfg      config.Config
	raw      *pgxpool.Pool
	srv      *httptest.Server
	openai   *mockOpenAI
	rawRedis *goredis.Client

	mu     sync.Mutex
	bodies []string
}

// newEnv starts the API; tune adjusts the configuration first.
func newEnv(t *testing.T, tune func(*config.Config)) *env {
	t.Helper()
	base, redisURL := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_REDIS_URL")
	if base == "" || redisURL == "" {
		t.Skip("TEST_DATABASE_URL and TEST_REDIS_URL are required for Phase 14 tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := "phase14_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
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

	m := newMockOpenAI(t)
	prefix := "test:phase14:" + uuid.NewString()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	rel := config.DefaultReliability()
	rel.DeadLetterQueue = prefix + ":dead-letter"
	rel.SchedulerInterval = 100 * time.Millisecond
	rel.InitialRetryDelay = 100 * time.Millisecond
	rel.MaxRetryDelay = 200 * time.Millisecond
	rel.MaxAttempts = 1
	cfg := config.Config{
		DatabaseURL: u.String(), RedisURL: redisURL, RedisQueueName: prefix + ":executions",
		WorkerCount: 2, WorkerShutdownTimeout: 5 * time.Second, Reliability: rel,
		CredentialEncryptionKey: base64.StdEncoding.EncodeToString(key),
		OpenAIBaseURL:           m.srv.URL + "/v1",
		APIAddr:                 "127.0.0.1:0",
		AuthTokenSecret:         base64.StdEncoding.EncodeToString(secret),
		AuthTokenTTL:            time.Hour,
		ModelPricing:            `{"gpt-5-mini":{"input_per_million":1000,"output_per_million":2000}}`,
		ExposeNodeData:          true,
	}
	if tune != nil {
		tune(&cfg)
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
	rt, err := app.NewAPI(ctx, cfg, quiet)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(rt.Handler())
	t.Cleanup(func() { srv.Close(); rt.Close() })
	return &env{t: t, cfg: cfg, raw: raw, srv: srv, openai: m, rawRedis: rawRedis}
}

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

type resp struct {
	status int
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

func (e *env) call(token, method, path string, body any) resp {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
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
	return resp{status: r.StatusCode, body: b}
}

func (e *env) must(r resp, status int) map[string]any {
	e.t.Helper()
	if r.status != status {
		e.t.Fatalf("status %d, want %d: %s", r.status, status, r.body)
	}
	return r.json(e.t)
}

type account struct{ token, workspace string }

func (e *env) register(name string) account {
	e.t.Helper()
	m := e.must(e.call("", "POST", "/api/v1/auth/register", map[string]any{
		"email": strings.ToLower(name) + "-" + uuid.NewString()[:8] + "@example.test", "name": name, "password": "password-" + name}), 201)
	return account{token: m["access_token"].(string), workspace: m["workspace"].(map[string]any)["id"].(string)}
}

// llmWorkflow publishes input -> llm (credential) -> output and returns the
// workflow ID.
func (e *env) llmWorkflow(a account) string {
	e.t.Helper()
	cred := e.must(e.call(a.token, "POST", "/api/v1/credentials", map[string]any{"workspace_id": a.workspace, "name": "OpenAI",
		"provider": "openai", "credential_type": "api_key", "secret": providerSecret}), 201)["id"].(string)
	project := e.must(e.call(a.token, "POST", "/api/v1/projects", map[string]any{"workspace_id": a.workspace, "name": "P"}), 201)["id"].(string)
	wf := e.must(e.call(a.token, "POST", "/api/v1/workflows", map[string]any{"project_id": project, "name": "Assistant"}), 201)["id"].(string)
	pos := map[string]any{"x": 0, "y": 0}
	def := map[string]any{"version": 1, "settings": map[string]any{},
		"nodes": []any{
			map[string]any{"id": "in", "type": "input", "name": "Input", "position": pos, "config": map[string]any{}},
			map[string]any{"id": "llm_1", "type": "llm", "name": "Answer", "position": pos,
				"config": map[string]any{"provider": "openai", "model": "gpt-5-mini", "credential_id": cred}},
			map[string]any{"id": "out", "type": "output", "name": "Output", "position": pos, "config": map[string]any{}},
		},
		"edges": []any{
			map[string]any{"id": "e1", "source": "in", "source_port": "data", "target": "llm_1", "target_port": "prompt"},
			map[string]any{"id": "e2", "source": "llm_1", "source_port": "response", "target": "out", "target_port": "value"},
		}}
	v := e.must(e.call(a.token, "POST", "/api/v1/workflows/"+wf+"/versions", map[string]any{"definition": def}), 201)["id"].(string)
	e.must(e.call(a.token, "POST", "/api/v1/workflows/"+wf+"/versions/"+v+"/publish", nil), 200)
	return wf
}

func (e *env) execute(a account, wf, query string) string {
	e.t.Helper()
	return e.must(e.call(a.token, "POST", "/api/v1/workflows/"+wf+"/execute", map[string]any{"input": map[string]any{"query": query}}), 202)["execution_id"].(string)
}

func (e *env) wait(a account, id string, want string) map[string]any {
	e.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		m := e.must(e.call(a.token, "GET", "/api/v1/executions/"+id, nil), 200)
		if s := m["status"].(string); s == "COMPLETED" || s == "FAILED" || s == "CANCELLED" {
			if s != want {
				e.t.Fatalf("execution %s ended %s, want %s: %v", id, s, want, m["error"])
			}
			return m
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("execution %s still %s", id, m["status"])
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// eventTypes returns "TYPE[:node]" for every event (all pages).
func (e *env) eventTypes(a account, id string) ([]string, []map[string]any) {
	e.t.Helper()
	var lines []string
	var events []map[string]any
	for page := 1; ; page++ {
		m := e.must(e.call(a.token, "GET", "/api/v1/executions/"+id+"/events?page_size=100&page="+itoa(page), nil), 200)
		list := m["events"].([]any)
		for _, x := range list {
			ev := x.(map[string]any)
			line := ev["type"].(string)
			if n, ok := ev["node_id"].(string); ok {
				line += ":" + n
			}
			lines = append(lines, line)
			events = append(events, ev)
		}
		if len(events) >= int(m["total"].(float64)) || len(list) == 0 {
			return lines, events
		}
	}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

// eventsSettled waits until the event stream ends with a terminal event
// (events are appended right after the durable state change).
func (e *env) eventsSettled(a account, id string) ([]string, []map[string]any) {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		lines, events := e.eventTypes(a, id)
		if n := len(lines); n > 0 && strings.HasPrefix(lines[n-1], "EXECUTION_") && lines[n-1] != "EXECUTION_STARTED" {
			return lines, events
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("events never settled: %v", lines)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// dbText dumps every persisted execution-related row as JSON text.
func (e *env) dbText() string {
	e.t.Helper()
	var sb strings.Builder
	for _, table := range []string{"executions", "node_executions", "execution_events", "execution_status_history", "workflow_versions"} {
		rows, err := e.raw.Query(context.Background(), "SELECT row_to_json(t)::text FROM "+table+" t")
		if err != nil {
			e.t.Fatal(err)
		}
		for rows.Next() {
			var s string
			_ = rows.Scan(&s)
			sb.WriteString(s)
		}
		rows.Close()
	}
	return sb.String()
}
