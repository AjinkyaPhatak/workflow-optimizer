package gmail_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/mail"
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
	"workflow-optimizer/internal/integration/gmail"
	"workflow-optimizer/internal/integration/gmail/gmailtest"
	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/oauth"
	"workflow-optimizer/internal/oauth/google"
	"workflow-optimizer/internal/workflow"
)

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

func (m *memRepo) Delete(context.Context, uuid.UUID, uuid.UUID) error { return nil }
func (m *memRepo) ListByWorkspace(context.Context, uuid.UUID) ([]credential.Credential, error) {
	return nil, nil
}

func (m *memRepo) UpdateData(_ context.Context, ws, id uuid.UUID, data json.RawMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.rows[id]
	c.EncryptedData = data
	m.rows[id] = c
	return nil
}

var ada = gmailtest.Account{Sub: "108234567890123456789", Email: "ada@gmail.example", Name: "Ada"}

type env struct {
	t       *testing.T
	mock    *gmailtest.Server
	creds   *credential.Service
	repo    *memRepo
	tm      *oauth.TokenManager
	app     *app.Application
	ws      uuid.UUID
	credID  uuid.UUID
	logs    *bytes.Buffer
	now     time.Time
	outputs []node.NodeOutput
	inputs  []node.NodeInput
}

// newEnv connects ada's Google account through the real Google provider
// (against the mock) and bootstraps the application with Gmail.
func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, mock: gmailtest.New(), ws: uuid.New(), logs: &bytes.Buffer{}, now: time.Now()}
	t.Cleanup(e.mock.Close)
	key := make([]byte, encryption.KeySize)
	_, _ = rand.Read(key)
	enc, _ := encryption.NewAESGCM(key)
	e.repo = &memRepo{rows: map[uuid.UUID]credential.Credential{}}
	e.creds, _ = credential.NewService(e.repo, enc)
	provider, err := google.New(e.mock.GoogleOptions("https://app.example.test/api/v1/oauth/callback/google"))
	if err != nil {
		t.Fatal(err)
	}
	reg, _ := oauth.NewRegistry(provider)
	flow := oauth.NewFlow(reg, oauth.NewMemoryStateStore(), 0)
	started, err := flow.Begin(context.Background(), google.ID, e.ws, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	code, state, err := e.mock.Authorize(started.AuthorizationURL, ada)
	if err != nil {
		t.Fatal(err)
	}
	done, err := flow.Complete(context.Background(), google.ID, oauth.Callback{State: state, Code: code, Binding: started.BindingToken})
	if err != nil {
		t.Fatal(err)
	}
	c, err := e.creds.CreateOAuth(context.Background(), credential.CreateOAuthInput{WorkspaceID: e.ws, Name: "Google — ada", Provider: google.ID, Token: done.Token})
	if err != nil {
		t.Fatal(err)
	}
	e.credID = c.ID
	e.tm = oauth.NewTokenManager(e.creds, reg, nil, slog.New(slog.NewJSONHandler(e.logs, nil))).WithClock(func() time.Time { return e.now })
	e.app, err = app.BootstrapWith(config.Config{}, app.Dependencies{Credentials: e.tm, Gmail: e.mock.GmailOptions()})
	if err != nil {
		t.Fatal(err)
	}
	e.mock.AddMessage(gmailtest.Message("m-1", "t-1", map[string]string{
		"From": "Grace <grace@example.test>", "To": "ada@gmail.example", "Subject": "Invoice October",
		"Date": "Tue, 06 Oct 2026 10:00:00 +0000", "Message-ID": "<inv-1@mail.example>", "References": "<thread-start@mail.example>",
		"Reply-To": "billing@example.test",
	}, "Please find the invoice attached."))
	e.mock.AddMessage(gmailtest.Message("m-2", "t-2", map[string]string{
		"From": "news@example.test", "To": "ada@gmail.example", "Subject": "Weekly news",
	}, "", gmailtest.Part("multipart/alternative", "", "", gmailtest.Part("text/plain", "", "News in text"), gmailtest.Part("text/html", "", "<p>News</p>")),
		gmailtest.Part("image/png", "chart.png", "PNGDATA")))
	return e
}

