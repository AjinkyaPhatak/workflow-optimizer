// Package google is the Google implementation of oauth.Provider (Phase C3).
// It talks to Google's OAuth 2.0 endpoints over HTTPS: authorization (with
// PKCE and offline access), code exchange, refresh, revocation, and the
// OpenID Connect userinfo endpoint for the account's stable identity (sub).
//
// It holds no per-user state and never logs. Errors are *oauth.Error whose
// messages are written here: Google's error_description and response
// bodies are untrusted and never copied into them.
package google

import (
	"context"
	"encoding/json"
	"errors"
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

// ID is the provider ID: the callback is /api/v1/oauth/callback/google and
// Google credentials have provider "google".
const ID = "google"

// Google's endpoints.
const (
	DefaultAuthURL     = "https://accounts.google.com/o/oauth2/v2/auth"
	DefaultTokenURL    = "https://oauth2.googleapis.com/token"
	DefaultRevokeURL   = "https://oauth2.googleapis.com/revoke"
	DefaultUserInfoURL = "https://openidconnect.googleapis.com/v1/userinfo"
)

// Scopes requested when connecting a Google account. They are the minimum
// for the Gmail actions (see docs on each):
//
//   - openid, email: the account's stable ID (sub) and its address, shown as
//     the account label. No profile, contacts or other Google data.
//   - gmail.readonly: gmail.search and gmail.read (list, search and read
//     messages; it cannot change or delete anything).
//   - gmail.compose: gmail.create_draft, gmail.send and gmail.reply (create
//     drafts and send messages; it cannot read the mailbox or delete mail).
//
// Not requested: https://mail.google.com/ (full mailbox control) and
// gmail.modify (label changes, trash).
var Scopes = []string{
	"openid",
	"email",
	"https://www.googleapis.com/auth/gmail.readonly",
	"https://www.googleapis.com/auth/gmail.compose",
}

// Options configure the provider. ClientID, ClientSecret and RedirectURL
// come from backend configuration (GOOGLE_CLIENT_ID, GOOGLE_CLIENT_SECRET,
// GOOGLE_OAUTH_REDIRECT_URI). The endpoint fields exist for tests; empty
// means Google's.
type Options struct {
	ClientID     string
	ClientSecret credential.Secret
	RedirectURL  string
	HTTPClient   *http.Client
	AuthURL      string
	TokenURL     string
	RevokeURL    string
	UserInfoURL  string
	// Timeout bounds each request to Google (default 15s).
	Timeout time.Duration
}

// Provider is the Google OAuth provider.
type Provider struct {
	cfg         oauth.ProviderConfig
	http        *http.Client
	userInfoURL string
	timeout     time.Duration
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
		return nil, oauth.NewError(oauth.KindInvalidConfiguration, "google: a client ID and a client secret are required")
	}
	cfg := oauth.ProviderConfig{
		ID: ID, Name: "Google", Description: "Gmail: search and read email, create drafts, send and reply.",
		ClientID: strings.TrimSpace(o.ClientID), ClientSecret: o.ClientSecret,
		AuthURL: or(o.AuthURL, DefaultAuthURL), TokenURL: or(o.TokenURL, DefaultTokenURL), RevokeURL: or(o.RevokeURL, DefaultRevokeURL),
		RedirectURL: strings.TrimSpace(o.RedirectURL), Scopes: append([]string(nil), Scopes...),
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !strings.HasSuffix(cfg.RedirectURL, "/api/v1/oauth/callback/"+ID) {
		return nil, oauth.NewError(oauth.KindInvalidConfiguration, "google: the redirect URI must end with /api/v1/oauth/callback/google")
	}
	hc := o.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &Provider{cfg: cfg, http: hc, userInfoURL: or(o.UserInfoURL, DefaultUserInfoURL), timeout: timeout}, nil
}

// Config implements oauth.Provider.
func (p *Provider) Config() oauth.ProviderConfig { return p.cfg }

// AuthorizationURL implements oauth.Provider: authorization code with PKCE
// (S256), offline access (a refresh token), and prompt=consent so that
// reconnecting an account issues a new refresh token too.
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
		"access_type":           {"offline"},
		"prompt":                {"consent"},
	}
	return p.cfg.AuthURL + "?" + q.Encode(), nil
}

// tokenResponse is Google's token endpoint answer.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
}

// errorResponse is Google's OAuth error body. Only the error code is used.
type errorResponse struct {
	Error string `json:"error"`
}

func (p *Provider) post(ctx context.Context, endpoint string, form url.Values) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	return p.http.Do(req)
}

// unavailable classifies a transport failure (including this provider's own
// request timeout). Cancellation of the caller's ctx passes through; nothing
// of the error text (which may contain URLs) is kept in the message.
func unavailable(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return &oauth.Error{Kind: oauth.KindProviderUnavailable, Message: "Google could not be reached", Err: err}
}

// tokenCall posts to the token endpoint and decodes the answer. failure is
// the kind for a rejected request that is not invalid_grant.
func (p *Provider) tokenCall(ctx context.Context, form url.Values, grantKind, failure oauth.Kind) (tokenResponse, error) {
	cctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	form.Set("client_id", p.cfg.ClientID)
	form.Set("client_secret", p.cfg.ClientSecret.Reveal())
	resp, err := p.post(cctx, p.cfg.TokenURL, form)
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
		return tokenResponse{}, oauth.NewError(oauth.KindProviderUnavailable, fmt.Sprintf("Google answered HTTP %d", resp.StatusCode))
	case resp.StatusCode != http.StatusOK:
		var e errorResponse
		_ = json.Unmarshal(body, &e)
		switch e.Error {
		case "invalid_grant":
			return tokenResponse{}, oauth.NewError(grantKind, "Google rejected the grant (invalid_grant)")
		case "invalid_client", "unauthorized_client":
			return tokenResponse{}, oauth.NewError(oauth.KindInvalidConfiguration, "Google rejected the OAuth client ("+e.Error+")")
		}
		return tokenResponse{}, oauth.NewError(failure, fmt.Sprintf("Google rejected the token request (HTTP %d)", resp.StatusCode))
	}
	var t tokenResponse
	if err := json.Unmarshal(body, &t); err != nil || t.AccessToken == "" {
		return tokenResponse{}, oauth.NewError(failure, "Google returned a malformed token response")
	}
	return t, nil
}

