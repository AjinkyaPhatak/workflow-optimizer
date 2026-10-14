package notion_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/oauth"
	"workflow-optimizer/internal/oauth/notion"
)

const (
	clientID = "notion-client-id"
	secret   = "secret_DO-NOT-LEAK"
	redirect = "http://localhost:3000/api/v1/oauth/callback/notion"
)

type call struct {
	Path, Version, User, Pass string
	Body                      map[string]string
}

// mockNotion is Notion's OAuth token and revoke endpoints.
type mockNotion struct {
	mu       sync.Mutex
	srv      *httptest.Server
	calls    []call
	status   int    // non-zero: answer the next call with this status
	errCode  string // ...and this error code
	refreshN int
}

func newMock(t *testing.T) *mockNotion {
	m := &mockNotion{}
	m.srv = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockNotion) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	user, pass, _ := r.BasicAuth()
	c := call{Path: r.URL.Path, Version: r.Header.Get("Notion-Version"), User: user, Pass: pass}
	b, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(b, &c.Body)
	m.calls = append(m.calls, c)
	w.Header().Set("Content-Type", "application/json")
	if m.status != 0 {
		w.WriteHeader(m.status)
		fmt.Fprintf(w, `{"error":%q,"error_description":"secret detail %s"}`, m.errCode, secret)
		m.status = 0
		return
	}
	if user != clientID || pass != secret {
		w.WriteHeader(401)
		_, _ = io.WriteString(w, `{"error":"invalid_client"}`)
		return
	}
	switch {
	case r.URL.Path == "/v1/oauth/token" && c.Body["grant_type"] == "authorization_code" && c.Body["code"] == "good-code":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "ntn_access_1", "refresh_token": "nrt_refresh_1", "token_type": "bearer", "bot_id": "bot-1",
			"workspace_id": "ws-123", "workspace_name": "Ada's Notion",
			"owner": map[string]any{"type": "user", "user": map[string]any{"id": "user-9", "name": "Ada", "person": map[string]any{"email": "ada@example.test"}}},
		})
	case r.URL.Path == "/v1/oauth/token" && c.Body["grant_type"] == "refresh_token" && c.Body["refresh_token"] != "":
		m.refreshN++
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": fmt.Sprintf("ntn_access_%d", m.refreshN+1),
			"refresh_token": fmt.Sprintf("nrt_refresh_%d", m.refreshN+1), "token_type": "bearer", "expires_in": 3600, "workspace_id": "ws-123", "bot_id": "bot-1"})
	case r.URL.Path == "/v1/oauth/revoke" && c.Body["token"] != "":
		_, _ = io.WriteString(w, `{"request_id":"r1"}`)
	default:
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
	}
}

