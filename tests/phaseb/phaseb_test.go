package phaseb_test

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

// Phase B: templates, workflow variables, Prompt Template, LLM and Structured
// Output, and the version model, through the production composition
// (app.NewAPI, app.NewWorker) against real PostgreSQL and Redis. Only the
// OpenAI API is mocked: prompts asking for JSON get a JSON answer.

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type mockOpenAI struct {
	srv     *httptest.Server
	mu      sync.Mutex
	prompts []string
	systems []string
}

func newMockOpenAI(t *testing.T) *mockOpenAI {
	m := &mockOpenAI{}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		prompt, system := "", ""
		for _, msg := range req.Messages {
			if msg.Role == "system" {
				system = msg.Content
			} else {
				prompt = msg.Content
			}
		}
		m.mu.Lock()
		m.prompts = append(m.prompts, prompt)
		m.systems = append(m.systems, system)
		m.mu.Unlock()
		answer := "answer to " + prompt
		if strings.Contains(prompt, "Respond with JSON") {
			answer = "Sure!\n```json\n{\"summary\": \"Launch moved\", \"sentiment\": \"neutral\", \"action_items\": [\"Tell sales\", \"Update plan\"], \"confidence\": 0.9}\n```"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":   req.Model,
			"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": answer}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 7, "completion_tokens": 5, "total_tokens": 12},
		})
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockOpenAI) last() (string, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.prompts) == 0 {
		return "", ""
	}
	return m.prompts[len(m.prompts)-1], m.systems[len(m.systems)-1]
}

type env struct {
	t      *testing.T
	srv    *httptest.Server
	raw    *pgxpool.Pool
	openai *mockOpenAI
}

