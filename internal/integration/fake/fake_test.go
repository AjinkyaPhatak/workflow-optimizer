package fake_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/app"
	"workflow-optimizer/internal/config"
	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/infrastructure/encryption"
	"workflow-optimizer/internal/integration"
	"workflow-optimizer/internal/integration/fake"
	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/observability"
	"workflow-optimizer/internal/workflow"
)

// The secret of the test credential. Every security assertion checks that
// it appears nowhere but inside the fake service's comparison.
const apiKey = "sk-test-INTEGRATION-SECRET-0123456789"

// memRepo is an in-memory credential.Repository (encrypted envelopes only).
type memRepo struct {
	mu   sync.Mutex
	rows map[uuid.UUID]credential.Credential
}

func (m *memRepo) Create(_ context.Context, c credential.Credential) (credential.Credential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[c.ID] = c
	return c, nil
}

func (m *memRepo) Get(_ context.Context, id uuid.UUID) (credential.Credential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.rows[id]
	if !ok {
		return credential.Credential{}, credential.ErrNotFound
	}
	return c, nil
}

func (m *memRepo) UpdateData(_ context.Context, ws, id uuid.UUID, data json.RawMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.rows[id]
	if !ok || c.WorkspaceID != ws {
		return credential.ErrNotFound
	}
	c.EncryptedData = data
	m.rows[id] = c
	return nil
}

func (m *memRepo) Delete(context.Context, uuid.UUID, uuid.UUID) error { return nil }

func (m *memRepo) ListByWorkspace(_ context.Context, ws uuid.UUID) ([]credential.Credential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []credential.Credential
	for _, c := range m.rows {
		if c.WorkspaceID == ws {
			out = append(out, c)
		}
	}
	return out, nil
}

