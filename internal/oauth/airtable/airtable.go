// Package airtable is the Airtable implementation of oauth.Provider (Phase
// J): an Airtable OAuth integration's authorization-code flow with PKCE
// (S256, which Airtable requires). The user picks the bases to share in
// Airtable's own consent screen.
//
// The token endpoint authenticates the client with HTTP Basic (a client
// with a secret) and rotates the refresh token on every refresh. Airtable
// has no revocation endpoint: disconnecting removes the tokens here only
// (the user can remove the grant in Airtable's account settings).
//
// It holds no per-user state and never logs. Errors are *oauth.Error whose
// messages are written here: Airtable's response bodies are never copied.
package airtable

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/oauth"
)

// ID is the provider ID: the callback is /api/v1/oauth/callback/airtable and
// Airtable credentials have provider "airtable".
const ID = "airtable"

// Airtable's endpoints.
const (
	DefaultAuthURL   = "https://airtable.com/oauth2/v1/authorize"
	DefaultTokenURL  = "https://airtable.com/oauth2/v1/token"
	DefaultWhoAmIURL = "https://api.airtable.com/v0/meta/whoami"
)

// Scopes requested when connecting Airtable, the minimum for the airtable.*
// actions:
//
//   - data.records:read, data.records:write: list, create and update
//     records of the bases the user shares (no deleting is offered).
//   - schema.bases:read: the base, table and field pickers.
//   - user.email:read: the account's address, shown as its label.
var Scopes = []string{"data.records:read", "data.records:write", "schema.bases:read", "user.email:read"}

// Options configure the provider. ClientID, ClientSecret and RedirectURL
// come from backend configuration (AIRTABLE_CLIENT_ID,
// AIRTABLE_CLIENT_SECRET, AIRTABLE_OAUTH_REDIRECT_URI). The endpoint fields
// exist for tests.
type Options struct {
	ClientID     string
	ClientSecret credential.Secret
	RedirectURL  string
	HTTPClient   *http.Client
	AuthURL      string
	TokenURL     string
	WhoAmIURL    string
	// Timeout bounds each request to Airtable (default 15s).
	Timeout time.Duration
}

// Provider is the Airtable OAuth provider.
type Provider struct {
	cfg       oauth.ProviderConfig
	http      *http.Client
	whoAmIURL string
	timeout   time.Duration
}

var _ oauth.Provider = (*Provider)(nil)

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// New validates the options and returns the provider.
func New(o Options) (*Provider, error) {
	if strings.TrimSpace(o.ClientID) == "" || o.ClientSecret.Empty() {
		return nil, oauth.NewError(oauth.KindInvalidConfiguration, "airtable: a client ID and a client secret are required")
	}
	cfg := oauth.ProviderConfig{
		ID: ID, Name: "Airtable", Description: "Airtable: find, add and update records in the bases you choose to share.",
		ClientID: strings.TrimSpace(o.ClientID), ClientSecret: o.ClientSecret,
		AuthURL: or(o.AuthURL, DefaultAuthURL), TokenURL: or(o.TokenURL, DefaultTokenURL),
		RedirectURL: strings.TrimSpace(o.RedirectURL), Scopes: append([]string(nil), Scopes...),
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !strings.HasSuffix(cfg.RedirectURL, "/api/v1/oauth/callback/"+ID) {
		return nil, oauth.NewError(oauth.KindInvalidConfiguration, "airtable: the redirect URI must end with /api/v1/oauth/callback/airtable")
	}
	hc := o.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &Provider{cfg: cfg, http: hc, whoAmIURL: or(o.WhoAmIURL, DefaultWhoAmIURL), timeout: timeout}, nil
}

// Config implements oauth.Provider.
func (p *Provider) Config() oauth.ProviderConfig { return p.cfg }

// AuthorizationURL implements oauth.Provider.
func (p *Provider) AuthorizationURL(req oauth.AuthorizationRequest) (string, error) {
	if req.State == "" || req.CodeChallenge == "" {
		return "", oauth.NewError(oauth.KindInvalidConfiguration, "state and code challenge are required")
	}
	q := url.Values{
		"client_id":             {p.cfg.ClientID},
		"redirect_uri":          {p.cfg.RedirectURL},
		"response_type":         {"code"},
		"scope":                 {strings.Join(p.cfg.Scopes, " ")},
		"state":                 {req.State},
		"code_challenge":        {req.CodeChallenge},
		"code_challenge_method": {"S256"},
	}
	return p.cfg.AuthURL + "?" + q.Encode(), nil
}

// tokenResponse is Airtable's token endpoint answer.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	ExpiresIn    int64  `json:"expires_in"`
}

// errorResponse is Airtable's OAuth error body. Only the code is used.
type errorResponse struct {
	Error string `json:"error"`
}

func unavailable(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return &oauth.Error{Kind: oauth.KindProviderUnavailable, Message: "Airtable could not be reached", Err: err}
}

