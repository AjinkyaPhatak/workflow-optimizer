package google_test

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/integration/gmail/gmailtest"
	"workflow-optimizer/internal/oauth"
	"workflow-optimizer/internal/oauth/google"
)

const redirect = "https://app.example.test/api/v1/oauth/callback/google"

var ada = gmailtest.Account{Sub: "108234567890123456789", Email: "ada@gmail.example", Name: "Ada Lovelace"}

func setup(t *testing.T) (*gmailtest.Server, *google.Provider) {
	t.Helper()
	s := gmailtest.New()
	t.Cleanup(s.Close)
	p, err := google.New(s.GoogleOptions(redirect))
	if err != nil {
		t.Fatal(err)
	}
	return s, p
}

// connect runs the code flow against the mock and returns the token.
func connect(t *testing.T, s *gmailtest.Server, p *google.Provider) (credential.OAuthToken, oauth.Identity, error) {
	t.Helper()
	verifier := "verifier-0123456789-0123456789-0123456789-0123456789"
	u, err := p.AuthorizationURL(oauth.AuthorizationRequest{State: "state-1", CodeChallenge: oauth.CodeChallenge(verifier)})
	if err != nil {
		t.Fatal(err)
	}
	code, _, err := s.Authorize(u, ada)
	if err != nil {
		t.Fatal(err)
	}
	return p.Exchange(context.Background(), oauth.ExchangeRequest{Code: code, CodeVerifier: verifier})
}

func noLeak(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	for _, s := range []string{"SHOULD-NOT-LEAK", gmailtest.ClientSecret, gmailtest.AccessPrefix, gmailtest.RefreshPrefix, gmailtest.CodePrefix} {
		if strings.Contains(err.Error(), s) {
			t.Fatalf("error leaks %q: %v", s, err)
		}
	}
}