func (e *env) NodeStarted(_ context.Context, _, _ string, in node.NodeInput) error {
	e.inputs = append(e.inputs, in)
	return nil
}
func (e *env) NodeFinished(_ context.Context, _ string, out node.NodeOutput, _ error) error {
	e.outputs = append(e.outputs, out)
	return nil
}
func (e *env) NodeFailedBeforeExecute(context.Context, string, string, execution.Stage, error) error {
	return nil
}

var pos = &workflow.Position{}

// single is input -> one Gmail node (config) -> output of port.
func (e *env) single(typ, port string, cfg map[string]any) workflow.Definition {
	cfg["credential_id"] = e.credID.String()
	def, _ := e.app.NodeRegistry.GetDefinition(typ)
	in := def.Inputs[0].Name
	return workflow.Definition{Version: workflow.DefinitionSchemaVersion, Settings: map[string]any{},
		Nodes: []workflow.Node{
			{ID: "in", Type: "input", Name: "Input", Position: pos, Config: map[string]any{}},
			{ID: "g", Type: typ, Name: "Gmail", Position: pos, Config: cfg},
			{ID: "out", Type: "output", Name: "Output", Position: pos, Config: map[string]any{}},
		},
		// The input object is connected to a port the node reads only as
		// text; a non-string value falls back to the config.
		Edges: []workflow.Edge{
			{ID: "e1", Source: "in", SourcePort: "data", Target: "g", TargetPort: in},
			{ID: "e2", Source: "g", SourcePort: port, Target: "out", TargetPort: "value"},
		}}
}

func (e *env) run(def workflow.Definition, input map[string]any, ws uuid.UUID) (execution.ExecutionResult, error) {
	e.t.Helper()
	if res := workflow.NewValidator(e.app.NodeRegistry).Validate(def); !res.Valid {
		e.t.Fatalf("invalid workflow: %+v", res.Errors)
	}
	return execution.NewGraphExecutor(e.app.NodeRegistry).ExecuteWithOptions(context.Background(), def, input,
		execution.ExecuteOptions{Observer: e, Scope: node.Scope{WorkspaceID: ws}, OperationKey: "exec-1"})
}

// result runs a single-node workflow and returns all of the node's ports.
func (e *env) result(typ string, cfg map[string]any, input map[string]any) (map[string]any, error) {
	e.t.Helper()
	def, _ := e.app.NodeRegistry.GetDefinition(typ)
	_, err := e.run(e.single(typ, def.Outputs[0].Name, cfg), input, e.ws)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	for k, v := range e.outputs[len(e.outputs)-2].Ports {
		out[k] = v.Data
	}
	return out, nil
}

