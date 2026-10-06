package phase11_test

// Phase 11 end to end, against real PostgreSQL:
//
//	encrypted credential (PostgreSQL) -> credential.Service (resolver)
//	  -> LLM node -> provider registry -> OpenAI provider
//	  -> mocked OpenAI HTTP server -> LLM response -> node output
//	  -> execution result (Phase 8 lifecycle, Phase 10 retry classification)
//
// Requires TEST_DATABASE_URL; each test uses its own schema.

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
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

	"workflow-optimizer/internal/app"
	"workflow-optimizer/internal/config"
	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/infrastructure/encryption"
	"workflow-optimizer/internal/infrastructure/postgres"
	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/retry"
	"workflow-optimizer/internal/workflow"
)

const apiKey = "sk-proj-e2e-NEVER-LEAK-777"

// mockOpenAI is a scripted OpenAI Chat Completions endpoint.
type mockOpenAI struct {
	*httptest.Server
	mu      sync.Mutex
	auth    []string
	prompts []string
	reply   func(w http.ResponseWriter)
}

func newMockOpenAI(t *testing.T) *mockOpenAI {
	t.Helper()
	m := &mockOpenAI{reply: func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, `{"model":"gpt-5-2025","choices":[{"message":{"role":"assistant","content":"Hello, Ada!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":4,"total_tokens":15}}`)
	}}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		m.auth = append(m.auth, r.Header.Get("Authorization"))
		for _, msg := range body.Messages {
			m.prompts = append(m.prompts, msg.Content)
		}
		reply := m.reply
		m.mu.Unlock()
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		reply(w)
	}))
	t.Cleanup(m.Close)
	return m
}

func (m *mockOpenAI) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.auth)
}

// Glue nodes (test only). The canonical V1 nodes connect by exact port
// type and none turns the JSON workflow input into the LLM's string prompt,
// so the graph is in -> to_text -> llm -> to_json -> out; the prompt text is
// built by the engine's own {{variable}} resolution.
type toText struct{}

func (toText) Type() string { return "test.to_text" }
func (toText) Execute(_ context.Context, in node.NodeInput) (node.NodeOutput, error) {
	text, _ := in.GetStringConfig("text")
	out := node.NewNodeOutput(nil)
	out.SetPort("text", node.NewStringValue(text))
	return out, nil
}

type toJSON struct{}

func (toJSON) Type() string { return "test.to_json" }
func (toJSON) Execute(_ context.Context, in node.NodeInput) (node.NodeOutput, error) {
	v, _ := in.GetPort("text")
	s, _ := v.String()
	out := node.NewNodeOutput(nil)
	out.SetPort("value", node.NewJSONValue(map[string]any{"answer": s}))
	return out, nil
}

func glueDefinitions() []node.NodeDefinition {
	return []node.NodeDefinition{
		{Type: "test.to_text", Name: "to text", Category: node.CategoryUtilities,
			Inputs:  []node.PortDefinition{node.NewPortDefinition("data", node.ValueTypeJSON, true, "")},
			Outputs: []node.PortDefinition{node.NewPortDefinition("text", node.ValueTypeString, true, "")},
			Config:  []node.ConfigField{node.NewConfigField("text", node.ValueTypeString, true, "", "")}},
		{Type: "test.to_json", Name: "to json", Category: node.CategoryUtilities,
			Inputs:  []node.PortDefinition{node.NewPortDefinition("text", node.ValueTypeString, true, "")},
			Outputs: []node.PortDefinition{node.NewPortDefinition("value", node.ValueTypeJSON, true, "")}},
	}
}

type env struct {
	raw    *pgxpool.Pool
	store  *postgres.Store
	execs  *postgres.ExecutionRepository
	creds  *credential.Service
	runner *execution.Runner
	svc    execution.ExecutionService
	openai *mockOpenAI
}

