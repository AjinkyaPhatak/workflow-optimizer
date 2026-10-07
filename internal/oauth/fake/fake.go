// Package fake is an in-memory OAuth 2.0 provider for tests (Phase C2). It
// implements oauth.Provider with real authorization-code semantics (single-
// use codes bound to a PKCE challenge, expiring access tokens, refresh with
// optional rotation, revocation) and no network. It is never configured in
// production.
package fake

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/oauth"
)

// ID is the fake provider's ID (and the credential provider of its accounts).
const ID = "fake_oauth"

// Config returns a fake provider configuration with the given callback URL.
func Config(redirectURL string) oauth.ProviderConfig {
	return oauth.ProviderConfig{
		ID: ID, Name: "Fake OAuth (test)", Description: "A fake provider for automated tests.",
		ClientID: "fake-client-id", ClientSecret: credential.NewSecret("fake-client-secret-DO-NOT-LEAK"),
		AuthURL: "https://oauth.fake.test/authorize", TokenURL: "https://oauth.fake.test/token",
		RevokeURL: "https://oauth.fake.test/revoke", RedirectURL: redirectURL,
		Scopes: []string{"records.read", "records.write"},
	}
}

type grant struct {
	identity oauth.Identity
	scopes   []string
	revoked  bool
}

type code struct {
	grant     *grant
	challenge string
	used      bool
}

type access struct {
	grant   *grant
	expires time.Time
}

// Provider is the fake provider. Exported fields are test knobs; set them
// before use or between calls.
type Provider struct {
	cfg oauth.ProviderConfig

	mu      sync.Mutex
	now     func() time.Time
	codes   map[string]*code
	access  map[string]*access
	refresh map[string]*grant

	// AccessTTL is the lifetime of issued access tokens (default 1h).
	AccessTTL time.Duration
	// RotateRefreshTokens makes every refresh return a new refresh token
	// and invalidate the old one.
	RotateRefreshTokens bool
	// Fail, when it returns an error for an operation ("exchange",
	// "refresh", "revoke"), makes that call fail with it.
	Fail func(operation string) error
	// Malformed makes the next exchange or refresh answer without an
	// access token (a malformed token response).
	Malformed bool
	// RefreshDelay slows refreshes (to test concurrent resolutions).
	RefreshDelay time.Duration

	exchanges, refreshes, revocations int
}

var _ oauth.Provider = (*Provider)(nil)

// New returns a fake provider with cfg (see Config).
func New(cfg oauth.ProviderConfig) *Provider {
	return &Provider{cfg: cfg, now: time.Now, codes: map[string]*code{}, access: map[string]*access{},
		refresh: map[string]*grant{}, AccessTTL: time.Hour}
}

// WithClock replaces the clock.
func (p *Provider) WithClock(now func() time.Time) *Provider {
	p.now = now
	return p
}

