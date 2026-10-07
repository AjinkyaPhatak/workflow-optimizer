// Package oauth is the provider-neutral OAuth 2.0 layer (Phase C2): the
// Provider abstraction and its configuration, the authorization-code flow
// with server-side single-use state and PKCE (Flow), and the token manager
// that keeps OAuth credentials usable at run time (TokenManager).
//
//	authorize: Flow.Begin -> state in StateStore -> provider authorization URL
//	callback:  Flow.Complete -> state taken (single use) -> Provider.Exchange
//	           -> token + identity (persisted by internal/connectedaccount)
//	run time:  ActionNode -> credential.Resolver (TokenManager)
//	           -> credential.Service.ResolveOAuth -> refresh if needed -> access token
//
// Nothing here knows any particular provider. Google, Discord, Notion... are
// Provider implementations; tests use internal/oauth/fake.
package oauth

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"

	"workflow-optimizer/internal/credential"
)

// ProviderConfig is an OAuth client's configuration. It comes from backend
// configuration only. ClientSecret is a credential.Secret: it never prints
// or marshals, and no API response includes it.
type ProviderConfig struct {
	// ID is the stable provider identifier, also the credential provider of
	// the credentials it creates ("google").
	ID string
	// Name is the display name ("Google").
	Name string
	// Description says what connecting gives access to.
	Description  string
	ClientID     string
	ClientSecret credential.Secret
	AuthURL      string
	TokenURL     string
	// RevokeURL is empty when the provider has no revocation endpoint.
	RevokeURL string
	// RedirectURL is this backend's callback URL registered with the
	// provider: <origin>/api/v1/oauth/callback/<ID>.
	RedirectURL string
	Scopes      []string
}

var providerID = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func absoluteURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

// Validate checks the configuration is usable.
func (c ProviderConfig) Validate() error {
	switch {
	case !providerID.MatchString(c.ID):
		return newError(KindInvalidConfiguration, fmt.Sprintf("provider id %q must match %s", c.ID, providerID))
	case strings.TrimSpace(c.Name) == "":
		return newError(KindInvalidConfiguration, "provider "+c.ID+" needs a name")
	case strings.TrimSpace(c.ClientID) == "":
		return newError(KindInvalidConfiguration, "provider "+c.ID+" needs a client ID")
	case !absoluteURL(c.AuthURL), !absoluteURL(c.TokenURL), !absoluteURL(c.RedirectURL):
		return newError(KindInvalidConfiguration, "provider "+c.ID+" needs absolute authorization, token and redirect URLs")
	case c.RevokeURL != "" && !absoluteURL(c.RevokeURL):
		return newError(KindInvalidConfiguration, "provider "+c.ID+" has an invalid revocation URL")
	}
	return nil
}

// Identity is the external account an authorization belongs to.
// ProviderAccountID is the stable identity (never assumed to be an email).
type Identity struct {
	ProviderAccountID string
	Email             string
	DisplayName       string
}

// AuthorizationRequest is what the authorization URL carries.
type AuthorizationRequest struct {
	State string
	// CodeChallenge is the PKCE S256 challenge of the flow's verifier.
	CodeChallenge string
}

// ExchangeRequest redeems an authorization code.
type ExchangeRequest struct {
	Code         string
	CodeVerifier string
}

// Provider is one OAuth provider. Implementations classify failures as
// *Error (never including tokens, codes, secrets or raw response bodies) and
// honour ctx. They hold no per-user state.
type Provider interface {
	Config() ProviderConfig
	// AuthorizationURL builds the URL the browser is sent to.
	AuthorizationURL(req AuthorizationRequest) (string, error)
	// Exchange redeems a code for tokens and the account they belong to.
	Exchange(ctx context.Context, req ExchangeRequest) (credential.OAuthToken, Identity, error)
	// Refresh obtains a new access token with token.RefreshToken. The result
	// may omit the refresh token (it is then kept: OAuthToken.Merge).
	Refresh(ctx context.Context, token credential.OAuthToken) (credential.OAuthToken, error)
	// Revoke invalidates the authorization at the provider. Providers
	// without revocation return an *Error of KindRevocationUnsupported.
	Revoke(ctx context.Context, token credential.OAuthToken) error
}

// Registry holds the configured providers. It is filled at startup and only
// read afterwards; lookups are safe for concurrent use.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

// NewRegistry returns a registry of the given providers.
func NewRegistry(providers ...Provider) (*Registry, error) {
	r := &Registry{providers: map[string]Provider{}}
	for _, p := range providers {
		if p == nil {
			return nil, newError(KindInvalidConfiguration, "nil provider")
		}
		cfg := p.Config()
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
		if _, dup := r.providers[cfg.ID]; dup {
			return nil, newError(KindInvalidConfiguration, "duplicate provider "+cfg.ID)
		}
		r.providers[cfg.ID] = p
	}
	return r, nil
}

// Get returns the provider with the given ID.
func (r *Registry) Get(id string) (Provider, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[id]
	return p, ok
}

// List returns the configurations of all providers, sorted by ID.
func (r *Registry) List() []ProviderConfig {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ProviderConfig, 0, len(r.providers))
	for _, p := range r.providers {
		out = append(out, p.Config())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
