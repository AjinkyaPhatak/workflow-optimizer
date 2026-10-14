// Package notion is the Notion implementation of oauth.Provider (Phase C5):
// a Notion public integration's authorization-code flow. The user picks the
// pages and databases to share in Notion's own consent screen; Notion has no
// scopes.
//
// The token endpoint authenticates the client with HTTP Basic and answers
// with the access token, a refresh token (rotated on every refresh), and the
// workspace and user that authorized; no extra identity call is needed.
// Notion does not support PKCE: the flow's code challenge is not sent.
//
// It holds no per-user state and never logs. Errors are *oauth.Error whose
// messages are written here: Notion's response bodies are never copied.
package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/oauth"
)

// ID is the provider ID: the callback is /api/v1/oauth/callback/notion and
// Notion credentials have provider "notion".
const ID = "notion"

// APIVersion is the Notion-Version header sent with every request.
const APIVersion = "2025-09-03"

// Notion's endpoints.
const (
	DefaultAuthURL   = "https://api.notion.com/v1/oauth/authorize"
	DefaultTokenURL  = "https://api.notion.com/v1/oauth/token"
	DefaultRevokeURL = "https://api.notion.com/v1/oauth/revoke"
)

// Options configure the provider. ClientID, ClientSecret and RedirectURL
// come from backend configuration (NOTION_CLIENT_ID, NOTION_CLIENT_SECRET,
// NOTION_OAUTH_REDIRECT_URI). The endpoint fields exist for tests.
type Options struct {
	ClientID     string
	ClientSecret credential.Secret
	RedirectURL  string
	HTTPClient   *http.Client
	AuthURL      string
	TokenURL     string
	RevokeURL    string
	// Timeout bounds each request to Notion (default 15s).
	Timeout time.Duration
}

// Provider is the Notion OAuth provider.
type Provider struct {
	cfg     oauth.ProviderConfig
	http    *http.Client
	timeout time.Duration
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
		return nil, oauth.NewError(oauth.KindInvalidConfiguration, "notion: a client ID and a client secret are required")
	}
	cfg := oauth.ProviderConfig{
		ID: ID, Name: "Notion", Description: "Notion: search pages and databases, and find, add and update database rows. You choose which pages to share.",
		ClientID: strings.TrimSpace(o.ClientID), ClientSecret: o.ClientSecret,
		AuthURL: or(o.AuthURL, DefaultAuthURL), TokenURL: or(o.TokenURL, DefaultTokenURL), RevokeURL: or(o.RevokeURL, DefaultRevokeURL),
		RedirectURL: strings.TrimSpace(o.RedirectURL),
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !strings.HasSuffix(cfg.RedirectURL, "/api/v1/oauth/callback/"+ID) {
		return nil, oauth.NewError(oauth.KindInvalidConfiguration, "notion: the redirect URI must end with /api/v1/oauth/callback/notion")
	}
	hc := o.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &Provider{cfg: cfg, http: hc, timeout: timeout}, nil
}

// Config implements oauth.Provider.
func (p *Provider) Config() oauth.ProviderConfig { return p.cfg }

// AuthorizationURL implements oauth.Provider.
func (p *Provider) AuthorizationURL(req oauth.AuthorizationRequest) (string, error) {
	if req.State == "" {
		return "", oauth.NewError(oauth.KindInvalidConfiguration, "state is required")
	}
	q := url.Values{
		"client_id":     {p.cfg.ClientID},
		"redirect_uri":  {p.cfg.RedirectURL},
		"response_type": {"code"},
		"owner":         {"user"},
		"state":         {req.State},
	}
	return p.cfg.AuthURL + "?" + q.Encode(), nil
}

// tokenResponse is Notion's token endpoint answer.
type tokenResponse struct {
	AccessToken   string `json:"access_token"`
	RefreshToken  string `json:"refresh_token"`
	TokenType     string `json:"token_type"`
	ExpiresIn     int64  `json:"expires_in"`
	BotID         string `json:"bot_id"`
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceName string `json:"workspace_name"`
	Owner         struct {
		Type string `json:"type"`
		User struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Person struct {
				Email string `json:"email"`
			} `json:"person"`
		} `json:"user"`
	} `json:"owner"`
}

// errorResponse is Notion's OAuth error body. Only the code is used.
type errorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func (e errorResponse) code() string {
	if e.Error != "" {
		return e.Error
	}
	return e.Code
}

func unavailable(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return &oauth.Error{Kind: oauth.KindProviderUnavailable, Message: "Notion could not be reached", Err: err}
}

// post sends a JSON body with the client's Basic credentials.
func (p *Provider) post(ctx context.Context, endpoint string, body any) (int, []byte, error) {
	cctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return 0, nil, err
	}
	req.SetBasicAuth(p.cfg.ClientID, p.cfg.ClientSecret.Reveal())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Notion-Version", APIVersion)
	resp, err := p.http.Do(req)
	if err != nil {
		return 0, nil, unavailable(ctx, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, unavailable(ctx, err)
	}
	return resp.StatusCode, data, nil
}