func failure(t *testing.T, err error) execution.ExecutionError {
	t.Helper()
	if err == nil {
		t.Fatal("expected a failure")
	}
	return execution.ErrorFromExecution(err)
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestGmailMetadata(t *testing.T) {
	e := newEnv(t)
	want := map[string]struct {
		name string
		side node.SideEffects
	}{
		"gmail.search": {"Gmail: Search Emails", node.SideEffectsNone}, "gmail.read": {"Gmail: Read Email", node.SideEffectsNone},
		"gmail.create_draft": {"Gmail: Create Draft", node.SideEffectsUnsafe}, "gmail.send": {"Gmail: Send Email", node.SideEffectsUnsafe},
		"gmail.reply": {"Gmail: Reply", node.SideEffectsUnsafe},
	}
	for typ, w := range want {
		d, err := e.app.NodeRegistry.GetDefinition(typ)
		if err != nil {
			t.Fatal(err)
		}
		if d.Name != w.name || d.SideEffects != w.side || d.Category != node.CategoryIntegration ||
			d.Integration == nil || d.Integration.Category != "Google" || d.Integration.Name != "Gmail" ||
			d.Auth == nil || *d.Auth != (node.AuthRequirement{Required: true, Provider: "google", CredentialType: "OAUTH2"}) {
			t.Fatalf("%s: %+v", typ, d)
		}
		if d.Config[0].Name != "credential_id" || !d.Config[0].Required {
			t.Fatalf("%s: the account comes first", typ)
		}
	}
	in, a, err := e.app.IntegrationRegistry.Action("gmail.send")
	if err != nil || in.Auth.Provider != "google" || a.SideEffects != node.SideEffectsUnsafe {
		t.Fatal(err)
	}
}

func TestSearch(t *testing.T) {
	e := newEnv(t)
	out, err := e.result("gmail.search", map[string]any{"query": "from:grace is:unread", "max_results": 5.0}, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	msgs := out["messages"].([]any)
	if out["count"] != 1.0 || len(msgs) != 1 {
		t.Fatalf("search: %v", out)
	}
	want := map[string]any{"message_id": "m-1", "thread_id": "t-1", "sender": "Grace <grace@example.test>", "subject": "Invoice October",
		"snippet": "Please find the invoice attached.", "timestamp": "2026-10-07T11:00:00Z", "unread": true}
	if mustJSON(msgs[0]) != mustJSON(want) {
		t.Fatalf("normalized message:\n got %s\nwant %s", mustJSON(msgs[0]), mustJSON(want))
	}
	reqs := e.mock.Requests()
	if reqs[0].Query.Get("q") != "from:grace is:unread" || reqs[0].Query.Get("maxResults") != "5" || reqs[1].Query.Get("format") != "metadata" || !reqs[0].Authorized {
		t.Fatalf("requests: %+v", reqs)
	}
	// Empty result.
	out, err = e.result("gmail.search", map[string]any{"query": "from:nobody"}, map[string]any{})
	if err != nil || out["count"] != 0.0 || len(out["messages"].([]any)) != 0 {
		t.Fatalf("empty: %v %v", out, err)
	}
	res := workflow.NewValidator(e.app.NodeRegistry).Validate(e.single("gmail.search", "messages", map[string]any{"max_results": 51.0}))
	if res.Valid || res.Errors[0].Port != "max_results" {
		t.Fatalf("max_results is bounded by the validator: %+v", res.Errors)
	}
}

func TestReadPlainAndMultipart(t *testing.T) {
	e := newEnv(t)
	out, err := e.result("gmail.read", map[string]any{"message_id": "m-1"}, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if out["message_id"] != "m-1" || out["thread_id"] != "t-1" || out["sender"] != "Grace <grace@example.test>" ||
		out["subject"] != "Invoice October" || out["body"] != "Please find the invoice attached." ||
		mustJSON(out["recipients"]) != `["ada@gmail.example"]` || mustJSON(out["attachments"]) != `[]` || out["timestamp"] != "2026-10-07T11:00:00Z" {
		t.Fatalf("plain: %v", out)
	}
	if q := e.mock.Requests()[0].Query; q.Get("format") != "full" {
		t.Fatalf("read uses format=full: %v", q)
	}
	out, err = e.result("gmail.read", map[string]any{"message_id": "{{input.id}}"}, map[string]any{"id": "m-2"})
	if err != nil {
		t.Fatal(err)
	}
	if out["body"] != "News in text" || mustJSON(out["attachments"]) != `[{"attachment_id":"att-chart.png","filename":"chart.png","mime_type":"image/png","size":7}]` {
		t.Fatalf("multipart: %v", out)
	}
	if len(out) != 8 {
		t.Fatalf("exactly the documented fields: %v", out)
	}
}

func decodeSent(t *testing.T, s gmailtest.Sent) (*mail.Message, string) {
	t.Helper()
	m, err := mail.ReadMessage(strings.NewReader(s.RFC822))
	if err != nil {
		t.Fatalf("not RFC 5322: %v", err)
	}
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(m.Body)
	body, _ := base64.StdEncoding.DecodeString(strings.ReplaceAll(buf.String(), "\r\n", ""))
	return m, string(body)
}

func TestCreateDraftAndSend(t *testing.T) {
	e := newEnv(t)
	input := map[string]any{"customer": map[string]any{"email": "cust@example.test"}, "subject": "Your order", "body": "It shipped."}
	out, err := e.result("gmail.create_draft", map[string]any{"to": "{{input.customer.email}}", "cc": "boss@example.test",
		"subject": "{{input.subject}}", "body": "{{input.body}}"}, input)
	if err != nil {
		t.Fatal(err)
	}
	if mustJSON(out) != `{"draft_id":"r-draft-1","message_id":"draft-msg-1","status":"draft"}` {
		t.Fatalf("draft output: %v", out)
	}
	sent := e.mock.Sent()
	m, body := decodeSent(t, sent[0])
	if sent[0].Kind != "draft" || m.Header.Get("To") != "cust@example.test" || m.Header.Get("Cc") != "boss@example.test" ||
		m.Header.Get("Subject") != "Your order" || body != "It shipped." {
		t.Fatalf("draft MIME: %v %q", m.Header, body)
	}

	out, err = e.result("gmail.send", map[string]any{"to": "a@example.test, B <b@example.test>", "bcc": "audit@example.test", "subject": "Hello", "body": "Hi there"}, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if mustJSON(out) != `{"message_id":"sent-2","status":"sent","thread_id":"thread-sent-2"}` {
		t.Fatalf("send output: %v", out)
	}
	m, body = decodeSent(t, e.mock.Sent()[1])
	if e.mock.Sent()[1].Kind != "send" || m.Header.Get("To") != `a@example.test, "B" <b@example.test>` || m.Header.Get("Bcc") != "audit@example.test" || body != "Hi there" {
		t.Fatalf("send MIME: %v %q", m.Header, body)
	}
	// Input validation happens before any request.
	before := len(e.mock.Requests())
	for _, cfg := range []map[string]any{{"to": "", "subject": "x"}, {"to": "not an address"}, {"to": "a@example.test", "subject": "x\r\nBcc: evil@example.test"}} {
		_, err := e.result("gmail.send", cfg, map[string]any{})
		if f := failure(t, err); f.Code != "INVALID_REQUEST" && f.Code != "INVALID_NODE_CONFIG" || f.Retryable {
			t.Fatalf("%v: %+v", cfg, f)
		}
	}
	if len(e.mock.Requests()) != before {
		t.Fatal("invalid input must not reach Gmail")
	}
}

func TestReplyThreadsCorrectly(t *testing.T) {
	e := newEnv(t)
	out, err := e.result("gmail.reply", map[string]any{"message_id": "m-1", "body": "Paid, thanks!", "cc": "accounting@example.test"}, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if mustJSON(out) != `{"message_id":"sent-1","status":"sent","thread_id":"t-1"}` {
		t.Fatalf("reply output: %v", out)
	}
	s := e.mock.Sent()[0]
	m, body := decodeSent(t, s)
	if s.ThreadID != "t-1" || m.Header.Get("To") != "billing@example.test" || m.Header.Get("Cc") != "accounting@example.test" ||
		m.Header.Get("Subject") != "Re: Invoice October" || m.Header.Get("In-Reply-To") != "<inv-1@mail.example>" ||
		m.Header.Get("References") != "<thread-start@mail.example> <inv-1@mail.example>" || body != "Paid, thanks!" {
		t.Fatalf("reply: thread %q headers %v body %q", s.ThreadID, m.Header, body)
	}
	if f := failure(t, func() error {
		_, err := e.result("gmail.reply", map[string]any{"message_id": "missing", "body": "x"}, map[string]any{})
		return err
	}()); f.Code != "RESOURCE_NOT_FOUND" {
		t.Fatalf("reply to a missing message: %+v", f)
	}
}

func TestGmailErrorMapping(t *testing.T) {
	cases := []struct {
		name      string
		typ       string
		cfg       map[string]any
		failure   gmailtest.Failure
		code      string
		retryable bool
		after     time.Duration
	}{
		{"401", "gmail.search", map[string]any{}, gmailtest.Failure{Status: 401, Body: `{"error":{"code":401,"message":"ya29.SHOULD-NOT-LEAK"}}`}, "AUTHENTICATION_FAILED", false, 0},
		{"403 permission", "gmail.search", map[string]any{}, gmailtest.Failure{Status: 403, Body: `{"error":{"errors":[{"reason":"insufficientPermissions"}]}}`}, "PERMISSION_DENIED", false, 0},
		{"403 rate limit", "gmail.search", map[string]any{}, gmailtest.Failure{Status: 403, Body: `{"error":{"errors":[{"reason":"userRateLimitExceeded"}]}}`}, "RATE_LIMITED", true, 0},
		{"404", "gmail.read", map[string]any{"message_id": "nope"}, gmailtest.Failure{}, "RESOURCE_NOT_FOUND", false, 0},
		{"400", "gmail.search", map[string]any{}, gmailtest.Failure{Status: 400, Body: `{"error":{"errors":[{"reason":"invalidArgument"}]}}`}, "INVALID_REQUEST", false, 0},
		{"429", "gmail.search", map[string]any{}, gmailtest.Failure{Status: 429, RetryAfter: "7"}, "RATE_LIMITED", true, 7 * time.Second},
		{"500 search", "gmail.search", map[string]any{}, gmailtest.Failure{Status: 500}, "PROVIDER_UNAVAILABLE", true, 0},
		// Unsafe: a 5xx may have sent the mail, so it is not repeated...
		{"503 send", "gmail.send", map[string]any{"to": "a@example.test"}, gmailtest.Failure{Status: 503}, "PROVIDER_UNAVAILABLE", false, 0},
		// ...but a 429 was refused without sending, so it may be.
		{"429 send", "gmail.send", map[string]any{"to": "a@example.test"}, gmailtest.Failure{Status: 429, RetryAfter: "3"}, "RATE_LIMITED", true, 3 * time.Second},
		{"malformed", "gmail.read", map[string]any{"message_id": "m-1"}, gmailtest.Failure{Status: 200, Body: `{"id": "m-1", "payload": `}, "MALFORMED_RESPONSE", false, 0},
		{"no payload", "gmail.read", map[string]any{"message_id": "m-1"}, gmailtest.Failure{Status: 200, Body: `{"id": "m-1"}`}, "MALFORMED_RESPONSE", false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			if c.failure.Status != 0 {
				e.mock.Fail("/gmail/", c.failure)
			}
			_, err := e.result(c.typ, c.cfg, map[string]any{})
			f := failure(t, err)
			if f.Code != c.code || f.Retryable != c.retryable || f.RetryAfter != c.after || f.Source != node.SourceProvider {
				t.Fatalf("got %+v", f)
			}
			for _, s := range []string{"SHOULD-NOT-LEAK", gmailtest.AccessPrefix, gmailtest.RefreshPrefix} {
				if strings.Contains(err.Error(), s) || strings.Contains(mustJSON(f), s) {
					t.Fatalf("leak %q: %v", s, err)
				}
			}
		})
	}
	t.Run("timeout", func(t *testing.T) {
		e := newEnv(t)
		o := e.mock.GmailOptions()
		o.Timeout = 50 * time.Millisecond
		a, err := app.BootstrapWith(config.Config{}, app.Dependencies{Credentials: e.tm, Gmail: o})
		if err != nil {
			t.Fatal(err)
		}
		e.app = a
		e.mock.Fail("/gmail/", gmailtest.Failure{Delay: time.Second})
		_, err = e.result("gmail.search", map[string]any{}, map[string]any{})
		if f := failure(t, err); f.Code != "TIMEOUT" || !f.Retryable {
			t.Fatalf("got %+v", f)
		}
	})
}

func TestCredentialPath(t *testing.T) {
	t.Run("expired access token is refreshed by the token manager", func(t *testing.T) {
		e := newEnv(t)
		_, initial, _ := e.creds.ResolveOAuth(context.Background(), e.ws, e.credID, "google")
		e.now = e.now.Add(2 * time.Hour)
		if _, err := e.result("gmail.search", map[string]any{}, map[string]any{}); err != nil {
			t.Fatal(err)
		}
		if e.mock.Count("token:refresh_token") != 1 {
			t.Fatal("one refresh")
		}
		_, after, _ := e.creds.ResolveOAuth(context.Background(), e.ws, e.credID, "google")
		if after.AccessToken.Reveal() == initial.AccessToken.Reveal() || after.RefreshToken.Reveal() != initial.RefreshToken.Reveal() {
			t.Fatal("new access token stored; Google's omitted refresh token kept")
		}
		if !strings.Contains(e.logs.String(), "connected_account_refreshed") {
			t.Fatal("refresh logged")
		}
	})
	t.Run("revoked account", func(t *testing.T) {
		e := newEnv(t)
		e.mock.RevokeAccount(ada.Sub)
		e.now = e.now.Add(2 * time.Hour)
		_, err := e.result("gmail.send", map[string]any{"to": "a@example.test"}, map[string]any{})
		if f := failure(t, err); f.Code != "CREDENTIAL_REVOKED" || f.Retryable {
			t.Fatalf("got %+v", f)
		}
		if len(e.mock.Requests()) != 0 {
			t.Fatal("no Gmail call without a valid token")
		}
	})
	t.Run("another workspace cannot use the account", func(t *testing.T) {
		e := newEnv(t)
		_, err := e.run(e.single("gmail.search", "messages", map[string]any{}), map[string]any{}, uuid.New())
		if f := failure(t, err); f.Code != "CREDENTIAL_NOT_FOUND" || f.Retryable {
			t.Fatalf("got %+v", f)
		}
		if len(e.mock.Requests()) != 0 {
			t.Fatal("no Gmail call")
		}
	})
}

func TestNoTokenLeavesTheBackendPath(t *testing.T) {
	e := newEnv(t)
	def := e.single("gmail.read", "body", map[string]any{"message_id": "m-1"})
	res, err := e.run(def, map[string]any{}, e.ws)
	if err != nil {
		t.Fatal(err)
	}
	e.mock.Fail("/gmail/", gmailtest.Failure{Status: 401, Body: `{"error":{"message":"ya29.SHOULD-NOT-LEAK"}}`})
	_, failed := e.run(def, map[string]any{}, e.ws)
	for where, text := range map[string]string{
		"workflow JSON": mustJSON(def), "node inputs": mustJSON(e.inputs), "node outputs": mustJSON(e.outputs),
		"result": mustJSON(res), "error": failed.Error(), "execution error": mustJSON(execution.ErrorFromExecution(failed)), "logs": e.logs.String(),
	} {
		for _, s := range []string{gmailtest.AccessPrefix, gmailtest.RefreshPrefix, gmailtest.ClientSecret, "SHOULD-NOT-LEAK", "Bearer"} {
			if strings.Contains(text, s) {
				t.Errorf("%q in %s", s, where)
			}
		}
	}
	// What a Gmail request carried: an authorized bearer token (checked by
	// the mock), nothing else secret.
	for _, r := range e.mock.Requests() {
		if !r.Authorized && r.Path != "/gmail/v1/users/me/messages/m-1" {
			t.Fatalf("unauthorized request: %+v", r)
		}
	}
	_ = gmail.ID
}