type fixture struct {
	t         *testing.T
	repo      *memRepo
	creds     *credential.Service
	svc       *fake.Service
	app       *app.Application
	workspace uuid.UUID
	credID    uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	key := make([]byte, encryption.KeySize)
	_, _ = rand.Read(key)
	enc, err := encryption.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo := &memRepo{rows: map[uuid.UUID]credential.Credential{}}
	creds, err := credential.NewService(repo, enc)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, repo: repo, creds: creds, workspace: uuid.New()}
	f.credID = f.credential(f.workspace, fake.Provider, credential.TypeAPIKey, apiKey)
	f.svc = fake.NewService(apiKey, map[string]any{"greeting": map[string]any{"text": "hello", "lang": "en"}})
	m, err := fake.Module(creds, f.svc)
	if err != nil {
		t.Fatal(err)
	}
	f.app, err = app.BootstrapWith(config.Config{}, app.Dependencies{Integrations: []integration.Module{m}})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) credential(ws uuid.UUID, provider string, typ credential.Type, secret string) uuid.UUID {
	f.t.Helper()
	c, err := f.creds.Create(context.Background(), credential.CreateInput{
		WorkspaceID: ws, Name: "Test", Provider: provider, Type: typ, Secret: credential.NewSecret(secret),
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return c.ID
}

var pos = &workflow.Position{}

// readWorkflow: input -> transform(path "key") -> test_integration.read -> output.
func readWorkflow(credentialID string) workflow.Definition {
	return workflow.Definition{
		Version: workflow.DefinitionSchemaVersion, Settings: map[string]any{},
		Nodes: []workflow.Node{
			{ID: "in", Type: "input", Name: "Input", Position: pos, Config: map[string]any{}},
			{ID: "pick", Type: "transform", Name: "Key", Position: pos, Config: map[string]any{"path": "key"}},
			{ID: "read", Type: fake.ReadType, Name: "Read", Position: pos, Config: map[string]any{"credential_id": credentialID}},
			{ID: "out", Type: "output", Name: "Output", Position: pos, Config: map[string]any{}},
		},
		Edges: []workflow.Edge{
			{ID: "e1", Source: "in", SourcePort: "data", Target: "pick", TargetPort: "input"},
			{ID: "e2", Source: "pick", SourcePort: "output", Target: "read", TargetPort: "key"},
			{ID: "e3", Source: "read", SourcePort: "record", Target: "out", TargetPort: "value"},
		},
	}
}

// writeWorkflow: input -> test_integration.write (key from {{input.key}}) -> output.
func writeWorkflow(credentialID string) workflow.Definition {
	return workflow.Definition{
		Version: workflow.DefinitionSchemaVersion, Settings: map[string]any{},
		Nodes: []workflow.Node{
			{ID: "in", Type: "input", Name: "Input", Position: pos, Config: map[string]any{}},
			{ID: "write", Type: fake.WriteType, Name: "Write", Position: pos, Config: map[string]any{"credential_id": credentialID, "key": "{{input.key}}"}},
			{ID: "out", Type: "output", Name: "Output", Position: pos, Config: map[string]any{}},
		},
		Edges: []workflow.Edge{
			{ID: "e1", Source: "in", SourcePort: "data", Target: "write", TargetPort: "value"},
			{ID: "e2", Source: "write", SourcePort: "result", Target: "out", TargetPort: "value"},
		},
	}
}

// recordingObserver keeps what the lifecycle would persist for each node:
// its resolved input and its output.
type recordingObserver struct {
	inputs  []node.NodeInput
	outputs []node.NodeOutput
	errs    []error
}

func (o *recordingObserver) NodeStarted(_ context.Context, _, _ string, in node.NodeInput) error {
	o.inputs = append(o.inputs, in)
	return nil
}

func (o *recordingObserver) NodeFinished(_ context.Context, _ string, out node.NodeOutput, err error) error {
	o.outputs = append(o.outputs, out)
	if err != nil {
		o.errs = append(o.errs, err)
	}
	return nil
}

func (o *recordingObserver) NodeFailedBeforeExecute(context.Context, string, string, execution.Stage, error) error {
	return nil
}

func (f *fixture) run(ctx context.Context, def workflow.Definition, input map[string]any, obs execution.NodeObserver) (execution.ExecutionResult, error) {
	return execution.NewGraphExecutor(f.app.NodeRegistry).ExecuteWithOptions(ctx, def, input, execution.ExecuteOptions{
		Observer: obs, Scope: node.Scope{WorkspaceID: f.workspace}, OperationKey: "exec-1",
	})
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func noSecret(t *testing.T, where, s string) {
	t.Helper()
	if strings.Contains(s, apiKey) {
		t.Fatalf("the credential secret leaked into %s: %s", where, s)
	}
}

func TestFakeIntegrationIsDiscoverableAsOrdinaryNodes(t *testing.T) {
	f := newFixture(t)
	for typ, side := range map[string]node.SideEffects{fake.ReadType: node.SideEffectsNone, fake.WriteType: node.SideEffectsUnsafe} {
		def, err := f.app.NodeRegistry.GetDefinition(typ)
		if err != nil {
			t.Fatal(err)
		}
		if def.SideEffects != side || def.Category != node.CategoryIntegration || def.Integration == nil || def.Integration.ID != fake.ID {
			t.Fatalf("%s: %+v", typ, def)
		}
		if def.Auth == nil || !def.Auth.Required || def.Auth.Provider != fake.Provider || def.Auth.CredentialType != "API_KEY" {
			t.Fatalf("%s auth: %+v", typ, def.Auth)
		}
		noSecret(t, "node definition", mustJSON(t, def))
	}
	if l := f.app.IntegrationRegistry.List(); len(l) != 2 || l[0].ID != "gmail" || l[1].ID != fake.ID {
		t.Fatalf("integration registry: %+v", l)
	}
	// Production composition installs Gmail only (Phase C3), never the fake.
	prod, err := app.Bootstrap(config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if l := prod.IntegrationRegistry.List(); len(l) != 1 || l[0].ID != "gmail" || prod.NodeRegistry.HasNodeType(fake.ReadType) {
		t.Fatal("the fake integration must not be part of the production composition")
	}
}

func TestReadRunsThroughTheGenericExecutor(t *testing.T) {
	f := newFixture(t)
	def := readWorkflow(f.credID.String())
	if res := workflow.NewValidator(f.app.NodeRegistry).Validate(def); !res.Valid {
		t.Fatalf("workflow invalid: %+v", res.Errors)
	}
	obs := &recordingObserver{}
	res, err := f.run(context.Background(), def, map[string]any{"key": "greeting"}, obs)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := res.Outputs["out"].GetPort("result")
	if got := mustJSON(t, v.Data); got != `{"lang":"en","text":"hello"}` {
		t.Fatalf("output: %s", got)
	}
	calls := f.svc.Calls()
	if len(calls) != 1 || calls[0].Operation != "read" || calls[0].Key != "greeting" || !calls[0].Authorized {
		t.Fatalf("service calls: %+v", calls)
	}
	// What the lifecycle persists (node inputs and outputs) and the workflow
	// JSON hold the credential ID only.
	noSecret(t, "workflow JSON", mustJSON(t, def))
	noSecret(t, "node inputs", mustJSON(t, obs.inputs))
	noSecret(t, "node outputs", mustJSON(t, obs.outputs))
	noSecret(t, "execution state", mustJSON(t, res))
	if !strings.Contains(mustJSON(t, def), f.credID.String()) {
		t.Fatal("the workflow references the credential by ID")
	}
}

func TestWriteGetsTheStableIdempotencyKey(t *testing.T) {
	f := newFixture(t)
	res, err := f.run(context.Background(), writeWorkflow(f.credID.String()), map[string]any{"key": "k1", "n": 1.0}, nil)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := res.Outputs["out"].GetPort("result")
	if got := mustJSON(t, v.Data); got != `{"key":"k1","written":true}` {
		t.Fatalf("output: %s", got)
	}
	if rec, ok := f.svc.Record("k1"); !ok || mustJSON(t, rec) != `{"key":"k1","n":1}` {
		t.Fatalf("stored: %v", rec)
	}
	if c := f.svc.Calls(); len(c) != 1 || c[0].IdempotencyKey != "exec-1/write" {
		t.Fatalf("idempotency key: %+v", c)
	}
}

func failureOf(t *testing.T, err error) execution.ExecutionError {
	t.Helper()
	if err == nil {
		t.Fatal("expected the execution to fail")
	}
	return execution.ErrorFromExecution(err)
}

func TestFailuresPropagateClassified(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Not found: a normal, non-retryable failure of the read node.
	e := failureOf(t, func() error {
		_, err := f.run(ctx, readWorkflow(f.credID.String()), map[string]any{"key": "missing"}, nil)
		return err
	}())
	if e.Code != "RESOURCE_NOT_FOUND" || e.Retryable || e.Source != node.SourceProvider || e.NodeID == nil || *e.NodeID != "read" {
		t.Fatalf("not found: %+v", e)
	}

	// The provider is down: retryable for the read...
	f.svc.Fail = func(string, string) error { return integration.FromHTTPStatus(503, 0, "maintenance") }
	_, err := f.run(ctx, readWorkflow(f.credID.String()), map[string]any{"key": "greeting"}, nil)
	if e := failureOf(t, err); e.Code != "PROVIDER_UNAVAILABLE" || !e.Retryable {
		t.Fatalf("read + 503: %+v", e)
	}
	// ...but not for the write: its side effects are unsafe. The metadata
	// changes only the retry decision, not how the node ran.
	_, err = f.run(ctx, writeWorkflow(f.credID.String()), map[string]any{"key": "k"}, nil)
	if e := failureOf(t, err); e.Code != "PROVIDER_UNAVAILABLE" || e.Retryable {
		t.Fatalf("write + 503: %+v", e)
	}
	f.svc.Fail = func(string, string) error { return integration.FromHTTPStatus(429, time.Second, "slow down") }
	_, err = f.run(ctx, writeWorkflow(f.credID.String()), map[string]any{"key": "k"}, nil)
	if e := failureOf(t, err); e.Code != "RATE_LIMITED" || !e.Retryable || e.RetryAfter != time.Second {
		t.Fatalf("write + 429 (not applied): %+v", e)
	}
	f.svc.Fail = func(string, string) error {
		return integration.NewError(integration.KindMalformedResponse, "the response was not JSON")
	}
	_, err = f.run(ctx, readWorkflow(f.credID.String()), map[string]any{"key": "greeting"}, nil)
	if e := failureOf(t, err); e.Code != "MALFORMED_RESPONSE" || e.Retryable {
		t.Fatalf("malformed: %+v", e)
	}
}

func TestCredentialFlow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	runWith := func(credID string) execution.ExecutionError {
		t.Helper()
		obs := &recordingObserver{}
		_, err := f.run(ctx, readWorkflow(credID), map[string]any{"key": "greeting"}, obs)
		e := failureOf(t, err)
		noSecret(t, "error", err.Error())
		noSecret(t, "execution error", mustJSON(t, e))
		noSecret(t, "node outputs", mustJSON(t, obs.outputs))
		return e
	}

	// A stored key the service rejects: authentication failure, final.
	wrong := f.credential(f.workspace, fake.Provider, credential.TypeAPIKey, "sk-test-WRONG-KEY-000000000000")
	if e := runWith(wrong.String()); e.Code != "AUTHENTICATION_FAILED" || e.Retryable {
		t.Fatalf("rejected key: %+v", e)
	}
	// Another workspace's credential does not exist here.
	foreign := f.credential(uuid.New(), fake.Provider, credential.TypeAPIKey, apiKey)
	if e := runWith(foreign.String()); e.Code != "CREDENTIAL_NOT_FOUND" || e.Retryable {
		t.Fatalf("foreign credential: %+v", e)
	}
	// A credential of another provider is refused before any call.
	openai := f.credential(f.workspace, "openai", credential.TypeAPIKey, apiKey)
	if e := runWith(openai.String()); e.Code != "CREDENTIAL_PROVIDER_MISMATCH" {
		t.Fatalf("provider mismatch: %+v", e)
	}
	// The integration accepts API keys only.
	bearer := f.credential(f.workspace, fake.Provider, credential.TypeBearerToken, apiKey)
	if e := runWith(bearer.String()); e.Code != "CREDENTIAL_INVALID" {
		t.Fatalf("credential type: %+v", e)
	}
	if e := runWith("not-a-uuid"); e.Code != "CREDENTIAL_INVALID" {
		t.Fatalf("bad id: %+v", e)
	}
	if e := runWith(""); e.Code != "CREDENTIAL_INVALID" {
		t.Fatalf("missing id: %+v", e)
	}
	for _, c := range f.svc.Calls() {
		if c.Authorized {
			t.Fatalf("no call may have been authorized: %+v", c)
		}
	}
	if n := len(f.svc.Calls()); n != 1 {
		t.Fatalf("only the rejected key reached the service, got %d calls", n)
	}
}

func TestSecretsCannotEnterWorkflowConfig(t *testing.T) {
	f := newFixture(t)
	def := readWorkflow(f.credID.String())
	def.Nodes[2].Config["access_token"] = "ya29.inline"
	res := workflow.NewValidator(f.app.NodeRegistry).Validate(def)
	if res.Valid {
		t.Fatal("a workflow with an inline token must not validate")
	}
	found := false
	for _, e := range res.Errors {
		if e.Code == workflow.ErrInvalidNodeConfig && e.Port == "access_token" {
			found = true
		}
	}
	if !found {
		t.Fatalf("errors: %+v", res.Errors)
	}
	// The node refuses it too, should a definition bypass validation.
	n, _ := f.app.NodeRegistry.Get(fake.ReadType)
	_, err := n.Execute(context.Background(), node.NodeInput{Config: map[string]any{"credential_id": f.credID.String(), "api_key": apiKey}, Scope: node.Scope{WorkspaceID: f.workspace}})
	var ne *node.NodeError
	if !errors.As(err, &ne) || ne.Code != node.ErrCodeConfiguration || strings.Contains(err.Error(), apiKey) {
		t.Fatalf("inline api_key: %v", err)
	}
	if len(f.svc.Calls()) != 0 {
		t.Fatal("no call may be made")
	}
}

func TestCancellationReachesTheClient(t *testing.T) {
	f := newFixture(t)
	f.svc.Delay = 5 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for len(f.svc.Calls()) == 0 {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	start := time.Now()
	_, err := f.run(ctx, readWorkflow(f.credID.String()), map[string]any{"key": "greeting"}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("the client did not stop on cancellation")
	}
	if e := execution.ErrorFromExecution(err); e.Code != execution.CodeCancelled {
		t.Fatalf("cancellation must stay a cancellation: %+v", e)
	}

	// A node timeout (the executor's, not the client's) is reported as such.
	_, err = execution.NewGraphExecutor(f.app.NodeRegistry).ExecuteWithOptions(context.Background(), readWorkflow(f.credID.String()),
		map[string]any{"key": "greeting"}, execution.ExecuteOptions{Scope: node.Scope{WorkspaceID: f.workspace}, NodeTimeout: 50 * time.Millisecond})
	if e := execution.ErrorFromExecution(err); e.Code != execution.CodeNodeTimeout || !e.Retryable {
		t.Fatalf("node timeout: %+v", e)
	}
}

// memEvents is an in-memory event store.
type memEvents struct {
	mu     sync.Mutex
	events []execution.ExecutionEvent
}

func (m *memEvents) Append(_ context.Context, e execution.ExecutionEvent) (execution.ExecutionEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, e)
	return e, nil
}

func (m *memEvents) ListByExecution(context.Context, uuid.UUID, execution.EventQuery) ([]execution.ExecutionEvent, int, error) {
	return nil, 0, nil
}

func TestObservabilityIdentifiesIntegrationActionsWithoutSecrets(t *testing.T) {
	f := newFixture(t)
	wrong := f.credential(f.workspace, fake.Provider, credential.TypeAPIKey, "sk-test-WRONG-KEY-000000000000")
	_, runErr := f.run(context.Background(), readWorkflow(wrong.String()), map[string]any{"key": "greeting"}, nil)
	failure := failureOf(t, runErr)

	var logs bytes.Buffer
	events := &memEvents{}
	rec := observability.NewRecorder(events, slog.New(slog.NewJSONHandler(&logs, nil)), observability.NewMetrics())
	id := uuid.New()
	ref := execution.NodeRef{ID: "read", Type: fake.ReadType, Invocation: 1}
	rec.NodeStarted(context.Background(), id, ref)
	rec.NodeFailed(context.Background(), id, ref, failure, 12*time.Millisecond)
	rec.NodeCompleted(context.Background(), id, execution.NodeRef{ID: "out", Type: "output", Invocation: 1}, time.Millisecond)

	if len(events.events) != 3 {
		t.Fatalf("events: %d", len(events.events))
	}
	failed := events.events[1].Data
	if failed["integration"] != fake.ID || failed["action"] != "read" || failed["node_type"] != fake.ReadType || failed["duration_ms"] != int64(12) {
		t.Fatalf("failed event: %v", failed)
	}
	if errData, _ := failed["error"].(map[string]any); errData["code"] != "AUTHENTICATION_FAILED" || errData["source"] != node.SourceProvider {
		t.Fatalf("error category: %v", failed["error"])
	}
	if _, ok := events.events[2].Data["integration"]; ok {
		t.Fatal("built-in nodes carry no integration fields")
	}
	noSecret(t, "events", mustJSON(t, events.events))
	noSecret(t, "logs", logs.String())
	if strings.Contains(logs.String(), "WRONG-KEY") || strings.Contains(mustJSON(t, events.events), "WRONG-KEY") {
		t.Fatal("the rejected key leaked into observability")
	}
}