func token(prefix string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// Config implements oauth.Provider.
func (p *Provider) Config() oauth.ProviderConfig { return p.cfg }

// AuthorizationURL implements oauth.Provider.
func (p *Provider) AuthorizationURL(req oauth.AuthorizationRequest) (string, error) {
	if req.State == "" || req.CodeChallenge == "" {
		return "", oauth.NewError(oauth.KindInvalidConfiguration, "state and code challenge are required")
	}
	q := url.Values{
		"response_type": {"code"}, "client_id": {p.cfg.ClientID}, "redirect_uri": {p.cfg.RedirectURL},
		"scope": {strings.Join(p.cfg.Scopes, " ")}, "state": {req.State},
		"code_challenge": {req.CodeChallenge}, "code_challenge_method": {"S256"},
	}
	return p.cfg.AuthURL + "?" + q.Encode(), nil
}

// Approve plays the user consenting on the provider's page: it checks the
// authorization URL like a provider would and returns the code and state the
// provider would send to the redirect URI.
func (p *Provider) Approve(authorizationURL string, who oauth.Identity) (codeValue, state string, err error) {
	u, err := url.Parse(authorizationURL)
	if err != nil || !strings.HasPrefix(authorizationURL, p.cfg.AuthURL+"?") {
		return "", "", errors.New("fake: not this provider's authorization URL")
	}
	q := u.Query()
	if q.Get("client_id") != p.cfg.ClientID || q.Get("redirect_uri") != p.cfg.RedirectURL ||
		q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		return "", "", errors.New("fake: invalid authorization request")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	codeValue = token("code-")
	p.codes[codeValue] = &code{grant: &grant{identity: who, scopes: strings.Fields(q.Get("scope"))}, challenge: q.Get("code_challenge")}
	return codeValue, q.Get("state"), nil
}

func (p *Provider) issue(g *grant, withRefresh bool) credential.OAuthToken {
	at := token("fake-access-")
	exp := p.now().Add(p.AccessTTL)
	p.access[at] = &access{grant: g, expires: exp}
	t := credential.OAuthToken{AccessToken: credential.NewSecret(at), TokenType: "Bearer", Expiry: exp,
		Scopes: append([]string(nil), g.scopes...), ProviderAccountID: g.identity.ProviderAccountID}
	if withRefresh {
		rt := token("fake-refresh-")
		p.refresh[rt] = g
		t.RefreshToken = credential.NewSecret(rt)
	}
	return t
}

func (p *Provider) fail(op string) error {
	if p.Fail != nil {
		return p.Fail(op)
	}
	return nil
}

// Exchange implements oauth.Provider.
func (p *Provider) Exchange(ctx context.Context, req oauth.ExchangeRequest) (credential.OAuthToken, oauth.Identity, error) {
	if err := ctx.Err(); err != nil {
		return credential.OAuthToken{}, oauth.Identity{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.exchanges++
	if err := p.fail("exchange"); err != nil {
		return credential.OAuthToken{}, oauth.Identity{}, err
	}
	c, ok := p.codes[req.Code]
	if !ok || c.used || oauth.CodeChallenge(req.CodeVerifier) != c.challenge {
		// The real error body would be untrusted; only the kind is kept.
		return credential.OAuthToken{}, oauth.Identity{}, oauth.NewError(oauth.KindInvalidCode, "the authorization code was rejected")
	}
	c.used = true
	if p.Malformed {
		p.Malformed = false
		return credential.OAuthToken{TokenType: "Bearer"}, c.grant.identity, nil
	}
	return p.issue(c.grant, true), c.grant.identity, nil
}

// Refresh implements oauth.Provider.
func (p *Provider) Refresh(ctx context.Context, t credential.OAuthToken) (credential.OAuthToken, error) {
	if p.RefreshDelay > 0 {
		select {
		case <-ctx.Done():
			return credential.OAuthToken{}, ctx.Err()
		case <-time.After(p.RefreshDelay):
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refreshes++
	if err := p.fail("refresh"); err != nil {
		return credential.OAuthToken{}, err
	}
	g, ok := p.refresh[t.RefreshToken.Reveal()]
	if !ok || g.revoked {
		return credential.OAuthToken{}, oauth.NewError(oauth.KindRevoked, "the refresh token is no longer valid")
	}
	if p.Malformed {
		p.Malformed = false
		return credential.OAuthToken{}, nil
	}
	out := p.issue(g, p.RotateRefreshTokens)
	if p.RotateRefreshTokens {
		delete(p.refresh, t.RefreshToken.Reveal())
	}
	// Like most providers, omit unchanged fields.
	out.Scopes, out.ProviderAccountID = nil, ""
	return out, nil
}

// Revoke implements oauth.Provider: the whole grant stops working.
func (p *Provider) Revoke(ctx context.Context, t credential.OAuthToken) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.revocations++
	if err := p.fail("revoke"); err != nil {
		return err
	}
	if g, ok := p.refresh[t.RefreshToken.Reveal()]; ok {
		g.revoked = true
	}
	if a, ok := p.access[t.AccessToken.Reveal()]; ok {
		a.grant.revoked = true
	}
	return nil
}

// RevokeAccount revokes every grant of an account at the provider (the user
// removing the app's access on the provider's side).
func (p *Provider) RevokeAccount(providerAccountID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, g := range p.refresh {
		if g.identity.ProviderAccountID == providerAccountID {
			g.revoked = true
		}
	}
	for _, a := range p.access {
		if a.grant.identity.ProviderAccountID == providerAccountID {
			a.grant.revoked = true
		}
	}
}

// ValidAccessToken reports whether an API would accept the access token.
func (p *Provider) ValidAccessToken(at string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	a, ok := p.access[at]
	return ok && !a.grant.revoked && p.now().Before(a.expires)
}

// Counts returns how many exchanges, refreshes and revocations were made.
func (p *Provider) Counts() (exchanges, refreshes, revocations int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exchanges, p.refreshes, p.revocations
}
