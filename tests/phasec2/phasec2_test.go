package phasec2_test

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
	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/infrastructure/postgres"
	"workflow-optimizer/internal/integration"
	fakeint "workflow-optimizer/internal/integration/fake"
	"workflow-optimizer/internal/oauth"
	oauthfake "workflow-optimizer/internal/oauth/fake"
)

// Phase C2: connected accounts and OAuth through the production composition
// (app.NewAPIWith, app.NewWorkerWith) against real PostgreSQL and Redis.
// The OAuth provider is the in-memory fake (no network), and the integration
// is the fake test_oauth_integration whose service accepts exactly the fake
// provider's currently valid access tokens.

// syncBuffer is a goroutine-safe log sink.
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

const callbackURL = "http://app.example.test/api/v1/oauth/callback/fake_oauth"

type env struct {
	t        *testing.T
	srv      *httptest.Server
	raw      *pgxpool.Pool
	provider *oauthfake.Provider
	service  *fakeint.Service
	logs     *syncBuffer
	// seen collects every API response body, for the leak checks.
	mu   sync.Mutex
	seen []string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	base, redisURL := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_REDIS_URL")
	if base == "" || redisURL == "" {
		t.Skip("TEST_DATABASE_URL and TEST_REDIS_URL are required for Phase C2 tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := "phasec2_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
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

	prefix := "test:phasec2:" + uuid.NewString()
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
	opts, _ := goredis.ParseURL(redisURL)
	rawRedis := goredis.NewClient(opts)
	t.Cleanup(func() {
		keys, _ := rawRedis.Keys(context.Background(), prefix+"*").Result()
		if len(keys) > 0 {
			_ = rawRedis.Del(context.Background(), keys...).Err()
		}
		_ = rawRedis.Close()
	})

	provider := oauthfake.New(oauthfake.Config(callbackURL))
	service := fakeint.NewServiceFunc(provider.ValidAccessToken, map[string]any{"greeting": map[string]any{"text": "hello"}})
	ext := app.Extensions{
		OAuthProviders: []oauth.Provider{provider},
		Integrations: func(r credential.Resolver) []integration.Module {
			m, err := fakeint.OAuthModule(r, service, oauthfake.ID)
			if err != nil {
				t.Fatal(err)
			}
			return []integration.Module{m}
		},
	}
	logs := &syncBuffer{}
	logger := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	rt, err := app.NewAPIWith(ctx, cfg, logger, ext)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(rt.Handler())
	t.Cleanup(func() { srv.Close(); rt.Close() })
	wctx, cancel := context.WithCancel(context.Background())
	w, err := app.NewWorkerWith(wctx, cfg, logger, ext)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = w.Run(wctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return &env{t: t, srv: srv, raw: raw, provider: provider, service: service, logs: logs}
}

type resp struct {
	status int
	body   []byte
	header http.Header
}

// browser is a client with a cookie jar that does not follow redirects.
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
		e.seen = append(e.seen, k+": "+strings.Join(vs, ","))
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

type user struct {
	token, workspace string
}

func (e *env) register(name string) user {
	reg := e.must(e.call("", "POST", "/api/v1/auth/register", map[string]any{
		"email": name + "-" + uuid.NewString()[:8] + "@example.test", "name": name, "password": "password-" + name}), 201)
	return user{token: reg["access_token"].(string), workspace: reg["workspace"].(map[string]any)["id"].(string)}
}

// connect runs the whole browser flow: authorize -> provider consent ->
// callback with the binding cookie. It returns the redirect location.
func (e *env) connect(u user, who oauth.Identity) *url.URL {
	e.t.Helper()
	b := e.browser()
	start := e.must(e.do(b, u.token, "POST", "/api/v1/connected-accounts/fake_oauth/authorize", map[string]any{"workspace_id": u.workspace}), 200)
	code, state, err := e.provider.Approve(start["authorization_url"].(string), who)
	if err != nil {
		e.t.Fatal(err)
	}
	r := e.do(b, "", "GET", "/api/v1/oauth/callback/fake_oauth?"+url.Values{"state": {state}, "code": {code}}.Encode(), nil)
	if r.status != http.StatusSeeOther {
		e.t.Fatalf("callback: %d %s", r.status, r.body)
	}
	loc, _ := url.Parse(r.header.Get("Location"))
	return loc
}

func readWorkflow(credentialID string) map[string]any {
	return map[string]any{"version": 1, "settings": map[string]any{}, "edges": []any{
		map[string]any{"id": "e1", "source": "in", "source_port": "data", "target": "read", "target_port": "key"},
		map[string]any{"id": "e2", "source": "read", "source_port": "record", "target": "out", "target_port": "value"},
	}, "nodes": []any{
		map[string]any{"id": "in", "type": "input", "name": "Input", "position": map[string]any{"x": 0, "y": 0}, "config": map[string]any{}},
		map[string]any{"id": "read", "type": "test_oauth_integration.read", "name": "Read", "position": map[string]any{"x": 1, "y": 0},
			"config": map[string]any{"credential_id": credentialID, "key": "greeting"}},
		map[string]any{"id": "out", "type": "output", "name": "Output", "position": map[string]any{"x": 2, "y": 0}, "config": map[string]any{}},
	}}
}

var ada = oauth.Identity{ProviderAccountID: "acct-42", Email: "ada@example.test", DisplayName: "Ada"}

func TestConnectedAccountsEndToEnd(t *testing.T) {
	e := newEnv(t)
	a, b := e.register("a"), e.register("b")

	// --- providers and authentication -------------------------------------------
	provs := e.must(e.call(a.token, "GET", "/api/v1/oauth/providers", nil), 200)["items"].([]any)
	if len(provs) != 1 || provs[0].(map[string]any)["id"] != "fake_oauth" || provs[0].(map[string]any)["name"] != "Fake OAuth (test)" {
		t.Fatalf("providers: %v", provs)
	}
	for _, r := range []resp{
		e.call("", "GET", "/api/v1/connected-accounts?workspace_id="+a.workspace, nil),
		e.call("", "POST", "/api/v1/connected-accounts/fake_oauth/authorize", map[string]any{"workspace_id": a.workspace}),
		e.call("", "GET", "/api/v1/oauth/providers", nil),
	} {
		if r.status != 401 {
			t.Fatalf("unauthenticated: %d %s", r.status, r.body)
		}
	}
	if r := e.call(b.token, "POST", "/api/v1/connected-accounts/fake_oauth/authorize", map[string]any{"workspace_id": a.workspace}); r.status != 404 {
		t.Fatalf("authorize in another workspace: %d %s", r.status, r.body)
	}
	if r := e.call(a.token, "POST", "/api/v1/connected-accounts/google/authorize", map[string]any{"workspace_id": a.workspace}); r.status != 404 || !strings.Contains(string(r.body), "OAUTH_PROVIDER_NOT_FOUND") {
		t.Fatalf("unconfigured provider: %d %s", r.status, r.body)
	}

	// --- authorize sets a browser binding; the callback needs it ---------------
	br := e.browser()
	start := e.do(br, a.token, "POST", "/api/v1/connected-accounts/fake_oauth/authorize", map[string]any{"workspace_id": a.workspace})
	if start.status != 200 {
		t.Fatalf("authorize: %d %s", start.status, start.body)
	}
	cookie := start.header.Get("Set-Cookie")
	if !strings.Contains(cookie, "HttpOnly") || !strings.Contains(cookie, "SameSite=Lax") || !strings.Contains(cookie, "Path=/api/v1/oauth/callback/") {
		t.Fatalf("binding cookie: %s", cookie)
	}
	var started map[string]any
	_ = json.Unmarshal(start.body, &started)
	code, state, err := e.provider.Approve(started["authorization_url"].(string), ada)
	if err != nil {
		t.Fatal(err)
	}
	// Another browser (no binding cookie) cannot complete it, and the state
	// is spent by the attempt.
	stolen := e.do(e.browser(), "", "GET", "/api/v1/oauth/callback/fake_oauth?"+url.Values{"state": {state}, "code": {code}}.Encode(), nil)
	if loc := stolen.header.Get("Location"); stolen.status != 303 || loc != "/settings/connected-accounts?error=invalid_state" {
		t.Fatalf("callback from another browser: %d %s", stolen.status, loc)
	}
	again := e.do(br, "", "GET", "/api/v1/oauth/callback/fake_oauth?"+url.Values{"state": {state}, "code": {code}}.Encode(), nil)
	if loc := again.header.Get("Location"); loc != "/settings/connected-accounts?error=invalid_state" {
		t.Fatalf("spent state: %s", loc)
	}
	// Denial.
	denied := e.browser()
	s2 := e.must(e.do(denied, a.token, "POST", "/api/v1/connected-accounts/fake_oauth/authorize", map[string]any{"workspace_id": a.workspace}), 200)
	_, st2, _ := e.provider.Approve(s2["authorization_url"].(string), ada)
	r := e.do(denied, "", "GET", "/api/v1/oauth/callback/fake_oauth?"+url.Values{"state": {st2}, "error": {"access_denied"}, "error_description": {"<script>Bearer x</script>"}}.Encode(), nil)
	if loc := r.header.Get("Location"); loc != "/settings/connected-accounts?error=authorization_denied" || r.header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("denied: %s", loc)
	}

	// --- successful connection (with a short-lived access token) ----------------
	e.provider.AccessTTL = time.Second
	loc := e.connect(a, ada)
	e.provider.AccessTTL = time.Hour
	if loc.Path != "/settings/connected-accounts" || loc.Query().Get("connected") != "fake_oauth" || loc.Query().Get("account") == "" {
		t.Fatalf("redirect: %s", loc)
	}
	accountID := loc.Query().Get("account")
	list := e.must(e.call(a.token, "GET", "/api/v1/connected-accounts?workspace_id="+a.workspace, nil), 200)["items"].([]any)
	if len(list) != 1 {
		t.Fatalf("accounts: %v", list)
	}
	acct := list[0].(map[string]any)
	if acct["id"] != accountID || acct["status"] != "ACTIVE" || acct["email"] != "ada@example.test" || acct["provider_name"] != "Fake OAuth (test)" {
		t.Fatalf("account: %v", acct)
	}
	credID := acct["credential_id"].(string)
	e.must(e.call(a.token, "GET", "/api/v1/connected-accounts/"+accountID, nil), 200)

	// Workspace isolation.
	for _, r := range []resp{
		e.call(b.token, "GET", "/api/v1/connected-accounts/"+accountID, nil),
		e.call(b.token, "DELETE", "/api/v1/connected-accounts/"+accountID, nil),
		e.call(b.token, "GET", "/api/v1/connected-accounts?workspace_id="+a.workspace, nil),
	} {
		if r.status != 404 {
			t.Fatalf("cross-workspace access: %d %s", r.status, r.body)
		}
	}
	// The OAuth credential is managed through its account only.
	if r := e.call(a.token, "DELETE", "/api/v1/credentials/"+credID, nil); r.status != 409 || !strings.Contains(string(r.body), "CREDENTIAL_IN_USE") {
		t.Fatalf("delete OAuth credential directly: %d %s", r.status, r.body)
	}
	if r := e.call(a.token, "POST", "/api/v1/credentials", map[string]any{"workspace_id": a.workspace, "name": "x", "provider": "openai", "credential_type": "oauth2", "secret": "x"}); r.status != 400 {
		t.Fatalf("creating an OAuth credential by hand: %d %s", r.status, r.body)
	}

	// --- a workflow uses the account by credential_id ---------------------------
	project := e.must(e.call(a.token, "POST", "/api/v1/projects", map[string]any{"workspace_id": a.workspace, "name": "P"}), 201)["id"].(string)
	wf := e.must(e.call(a.token, "POST", "/api/v1/workflows", map[string]any{"project_id": project, "name": "Reader"}), 201)["id"].(string)
	v := e.must(e.call(a.token, "POST", "/api/v1/workflows/"+wf+"/versions", map[string]any{"definition": readWorkflow(credID)}), 201)["id"].(string)
	e.must(e.call(a.token, "POST", "/api/v1/workflows/"+wf+"/versions/"+v+"/publish", nil), 200)

	// Workspace B cannot attach A's account to its workflow.
	projectB := e.must(e.call(b.token, "POST", "/api/v1/projects", map[string]any{"workspace_id": b.workspace, "name": "P"}), 201)["id"].(string)
	wfB := e.must(e.call(b.token, "POST", "/api/v1/workflows", map[string]any{"project_id": projectB, "name": "Thief"}), 201)["id"].(string)
	if r := e.call(b.token, "POST", "/api/v1/workflows/"+wfB+"/versions", map[string]any{"definition": readWorkflow(credID)}); r.status != 422 || !strings.Contains(string(r.body), "credential not found in this workspace") {
		t.Fatalf("cross-workspace credential reference: %d %s", r.status, r.body)
	}

	// The access token has expired by now: the run refreshes it through the
	// backend, transparently.
	time.Sleep(1200 * time.Millisecond)
	run := func() map[string]any {
		id := e.must(e.call(a.token, "POST", "/api/v1/workflows/"+wf+"/execute", map[string]any{"input": map[string]any{}}), 202)["execution_id"].(string)
		ex := e.wait(a.token, id)
		e.call(a.token, "GET", "/api/v1/executions/"+id+"/nodes", nil)
		e.call(a.token, "GET", "/api/v1/executions/"+id+"/events", nil)
		return ex
	}
	ex := run()
	if ex["status"] != "COMPLETED" {
		t.Fatalf("execution: %v", ex)
	}
	if !strings.Contains(mustJSON(ex), `"text":"hello"`) {
		t.Fatalf("output: %v", ex)
	}
	if _, refreshes, _ := e.provider.Counts(); refreshes != 1 {
		t.Fatalf("refreshes: %d", refreshes)
	}
	calls := e.service.Calls()
	if len(calls) != 1 || !calls[0].Authorized {
		t.Fatalf("service calls: %+v", calls)
	}
	if !strings.Contains(e.logs.String(), "connected_account_refreshed") || !strings.Contains(e.logs.String(), "oauth_authorization_completed") {
		t.Fatal("OAuth lifecycle is observable in logs")
	}
	got := e.must(e.call(a.token, "GET", "/api/v1/connected-accounts/"+accountID, nil), 200)
	if got["status"] != "ACTIVE" || got["last_used_at"] == nil {
		t.Fatalf("after use: %v", got)
	}

	// --- disconnect: revoked at the provider, no further use --------------------
	dis := e.must(e.call(a.token, "DELETE", "/api/v1/connected-accounts/"+accountID, nil), 200)
	if dis["status"] != "DISCONNECTED" {
		t.Fatalf("disconnect: %v", dis)
	}
	if _, _, revocations := e.provider.Counts(); revocations != 1 {
		t.Fatal("the provider is asked to revoke")
	}
	ex = run()
	if ex["status"] != "FAILED" || !strings.Contains(mustJSON(ex), "CREDENTIAL_REVOKED") || strings.Contains(mustJSON(ex), `"retryable":true`) {
		t.Fatalf("a disconnected account cannot be used: %v", ex)
	}

	// --- reconnect the same external account: same account and credential -----
	loc = e.connect(a, oauth.Identity{ProviderAccountID: "acct-42", Email: "ada.l@example.test"})
	if loc.Query().Get("account") != accountID {
		t.Fatalf("reconnect must update the existing account: %s", loc)
	}
	list = e.must(e.call(a.token, "GET", "/api/v1/connected-accounts?workspace_id="+a.workspace, nil), 200)["items"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["status"] != "ACTIVE" || list[0].(map[string]any)["credential_id"] != credID || list[0].(map[string]any)["email"] != "ada.l@example.test" {
		t.Fatalf("after reconnect: %v", list)
	}
	if ex := run(); ex["status"] != "COMPLETED" {
		t.Fatalf("the workflow works again after reconnecting: %v", ex)
	}

	// --- nothing secret anywhere --------------------------------------------------
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
	// The authorize responses carry the provider URL, which by OAuth design
	// holds the public client ID and the flow's state; every other response
	// must hold neither.
	var others []string
	for _, s := range e.seen {
		if !strings.Contains(s, `"authorization_url"`) {
			others = append(others, s)
		}
	}
	otherText := strings.Join(others, "\n")
	e.mu.Unlock()
	for where, text := range map[string]string{"database": dbText, "API responses": apiText, "logs": e.logs.String()} {
		for _, secret := range []string{"fake-access-", "fake-refresh-", "code-", "DO-NOT-LEAK", "fake-client-secret", "code_verifier"} {
			if strings.Contains(text, secret) {
				t.Errorf("%q leaked into %s", secret, where)
			}
		}
	}
	for where, text := range map[string]string{"database": dbText, "other API responses": otherText, "logs": e.logs.String()} {
		for _, field := range []string{state, "client_id", "refresh_token", "access_token\":\"fake"} {
			if strings.Contains(text, field) {
				t.Errorf("%q appears in %s", field, where)
			}
		}
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
