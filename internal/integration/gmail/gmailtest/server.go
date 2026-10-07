// Package gmailtest is an in-process mock of Google's OAuth 2.0 endpoints
// and the Gmail REST API for automated tests (Phase C3). It enforces what
// the real services do for this integration: client credentials, PKCE,
// single-use codes, expiring access tokens, refresh, revocation, bearer
// authentication of Gmail calls. It is never used in production.
package gmailtest

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/integration/gmail"
	"workflow-optimizer/internal/oauth/google"
)

// Client credentials the mock accepts.
const (
	ClientID     = "mock-client-id.apps.googleusercontent.com"
	ClientSecret = "mock-client-secret-DO-NOT-LEAK"
)

// Token prefixes, for leak checks.
const (
	AccessPrefix  = "ya29.mock-access-"
	RefreshPrefix = "1//mock-refresh-"
	CodePrefix    = "4/mock-code-"
)

// Account is a Google account of the mock.
type Account struct {
	Sub, Email, Name string
}

type grant struct {
	account Account
	scopes  []string
	revoked bool
}

type code struct {
	grant     *grant
	challenge string
	redirect  string
	used      bool
}

type access struct {
	grant   *grant
	expires time.Time
}

// Request is a recorded Gmail API request (without its Authorization header
// value: only whether it was authorized).
type Request struct {
	Method, Path string
	Query        url.Values
	Authorized   bool
}

// Sent is a message the Gmail API received (send or draft), decoded.
type Sent struct {
	Kind     string // "send" or "draft"
	RFC822   string
	ThreadID string
}

// Failure makes the next matching request answer with Status, Body and
// Retry-After (when set). Delay slows it down instead (timeouts).
type Failure struct {
	Status     int
	Body       string
	RetryAfter string
	Delay      time.Duration
}

// Server is the mock.
type Server struct {
	*httptest.Server

	mu       sync.Mutex
	codes    map[string]*code
	access   map[string]*access
	refresh  map[string]*grant
	messages []map[string]any
	sent     []Sent
	requests []Request
	failures map[string][]Failure
	counts   map[string]int

	// AccessTTL is the lifetime of issued access tokens (expires_in).
	AccessTTL time.Duration
	// GrantedScopes, when set, replaces the requested scopes in the token
	// response (a user unchecking permissions).
	GrantedScopes []string
}

// New starts the mock; it is closed with t.Cleanup by the caller.
func New() *Server {
	s := &Server{codes: map[string]*code{}, access: map[string]*access{}, refresh: map[string]*grant{},
		failures: map[string][]Failure{}, counts: map[string]int{}, AccessTTL: time.Hour}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", s.token)
	mux.HandleFunc("POST /revoke", s.revoke)
	mux.HandleFunc("GET /userinfo", s.userinfo)
	mux.HandleFunc("GET /gmail/v1/users/me/messages", s.list)
	mux.HandleFunc("GET /gmail/v1/users/me/messages/{id}", s.get)
	mux.HandleFunc("POST /gmail/v1/users/me/messages/send", s.send)
	mux.HandleFunc("POST /gmail/v1/users/me/drafts", s.draft)
	s.Server = httptest.NewServer(mux)
	return s
}

// GoogleOptions points the Google OAuth provider at the mock.
func (s *Server) GoogleOptions(redirectURL string) google.Options {
	return google.Options{ClientID: ClientID, ClientSecret: credential.NewSecret(ClientSecret), RedirectURL: redirectURL,
		AuthURL: "https://accounts.google.test/o/oauth2/v2/auth", TokenURL: s.URL + "/token", RevokeURL: s.URL + "/revoke",
		UserInfoURL: s.URL + "/userinfo", HTTPClient: s.Client()}
}

// GmailOptions points the Gmail client at the mock.
func (s *Server) GmailOptions() gmail.Options {
	return gmail.Options{BaseURL: s.URL + "/gmail/v1", HTTPClient: s.Client()}
}