func (m *mockNotion) provider(t *testing.T) *notion.Provider {
	p, err := notion.New(notion.Options{ClientID: clientID, ClientSecret: credential.NewSecret(secret), RedirectURL: redirect,
		HTTPClient: m.srv.Client(), TokenURL: m.srv.URL + "/v1/oauth/token", RevokeURL: m.srv.URL + "/v1/oauth/revoke"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func noSecret(t *testing.T, err error) {
	t.Helper()
	if err != nil && (strings.Contains(err.Error(), secret) || strings.Contains(fmt.Sprintf("%+v", err), secret)) {
		t.Fatalf("error leaks the client secret: %v", err)
	}
}

func TestConfiguration(t *testing.T) {
	for name, o := range map[string]notion.Options{
		"no secret":      {ClientID: "id", RedirectURL: redirect},
		"no id":          {ClientSecret: credential.NewSecret("s"), RedirectURL: redirect},
		"wrong callback": {ClientID: "id", ClientSecret: credential.NewSecret("s"), RedirectURL: "http://localhost:3000/api/v1/oauth/callback/google"},
		"relative":       {ClientID: "id", ClientSecret: credential.NewSecret("s"), RedirectURL: "/api/v1/oauth/callback/notion"},
	} {
		if _, err := notion.New(o); oauth.KindOf(err) != oauth.KindInvalidConfiguration {
			t.Fatalf("%s: %v", name, err)
		}
	}
	p, err := notion.New(notion.Options{ClientID: "id", ClientSecret: credential.NewSecret(secret), RedirectURL: redirect})
	if err != nil {
		t.Fatal(err)
	}
	cfg := p.Config()
	if cfg.ID != "notion" || cfg.Name != "Notion" || cfg.AuthURL != notion.DefaultAuthURL || len(cfg.Scopes) != 0 {
		t.Fatalf("config: %+v", cfg)
	}
	if b, _ := json.Marshal(cfg); strings.Contains(string(b), secret) {
		t.Fatal("the config marshals the secret")
	}
}

func TestAuthorizationURL(t *testing.T) {
	p := newMock(t).provider(t)
	raw, err := p.AuthorizationURL(oauth.AuthorizationRequest{State: "st", CodeChallenge: "ch"})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	if u.Host != "api.notion.com" || q.Get("client_id") != clientID || q.Get("redirect_uri") != redirect || q.Get("response_type") != "code" ||
		q.Get("owner") != "user" || q.Get("state") != "st" || q.Has("code_challenge") || q.Has("client_secret") {
		t.Fatalf("authorization URL: %s", raw)
	}
	if _, err := p.AuthorizationURL(oauth.AuthorizationRequest{}); err == nil {
		t.Fatal("a state is required")
	}
}

// The whole C2 flow (single-use state, browser binding) with Notion.
func TestFlowExchangeIdentifiesWorkspaceUser(t *testing.T) {
	m := newMock(t)
	p := m.provider(t)
	reg, _ := oauth.NewRegistry(p)
	flow := oauth.NewFlow(reg, oauth.NewMemoryStateStore(), 0)
	started, err := flow.Begin(context.Background(), notion.ID, uuid.New(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(started.AuthorizationURL)
	done, err := flow.Complete(context.Background(), notion.ID, oauth.Callback{State: u.Query().Get("state"), Code: "good-code", Binding: started.BindingToken})
	if err != nil {
		t.Fatal(err)
	}
	tok, id := done.Token, done.Identity
	if tok.AccessToken.Reveal() != "ntn_access_1" || tok.RefreshToken.Reveal() != "nrt_refresh_1" || !tok.Expiry.IsZero() ||
		tok.ProviderAccountID != "ws-123/user-9" || id.ProviderAccountID != "ws-123/user-9" || id.Email != "ada@example.test" || id.DisplayName != "Ada's Notion" {
		t.Fatalf("token %+v identity %+v", tok, id)
	}
	c := m.calls[0]
	if c.Version != notion.APIVersion || c.Body["redirect_uri"] != redirect || c.Body["code_verifier"] != "" {
		t.Fatalf("token request: %+v", c)
	}
}

func TestExchangeFailures(t *testing.T) {
	cases := map[string]struct {
		status  int
		errCode string
		code    string
		kind    oauth.Kind
	}{
		"bad code":      {0, "", "bad-code", oauth.KindInvalidCode},
		"bad client":    {401, "invalid_client", "good-code", oauth.KindInvalidConfiguration},
		"notion down":   {503, "", "good-code", oauth.KindProviderUnavailable},
		"rate limited":  {429, "", "good-code", oauth.KindProviderUnavailable},
		"other refusal": {400, "invalid_request", "good-code", oauth.KindTokenExchangeFailed},
	}
	for name, c := range cases {
		m := newMock(t)
		m.status, m.errCode = c.status, c.errCode
		_, _, err := m.provider(t).Exchange(context.Background(), oauth.ExchangeRequest{Code: c.code})
		if oauth.KindOf(err) != c.kind {
			t.Fatalf("%s: %v", name, err)
		}
		noSecret(t, err)
	}
}

func TestRefreshRotatesAndRevokes(t *testing.T) {
	m := newMock(t)
	p := m.provider(t)
	tok, err := p.Refresh(context.Background(), credential.OAuthToken{RefreshToken: credential.NewSecret("nrt_refresh_1")})
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken.Reveal() != "ntn_access_2" || tok.RefreshToken.Reveal() != "nrt_refresh_2" || tok.Expiry.IsZero() {
		t.Fatalf("refreshed: %+v", tok)
	}
	m.status, m.errCode = 400, "invalid_grant"
	if _, err := p.Refresh(context.Background(), credential.OAuthToken{RefreshToken: credential.NewSecret("old")}); oauth.KindOf(err) != oauth.KindRevoked {
		t.Fatalf("invalid_grant on refresh: %v", err)
	}
	if _, err := p.Refresh(context.Background(), credential.OAuthToken{}); oauth.KindOf(err) != oauth.KindRevoked {
		t.Fatalf("no refresh token: %v", err)
	}
	// Revocation: success, and a token Notion no longer knows.
	if err := p.Revoke(context.Background(), credential.OAuthToken{AccessToken: credential.NewSecret("ntn_access_2")}); err != nil {
		t.Fatal(err)
	}
	if last := m.calls[len(m.calls)-1]; last.Path != "/v1/oauth/revoke" || last.Body["token"] != "ntn_access_2" || last.User != clientID {
		t.Fatalf("revoke request: %+v", last)
	}
	m.status, m.errCode = 400, "invalid_grant"
	if err := p.Revoke(context.Background(), credential.OAuthToken{AccessToken: credential.NewSecret("gone")}); err != nil {
		t.Fatalf("already revoked: %v", err)
	}
	m.status = 503
	err = p.Revoke(context.Background(), credential.OAuthToken{AccessToken: credential.NewSecret("x")})
	if oauth.KindOf(err) != oauth.KindProviderUnavailable {
		t.Fatalf("revoke while Notion is down: %v", err)
	}
	noSecret(t, err)
}