func newEnv(t *testing.T) *env {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL is required for Phase 11 end-to-end tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := "phase11_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
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
	store, err := postgres.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)

	// The production credential chain: AES-256-GCM -> repository -> service.
	key := make([]byte, encryption.KeySize)
	_, _ = rand.Read(key)
	enc, err := encryption.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := credential.NewService(postgres.NewCredentialRepository(store), enc)
	if err != nil {
		t.Fatal(err)
	}
	mock := newMockOpenAI(t)
	application, err := app.BootstrapWith(config.Config{OpenAIBaseURL: mock.URL + "/v1"},
		app.Dependencies{Credentials: creds, HTTPClient: mock.Client()})
	if err != nil {
		t.Fatal(err)
	}
	for i, n := range []node.Node{toText{}, toJSON{}} {
		if err := application.NodeRegistry.RegisterNode(n, glueDefinitions()[i]); err != nil {
			t.Fatal(err)
		}
	}
	versions := postgres.NewWorkflowVersionRepository(store)
	execs := postgres.NewExecutionRepository(store)
	r, err := execution.NewRunner(execution.RunnerConfig{
		Executions: execs, NodeExecutions: postgres.NewNodeExecutionRepository(store),
		Definitions: versions, Workspaces: versions,
		Validator: workflow.NewValidator(application.NodeRegistry),
		Graph:     execution.NewGraphExecutor(application.NodeRegistry),
		Backoff:   retry.Policy{MaxAttempts: 3, InitialDelay: 50 * time.Millisecond, MaxDelay: time.Second, BackoffMultiplier: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := execution.NewLifecycleService(execs).WithDefaults(execution.ExecutionDefaults{MaxAttempts: 3, Timeout: time.Minute})
	return &env{raw: store.Pool, store: store, execs: execs, creds: creds, runner: r, svc: svc, openai: mock}
}

func (e *env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.raw.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// workspace seeds a workspace with a project and returns both IDs.
func (e *env) workspace(t *testing.T) (ws, project uuid.UUID) {
	t.Helper()
	user := uuid.New()
	ws, project = uuid.New(), uuid.New()
	e.exec(t, "INSERT INTO users (id, email, name, password_hash) VALUES ($1, $2, 'U', 'h')", user, user.String()+"@example.test")
	e.exec(t, "INSERT INTO workspaces (id, name, owner_id) VALUES ($1, 'W', $2)", ws, user)
	e.exec(t, "INSERT INTO projects (id, workspace_id, name) VALUES ($1, $2, 'P')", project, ws)
	return ws, project
}

// workflow publishes in -> prompt -> llm -> out in the project, using
// credential credID (only its ID is in the definition).
func (e *env) workflow(t *testing.T, project, credID uuid.UUID) (wf, version uuid.UUID) {
	t.Helper()
	pos := &workflow.Position{}
	def := workflow.Definition{
		Version: workflow.DefinitionSchemaVersion,
		Nodes: []workflow.Node{
			{ID: "in", Type: "input", Name: "in", Position: pos, Config: map[string]any{}},
			{ID: "greet", Type: "test.to_text", Name: "greet", Position: pos, Config: map[string]any{"text": "Say hello to {{input.name}}"}},
			{ID: "chat", Type: "llm", Name: "chat", Position: pos, Config: map[string]any{
				"provider": "openai", "credential_id": credID.String(), "model": "gpt-5", "temperature": 0.7}},
			{ID: "wrap", Type: "test.to_json", Name: "wrap", Position: pos, Config: map[string]any{}},
			{ID: "out", Type: "output", Name: "out", Position: pos, Config: map[string]any{}},
		},
		Edges: []workflow.Edge{
			{ID: "e1", Source: "in", SourcePort: "data", Target: "greet", TargetPort: "data"},
			{ID: "e2", Source: "greet", SourcePort: "text", Target: "chat", TargetPort: "prompt"},
			{ID: "e3", Source: "chat", SourcePort: "response", Target: "wrap", TargetPort: "text"},
			{ID: "e4", Source: "wrap", SourcePort: "value", Target: "out", TargetPort: "value"},
		},
		Settings: map[string]any{},
	}
	raw, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), apiKey) {
		t.Fatal("workflow definition contains the secret")
	}
	wf, version = uuid.New(), uuid.New()
	e.exec(t, "INSERT INTO workflows (id, project_id, name) VALUES ($1, $2, 'F')", wf, project)
	e.exec(t, "INSERT INTO workflow_versions (id, workflow_id, version_number, definition, status) VALUES ($1, $2, 1, $3, 'PUBLISHED')", version, wf, raw)
	return wf, version
}

func (e *env) credential(t *testing.T, ws uuid.UUID) uuid.UUID {
	t.Helper()
	c, err := e.creds.Create(context.Background(), credential.CreateInput{WorkspaceID: ws, Name: "OpenAI", Provider: "openai",
		Type: credential.TypeAPIKey, Secret: credential.NewSecret(apiKey)})
	if err != nil {
		t.Fatal(err)
	}
	return c.ID
}

func (e *env) run(t *testing.T, wf, version uuid.UUID) (execution.Execution, error) {
	t.Helper()
	ex, err := e.svc.Create(context.Background(), wf, version, map[string]any{"name": "Ada"})
	if err != nil {
		t.Fatal(err)
	}
	_, runErr := e.runner.Run(context.Background(), ex.ID)
	got, err := e.execs.Get(context.Background(), ex.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got, runErr
}

// assertNoSecretPersisted checks every lifecycle table for the API key.
func (e *env) assertNoSecretPersisted(t *testing.T) {
	t.Helper()
	for _, table := range []string{"executions", "node_executions", "execution_status_history", "execution_dead_letters", "workflow_versions"} {
		var n int
		if err := e.raw.QueryRow(context.Background(), "SELECT count(*) FROM "+table+" t WHERE row_to_json(t)::text LIKE '%' || $1 || '%'", "NEVER-LEAK").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("the API key appears in %d %s rows", n, table)
		}
	}
}

func TestEndToEndLLMExecutionWithEncryptedCredential(t *testing.T) {
	e := newEnv(t)
	ws, project := e.workspace(t)
	credID := e.credential(t, ws)
	wf, version := e.workflow(t, project, credID)

	got, err := e.run(t, wf, version)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got.Status != execution.StatusCompleted {
		t.Fatalf("status %s error %+v", got.Status, got.Error)
	}
	out, _ := json.Marshal(got.Output)
	if !strings.Contains(string(out), `"Hello, Ada!"`) {
		t.Fatalf("execution output = %s", out)
	}
	// The provider authenticated with the decrypted credential and received
	// the resolved prompt.
	if e.openai.calls() != 1 || e.openai.auth[0] != "Bearer "+apiKey {
		t.Fatalf("authorization = %v", e.openai.auth)
	}
	if len(e.openai.prompts) != 1 || e.openai.prompts[0] != "Say hello to Ada" {
		t.Fatalf("prompts = %v", e.openai.prompts)
	}
	// The LLM node's record holds the response and usage, not the secret.
	var nodeOut string
	if err := e.raw.QueryRow(context.Background(), "SELECT output::text FROM node_executions WHERE execution_id = $1 AND node_id = 'chat'", got.ID).Scan(&nodeOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(nodeOut, "Hello, Ada!") || !strings.Contains(nodeOut, `"total_tokens": 15`) {
		t.Fatalf("llm node output = %s", nodeOut)
	}
	e.assertNoSecretPersisted(t)
}

// A workflow of workspace B that references workspace A's credential cannot
// use it: the execution fails CREDENTIAL_NOT_FOUND (not retried) and nothing
// is sent to the provider.
func TestCrossWorkspaceCredentialIsRejected(t *testing.T) {
	e := newEnv(t)
	wsA, _ := e.workspace(t)
	_, projectB := e.workspace(t)
	credA := e.credential(t, wsA)
	wf, version := e.workflow(t, projectB, credA)

	got, _ := e.run(t, wf, version)
	if got.Status != execution.StatusFailed || got.Error == nil || got.Error.Code != "CREDENTIAL_NOT_FOUND" || got.Error.Retryable || got.Attempt != 1 {
		t.Fatalf("status %s attempt %d error %+v", got.Status, got.Attempt, got.Error)
	}
	if e.openai.calls() != 0 {
		t.Fatal("a request was sent with another workspace's credential")
	}
	e.assertNoSecretPersisted(t)
}

// Provider errors keep their Phase 10 classification: 429 with Retry-After
// schedules a retry no earlier than Retry-After; an invalid key fails at
// once; neither error carries the key or the provider's message.
func TestProviderErrorsFlowIntoPhase10(t *testing.T) {
	e := newEnv(t)
	ws, project := e.workspace(t)
	wf, version := e.workflow(t, project, e.credential(t, ws))

	e.openai.reply = func(w http.ResponseWriter) {
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"type":"requests","code":"rate_limit_exceeded","message":"slow down"}}`)
	}
	before := time.Now()
	got, err := e.run(t, wf, version)
	if !errors.As(err, new(*execution.RetryScheduledError)) {
		t.Fatalf("429: %v", err)
	}
	if got.Status != execution.StatusPending || got.LastError == nil || got.LastError.Code != "RATE_LIMITED" ||
		!got.LastError.Retryable || got.NextAttemptAt == nil || got.NextAttemptAt.Before(before.Add(2*time.Second)) {
		t.Fatalf("after 429: %+v last=%+v", got, got.LastError)
	}

	e.openai.reply = func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"Incorrect API key provided: `+apiKey+`","type":"invalid_request_error","code":"invalid_api_key"}}`)
	}
	got, _ = e.run(t, wf, version)
	if got.Status != execution.StatusFailed || got.Error == nil || got.Error.Code != "AUTHENTICATION_FAILED" || got.Error.Retryable {
		t.Fatalf("401: %s %+v", got.Status, got.Error)
	}
	if strings.Contains(got.Error.Message, "Incorrect API key") {
		t.Fatalf("provider message persisted: %s", got.Error.Message)
	}
	e.assertNoSecretPersisted(t)
}