func newEnv(t *testing.T) *env {
	t.Helper()
	base, redisURL := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_REDIS_URL")
	if base == "" || redisURL == "" {
		t.Skip("TEST_DATABASE_URL and TEST_REDIS_URL are required for Phase B tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := "phaseb_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
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
	prefix := "test:phaseb:" + uuid.NewString()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	rel := config.DefaultReliability()
	rel.DeadLetterQueue = prefix + ":dead-letter"
	rel.SchedulerInterval = 100 * time.Millisecond
	rel.MaxAttempts = 1
	cfg := config.Config{
		DatabaseURL: u.String(), RedisURL: redisURL, RedisQueueName: prefix + ":executions",
		WorkerCount: 2, WorkerShutdownTimeout: 5 * time.Second, Reliability: rel,
		CredentialEncryptionKey: base64.StdEncoding.EncodeToString(key),
		OpenAIBaseURL:           m.srv.URL + "/v1",
		APIAddr:                 "127.0.0.1:0",
		AuthTokenSecret:         base64.StdEncoding.EncodeToString(secret),
		AuthTokenTTL:            time.Hour,
		ExposeNodeData:          true,
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

	wctx, cancel := context.WithCancel(context.Background())
	w, err := app.NewWorker(wctx, cfg, quiet)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = w.Run(wctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return &env{t: t, srv: srv, raw: raw, openai: m}
}

type resp struct {
	status int
	body   []byte
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
	return resp{status: r.StatusCode, body: b}
}

func (e *env) must(r resp, status int) map[string]any {
	e.t.Helper()
	if r.status != status {
		e.t.Fatalf("status %d, want %d: %s", r.status, status, r.body)
	}
	var m map[string]any
	if err := json.Unmarshal(r.body, &m); err != nil {
		e.t.Fatalf("decode %s: %v", r.body, err)
	}
	return m
}

func (e *env) wait(token, id string) map[string]any {
	e.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		m := e.must(e.call(token, "GET", "/api/v1/executions/"+id, nil), 200)
		if s := m["status"].(string); s == "COMPLETED" || s == "FAILED" || s == "CANCELLED" {
			return m
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("execution %s still %s", id, m["status"])
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func nodeOfType(def map[string]any, typ string) map[string]any {
	for _, n := range def["nodes"].([]any) {
		if node := n.(map[string]any); node["type"] == typ {
			return node
		}
	}
	return nil
}

func TestTemplateVariablesStructuredOutputAndVersions(t *testing.T) {
	e := newEnv(t)
	reg := e.must(e.call("", "POST", "/api/v1/auth/register", map[string]any{
		"email": "b-" + uuid.NewString()[:8] + "@example.test", "name": "B", "password": "password-b"}), 201)
	tok := reg["access_token"].(string)
	ws := reg["workspace"].(map[string]any)["id"].(string)
	cred := e.must(e.call(tok, "POST", "/api/v1/credentials", map[string]any{"workspace_id": ws, "name": "OpenAI",
		"provider": "openai", "credential_type": "api_key", "secret": "sk-phaseb-secret"}), 201)["id"].(string)
	project := e.must(e.call(tok, "POST", "/api/v1/projects", map[string]any{"workspace_id": ws, "name": "P"}), 201)["id"].(string)

	// --- the node catalog carries options metadata -------------------------
	nodes := e.must(e.call(tok, "GET", "/api/v1/nodes", nil), 200)["items"].([]any)
	var model map[string]any
	for _, n := range nodes {
		if d := n.(map[string]any); d["type"] == "llm" {
			for _, f := range d["config"].([]any) {
				if f.(map[string]any)["name"] == "model" {
					model = f.(map[string]any)
				}
			}
		}
	}
	if model == nil || model["allow_custom"] != true || len(model["options"].([]any)) == 0 || model["label"] != "Model" {
		t.Fatalf("model field metadata: %v", model)
	}

	// --- templates ---------------------------------------------------------------
	list := e.must(e.call(tok, "GET", "/api/v1/templates", nil), 200)["items"].([]any)
	if len(list) != 3 || list[2].(map[string]any)["id"] != "structured-ai-response" {
		t.Fatalf("templates: %v", list)
	}
	if r := e.call(tok, "POST", "/api/v1/workflows", map[string]any{"project_id": project, "name": "X", "template_id": "nope"}); r.status != 400 {
		t.Fatalf("unknown template: %d %s", r.status, r.body)
	}
	created := e.must(e.call(tok, "POST", "/api/v1/workflows", map[string]any{"project_id": project, "name": "Meeting Summarizer", "template_id": "structured-ai-response"}), 201)
	wf := created["id"].(string)
	v1 := created["version"].(map[string]any)
	if v1["status"] != "DRAFT" || v1["version_number"] != 1.0 || created["active_version_id"] != nil {
		t.Fatalf("a template starts as a new draft: %v", created)
	}
	def := e.must(e.call(tok, "GET", "/api/v1/workflows/"+wf+"/versions/"+v1["id"].(string), nil), 200)["definition"].(map[string]any)
	for _, n := range def["nodes"].([]any) {
		if id := n.(map[string]any)["id"].(string); id == "llm" || id == "prompt" || !strings.Contains(id, "_") {
			t.Fatalf("fresh node IDs expected, got %q", id)
		}
	}
	vars := def["variables"].([]any)
	if len(vars) != 1 || vars[0].(map[string]any)["name"] != "audience" {
		t.Fatalf("variables survive persistence: %v", vars)
	}

	// --- edit: credential, a system prompt and a second variable; save ---------
	llm := nodeOfType(def, "llm")
	llm["config"].(map[string]any)["credential_id"] = cred
	llm["config"].(map[string]any)["system"] = "Write in a {{tone}} tone."
	def["variables"] = append(vars, map[string]any{"name": "tone", "type": "string", "default": nil, "description": "Required per run"})
	v2 := e.must(e.call(tok, "POST", "/api/v1/workflows/"+wf+"/versions", map[string]any{"definition": def}), 201)
	if v2["version_number"] != 2.0 || v2["status"] != "DRAFT" {
		t.Fatalf("save creates a new draft: %v", v2)
	}
	stored := e.must(e.call(tok, "GET", "/api/v1/workflows/"+wf+"/versions/"+v2["id"].(string), nil), 200)["definition"].(map[string]any)
	if len(stored["variables"].([]any)) != 2 {
		t.Fatalf("variables round trip: %v", stored["variables"])
	}
	// v1 is untouched by saving v2.
	again := e.must(e.call(tok, "GET", "/api/v1/workflows/"+wf+"/versions/"+v1["id"].(string), nil), 200)["definition"].(map[string]any)
	if nodeOfType(again, "llm")["config"].(map[string]any)["credential_id"] != nil {
		t.Fatal("versions are immutable")
	}
	pub := e.must(e.call(tok, "POST", "/api/v1/workflows/"+wf+"/versions/"+v2["id"].(string)+"/publish", nil), 200)
	if pub["status"] != "PUBLISHED" {
		t.Fatalf("publish: %v", pub)
	}

	// --- execute: variable defaults, required variables, structured output ----
	if r := e.call(tok, "POST", "/api/v1/workflows/"+wf+"/execute", map[string]any{"input": map[string]any{"query": "q"}}); r.status != 400 || !strings.Contains(string(r.body), `variable \"tone\" has no value`) {
		t.Fatalf("a variable without default is required: %d %s", r.status, r.body)
	}
	if r := e.call(tok, "POST", "/api/v1/workflows/"+wf+"/execute", map[string]any{"input": map[string]any{"query": "q", "tone": 5}}); r.status != 400 {
		t.Fatalf("wrong variable type: %d %s", r.status, r.body)
	}
	id := e.must(e.call(tok, "POST", "/api/v1/workflows/"+wf+"/execute", map[string]any{"input": map[string]any{
		"query": "The launch moved to May.", "tone": "friendly"}}), 202)["execution_id"].(string)
	ex := e.wait(tok, id)
	if ex["status"] != "COMPLETED" {
		t.Fatalf("execution: %v", ex)
	}
	prompt, system := e.openai.last()
	if !strings.Contains(prompt, "Summarize the text below for a busy executive.") || !strings.Contains(prompt, "The launch moved to May.") {
		t.Fatalf("Prompt Template + variable default + input: %q", prompt)
	}
	if system != "Write in a friendly tone." {
		t.Fatalf("LLM system prompt with a supplied variable: %q", system)
	}
	if in := ex["input"].(map[string]any); in["audience"] != "a busy executive" || in["tone"] != "friendly" {
		t.Fatalf("defaults are recorded in the execution input: %v", in)
	}
	out, _ := json.Marshal(ex["output"])
	if !strings.Contains(string(out), `"action_items":["Tell sales","Update plan"]`) || strings.Contains(string(out), "confidence") {
		t.Fatalf("structured output (schema-shaped): %s", out)
	}

	// --- editing a published workflow makes a new draft; v2 stays as it was ----
	nodeOfType(stored, "prompt")["config"].(map[string]any)["template"] = "Changed {{input.query}}"
	v3 := e.must(e.call(tok, "POST", "/api/v1/workflows/"+wf+"/versions", map[string]any{"definition": stored}), 201)
	if v3["status"] != "DRAFT" || v3["version_number"] != 3.0 {
		t.Fatalf("v3: %v", v3)
	}
	w := e.must(e.call(tok, "GET", "/api/v1/workflows/"+wf, nil), 200)
	if w["active_version_id"] != v2["id"] {
		t.Fatal("the active version is still v2")
	}
	pubDef := e.must(e.call(tok, "GET", "/api/v1/workflows/"+wf+"/versions/"+v2["id"].(string), nil), 200)["definition"].(map[string]any)
	if tpl := nodeOfType(pubDef, "prompt")["config"].(map[string]any)["template"].(string); !strings.HasPrefix(tpl, "Summarize the text below for {{audience}}.") {
		t.Fatalf("published v2 must be unchanged: %q", tpl)
	}
	if _, err := e.raw.Exec(context.Background(), `UPDATE workflow_versions SET definition = '{}' WHERE id = $1`, v2["id"]); err == nil {
		t.Fatal("the database refuses to change a published version")
	}

	// --- backend validation of options and variables -----------------------------
	bad := e.must(e.call(tok, "GET", "/api/v1/workflows/"+wf+"/versions/"+v3["id"].(string), nil), 200)["definition"].(map[string]any)
	nodeOfType(bad, "llm")["config"].(map[string]any)["provider"] = "acme"
	bad["variables"] = []any{map[string]any{"name": "1bad", "type": "string", "default": nil}}
	r := e.call(tok, "POST", "/api/v1/workflows/"+wf+"/versions", map[string]any{"definition": bad})
	if r.status != 422 || !strings.Contains(string(r.body), "allowed options") || !strings.Contains(string(r.body), "INVALID_VARIABLE") {
		t.Fatalf("invalid provider and variable are rejected: %d %s", r.status, r.body)
	}
}

func TestDataTransformationTemplateRuns(t *testing.T) {
	e := newEnv(t)
	reg := e.must(e.call("", "POST", "/api/v1/auth/register", map[string]any{
		"email": "t-" + uuid.NewString()[:8] + "@example.test", "name": "T", "password": "password-t"}), 201)
	tok := reg["access_token"].(string)
	ws := reg["workspace"].(map[string]any)["id"].(string)
	project := e.must(e.call(tok, "POST", "/api/v1/projects", map[string]any{"workspace_id": ws, "name": "P"}), 201)["id"].(string)
	created := e.must(e.call(tok, "POST", "/api/v1/workflows", map[string]any{"project_id": project, "name": "Reshape", "template_id": "data-transformation"}), 201)
	wf, v := created["id"].(string), created["version"].(map[string]any)["id"].(string)
	e.must(e.call(tok, "POST", "/api/v1/workflows/"+wf+"/versions/"+v+"/publish", nil), 200)
	id := e.must(e.call(tok, "POST", "/api/v1/workflows/"+wf+"/execute", map[string]any{"input": map[string]any{"query": "hello"}}), 202)["execution_id"].(string)
	ex := e.wait(tok, id)
	out, _ := json.Marshal(ex["output"])
	if ex["status"] != "COMPLETED" || !strings.Contains(string(out), `"question":"hello"`) || !strings.Contains(string(out), `"source":"web form"`) || !strings.Contains(string(out), `"received":true`) {
		t.Fatalf("transform mapping with a variable default: %v %s", ex["status"], out)
	}
}
