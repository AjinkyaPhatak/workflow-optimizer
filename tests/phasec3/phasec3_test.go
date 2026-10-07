package phasec3_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
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
	"workflow-optimizer/internal/integration/gmail/gmailtest"
	"workflow-optimizer/internal/oauth"
	"workflow-optimizer/internal/oauth/google"
)

// Phase C3: Gmail through the production composition (app.NewAPIWith,
// app.NewWorkerWith) against real PostgreSQL and Redis. Google's OAuth
// endpoints and the Gmail API are the in-process mock (gmailtest): no real
// Google request is ever made.

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

const callbackURL = "http://localhost:3000/api/v1/oauth/callback/google"

type env struct {
	t    *testing.T
	srv  *httptest.Server
	raw  *pgxpool.Pool
	mock *gmailtest.Server
	logs *syncBuffer
	mu   sync.Mutex
	seen []string
}

// newEnv starts API (and, with worker, a worker). googleCfg puts the Google
// OAuth client in the configuration (real endpoints, never called);
// useMock installs the Google provider and Gmail client against the mock.
func newEnv(t *testing.T, googleCfg, useMock, worker bool) *env {
	t.Helper()
	base, redisURL := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_REDIS_URL")
	if base == "" || redisURL == "" {
		t.Skip("TEST_DATABASE_URL and TEST_REDIS_URL are required for Phase C3 tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := "phasec3_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
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

	prefix := "test:phasec3:" + uuid.NewString()
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
		APIAddr:                 "127.0.0.1:0",
		AuthTokenSecret:         base64.StdEncoding.EncodeToString(secret),
		AuthTokenTTL:            time.Hour,
		ExposeNodeData:          true,
	}
	if googleCfg {
		cfg.GoogleClientID, cfg.GoogleClientSecret, cfg.GoogleOAuthRedirectURI = "real-id.apps.googleusercontent.com", "real-secret-DO-NOT-LEAK", callbackURL
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

	e := &env{t: t, raw: raw, logs: &syncBuffer{}}
	var ext app.Extensions
	if useMock {
		e.mock = gmailtest.New()
		t.Cleanup(e.mock.Close)
		p, err := google.New(e.mock.GoogleOptions(callbackURL))
		if err != nil {
			t.Fatal(err)
		}
		ext = app.Extensions{OAuthProviders: []oauth.Provider{p}, Gmail: e.mock.GmailOptions()}
	}
	logger := slog.New(slog.NewJSONHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	rt, err := app.NewAPIWith(ctx, cfg, logger, ext)
	if err != nil {
		t.Fatal(err)
	}
	e.srv = httptest.NewServer(rt.Handler())
	t.Cleanup(func() { e.srv.Close(); rt.Close() })
	if worker {
		wctx, cancel := context.WithCancel(context.Background())
		w, err := app.NewWorkerWith(wctx, cfg, logger, ext)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() { defer close(done); _ = w.Run(wctx) }()
		t.Cleanup(func() { cancel(); <-done })
	}
	return e
}

type resp struct {
	status int
	body   []byte
	header http.Header
}

func (e *env) browser() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (e *env) do(client *http.Client, token, method, path string, body any) resp {
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
	r, err := client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	e.mu.Lock()
	e.seen = append(e.seen, string(b))
	for k, vs := range r.Header {
		if k != "Set-Cookie" {
			e.seen = append(e.seen, k+": "+strings.Join(vs, ","))
		}
	}
	e.mu.Unlock()
	return resp{status: r.StatusCode, body: b, header: r.Header}
}

func (e *env) call(token, method, path string, body any) resp {
	return e.do(http.DefaultClient, token, method, path, body)
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

type user struct{ token, workspace string }

func (e *env) register(name string) user {
	reg := e.must(e.call("", "POST", "/api/v1/auth/register", map[string]any{
		"email": name + "-" + uuid.NewString()[:8] + "@example.test", "name": name, "password": "password-" + name}), 201)
	return user{token: reg["access_token"].(string), workspace: reg["workspace"].(map[string]any)["id"].(string)}
}

var ada = gmailtest.Account{Sub: "108234567890123456789", Email: "ada@gmail.example", Name: "Ada Lovelace"}

// connect runs the browser flow against the mock Google consent page.
func (e *env) connect(u user, who gmailtest.Account) *url.URL {
	e.t.Helper()
	b := e.browser()
	start := e.must(e.do(b, u.token, "POST", "/api/v1/connected-accounts/google/authorize", map[string]any{"workspace_id": u.workspace}), 200)
	code, state, err := e.mock.Authorize(start["authorization_url"].(string), who)
	if err != nil {
		e.t.Fatal(err)
	}
	r := e.do(b, "", "GET", "/api/v1/oauth/callback/google?"+url.Values{"state": {state}, "code": {code}, "scope": {"ignored"}}.Encode(), nil)
	if r.status != http.StatusSeeOther {
		e.t.Fatalf("callback: %d %s", r.status, r.body)
	}
	loc, _ := url.Parse(r.header.Get("Location"))
	return loc
}

func TestGoogleIsOfferedOnlyWhenConfigured(t *testing.T) {
	off := newEnv(t, false, false, false)
	u := off.register("a")
	if items := off.must(off.call(u.token, "GET", "/api/v1/oauth/providers", nil), 200)["items"].([]any); len(items) != 0 {
		t.Fatalf("no provider without configuration: %v", items)
	}
	if r := off.call(u.token, "POST", "/api/v1/connected-accounts/google/authorize", map[string]any{"workspace_id": u.workspace}); r.status != 404 {
		t.Fatalf("authorize without configuration: %d", r.status)
	}
	// Gmail nodes are in the catalog either way; they need a Google account.
	nodes := off.must(off.call(u.token, "GET", "/api/v1/nodes", nil), 200)["items"].([]any)
	gmailNodes := 0
	for _, n := range nodes {
		d := n.(map[string]any)
		if strings.HasPrefix(d["type"].(string), "gmail.") {
			gmailNodes++
			if d["auth"].(map[string]any)["provider"] != "google" || d["integration"].(map[string]any)["category"] != "Google" {
				t.Fatalf("gmail metadata: %v", d)
			}
		}
	}
	if gmailNodes != 5 {
		t.Fatalf("gmail nodes: %d", gmailNodes)
	}

	on := newEnv(t, true, false, false)
	u = on.register("b")
	items := on.must(on.call(u.token, "GET", "/api/v1/oauth/providers", nil), 200)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != "google" || items[0].(map[string]any)["name"] != "Google" {
		t.Fatalf("providers: %v", items)
	}
	start := on.must(on.call(u.token, "POST", "/api/v1/connected-accounts/google/authorize", map[string]any{"workspace_id": u.workspace}), 200)
	authURL, _ := url.Parse(start["authorization_url"].(string))
	q := authURL.Query()
	if authURL.Host != "accounts.google.com" || q.Get("client_id") != "real-id.apps.googleusercontent.com" || q.Get("redirect_uri") != callbackURL ||
		q.Get("access_type") != "offline" || q.Get("code_challenge_method") != "S256" ||
		q.Get("scope") != "openid email https://www.googleapis.com/auth/gmail.readonly https://www.googleapis.com/auth/gmail.compose" {
		t.Fatalf("authorization URL: %s", authURL)
	}
	on.mu.Lock()
	defer on.mu.Unlock()
	if strings.Contains(strings.Join(on.seen, "\n"), "real-secret-DO-NOT-LEAK") {
		t.Fatal("the client secret reached an API response")
	}
}

func node(id, typ string, cfg map[string]any) map[string]any {
	return map[string]any{"id": id, "type": typ, "name": id, "position": map[string]any{"x": 0, "y": 0}, "config": cfg}
}

func edge(id, src, sp, dst, dp string) map[string]any {
	return map[string]any{"id": id, "source": src, "source_port": sp, "target": dst, "target_port": dp}
}

func TestGmailEndToEnd(t *testing.T) {
	e := newEnv(t, false, true, true)
	e.mock.AddMessage(gmailtest.Message("m-1", "t-1", map[string]string{
		"From": "Grace <grace@example.test>", "To": "ada@gmail.example", "Subject": "Invoice October",
		"Date": "Tue, 06 Oct 2026 10:00:00 +0000", "Message-ID": "<inv-1@mail.example>",
	}, "Please pay the invoice."))
	a, b := e.register("a"), e.register("b")

	// --- connect a Google account (short-lived token: the first run refreshes)
	e.mock.AccessTTL = time.Second
	loc := e.connect(a, ada)
	e.mock.AccessTTL = time.Hour
	if loc.Query().Get("connected") != "google" {
		t.Fatalf("redirect: %s", loc)
	}
	accounts := e.must(e.call(a.token, "GET", "/api/v1/connected-accounts?workspace_id="+a.workspace, nil), 200)["items"].([]any)
	acct := accounts[0].(map[string]any)
	if len(accounts) != 1 || acct["provider"] != "google" || acct["provider_name"] != "Google" || acct["email"] != ada.Email || acct["status"] != "ACTIVE" {
		t.Fatalf("accounts: %v", accounts)
	}
	var stableID string
	_ = e.raw.QueryRow(context.Background(), "SELECT provider_account_id FROM connected_accounts").Scan(&stableID)
	if stableID != ada.Sub {
		t.Fatalf("the stable identity is Google's sub, got %q", stableID)
	}
	cred := acct["credential_id"].(string)

	// --- search -> pick the first message -> read -> reply -------------------
	def := map[string]any{"version": 1, "settings": map[string]any{}, "nodes": []any{
		node("in", "input", map[string]any{}),
		node("search", "gmail.search", map[string]any{"credential_id": cred, "query": "from:grace", "max_results": 5}),
		node("first", "transform", map[string]any{"path": "0.message_id"}),
		node("read", "gmail.read", map[string]any{"credential_id": cred}),
		node("reply", "gmail.reply", map[string]any{"credential_id": cred, "message_id": "{{read.message_id}}", "body": "Paid: {{read.subject}}"}),
		node("out", "output", map[string]any{}),
	}, "edges": []any{
		edge("e1", "in", "data", "search", "query"),
		edge("e2", "search", "messages", "first", "input"),
		edge("e3", "first", "output", "read", "message_id"),
		edge("e4", "read", "message_id", "reply", "message_id"),
		edge("e5", "reply", "message_id", "out", "value"),
	}}
	project := e.must(e.call(a.token, "POST", "/api/v1/projects", map[string]any{"workspace_id": a.workspace, "name": "P"}), 201)["id"].(string)
	wf := e.must(e.call(a.token, "POST", "/api/v1/workflows", map[string]any{"project_id": project, "name": "Invoices"}), 201)["id"].(string)
	v := e.must(e.call(a.token, "POST", "/api/v1/workflows/"+wf+"/versions", map[string]any{"definition": def}), 201)["id"].(string)
	e.must(e.call(a.token, "POST", "/api/v1/workflows/"+wf+"/versions/"+v+"/publish", nil), 200)
	time.Sleep(1100 * time.Millisecond) // the access token has expired
	run := func(workflowID string) (map[string]any, string) {
		id := e.must(e.call(a.token, "POST", "/api/v1/workflows/"+workflowID+"/execute", map[string]any{"input": map[string]any{}}), 202)["execution_id"].(string)
		deadline := time.Now().Add(30 * time.Second)
		for {
			ex := e.must(e.call(a.token, "GET", "/api/v1/executions/"+id, nil), 200)
			if s := ex["status"].(string); s == "COMPLETED" || s == "FAILED" {
				e.call(a.token, "GET", "/api/v1/executions/"+id+"/nodes", nil)
				e.call(a.token, "GET", "/api/v1/executions/"+id+"/events", nil)
				return ex, id
			}
			if time.Now().After(deadline) {
				t.Fatalf("execution %s still running", id)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	ex, exID := run(wf)
	if ex["status"] != "COMPLETED" {
		t.Fatalf("execution: %v", ex)
	}
	if e.mock.Count("token:refresh_token") != 1 {
		t.Fatalf("the expired token is refreshed once by the backend: %d", e.mock.Count("token:refresh_token"))
	}
	sent := e.mock.Sent()
	if len(sent) != 1 || sent[0].ThreadID != "t-1" || !strings.Contains(sent[0].RFC822, "Subject: Re: Invoice October") ||
		!strings.Contains(sent[0].RFC822, "In-Reply-To: <inv-1@mail.example>") || !strings.Contains(sent[0].RFC822, `To: "Grace" <grace@example.test>`) {
		t.Fatalf("reply: %+v", sent)
	}
	nodes := e.must(e.call(a.token, "GET", "/api/v1/executions/"+exID+"/nodes", nil), 200)
	if !strings.Contains(mustJSON(nodes), "Please pay the invoice.") || !strings.Contains(mustJSON(nodes), `"subject":"Invoice October"`) {
		t.Fatalf("read output reaches the workflow: %s", mustJSON(nodes)[:400])
	}

	// --- send and draft --------------------------------------------------------
	sendDef := map[string]any{"version": 1, "settings": map[string]any{}, "nodes": []any{
		node("in", "input", map[string]any{}),
		node("draft", "gmail.create_draft", map[string]any{"credential_id": cred, "to": "c@example.test", "subject": "Draft", "body": "Later"}),
		node("send", "gmail.send", map[string]any{"credential_id": cred, "to": "c@example.test", "subject": "Hello", "body": "Now"}),
		node("out", "output", map[string]any{}),
	}, "edges": []any{edge("e1", "in", "data", "draft", "body"), edge("e2", "draft", "draft_id", "send", "body"), edge("e3", "send", "status", "out", "value")}}
	// (draft_id flows into send's body input only to order the nodes; the
	// connected input overrides the Body setting.)
	wf2 := e.must(e.call(a.token, "POST", "/api/v1/workflows", map[string]any{"project_id": project, "name": "Mail"}), 201)["id"].(string)
	v2 := e.must(e.call(a.token, "POST", "/api/v1/workflows/"+wf2+"/versions", map[string]any{"definition": sendDef}), 201)["id"].(string)
	e.must(e.call(a.token, "POST", "/api/v1/workflows/"+wf2+"/versions/"+v2+"/publish", nil), 200)
	if ex, _ := run(wf2); ex["status"] != "COMPLETED" || !strings.Contains(mustJSON(ex), `"sent"`) {
		t.Fatalf("send: %v", ex)
	}
	if s := e.mock.Sent(); len(s) != 3 || s[1].Kind != "draft" || s[2].Kind != "send" {
		t.Fatalf("draft then send: %+v", s)
	}

	// --- workspace isolation -------------------------------------------------------
	projectB := e.must(e.call(b.token, "POST", "/api/v1/projects", map[string]any{"workspace_id": b.workspace, "name": "P"}), 201)["id"].(string)
	wfB := e.must(e.call(b.token, "POST", "/api/v1/workflows", map[string]any{"project_id": projectB, "name": "Steal"}), 201)["id"].(string)
	if r := e.call(b.token, "POST", "/api/v1/workflows/"+wfB+"/versions", map[string]any{"definition": def}); r.status != 422 || !strings.Contains(string(r.body), "credential not found in this workspace") {
		t.Fatalf("workspace B using A's Gmail account: %d %s", r.status, r.body)
	}
	if r := e.call(b.token, "GET", "/api/v1/connected-accounts/"+acct["id"].(string), nil); r.status != 404 {
		t.Fatalf("B reads A's account: %d", r.status)
	}

	// --- disconnect: revoked at Google; runs fail with CREDENTIAL_REVOKED --------------
	dis := e.must(e.call(a.token, "DELETE", "/api/v1/connected-accounts/"+acct["id"].(string), nil), 200)
	if dis["status"] != "DISCONNECTED" || e.mock.Count("revoke") != 1 {
		t.Fatalf("disconnect: %v revoke=%d", dis, e.mock.Count("revoke"))
	}
	if ex, _ := run(wf2); ex["status"] != "FAILED" || !strings.Contains(mustJSON(ex), "CREDENTIAL_REVOKED") {
		t.Fatalf("after disconnect: %v", ex)
	}
	// --- reconnect the same Google account: same account, works again --------------
	loc = e.connect(a, ada)
	if loc.Query().Get("account") != acct["id"] {
		t.Fatalf("reconnect reuses the account: %s", loc)
	}
	if ex, _ := run(wf2); ex["status"] != "COMPLETED" {
		t.Fatalf("after reconnect: %v", ex)
	}

	// --- Google revokes access on its side: the next refresh fails, final ------------
	// Reconnect with a 1-second access token, then revoke at Google: the
	// next run must refresh, and the refresh gets invalid_grant.
	e.mock.AccessTTL = time.Second
	e.connect(a, ada)
	e.mock.RevokeAccount(ada.Sub)
	time.Sleep(1100 * time.Millisecond)
	if ex, _ := run(wf2); ex["status"] != "FAILED" || !strings.Contains(mustJSON(ex), "CREDENTIAL_REVOKED") || strings.Contains(mustJSON(ex), `"retryable":true`) {
		t.Fatalf("revoked by Google: %v", ex)
	}
	if got := e.must(e.call(a.token, "GET", "/api/v1/connected-accounts/"+acct["id"].(string), nil), 200); got["status"] != "REVOKED" {
		t.Fatalf("account status: %v", got["status"])
	}

	// --- no secret anywhere ----------------------------------------------------------
	var dbText string
	if err := e.raw.QueryRow(context.Background(), `SELECT
		(SELECT coalesce(string_agg(row_to_json(c)::text, ' '), '') FROM credentials c) ||
		(SELECT coalesce(string_agg(row_to_json(a)::text, ' '), '') FROM connected_accounts a) ||
		(SELECT coalesce(string_agg(row_to_json(v)::text, ' '), '') FROM workflow_versions v) ||
		(SELECT coalesce(string_agg(row_to_json(x)::text, ' '), '') FROM executions x) ||
		(SELECT coalesce(string_agg(row_to_json(n)::text, ' '), '') FROM node_executions n) ||
		(SELECT coalesce(string_agg(row_to_json(ev)::text, ' '), '') FROM execution_events ev)`).Scan(&dbText); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	apiText := strings.Join(e.seen, "\n")
	e.mu.Unlock()
	for where, text := range map[string]string{"database": dbText, "API responses": apiText, "logs": e.logs.String()} {
		for _, s := range []string{gmailtest.AccessPrefix, gmailtest.RefreshPrefix, gmailtest.CodePrefix, gmailtest.ClientSecret, "SHOULD-NOT-LEAK", "code_verifier"} {
			if strings.Contains(text, s) {
				t.Errorf("%q leaked into %s", s, where)
			}
		}
	}
	if strings.Contains(dbText, "Bearer ") || strings.Contains(e.logs.String(), "Bearer ") {
		t.Error("an Authorization header value was recorded")
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
