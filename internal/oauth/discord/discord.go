// Package discord is the Discord implementation of oauth.Provider (Phase
// C6): connecting Discord means adding this application's bot to a server
// (Discord's "advanced bot authorization": scopes identify and bot, with an
// authorization code). The connected account is the server: the token
// response names the guild the bot was added to, and that guild's ID is the
// provider account ID that Discord nodes are confined to.
//
// The user's own token is kept (and refreshed) only as proof of the
// authorization; Discord nodes act with the bot token from configuration.
//
// It holds no per-user state and never logs. Errors are *oauth.Error whose
// messages are written here; Discord's bodies are never copied.
package discord

import (
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

// ID is the provider ID: the callback is /api/v1/oauth/callback/discord and
// Discord credentials have provider "discord".
const ID = "discord"

// Discord's endpoints.
const (
	DefaultAuthURL   = "https://discord.com/oauth2/authorize"
	DefaultTokenURL  = "https://discord.com/api/v10/oauth2/token"
	DefaultRevokeURL = "https://discord.com/api/v10/oauth2/token/revoke"
)

// Scopes: identify (the authorizing user; no email, no servers list) and bot
// (adds the bot to the server the user picks).
var Scopes = []string{"identify", "bot"}

// Permissions the bot asks for in the server: View Channels (1<<10), Send
// Messages (1<<11), Embed Links (1<<14), Read Message History (1<<16).
// Nothing administrative: no managing channels, roles, members or messages.
const Permissions = 1<<10 | 1<<11 | 1<<14 | 1<<16

// Options configure the provider (DISCORD_CLIENT_ID, DISCORD_CLIENT_SECRET,
// DISCORD_OAUTH_REDIRECT_URI). The endpoint fields exist for tests.
type Options struct {
	ClientID     string
	ClientSecret credential.Secret
	RedirectURL  string
	HTTPClient   *http.Client
	AuthURL      string
	TokenURL     string
	RevokeURL    string
	// Timeout bounds each request to Discord (default 15s).
	Timeout time.Duration
}

// Provider is the Discord OAuth provider.
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
		return nil, oauth.NewError(oauth.KindInvalidConfiguration, "discord: a client ID and a client secret are required")
	}
	cfg := oauth.ProviderConfig{
		ID: ID, Name: "Discord", Description: "Discord: add the bot to a server to send messages to its channels and read recent messages.",
		ClientID: strings.TrimSpace(o.ClientID), ClientSecret: o.ClientSecret,
		AuthURL: or(o.AuthURL, DefaultAuthURL), TokenURL: or(o.TokenURL, DefaultTokenURL), RevokeURL: or(o.RevokeURL, DefaultRevokeURL),
		RedirectURL: strings.TrimSpace(o.RedirectURL), Scopes: append([]string(nil), Scopes...),
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !strings.HasSuffix(cfg.RedirectURL, "/api/v1/oauth/callback/"+ID) {
		return nil, oauth.NewError(oauth.KindInvalidConfiguration, "discord: the redirect URI must end with /api/v1/oauth/callback/discord")
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

// AuthorizationURL implements oauth.Provider: the user picks a server to add
// the bot to (guild install) and approves its permissions.
func (p *Provider) AuthorizationURL(req oauth.AuthorizationRequest) (string, error) {
	if req.State == "" {
		return "", oauth.NewError(oauth.KindInvalidConfiguration, "state is required")
	}
	q := url.Values{
		"client_id":        {p.cfg.ClientID},
		"redirect_uri":     {p.cfg.RedirectURL},
		"response_type":    {"code"},
		"scope":            {strings.Join(p.cfg.Scopes, " ")},
		"permissions":      {fmt.Sprint(Permissions)},
		"integration_type": {"0"},
		"state":            {req.State},
	}
	return p.cfg.AuthURL + "?" + q.Encode(), nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	Guild        *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"guild"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func unavailable(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return &oauth.Error{Kind: oauth.KindProviderUnavailable, Message: "Discord could not be reached", Err: err}
}

// post sends a form with the client's Basic credentials (Discord's token
// endpoints do not accept JSON).
func (p *Provider) post(ctx context.Context, endpoint string, form url.Values) (int, []byte, error) {
	cctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, err
	}
	req.SetBasicAuth(p.cfg.ClientID, p.cfg.ClientSecret.Reveal())
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
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

func (p *Provider) tokenCall(ctx context.Context, form url.Values, grantKind, failure oauth.Kind) (tokenResponse, error) {
	status, data, err := p.post(ctx, p.cfg.TokenURL, form)
	if err != nil {
		return tokenResponse{}, err
	}
	switch {
	case status == http.StatusTooManyRequests || status >= 500:
		return tokenResponse{}, oauth.NewError(oauth.KindProviderUnavailable, fmt.Sprintf("Discord answered HTTP %d", status))
	case status != http.StatusOK:
		var e errorResponse
		_ = json.Unmarshal(data, &e)
		switch e.Error {
		case "invalid_grant":
			return tokenResponse{}, oauth.NewError(grantKind, "Discord rejected the grant (invalid_grant)")
		case "invalid_client", "unauthorized_client":
			return tokenResponse{}, oauth.NewError(oauth.KindInvalidConfiguration, "Discord rejected the OAuth client ("+e.Error+")")
		}
		return tokenResponse{}, oauth.NewError(failure, fmt.Sprintf("Discord rejected the token request (HTTP %d)", status))
	}
	var t tokenResponse
	if err := json.Unmarshal(data, &t); err != nil || t.AccessToken == "" {
		return tokenResponse{}, oauth.NewError(failure, "Discord returned a malformed token response")
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

// Exchange implements oauth.Provider. The account is the server the bot was
// added to, as named by the token response (the redirect's guild_id
// parameter is only a hint and is not used).
func (p *Provider) Exchange(ctx context.Context, req oauth.ExchangeRequest) (credential.OAuthToken, oauth.Identity, error) {
	t, err := p.tokenCall(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {req.Code}, "redirect_uri": {p.cfg.RedirectURL}},
		oauth.KindInvalidCode, oauth.KindTokenExchangeFailed)
	if err != nil {
		return credential.OAuthToken{}, oauth.Identity{}, err
	}
	if t.Guild == nil || strings.TrimSpace(t.Guild.ID) == "" {
		return credential.OAuthToken{}, oauth.Identity{}, oauth.NewError(oauth.KindAuthorizationDenied, "the bot was not added to a server")
	}
	tok := token(t, time.Now())
	tok.ProviderAccountID = t.Guild.ID
	name := t.Guild.Name
	if name == "" {
		name = "Server " + t.Guild.ID
	}
	return tok, oauth.Identity{ProviderAccountID: t.Guild.ID, DisplayName: name}, nil
}

// Refresh implements oauth.Provider. invalid_grant is KindRevoked.
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

// Revoke implements oauth.Provider: it ends the user's authorization. The
// bot stays in the server until someone removes it there.
func (p *Provider) Revoke(ctx context.Context, t credential.OAuthToken) error {
	tok, hint := t.RefreshToken, "refresh_token"
	if tok.Empty() {
		tok, hint = t.AccessToken, "access_token"
	}
	if tok.Empty() {
		return nil
	}
	status, data, err := p.post(ctx, p.cfg.RevokeURL, url.Values{"token": {tok.Reveal()}, "token_type_hint": {hint}})
	if err != nil {
		return err
	}
	switch {
	case status == http.StatusOK:
		return nil
	case status == http.StatusBadRequest:
		var e errorResponse
		if json.Unmarshal(data, &e) == nil && (e.Error == "invalid_token" || e.Error == "invalid_grant") {
			return nil
		}
	case status == http.StatusTooManyRequests || status >= 500:
		return oauth.NewError(oauth.KindProviderUnavailable, fmt.Sprintf("Discord revocation answered HTTP %d", status))
	}
	return oauth.NewError(oauth.KindProviderUnavailable, fmt.Sprintf("Discord revocation failed (HTTP %d)", status))
}
