package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/credential"
)

// FlowConnect is the only flow so far: connect (or reconnect) an account.
const FlowConnect = "connect"

// DefaultStateTTL bounds how long an authorization may take.
const DefaultStateTTL = 10 * time.Minute

// ErrStateNotFound: no such state (never issued, expired or already taken).
var ErrStateNotFound = errors.New("oauth: state not found")

// State is the server-side record of one authorization in progress. It is
// stored under the hash of the state token; the token itself, the browser
// binding and the code are never stored. CodeVerifier (PKCE) lives here for
// at most the state's lifetime and is useless without the authorization code.
type State struct {
	Provider    string    `json:"provider"`
	WorkspaceID uuid.UUID `json:"workspace_id"`
	UserID      uuid.UUID `json:"user_id"`
	Flow        string    `json:"flow"`
	// BindingHash is the SHA-256 of the browser binding (an HttpOnly cookie
	// set when the flow began): the callback must come from that browser.
	BindingHash  string    `json:"binding_hash"`
	CodeVerifier string    `json:"code_verifier"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// StateStore keeps states until they are taken or expire. Take is atomic:
// a state can be taken once, so it is single-use even under concurrent
// callbacks. Keys are hashes of state tokens.
type StateStore interface {
	Save(ctx context.Context, key string, s State, ttl time.Duration) error
	// Take returns and removes the state (ErrStateNotFound if absent).
	Take(ctx context.Context, key string) (State, error)
}

// randomToken returns n random bytes, base64url-encoded (unpredictable).
func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// StateKey is the store key of a state token.
func StateKey(stateToken string) string {
	sum := sha256.Sum256([]byte(stateToken))
	return hex.EncodeToString(sum[:])
}

func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// CodeChallenge is the PKCE S256 challenge of a verifier (RFC 7636).
func CodeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Flow runs the authorization-code flow for the registered providers.
type Flow struct {
	providers *Registry
	states    StateStore
	ttl       time.Duration
	now       func() time.Time
}

// NewFlow returns a flow; ttl <= 0 means DefaultStateTTL.
func NewFlow(providers *Registry, states StateStore, ttl time.Duration) *Flow {
	if ttl <= 0 {
		ttl = DefaultStateTTL
	}
	return &Flow{providers: providers, states: states, ttl: ttl, now: time.Now}
}

// WithClock replaces the clock (tests).
func (f *Flow) WithClock(now func() time.Time) *Flow {
	f.now = now
	return f
}

// Started is the result of Begin. StateToken goes into the provider URL;
// BindingToken goes to the initiating browser only, as an HttpOnly cookie.
type Started struct {
	AuthorizationURL string
	StateToken       string
	BindingToken     string
	ExpiresAt        time.Time
}

// Begin starts an authorization for user in workspace (the caller has
// authorized them). It stores the state and returns the provider URL.
func (f *Flow) Begin(ctx context.Context, providerID string, workspaceID, userID uuid.UUID) (Started, error) {
	p, ok := f.providers.Get(providerID)
	if !ok {
		return Started{}, newError(KindInvalidConfiguration, "unknown provider")
	}
	if workspaceID == uuid.Nil || userID == uuid.Nil {
		return Started{}, newError(KindInvalidState, "an authorization needs a user and a workspace")
	}
	stateToken, err := randomToken(32)
	if err != nil {
		return Started{}, err
	}
	binding, err := randomToken(32)
	if err != nil {
		return Started{}, err
	}
	verifier, err := randomToken(48)
	if err != nil {
		return Started{}, err
	}
	authURL, err := p.AuthorizationURL(AuthorizationRequest{State: stateToken, CodeChallenge: CodeChallenge(verifier)})
	if err != nil {
		return Started{}, err
	}
	expires := f.now().Add(f.ttl).UTC()
	s := State{Provider: providerID, WorkspaceID: workspaceID, UserID: userID, Flow: FlowConnect,
		BindingHash: hashString(binding), CodeVerifier: verifier, ExpiresAt: expires}
	if err := f.states.Save(ctx, StateKey(stateToken), s, f.ttl); err != nil {
		return Started{}, err
	}
	return Started{AuthorizationURL: authURL, StateToken: stateToken, BindingToken: binding, ExpiresAt: expires}, nil
}

// Callback is what the provider sent to the callback endpoint.
type Callback struct {
	State string
	Code  string
	// Error is the provider's error parameter (e.g. access_denied); its
	// description is untrusted and not used.
	Error string
	// Binding is the browser binding cookie (empty when absent).
	Binding string
}

// Completed is a successful callback: whom the flow belongs to, and the
// account it authorized. The token is runtime memory only.
type Completed struct {
	State    State
	Token    credential.OAuthToken
	Identity Identity
}

// Complete validates a callback and exchanges its code. The state is taken
// first, so it is consumed even when validation fails afterwards: a state is
// never usable twice. It checks, in order: the state exists (not expired,
// not used), belongs to this provider and to this browser, the provider did
// not report an error, and the code exchange yields a usable token and
// identity.
func (f *Flow) Complete(ctx context.Context, providerID string, cb Callback) (Completed, error) {
	p, ok := f.providers.Get(providerID)
	if !ok {
		return Completed{}, newError(KindInvalidConfiguration, "unknown provider")
	}
	if strings.TrimSpace(cb.State) == "" {
		return Completed{}, newError(KindInvalidState, "the callback has no state")
	}
	s, err := f.states.Take(ctx, StateKey(cb.State))
	if errors.Is(err, ErrStateNotFound) {
		return Completed{}, newError(KindInvalidState, "the authorization is unknown, expired or already completed")
	}
	if err != nil {
		return Completed{}, &Error{Kind: KindProviderUnavailable, Message: "the authorization state could not be read", Err: err}
	}
	switch {
	case !f.now().Before(s.ExpiresAt):
		return Completed{}, newError(KindInvalidState, "the authorization expired")
	case s.Provider != providerID:
		return Completed{}, newError(KindInvalidState, "the authorization was started for another provider")
	case s.Flow != FlowConnect:
		return Completed{}, newError(KindInvalidState, "unknown authorization flow")
	case cb.Binding == "" || subtle.ConstantTimeCompare([]byte(hashString(cb.Binding)), []byte(s.BindingHash)) != 1:
		return Completed{}, newError(KindInvalidState, "the authorization was started in another browser")
	}
	if cb.Error != "" {
		if cb.Error == "access_denied" {
			return Completed{State: s}, newError(KindAuthorizationDenied, "access was not granted")
		}
		return Completed{State: s}, newError(KindAuthorizationDenied, "the provider reported an authorization error")
	}
	if strings.TrimSpace(cb.Code) == "" {
		return Completed{State: s}, newError(KindInvalidCode, "the callback has no authorization code")
	}
	tok, id, err := p.Exchange(ctx, ExchangeRequest{Code: cb.Code, CodeVerifier: s.CodeVerifier})
	if err != nil {
		if KindOf(err) == "" {
			err = &Error{Kind: KindTokenExchangeFailed, Message: "the code exchange failed", Err: err}
		}
		return Completed{State: s}, err
	}
	if tok.AccessToken.Empty() {
		return Completed{State: s}, newError(KindTokenExchangeFailed, "the provider returned no access token")
	}
	if strings.TrimSpace(id.ProviderAccountID) == "" {
		return Completed{State: s}, newError(KindTokenExchangeFailed, "the provider did not identify the account")
	}
	if tok.ProviderAccountID == "" {
		tok.ProviderAccountID = id.ProviderAccountID
	}
	return Completed{State: s, Token: tok, Identity: id}, nil
}

// MemoryStateStore is an in-process StateStore (tests and single-process
// development). Production uses the Redis store.
type MemoryStateStore struct {
	mu     sync.Mutex
	states map[string]State
}

// NewMemoryStateStore returns an empty store.
func NewMemoryStateStore() *MemoryStateStore {
	return &MemoryStateStore{states: map[string]State{}}
}

func (m *MemoryStateStore) Save(_ context.Context, key string, s State, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, dup := m.states[key]; dup {
		return errors.New("oauth: duplicate state")
	}
	m.states[key] = s
	return nil
}

func (m *MemoryStateStore) Take(_ context.Context, key string) (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.states[key]
	if !ok {
		return State{}, ErrStateNotFound
	}
	delete(m.states, key)
	return s, nil
}

// Len is the number of stored states (tests).
func (m *MemoryStateStore) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.states)
}