func TestNewValidatesConfiguration(t *testing.T) {
	ok := google.Options{ClientID: "id", ClientSecret: credential.NewSecret("s"), RedirectURL: redirect}
	if _, err := google.New(ok); err != nil {
		t.Fatal(err)
	}
	for name, o := range map[string]google.Options{
		"no client id":     {ClientSecret: credential.NewSecret("s"), RedirectURL: redirect},
		"no secret":        {ClientID: "id", RedirectURL: redirect},
		"wrong callback":   {ClientID: "id", ClientSecret: credential.NewSecret("s"), RedirectURL: "https://app.example.test/oauth/google"},
		"relative":         {ClientID: "id", ClientSecret: credential.NewSecret("s"), RedirectURL: "/api/v1/oauth/callback/google"},
		"other provider's": {ClientID: "id", ClientSecret: credential.NewSecret("s"), RedirectURL: "https://x.test/api/v1/oauth/callback/fake_oauth"},
	} {
		if _, err := google.New(o); oauth.KindOf(err) != oauth.KindInvalidConfiguration {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
}

func TestAuthorizationURLRequestsMinimalScopesAndOfflineAccess(t *testing.T) {
	p, err := google.New(google.Options{ClientID: "id.apps.googleusercontent.com", ClientSecret: credential.NewSecret("secret-XYZ"), RedirectURL: redirect})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := p.AuthorizationURL(oauth.AuthorizationRequest{State: "st", CodeChallenge: "ch"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, "https://accounts.google.com/o/oauth2/v2/auth?") {
		t.Fatalf("Google's authorization endpoint: %s", raw)
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	want := map[string]string{
		"client_id": "id.apps.googleusercontent.com", "redirect_uri": redirect, "response_type": "code", "state": "st",
		"code_challenge": "ch", "code_challenge_method": "S256", "access_type": "offline", "prompt": "consent",
		"scope": "openid email https://www.googleapis.com/auth/gmail.readonly https://www.googleapis.com/auth/gmail.compose",
	}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
	if strings.Contains(raw, "secret-XYZ") || strings.Contains(raw, "mail.google.com") || strings.Contains(raw, "gmail.modify") || q.Has("include_granted_scopes") {
		t.Fatalf("no secret and no broad scopes: %s", raw)
	}
	cfg := p.Config()
	if cfg.ID != "google" || cfg.Name != "Google" || cfg.TokenURL != google.DefaultTokenURL || cfg.RevokeURL != google.DefaultRevokeURL {
		t.Fatalf("config: %+v", cfg)
	}
	b, _ := json.Marshal(cfg)
	if strings.Contains(string(b), "secret-XYZ") {
		t.Fatal("the client secret marshals")
	}
}

func TestExchangeIdentifiesTheAccountBySub(t *testing.T) {
	s, p := setup(t)
	tok, id, err := connect(t, s, p)
	if err != nil {
		t.Fatal(err)
	}
	if id.ProviderAccountID != ada.Sub || id.Email != ada.Email || id.DisplayName != ada.Name || tok.ProviderAccountID != ada.Sub {
		t.Fatalf("identity: %+v", id)
	}
	if !s.ValidAccessToken(tok.AccessToken.Reveal()) || !strings.HasPrefix(tok.RefreshToken.Reveal(), gmailtest.RefreshPrefix) ||
		tok.TokenType != "Bearer" || time.Until(tok.Expiry) < 59*time.Minute || len(tok.Scopes) != 4 {
		t.Fatalf("token: %+v", tok)
	}
	if s.Count("token:authorization_code") != 1 || s.Count("userinfo") != 1 {
		t.Fatal("one exchange, one userinfo call")
	}
}

func TestExchangeFailures(t *testing.T) {
	ctx := context.Background()
	t.Run("invalid code", func(t *testing.T) {
		_, p := setup(t)
		_, _, err := p.Exchange(ctx, oauth.ExchangeRequest{Code: "4/forged", CodeVerifier: "v"})
		if oauth.KindOf(err) != oauth.KindInvalidCode {
			t.Fatalf("got %v", err)
		}
		noLeak(t, err)
	})
	t.Run("code replay", func(t *testing.T) {
		s, p := setup(t)
		verifier := "verifier-0123456789-0123456789-0123456789-0123456789"
		u, _ := p.AuthorizationURL(oauth.AuthorizationRequest{State: "s", CodeChallenge: oauth.CodeChallenge(verifier)})
		code, _, _ := s.Authorize(u, ada)
		if _, _, err := p.Exchange(ctx, oauth.ExchangeRequest{Code: code, CodeVerifier: verifier}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := p.Exchange(ctx, oauth.ExchangeRequest{Code: code, CodeVerifier: verifier}); oauth.KindOf(err) != oauth.KindInvalidCode {
			t.Fatalf("a code works once: %v", err)
		}
	})
	t.Run("wrong PKCE verifier", func(t *testing.T) {
		s, p := setup(t)
		u, _ := p.AuthorizationURL(oauth.AuthorizationRequest{State: "s", CodeChallenge: oauth.CodeChallenge("right-verifier-0123456789-0123456789-0123")})
		code, _, _ := s.Authorize(u, ada)
		if _, _, err := p.Exchange(ctx, oauth.ExchangeRequest{Code: code, CodeVerifier: "wrong"}); oauth.KindOf(err) != oauth.KindInvalidCode {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("permissions not granted", func(t *testing.T) {
		s, p := setup(t)
		s.GrantedScopes = []string{"openid", "https://www.googleapis.com/auth/userinfo.email", "https://www.googleapis.com/auth/gmail.readonly"}
		_, _, err := connect(t, s, p)
		if oauth.KindOf(err) != oauth.KindAuthorizationDenied || !strings.Contains(err.Error(), "gmail.compose") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("email scope in URL form is accepted", func(t *testing.T) {
		s, p := setup(t)
		s.GrantedScopes = []string{"openid", "https://www.googleapis.com/auth/userinfo.email",
			"https://www.googleapis.com/auth/gmail.readonly", "https://www.googleapis.com/auth/gmail.compose"}
		if _, _, err := connect(t, s, p); err != nil {
			t.Fatal(err)
		}
	})
	for name, c := range map[string]struct {
		failure gmailtest.Failure
		kind    oauth.Kind
	}{
		"503":            {gmailtest.Failure{Status: 503, Body: `{"error":"backend_error"}`}, oauth.KindProviderUnavailable},
		"429":            {gmailtest.Failure{Status: 429}, oauth.KindProviderUnavailable},
		"invalid client": {gmailtest.Failure{Status: 401, Body: `{"error":"invalid_client","error_description":"SHOULD-NOT-LEAK"}`}, oauth.KindInvalidConfiguration},
		"other 400":      {gmailtest.Failure{Status: 400, Body: `{"error":"invalid_request","error_description":"SHOULD-NOT-LEAK"}`}, oauth.KindTokenExchangeFailed},
		"malformed":      {gmailtest.Failure{Status: 200, Body: `{"access_tok`}, oauth.KindTokenExchangeFailed},
		"no token":       {gmailtest.Failure{Status: 200, Body: `{"token_type":"Bearer"}`}, oauth.KindTokenExchangeFailed},
		"no refresh":     {gmailtest.Failure{Status: 200, Body: `{"access_token":"ya29.x","expires_in":3600,"scope":"openid email https://www.googleapis.com/auth/gmail.readonly https://www.googleapis.com/auth/gmail.compose"}`}, oauth.KindTokenExchangeFailed},
	} {
		t.Run(name, func(t *testing.T) {
			s, p := setup(t)
			s.Fail("/token", c.failure)
			_, _, err := connect(t, s, p)
			if oauth.KindOf(err) != c.kind {
				t.Fatalf("got %v, want %s", err, c.kind)
			}
			noLeak(t, err)
		})
	}
	t.Run("userinfo without sub", func(t *testing.T) {
		s, p := setup(t)
		s.Fail("/userinfo", gmailtest.Failure{Status: 200, Body: `{"email":"x@gmail.example"}`})
		if _, _, err := connect(t, s, p); oauth.KindOf(err) != oauth.KindTokenExchangeFailed {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		s := gmailtest.New()
		defer s.Close()
		o := s.GoogleOptions(redirect)
		o.Timeout = 50 * time.Millisecond
		p, _ := google.New(o)
		s.Fail("/token", gmailtest.Failure{Delay: time.Second})
		if _, _, err := connect(t, s, p); oauth.KindOf(err) != oauth.KindProviderUnavailable {
			t.Fatalf("got %v", err)
		}
	})
}

func TestRefreshAndRevoke(t *testing.T) {
	ctx := context.Background()
	s, p := setup(t)
	tok, _, err := connect(t, s, p)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := p.Refresh(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if !s.ValidAccessToken(fresh.AccessToken.Reveal()) || fresh.AccessToken.Reveal() == tok.AccessToken.Reveal() || !fresh.RefreshToken.Empty() {
		t.Fatalf("refresh: a new access token, and (like Google) no refresh token: %+v", fresh)
	}
	// The token manager keeps the stored refresh token.
	if merged := tok.Merge(fresh); merged.RefreshToken.Reveal() != tok.RefreshToken.Reveal() || merged.ProviderAccountID != ada.Sub {
		t.Fatal("merge keeps the refresh token")
	}

	s.Fail("/token", gmailtest.Failure{Status: 500})
	if _, err := p.Refresh(ctx, tok); oauth.KindOf(err) != oauth.KindProviderUnavailable {
		t.Fatalf("500: %v", err)
	}
	s.Fail("/token", gmailtest.Failure{Status: 200, Body: `not json`})
	if _, err := p.Refresh(ctx, tok); oauth.KindOf(err) != oauth.KindTokenRefreshFailed {
		t.Fatalf("malformed: %v", err)
	}

	// Revoke ends the grant: refresh then fails with invalid_grant -> revoked.
	if err := p.Revoke(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if s.Count("revoke") != 1 {
		t.Fatal("revocation endpoint called")
	}
	_, err = p.Refresh(ctx, tok)
	if oauth.KindOf(err) != oauth.KindRevoked || oauth.KindRevoked.Retryable() {
		t.Fatalf("invalid_grant: %v", err)
	}
	noLeak(t, err)
	// Revoking an unknown token counts as revoked; outages are reported.
	if err := p.Revoke(ctx, credential.OAuthToken{RefreshToken: credential.NewSecret("1//gone")}); err != nil {
		t.Fatalf("already revoked: %v", err)
	}
	s.Fail("/revoke", gmailtest.Failure{Status: 503})
	if err := p.Revoke(ctx, tok); oauth.KindOf(err) != oauth.KindProviderUnavailable {
		t.Fatalf("revoke outage: %v", err)
	}
	if _, err := p.Refresh(ctx, credential.OAuthToken{AccessToken: credential.NewSecret("a")}); oauth.KindOf(err) != oauth.KindRevoked {
		t.Fatal("no refresh token: revoked")
	}
}