func random(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// Authorize plays the user consenting on Google's page for account: it
// checks the authorization URL and returns the code and state Google would
// redirect with.
func (s *Server) Authorize(authorizationURL string, account Account) (codeValue, state string, err error) {
	u, err := url.Parse(authorizationURL)
	if err != nil {
		return "", "", err
	}
	q := u.Query()
	if q.Get("client_id") != ClientID || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" ||
		q.Get("code_challenge") == "" || q.Get("access_type") != "offline" || q.Get("redirect_uri") == "" {
		return "", "", fmt.Errorf("gmailtest: invalid authorization request: %s", authorizationURL)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	codeValue = random(CodePrefix)
	s.codes[codeValue] = &code{grant: &grant{account: account, scopes: strings.Fields(q.Get("scope"))},
		challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri")}
	return codeValue, q.Get("state"), nil
}

// Fail queues a failure for the next request whose path starts with prefix.
func (s *Server) Fail(prefix string, f Failure) {
	s.mu.Lock()
	s.failures[prefix] = append(s.failures[prefix], f)
	s.mu.Unlock()
}

// RevokeAccount revokes every grant of an account (the user removing the
// app in their Google account settings).
func (s *Server) RevokeAccount(sub string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.refresh {
		if g.account.Sub == sub {
			g.revoked = true
		}
	}
}

// AddMessage stores a message resource (Gmail's JSON shape).
func (s *Server) AddMessage(m map[string]any) {
	s.mu.Lock()
	s.messages = append(s.messages, m)
	s.mu.Unlock()
}

// Sent returns what was sent or drafted.
func (s *Server) Sent() []Sent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Sent{}, s.sent...)
}

// Requests returns the recorded Gmail API requests.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request{}, s.requests...)
}

// Count returns how often an endpoint ("token:authorization_code",
// "token:refresh_token", "revoke", "userinfo") was called.
func (s *Server) Count(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts[name]
}

// ValidAccessToken reports whether Gmail would accept the token.
func (s *Server) ValidAccessToken(t string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.access[t]
	return ok && !a.grant.revoked && time.Now().Before(a.expires)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// injected answers a queued failure, if any (lock held by the caller is
// released for delays).
func (s *Server) injected(w http.ResponseWriter, r *http.Request) bool {
	s.mu.Lock()
	var f *Failure
	for prefix, fs := range s.failures {
		if strings.HasPrefix(r.URL.Path, prefix) && len(fs) > 0 {
			f = &fs[0]
			s.failures[prefix] = fs[1:]
			break
		}
	}
	s.mu.Unlock()
	if f == nil {
		return false
	}
	if f.Delay > 0 {
		select {
		case <-r.Context().Done():
		case <-time.After(f.Delay):
		}
		return false
	}
	if f.RetryAfter != "" {
		w.Header().Set("Retry-After", f.RetryAfter)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(f.Status)
	_, _ = w.Write([]byte(f.Body))
	return true
}

func oauthError(w http.ResponseWriter, status int, code string) {
	// Real Google bodies carry a description; it must never be copied.
	writeJSON(w, status, map[string]any{"error": code, "error_description": "Bad Request ya29.SHOULD-NOT-LEAK"})
}

func (s *Server) issue(g *grant, withRefresh bool) map[string]any {
	at := random(AccessPrefix)
	s.access[at] = &access{grant: g, expires: time.Now().Add(s.AccessTTL)}
	scopes := g.scopes
	if s.GrantedScopes != nil {
		scopes = s.GrantedScopes
	}
	out := map[string]any{"access_token": at, "expires_in": int(s.AccessTTL.Seconds()), "token_type": "Bearer", "scope": strings.Join(scopes, " ")}
	if withRefresh {
		rt := random(RefreshPrefix)
		s.refresh[rt] = g
		out["refresh_token"] = rt
	}
	return out
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if s.injected(w, r) {
		return
	}
	_ = r.ParseForm()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counts["token:"+r.PostForm.Get("grant_type")]++
	if r.PostForm.Get("client_id") != ClientID || r.PostForm.Get("client_secret") != ClientSecret {
		oauthError(w, 401, "invalid_client")
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		c, ok := s.codes[r.PostForm.Get("code")]
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if !ok || c.used || base64.RawURLEncoding.EncodeToString(sum[:]) != c.challenge || r.PostForm.Get("redirect_uri") != c.redirect {
			oauthError(w, 400, "invalid_grant")
			return
		}
		c.used = true
		writeJSON(w, 200, s.issue(c.grant, true))
	case "refresh_token":
		g, ok := s.refresh[r.PostForm.Get("refresh_token")]
		if !ok || g.revoked {
			oauthError(w, 400, "invalid_grant")
			return
		}
		writeJSON(w, 200, s.issue(g, false)) // Google omits refresh_token here
	default:
		oauthError(w, 400, "unsupported_grant_type")
	}
}

func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	if s.injected(w, r) {
		return
	}
	_ = r.ParseForm()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counts["revoke"]++
	t := r.PostForm.Get("token")
	if g, ok := s.refresh[t]; ok {
		g.revoked = true
		w.WriteHeader(200)
		return
	}
	if a, ok := s.access[t]; ok {
		a.grant.revoked = true
		w.WriteHeader(200)
		return
	}
	oauthError(w, 400, "invalid_token")
}

// bearer returns the grant of a valid bearer token.
func (s *Server) bearer(r *http.Request) (*grant, bool) {
	t, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return nil, false
	}
	a, ok := s.access[t]
	if !ok || a.grant.revoked || !time.Now().Before(a.expires) {
		return nil, false
	}
	return a.grant, true
}