func (p *Provider) token(t tokenResponse, now time.Time) credential.OAuthToken {
	tok := credential.OAuthToken{AccessToken: credential.NewSecret(t.AccessToken), RefreshToken: credential.NewSecret(t.RefreshToken),
		TokenType: t.TokenType, Scopes: strings.Fields(t.Scope)}
	if t.ExpiresIn > 0 {
		tok.Expiry = now.Add(time.Duration(t.ExpiresIn) * time.Second).UTC()
	}
	return tok
}

// Exchange implements oauth.Provider: the code (with its PKCE verifier) is
// redeemed, every requested scope must have been granted, and the account
// is identified with userinfo's sub.
func (p *Provider) Exchange(ctx context.Context, req oauth.ExchangeRequest) (credential.OAuthToken, oauth.Identity, error) {
	t, err := p.tokenCall(ctx, url.Values{
		"grant_type": {"authorization_code"}, "code": {req.Code}, "code_verifier": {req.CodeVerifier}, "redirect_uri": {p.cfg.RedirectURL},
	}, oauth.KindInvalidCode, oauth.KindTokenExchangeFailed)
	if err != nil {
		return credential.OAuthToken{}, oauth.Identity{}, err
	}
	tok := p.token(t, time.Now())
	if tok.RefreshToken.Empty() {
		return credential.OAuthToken{}, oauth.Identity{}, oauth.NewError(oauth.KindTokenExchangeFailed, "Google issued no refresh token")
	}
	// With granular consent a user can leave permissions unchecked.
	if missing := p.missingScopes(tok.Scopes); len(missing) > 0 {
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

// missingScopes lists requested scopes Google did not grant. "openid" and
// "email" may come back as their URL forms.
func (p *Provider) missingScopes(granted []string) []string {
	aliases := map[string]string{"email": "https://www.googleapis.com/auth/userinfo.email"}
	var missing []string
	for _, s := range p.cfg.Scopes {
		if !slices.Contains(granted, s) && (aliases[s] == "" || !slices.Contains(granted, aliases[s])) && s != "openid" {
			missing = append(missing, s)
		}
	}
	return missing
}

type userInfo struct {
	Sub   string `json:"sub"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

func (p *Provider) identity(ctx context.Context, access credential.Secret) (oauth.Identity, error) {
	cctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, p.userInfoURL, nil)
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
		return oauth.Identity{}, oauth.NewError(oauth.KindProviderUnavailable, fmt.Sprintf("Google userinfo answered HTTP %d", resp.StatusCode))
	}
	if resp.StatusCode != http.StatusOK {
		return oauth.Identity{}, oauth.NewError(oauth.KindTokenExchangeFailed, fmt.Sprintf("Google userinfo answered HTTP %d", resp.StatusCode))
	}
	var u userInfo
	if err := json.Unmarshal(body, &u); err != nil || strings.TrimSpace(u.Sub) == "" {
		return oauth.Identity{}, oauth.NewError(oauth.KindTokenExchangeFailed, "Google did not identify the account")
	}
	return oauth.Identity{ProviderAccountID: u.Sub, Email: u.Email, DisplayName: u.Name}, nil
}

// Refresh implements oauth.Provider. invalid_grant (revoked access, expired
// or rotated refresh token, password change) is KindRevoked: the token
// manager then removes the tokens and the node fails with
// CREDENTIAL_REVOKED, never retried. Google normally omits the refresh token
// here; the token manager keeps the stored one.
func (p *Provider) Refresh(ctx context.Context, t credential.OAuthToken) (credential.OAuthToken, error) {
	if t.RefreshToken.Empty() {
		return credential.OAuthToken{}, oauth.NewError(oauth.KindRevoked, "there is no refresh token")
	}
	resp, err := p.tokenCall(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {t.RefreshToken.Reveal()}},
		oauth.KindRevoked, oauth.KindTokenRefreshFailed)
	if err != nil {
		return credential.OAuthToken{}, err
	}
	return p.token(resp, time.Now()), nil
}

// Revoke implements oauth.Provider: revoking the refresh token (or the
// access token when there is none) ends the whole grant. A token Google no
// longer knows counts as revoked.
func (p *Provider) Revoke(ctx context.Context, t credential.OAuthToken) error {
	token := t.RefreshToken
	if token.Empty() {
		token = t.AccessToken
	}
	if token.Empty() {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	resp, err := p.post(cctx, p.cfg.RevokeURL, url.Values{"token": {token.Reveal()}})
	if err != nil {
		return unavailable(ctx, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode == http.StatusBadRequest:
		var e errorResponse
		if json.Unmarshal(body, &e) == nil && (e.Error == "invalid_token" || e.Error == "invalid_request") {
			return nil
		}
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return oauth.NewError(oauth.KindProviderUnavailable, fmt.Sprintf("Google revocation answered HTTP %d", resp.StatusCode))
	}
	return errors.New("google: revocation failed")
}