// tokenCall posts a form with the client's Basic credentials to the token
// endpoint. grantKind is the kind for invalid_grant, failure for any other
// rejection.
func (p *Provider) tokenCall(ctx context.Context, form url.Values, grantKind, failure oauth.Kind) (tokenResponse, error) {
	cctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, p.cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	req.SetBasicAuth(url.QueryEscape(p.cfg.ClientID), url.QueryEscape(p.cfg.ClientSecret.Reveal()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := p.http.Do(req)
	if err != nil {
		return tokenResponse{}, unavailable(ctx, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return tokenResponse{}, unavailable(ctx, err)
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return tokenResponse{}, oauth.NewError(oauth.KindProviderUnavailable, fmt.Sprintf("Airtable answered HTTP %d", resp.StatusCode))
	case resp.StatusCode != http.StatusOK:
		var e errorResponse
		_ = json.Unmarshal(body, &e)
		switch e.Error {
		case "invalid_grant":
			return tokenResponse{}, oauth.NewError(grantKind, "Airtable rejected the grant (invalid_grant)")
		case "invalid_client", "unauthorized_client":
			return tokenResponse{}, oauth.NewError(oauth.KindInvalidConfiguration, "Airtable rejected the OAuth client ("+e.Error+")")
		}
		return tokenResponse{}, oauth.NewError(failure, fmt.Sprintf("Airtable rejected the token request (HTTP %d)", resp.StatusCode))
	}
	var t tokenResponse
	if err := json.Unmarshal(body, &t); err != nil || t.AccessToken == "" {
		return tokenResponse{}, oauth.NewError(failure, "Airtable returned a malformed token response")
	}
	return t, nil
}

func token(t tokenResponse, now time.Time) credential.OAuthToken {
	tok := credential.OAuthToken{AccessToken: credential.NewSecret(t.AccessToken), RefreshToken: credential.NewSecret(t.RefreshToken),
		TokenType: t.TokenType, Scopes: strings.Fields(t.Scope)}
	if t.ExpiresIn > 0 {
		tok.Expiry = now.Add(time.Duration(t.ExpiresIn) * time.Second).UTC()
	}
	return tok
}

// Exchange implements oauth.Provider: the code (with its PKCE verifier) is
// redeemed, every requested scope must have been granted, and the account
// is identified with whoami's user ID.
func (p *Provider) Exchange(ctx context.Context, req oauth.ExchangeRequest) (credential.OAuthToken, oauth.Identity, error) {
	t, err := p.tokenCall(ctx, url.Values{
		"grant_type": {"authorization_code"}, "code": {req.Code}, "code_verifier": {req.CodeVerifier}, "redirect_uri": {p.cfg.RedirectURL},
	}, oauth.KindInvalidCode, oauth.KindTokenExchangeFailed)
	if err != nil {
		return credential.OAuthToken{}, oauth.Identity{}, err
	}
	tok := token(t, time.Now())
	if tok.RefreshToken.Empty() {
		return credential.OAuthToken{}, oauth.Identity{}, oauth.NewError(oauth.KindTokenExchangeFailed, "Airtable issued no refresh token")
	}
	var missing []string
	for _, s := range p.cfg.Scopes {
		if !slices.Contains(tok.Scopes, s) {
			missing = append(missing, s)
		}
	}
	if len(missing) > 0 {
		return credential.OAuthToken{}, oauth.Identity{}, oauth.NewError(oauth.KindAuthorizationDenied,
			"not every requested permission was granted: "+strings.Join(missing, ", "))
	}
	id, err := p.identity(ctx, tok.AccessToken)
	if err != nil {
		return credential.OAuthToken{}, oauth.Identity{}, err
	}
	tok.ProviderAccountID = id.ProviderAccountID
	return tok, id, nil
}

type whoAmI struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

func (p *Provider) identity(ctx context.Context, access credential.Secret) (oauth.Identity, error) {
	cctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, p.whoAmIURL, nil)
	if err != nil {
		return oauth.Identity{}, err
	}
	req.Header.Set("Authorization", "Bearer "+access.Reveal())
	req.Header.Set("Accept", "application/json")
	resp, err := p.http.Do(req)
	if err != nil {
		return oauth.Identity{}, unavailable(ctx, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return oauth.Identity{}, unavailable(ctx, err)
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return oauth.Identity{}, oauth.NewError(oauth.KindProviderUnavailable, fmt.Sprintf("Airtable whoami answered HTTP %d", resp.StatusCode))
	}
	if resp.StatusCode != http.StatusOK {
		return oauth.Identity{}, oauth.NewError(oauth.KindTokenExchangeFailed, fmt.Sprintf("Airtable whoami answered HTTP %d", resp.StatusCode))
	}
	var w whoAmI
	if err := json.Unmarshal(body, &w); err != nil || strings.TrimSpace(w.ID) == "" {
		return oauth.Identity{}, oauth.NewError(oauth.KindTokenExchangeFailed, "Airtable did not identify the account")
	}
	return oauth.Identity{ProviderAccountID: w.ID, Email: w.Email, DisplayName: w.Email}, nil
}

// Refresh implements oauth.Provider. Airtable rotates the refresh token: the
// new one replaces the stored one. invalid_grant is KindRevoked.
func (p *Provider) Refresh(ctx context.Context, t credential.OAuthToken) (credential.OAuthToken, error) {
	if t.RefreshToken.Empty() {
		return credential.OAuthToken{}, oauth.NewError(oauth.KindRevoked, "there is no refresh token")
	}
	resp, err := p.tokenCall(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {t.RefreshToken.Reveal()}},
		oauth.KindRevoked, oauth.KindTokenRefreshFailed)
	if err != nil {
		return credential.OAuthToken{}, err
	}
	return token(resp, time.Now()), nil
}

// Revoke implements oauth.Provider: Airtable cannot revoke tokens.
func (p *Provider) Revoke(context.Context, credential.OAuthToken) error {
	return oauth.NewError(oauth.KindRevocationUnsupported, "Airtable has no token revocation")
}