// tokenCall posts to the token endpoint. grantKind is the kind for
// invalid_grant, failure for any other rejection.
func (p *Provider) tokenCall(ctx context.Context, body map[string]string, grantKind, failure oauth.Kind) (tokenResponse, error) {
	status, data, err := p.post(ctx, p.cfg.TokenURL, body)
	if err != nil {
		return tokenResponse{}, err
	}
	switch {
	case status == http.StatusTooManyRequests || status >= 500:
		return tokenResponse{}, oauth.NewError(oauth.KindProviderUnavailable, fmt.Sprintf("Notion answered HTTP %d", status))
	case status != http.StatusOK:
		var e errorResponse
		_ = json.Unmarshal(data, &e)
		switch e.code() {
		case "invalid_grant":
			return tokenResponse{}, oauth.NewError(grantKind, "Notion rejected the grant (invalid_grant)")
		case "invalid_client", "unauthorized_client":
			return tokenResponse{}, oauth.NewError(oauth.KindInvalidConfiguration, "Notion rejected the OAuth client ("+e.code()+")")
		}
		return tokenResponse{}, oauth.NewError(failure, fmt.Sprintf("Notion rejected the token request (HTTP %d)", status))
	}
	var t tokenResponse
	if err := json.Unmarshal(data, &t); err != nil || t.AccessToken == "" {
		return tokenResponse{}, oauth.NewError(failure, "Notion returned a malformed token response")
	}
	return t, nil
}

func token(t tokenResponse, now time.Time) credential.OAuthToken {
	tok := credential.OAuthToken{AccessToken: credential.NewSecret(t.AccessToken), RefreshToken: credential.NewSecret(t.RefreshToken), TokenType: t.TokenType}
	if t.ExpiresIn > 0 {
		tok.Expiry = now.Add(time.Duration(t.ExpiresIn) * time.Second).UTC()
	}
	return tok
}

// Exchange implements oauth.Provider. The account is the authorizing user
// in the workspace (one Notion user can connect several workspaces).
func (p *Provider) Exchange(ctx context.Context, req oauth.ExchangeRequest) (credential.OAuthToken, oauth.Identity, error) {
	t, err := p.tokenCall(ctx, map[string]string{"grant_type": "authorization_code", "code": req.Code, "redirect_uri": p.cfg.RedirectURL},
		oauth.KindInvalidCode, oauth.KindTokenExchangeFailed)
	if err != nil {
		return credential.OAuthToken{}, oauth.Identity{}, err
	}
	who := t.Owner.User.ID
	if t.Owner.Type != "user" || who == "" {
		who = "bot:" + t.BotID
	}
	if t.WorkspaceID == "" || who == "bot:" {
		return credential.OAuthToken{}, oauth.Identity{}, oauth.NewError(oauth.KindTokenExchangeFailed, "Notion did not identify the workspace")
	}
	tok := token(t, time.Now())
	tok.ProviderAccountID = t.WorkspaceID + "/" + who
	name := t.WorkspaceName
	if name == "" {
		name = t.Owner.User.Name
	}
	return tok, oauth.Identity{ProviderAccountID: tok.ProviderAccountID, Email: t.Owner.User.Person.Email, DisplayName: name}, nil
}

// Refresh implements oauth.Provider. Notion rotates the refresh token: the
// new one replaces the stored one. invalid_grant is KindRevoked.
func (p *Provider) Refresh(ctx context.Context, t credential.OAuthToken) (credential.OAuthToken, error) {
	if t.RefreshToken.Empty() {
		return credential.OAuthToken{}, oauth.NewError(oauth.KindRevoked, "there is no refresh token")
	}
	resp, err := p.tokenCall(ctx, map[string]string{"grant_type": "refresh_token", "refresh_token": t.RefreshToken.Reveal()},
		oauth.KindRevoked, oauth.KindTokenRefreshFailed)
	if err != nil {
		return credential.OAuthToken{}, err
	}
	return token(resp, time.Now()), nil
}

// Revoke implements oauth.Provider: the access token is revoked, which
// removes the integration's access. A token Notion no longer knows counts
// as revoked.
func (p *Provider) Revoke(ctx context.Context, t credential.OAuthToken) error {
	if t.AccessToken.Empty() {
		return nil
	}
	status, data, err := p.post(ctx, p.cfg.RevokeURL, map[string]string{"token": t.AccessToken.Reveal()})
	if err != nil {
		return err
	}
	switch {
	case status == http.StatusOK:
		return nil
	case status == http.StatusBadRequest || status == http.StatusUnauthorized:
		var e errorResponse
		if json.Unmarshal(data, &e) == nil && (e.code() == "invalid_grant" || e.code() == "invalid_token" || e.code() == "unauthorized") {
			return nil
		}
	case status == http.StatusTooManyRequests || status >= 500:
		return oauth.NewError(oauth.KindProviderUnavailable, fmt.Sprintf("Notion revocation answered HTTP %d", status))
	}
	return oauth.NewError(oauth.KindProviderUnavailable, fmt.Sprintf("Notion revocation failed (HTTP %d)", status))
}