func (s *Server) userinfo(w http.ResponseWriter, r *http.Request) {
	if s.injected(w, r) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counts["userinfo"]++
	g, ok := s.bearer(r)
	if !ok {
		writeJSON(w, 401, map[string]any{"error": "invalid_token"})
		return
	}
	writeJSON(w, 200, map[string]any{"sub": g.account.Sub, "email": g.account.Email, "email_verified": true, "name": g.account.Name})
}

func gmailError(w http.ResponseWriter, status int, reason string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": status, "message": "Request had invalid authentication credentials ya29.SHOULD-NOT-LEAK",
		"errors": []any{map[string]any{"reason": reason}}}})
}

// gmailAuth records the request and checks its bearer token.
func (s *Server) gmailAuth(w http.ResponseWriter, r *http.Request) bool {
	s.mu.Lock()
	_, ok := s.bearer(r)
	s.requests = append(s.requests, Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Authorized: ok})
	s.mu.Unlock()
	if s.injected(w, r) {
		return false
	}
	if !ok {
		gmailError(w, 401, "authError")
		return false
	}
	return true
}

func header(m map[string]any, name string) string {
	p, _ := m["payload"].(map[string]any)
	hs, _ := p["headers"].([]any)
	for _, h := range hs {
		hm := h.(map[string]any)
		if strings.EqualFold(hm["name"].(string), name) {
			return hm["value"].(string)
		}
	}
	return ""
}

// list supports "from:x" and plain-word queries (matched against subject).
func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	if !s.gmailAuth(w, r) {
		return
	}
	q := strings.ToLower(r.URL.Query().Get("q"))
	max, _ := strconv.Atoi(r.URL.Query().Get("maxResults"))
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []any{}
	for _, m := range s.messages {
		match := true
		for _, term := range strings.Fields(q) {
			if v, ok := strings.CutPrefix(term, "from:"); ok {
				match = match && strings.Contains(strings.ToLower(header(m, "From")), v)
			} else if !strings.Contains(term, ":") {
				match = match && strings.Contains(strings.ToLower(header(m, "Subject")), term)
			}
		}
		if match && (max == 0 || len(out) < max) {
			out = append(out, map[string]any{"id": m["id"], "threadId": m["threadId"]})
		}
	}
	resp := map[string]any{"resultSizeEstimate": len(out)}
	if len(out) > 0 {
		resp["messages"] = out
	}
	writeJSON(w, 200, resp)
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	if !s.gmailAuth(w, r) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.messages {
		if m["id"] == r.PathValue("id") {
			writeJSON(w, 200, m)
			return
		}
	}
	gmailError(w, 404, "notFound")
}

