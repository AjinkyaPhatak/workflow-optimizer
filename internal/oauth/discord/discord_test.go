package discord_test

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
	"workflow-optimizer/internal/oauth/discord"
)

const (
	clientID = "123456789012345678"
	secret   = "discord-secret-DO-NOT-LEAK"
	redirect = "http://localhost:3000/api/v1/oauth/callback/discord"
)

type call struct {
	Path, ContentType, User, Pass string
	Form                          url.Values
}

type mockDiscord struct {
	mu      sync.Mutex
	srv     *httptest.Server
	calls   []call
	status  int
	errCode string
	noGuild bool
}

func newMock(t *testing.T) *mockDiscord {
	m := &mockDiscord{}
	m.srv = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockDiscord) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	user, pass, _ := r.BasicAuth()
	b, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(b))
	c := call{Path: r.URL.Path, ContentType: r.Header.Get("Content-Type"), User: user, Pass: pass, Form: form}
	m.calls = append(m.calls, c)
	w.Header().Set("Content-Type", "application/json")
	if m.status != 0 {
		w.WriteHeader(m.status)
		fmt.Fprintf(w, `{"error":%q,"error_description":"%s"}`, m.errCode, secret)
		m.status = 0
		return
	}
	if user != clientID || pass != secret || c.ContentType != "application/x-www-form-urlencoded" {
		w.WriteHeader(401)
		_, _ = io.WriteString(w, `{"error":"invalid_client"}`)
		return
	}
	switch {
	case r.URL.Path == "/token" && form.Get("grant_type") == "authorization_code" && form.Get("code") == "good-code":
		resp := map[string]any{"access_token": "user-access-1", "refresh_token": "user-refresh-1", "token_type": "Bearer", "expires_in": 604800, "scope": "identify bot"}
		if !m.noGuild {
			resp["guild"] = map[string]any{"id": "100000000000000001", "name": "Ada's Server", "owner_id": "1"}
		}
		_ = json.NewEncoder(w).Encode(resp)
	case r.URL.Path == "/token" && form.Get("grant_type") == "refresh_token":
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "user-access-2", "refresh_token": "user-refresh-2", "token_type": "Bearer", "expires_in": 604800, "scope": "identify bot"})
	case r.URL.Path == "/revoke" && form.Get("token") != "":
		_, _ = io.WriteString(w, `{}`)
	default:
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
	}
}

