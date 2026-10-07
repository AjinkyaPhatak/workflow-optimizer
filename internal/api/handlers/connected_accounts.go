package handlers

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"workflow-optimizer/internal/api/httpx"
	"workflow-optimizer/internal/api/requests"
	"workflow-optimizer/internal/api/responses"
	"workflow-optimizer/internal/connectedaccount"
	"workflow-optimizer/internal/oauth"
)

// ConnectedAccountsPage is where the browser lands after an OAuth callback.
// It is a path on the same origin as the API (the frontend proxies
// /api/v1), so no open redirect is possible.
const ConnectedAccountsPage = "/settings/connected-accounts"

// oauthCallbackPath is the cookie path of browser bindings.
const oauthCallbackPath = "/api/v1/oauth/callback/"

// bindingCookie is the name of a flow's browser-binding cookie: one per
// state, so parallel flows do not overwrite each other.
func bindingCookie(stateToken string) string {
	return "wo_oauth_" + oauth.StateKey(stateToken)[:24]
}

func secureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (h *Handlers) connectedAccount(a connectedaccount.Account) responses.ConnectedAccount {
	return responses.NewConnectedAccount(a, h.ConnectedAccounts.ProviderName(a.Provider))
}

// ListOAuthProviders handles GET /api/v1/oauth/providers: the configured
// providers (public metadata only; none in a default deployment).
func (h *Handlers) ListOAuthProviders(w http.ResponseWriter, r *http.Request) {
	ps := h.ConnectedAccounts.Providers()
	out := responses.List[responses.OAuthProvider]{Items: make([]responses.OAuthProvider, 0, len(ps))}
	for _, p := range ps {
		out.Items = append(out.Items, responses.NewOAuthProvider(p))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// ListConnectedAccounts handles GET /api/v1/connected-accounts?workspace_id=.
func (h *Handlers) ListConnectedAccounts(w http.ResponseWriter, r *http.Request) {
	ws, ok := h.queryID(w, r, "workspace_id")
	if !ok {
		return
	}
	as, err := h.ConnectedAccounts.List(r.Context(), user(r), ws)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := responses.List[responses.ConnectedAccount]{Items: make([]responses.ConnectedAccount, 0, len(as))}
	for _, a := range as {
		out.Items = append(out.Items, h.connectedAccount(a))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// GetConnectedAccount handles GET /api/v1/connected-accounts/{accountID}.
func (h *Handlers) GetConnectedAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r, "accountID")
	if !ok {
		return
	}
	a, err := h.ConnectedAccounts.Get(r.Context(), user(r), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.connectedAccount(a))
}

// DisconnectConnectedAccount handles DELETE /api/v1/connected-accounts/{accountID}.
// The account stays listed as DISCONNECTED; its tokens are removed.
func (h *Handlers) DisconnectConnectedAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r, "accountID")
	if !ok {
		return
	}
	a, err := h.ConnectedAccounts.Disconnect(r.Context(), user(r), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.connectedAccount(a))
}

// AuthorizeConnectedAccount handles
// POST /api/v1/connected-accounts/{provider}/authorize: it starts an OAuth
// flow and returns the provider URL to send the browser to. It also sets
// the flow's browser binding, an HttpOnly cookie only the callback can read.
func (h *Handlers) AuthorizeConnectedAccount(w http.ResponseWriter, r *http.Request) {
	var req requests.AuthorizeConnectedAccount
	if !h.decode(w, r, &req) {
		return
	}
	ws, err := req.Validate()
	if err != nil {
		h.fail(w, r, err)
		return
	}
	started, err := h.ConnectedAccounts.Authorize(r.Context(), user(r), ws, r.PathValue("provider"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: bindingCookie(started.StateToken), Value: started.BindingToken, Path: oauthCallbackPath,
		MaxAge: int(time.Until(started.ExpiresAt).Seconds()) + 1, HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteLaxMode,
	})
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, responses.AuthorizationStarted{AuthorizationURL: started.AuthorizationURL, ExpiresAt: started.ExpiresAt})
}

// OAuthCallback handles GET /api/v1/oauth/callback/{provider}, the provider's
// redirect. It is public (a browser redirect carries no bearer token): the
// server-side state says whose flow it is, and the binding cookie proves it
// is the same browser. It always answers with a redirect to the connected
// accounts page carrying only an outcome (never a token, code or the
// provider's error text).
func (h *Handlers) OAuthCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := q.Get("state")
	cb := oauth.Callback{State: state, Code: q.Get("code"), Error: q.Get("error")}
	if state != "" {
		name := bindingCookie(state)
		if c, err := r.Cookie(name); err == nil {
			cb.Binding = c.Value
		}
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: oauthCallbackPath, MaxAge: -1,
			HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteLaxMode})
	}
	provider := r.PathValue("provider")
	outcome := url.Values{}
	if a, err := h.ConnectedAccounts.Callback(r.Context(), provider, cb); err != nil {
		kind := oauth.KindOf(err)
		if kind == "" {
			kind = oauth.KindTokenExchangeFailed
		}
		outcome.Set("error", string(kind))
	} else {
		outcome.Set("connected", a.Provider)
		outcome.Set("account", a.ID.String())
	}
	w.Header().Set("Cache-Control", "no-store")
	// The callback URL carries the code: never send it on as a Referer.
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, ConnectedAccountsPage+"?"+outcome.Encode(), http.StatusSeeOther)
}