type rawBody struct {
	Raw      string `json:"raw"`
	ThreadID string `json:"threadId"`
	Message  *struct {
		Raw string `json:"raw"`
	} `json:"message"`
}

func decodeRaw(s string) string {
	b, err := base64.URLEncoding.DecodeString(s)
	if err != nil {
		return ""
	}
	return string(b)
}

func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	if !s.gmailAuth(w, r) {
		return
	}
	var b rawBody
	if json.NewDecoder(r.Body).Decode(&b) != nil || decodeRaw(b.Raw) == "" {
		gmailError(w, 400, "invalidArgument")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.sent) + 1
	thread := b.ThreadID
	if thread == "" {
		thread = fmt.Sprintf("thread-sent-%d", n)
	}
	s.sent = append(s.sent, Sent{Kind: "send", RFC822: decodeRaw(b.Raw), ThreadID: b.ThreadID})
	writeJSON(w, 200, map[string]any{"id": fmt.Sprintf("sent-%d", n), "threadId": thread, "labelIds": []string{"SENT"}})
}

func (s *Server) draft(w http.ResponseWriter, r *http.Request) {
	if !s.gmailAuth(w, r) {
		return
	}
	var b rawBody
	if json.NewDecoder(r.Body).Decode(&b) != nil || b.Message == nil || decodeRaw(b.Message.Raw) == "" {
		gmailError(w, 400, "invalidArgument")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.sent) + 1
	s.sent = append(s.sent, Sent{Kind: "draft", RFC822: decodeRaw(b.Message.Raw)})
	writeJSON(w, 200, map[string]any{"id": fmt.Sprintf("r-draft-%d", n), "message": map[string]any{"id": fmt.Sprintf("draft-msg-%d", n), "threadId": fmt.Sprintf("thread-draft-%d", n)}})
}

// Message builds a Gmail message resource. parts, when given, become the
// payload's multipart children; otherwise body is a text/plain payload.
func Message(id, thread string, headers map[string]string, body string, parts ...map[string]any) map[string]any {
	hs := []any{}
	for _, k := range []string{"From", "To", "Cc", "Subject", "Date", "Message-ID", "References", "Reply-To"} {
		if v, ok := headers[k]; ok {
			hs = append(hs, map[string]any{"name": k, "value": v})
		}
	}
	payload := map[string]any{"mimeType": "text/plain", "headers": hs, "body": map[string]any{"size": len(body), "data": base64.URLEncoding.EncodeToString([]byte(body))}}
	if len(parts) > 0 {
		ps := []any{}
		for _, p := range parts {
			ps = append(ps, p)
		}
		payload = map[string]any{"mimeType": "multipart/mixed", "headers": hs, "body": map[string]any{"size": 0}, "parts": ps}
	}
	return map[string]any{"id": id, "threadId": thread, "snippet": body, "internalDate": "1791370800000", "labelIds": []any{"INBOX", "UNREAD"}, "payload": payload}
}

// Part builds a MIME part (filename set: an attachment).
func Part(mimeType, filename, data string, children ...map[string]any) map[string]any {
	p := map[string]any{"mimeType": mimeType, "filename": filename, "body": map[string]any{"size": len(data), "data": base64.URLEncoding.EncodeToString([]byte(data))}}
	if filename != "" {
		p["body"] = map[string]any{"size": len(data), "attachmentId": "att-" + filename}
	}
	if len(children) > 0 {
		cs := []any{}
		for _, c := range children {
			cs = append(cs, c)
		}
		p["parts"] = cs
	}
	return p
}