func (m *mockDiscord) provider(t *testing.T) *discord.Provider {
	p, err := discord.New(discord.Options{ClientID: clientID, ClientSecret: credential.NewSecret(secret), RedirectURL: redirect,
		HTTPClient: m.srv.Client(), TokenURL: m.srv.URL + "/token", RevokeURL: m.srv.URL + "/revoke"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func noSecret(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(fmt.Sprintf("%v %+v", err, err), secret) {
		t.Fatalf("error leaks the client secret: %v", err)
	}
}

func TestConfiguration(t *testing.T) {
	for name, o := range map[string]discord.Options{
		"no secret":      {ClientID: "1", RedirectURL: redirect},
		"wrong callback": {ClientID: "1", ClientSecret: credential.NewSecret("s"), RedirectURL: "http://localhost:3000/api/v1/oauth/callback/notion"},
	} {
		if _, err := discord.New(o); oauth.KindOf(err) != oauth.KindInvalidConfiguration {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if discord.Permissions != 84992 {
		t.Fatalf("permissions %d: view channels, send messages, embed links, read history only", discord.Permissions)
	}
}

func TestAuthorizationURLAddsTheBot(t *testing.T) {
	raw, err := newMock(t).provider(t).AuthorizationURL(oauth.AuthorizationRequest{State: "st", CodeChallenge: "ch"})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	if u.Host != "discord.com" || q.Get("client_id") != clientID || q.Get("redirect_uri") != redirect || q.Get("response_type") != "code" ||
		q.Get("scope") != "identify bot" || q.Get("permissions") != "84992" || q.Get("integration_type") != "0" || q.Get("state") != "st" ||
		q.Has("code_challenge") {
		t.Fatalf("authorization URL: %s", raw)
	}
}

func TestFlowConnectsTheServer(t *testing.T) {
	m := newMock(t)
	reg, _ := oauth.NewRegistry(m.provider(t))
	flow := oauth.NewFlow(reg, oauth.NewMemoryStateStore(), 0)
	started, err := flow.Begin(context.Background(), discord.ID, uuid.New(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(started.AuthorizationURL)
	done, err := flow.Complete(context.Background(), discord.ID, oauth.Callback{State: u.Query().Get("state"), Code: "good-code", Binding: started.BindingToken})
	if err != nil {
		t.Fatal(err)
	}
	if done.Identity.ProviderAccountID != "100000000000000001" || done.Identity.DisplayName != "Ada's Server" ||
		done.Token.ProviderAccountID != "100000000000000001" || done.Token.Expiry.IsZero() || done.Token.RefreshToken.Reveal() != "user-refresh-1" {
		t.Fatalf("completed: %+v %+v", done.Token, done.Identity)
	}
	if c := m.calls[0]; c.Form.Get("redirect_uri") != redirect || c.Form.Has("client_secret") {
		t.Fatalf("token request: %+v", c)
	}
}

func TestExchangeFailures(t *testing.T) {
	m := newMock(t)
	m.noGuild = true
	_, _, err := m.provider(t).Exchange(context.Background(), oauth.ExchangeRequest{Code: "good-code"})
	if oauth.KindOf(err) != oauth.KindAuthorizationDenied {
		t.Fatalf("no server: %v", err)
	}
	for name, c := range map[string]struct {
		status  int
		errCode string
		code    string
		kind    oauth.Kind
	}{
		"bad code":   {0, "", "bad-code", oauth.KindInvalidCode},
		"bad client": {401, "invalid_client", "good-code", oauth.KindInvalidConfiguration},
		"down":       {502, "", "good-code", oauth.KindProviderUnavailable},
	} {
		m := newMock(t)
		m.status, m.errCode = c.status, c.errCode
		_, _, err := m.provider(t).Exchange(context.Background(), oauth.ExchangeRequest{Code: c.code})
		if oauth.KindOf(err) != c.kind {
			t.Fatalf("%s: %v", name, err)
		}
		noSecret(t, err)
	}
}

func TestRefreshAndRevoke(t *testing.T) {
	m := newMock(t)
	p := m.provider(t)
	tok, err := p.Refresh(context.Background(), credential.OAuthToken{RefreshToken: credential.NewSecret("user-refresh-1")})
	if err != nil || tok.AccessToken.Reveal() != "user-access-2" || tok.RefreshToken.Reveal() != "user-refresh-2" {
		t.Fatalf("refresh: %+v %v", tok, err)
	}
	m.status, m.errCode = 400, "invalid_grant"
	if _, err := p.Refresh(context.Background(), credential.OAuthToken{RefreshToken: credential.NewSecret("old")}); oauth.KindOf(err) != oauth.KindRevoked {
		t.Fatalf("invalid_grant: %v", err)
	}
	if err := p.Revoke(context.Background(), credential.OAuthToken{AccessToken: credential.NewSecret("a"), RefreshToken: credential.NewSecret("r")}); err != nil {
		t.Fatal(err)
	}
	if last := m.calls[len(m.calls)-1]; last.Form.Get("token") != "r" || last.Form.Get("token_type_hint") != "refresh_token" {
		t.Fatalf("revoke request: %+v", last)
	}
	m.status, m.errCode = 400, "invalid_token"
	if err := p.Revoke(context.Background(), credential.OAuthToken{AccessToken: credential.NewSecret("gone")}); err != nil {
		t.Fatalf("already revoked: %v", err)
	}
}
