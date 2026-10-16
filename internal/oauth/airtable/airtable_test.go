package airtable_test

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
	"time"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/oauth"
	"workflow-optimizer/internal/oauth/airtable"
)

const (
	clientID = "airtable-client-id"
	secret   = "secret-DO-NOT-LEAK"
	redirect = "http://localhost:3000/api/v1/oauth/callback/airtable"
)

// mockAirtable is Airtable's token and whoami endpoints.
type mockAirtable struct {
	mu       sync.Mutex
	srv      *httptest.Server
	forms    []url.Values
	status   int    // non-zero: answer the next token call with this status
	errCode  string // ...and this error code
	scope    string // granted scopes (default: all)
	refreshN int
}

func newMock(t *testing.T) (*mockAirtable, *airtable.Provider) {
	m := &mockAirtable{scope: strings.Join(airtable.Scopes, " ")}
	m.srv = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.srv.Close)
	p, err := airtable.New(airtable.Options{ClientID: clientID, ClientSecret: credential.NewSecret(secret), RedirectURL: redirect,
		HTTPClient: m.srv.Client(), TokenURL: m.srv.URL + "/oauth2/v1/token", WhoAmIURL: m.srv.URL + "/v0/meta/whoami"})
	if err != nil {
		t.Fatal(err)
	}
	return m, p
}

func (m *mockAirtable) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/v0/meta/whoami" {
		if r.Header.Get("Authorization") != "Bearer at_1" {
			w.WriteHeader(401)
			return
		}
		_, _ = io.WriteString(w, `{"id":"usrAda","email":"ada@example.test"}`)
		return
	}
	_ = r.ParseForm()
	m.forms = append(m.forms, r.PostForm)
	user, pass, _ := r.BasicAuth()
	switch {
	case m.status != 0:
		w.WriteHeader(m.status)
		fmt.Fprintf(w, `{"error":%q,"error_description":"secret detail %s"}`, m.errCode, secret)
		m.status = 0
	case user != clientID || pass != secret:
		w.WriteHeader(401)
		_, _ = io.WriteString(w, `{"error":"invalid_client"}`)
	case r.PostForm.Get("grant_type") == "authorization_code" && r.PostForm.Get("code") == "good" && r.PostForm.Get("code_verifier") == "verifier":
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at_1", "refresh_token": "rt_1", "token_type": "Bearer",
			"scope": m.scope, "expires_in": 3600})
	case r.PostForm.Get("grant_type") == "refresh_token" && r.PostForm.Get("refresh_token") != "":
		m.refreshN++
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": fmt.Sprintf("at_%d", m.refreshN+1),
			"refresh_token": fmt.Sprintf("rt_%d", m.refreshN+1), "token_type": "Bearer", "scope": m.scope, "expires_in": 3600})
	default:
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"error":"invalid_request"}`)
	}
}

func TestNewValidates(t *testing.T) {
	if _, err := airtable.New(airtable.Options{ClientID: clientID, RedirectURL: redirect}); err == nil {
		t.Fatal("a secret is required")
	}
	if _, err := airtable.New(airtable.Options{ClientID: clientID, ClientSecret: credential.NewSecret(secret), RedirectURL: "http://localhost/cb"}); err == nil {
		t.Fatal("the redirect URI must be the airtable callback")
	}
}

func TestAuthorizationURL(t *testing.T) {
	_, p := newMock(t)
	raw, err := p.AuthorizationURL(oauth.AuthorizationRequest{State: "st", CodeChallenge: "ch"})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	if !strings.HasPrefix(raw, airtable.DefaultAuthURL+"?") || q.Get("client_id") != clientID || q.Get("redirect_uri") != redirect ||
		q.Get("code_challenge") != "ch" || q.Get("code_challenge_method") != "S256" || q.Get("state") != "st" ||
		q.Get("scope") != "data.records:read data.records:write schema.bases:read user.email:read" {
		t.Fatalf("url: %s", raw)
	}
	if _, err := p.AuthorizationURL(oauth.AuthorizationRequest{State: "st"}); err == nil {
		t.Fatal("PKCE is required")
	}
	if strings.Contains(raw, secret) {
		t.Fatal("the secret is in the URL")
	}
}

func TestExchangeRefreshRevoke(t *testing.T) {
	m, p := newMock(t)
	ctx := context.Background()
	tok, id, err := p.Exchange(ctx, oauth.ExchangeRequest{Code: "good", CodeVerifier: "verifier"})
	if err != nil {
		t.Fatal(err)
	}
	if id.ProviderAccountID != "usrAda" || id.Email != "ada@example.test" || tok.ProviderAccountID != "usrAda" ||
		tok.AccessToken.Reveal() != "at_1" || tok.RefreshToken.Reveal() != "rt_1" || time.Until(tok.Expiry) < 59*time.Minute {
		t.Fatalf("exchange: %+v %+v", tok, id)
	}
	if f := m.forms[0]; f.Get("redirect_uri") != redirect || f.Get("client_secret") != "" {
		t.Fatalf("form: %v", f)
	}

	// The refresh token rotates.
	next, err := p.Refresh(ctx, tok)
	if err != nil || next.AccessToken.Reveal() != "at_2" || next.RefreshToken.Reveal() != "rt_2" {
		t.Fatalf("refresh: %+v %v", next, err)
	}
	m.status, m.errCode = 400, "invalid_grant"
	if _, err := p.Refresh(ctx, next); oauth.KindOf(err) != oauth.KindRevoked || strings.Contains(err.Error(), secret) {
		t.Fatalf("revoked refresh: %v", err)
	}
	if err := p.Revoke(ctx, next); oauth.KindOf(err) != oauth.KindRevocationUnsupported {
		t.Fatalf("revoke: %v", err)
	}
}

func TestExchangeFailures(t *testing.T) {
	m, p := newMock(t)
	ctx := context.Background()
	if _, _, err := p.Exchange(ctx, oauth.ExchangeRequest{Code: "bad", CodeVerifier: "verifier"}); oauth.KindOf(err) != oauth.KindTokenExchangeFailed {
		t.Fatalf("bad code: %v", err)
	}
	m.status, m.errCode = 400, "invalid_grant"
	if _, _, err := p.Exchange(ctx, oauth.ExchangeRequest{Code: "good", CodeVerifier: "verifier"}); oauth.KindOf(err) != oauth.KindInvalidCode {
		t.Fatalf("invalid grant: %v", err)
	}
	m.status = 503
	if _, _, err := p.Exchange(ctx, oauth.ExchangeRequest{Code: "good", CodeVerifier: "verifier"}); oauth.KindOf(err) != oauth.KindProviderUnavailable {
		t.Fatalf("unavailable: %v", err)
	}
	// A permission left unchecked fails the connection.
	m.scope = "data.records:read schema.bases:read user.email:read"
	_, _, err := p.Exchange(ctx, oauth.ExchangeRequest{Code: "good", CodeVerifier: "verifier"})
	if oauth.KindOf(err) != oauth.KindAuthorizationDenied || !strings.Contains(err.Error(), "data.records:write") {
		t.Fatalf("missing scope: %v", err)
	}
}
