package oauth_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/oauth"
	"workflow-optimizer/internal/oauth/fake"
)

type flowEnv struct {
	clock    *clock
	provider *fake.Provider
	states   *oauth.MemoryStateStore
	flow     *oauth.Flow
	ws, user uuid.UUID
}

func newFlowEnv(t *testing.T) *flowEnv {
	c := newClock()
	p := fakeProvider(c)
	other := fake.Config(callbackURL)
	other.ID, other.Name = "other_oauth", "Other"
	states := oauth.NewMemoryStateStore()
	return &flowEnv{clock: c, provider: p, states: states,
		flow: oauth.NewFlow(registry(t, p, fake.New(other)), states, 10*time.Minute).WithClock(c.Now),
		ws:   uuid.New(), user: uuid.New()}
}

func (e *flowEnv) begin(t *testing.T) oauth.Started {
	t.Helper()
	s, err := e.flow.Begin(context.Background(), fake.ID, e.ws, e.user)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (e *flowEnv) approve(t *testing.T, s oauth.Started) oauth.Callback {
	t.Helper()
	code, state, err := e.provider.Approve(s.AuthorizationURL, ada)
	if err != nil {
		t.Fatal(err)
	}
	return oauth.Callback{State: state, Code: code, Binding: s.BindingToken}
}

func kindOf(t *testing.T, err error, want oauth.Kind) {
	t.Helper()
	if got := oauth.KindOf(err); got != want {
		t.Fatalf("got %v (kind %q), want kind %q", err, got, want)
	}
}

func TestProviderConfigAndRegistry(t *testing.T) {
	cfg := fake.Config(callbackURL)
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*oauth.ProviderConfig){
		"bad id":           func(c *oauth.ProviderConfig) { c.ID = "Fake-OAuth" },
		"no name":          func(c *oauth.ProviderConfig) { c.Name = "" },
		"no client id":     func(c *oauth.ProviderConfig) { c.ClientID = "" },
		"relative auth":    func(c *oauth.ProviderConfig) { c.AuthURL = "/authorize" },
		"no token url":     func(c *oauth.ProviderConfig) { c.TokenURL = "" },
		"no redirect":      func(c *oauth.ProviderConfig) { c.RedirectURL = "" },
		"bad revoke url":   func(c *oauth.ProviderConfig) { c.RevokeURL = "ftp://x" },
		"javascript redir": func(c *oauth.ProviderConfig) { c.RedirectURL = "javascript:alert(1)" },
	} {
		c := fake.Config(callbackURL)
		mutate(&c)
		if oauth.KindOf(c.Validate()) != oauth.KindInvalidConfiguration {
			t.Errorf("%s accepted", name)
		}
	}
	// The client secret never serializes.
	b, _ := json.Marshal(cfg)
	if strings.Contains(string(b), "DO-NOT-LEAK") {
		t.Fatalf("client secret marshalled: %s", b)
	}
	if _, err := oauth.NewRegistry(fake.New(cfg), fake.New(cfg)); oauth.KindOf(err) != oauth.KindInvalidConfiguration {
		t.Fatalf("duplicate provider: %v", err)
	}
	other := fake.Config(callbackURL)
	other.ID = "another"
	r := registry(t, fake.New(other), fake.New(cfg))
	if l := r.List(); len(l) != 2 || l[0].ID != "another" || l[1].ID != fake.ID {
		t.Fatalf("List: %+v", l)
	}
	if _, ok := r.Get("google"); ok {
		t.Fatal("no real provider is configured")
	}
}

func TestBeginIssuesUnpredictableBoundState(t *testing.T) {
	e := newFlowEnv(t)
	a, b := e.begin(t), e.begin(t)
	if a.StateToken == b.StateToken || a.BindingToken == b.BindingToken || len(a.StateToken) < 43 || len(a.BindingToken) < 43 {
		t.Fatalf("states must be unique and carry 256 bits: %q %q", a.StateToken, b.StateToken)
	}
	u, _ := url.Parse(a.AuthorizationURL)
	q := u.Query()
	if q.Get("state") != a.StateToken || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" ||
		q.Get("client_id") != "fake-client-id" || q.Get("redirect_uri") != callbackURL {
		t.Fatalf("authorization URL: %s", a.AuthorizationURL)
	}
	if strings.Contains(a.AuthorizationURL, "DO-NOT-LEAK") || strings.Contains(a.AuthorizationURL, a.BindingToken) {
		t.Fatal("the URL carries no secret and no binding")
	}
	if !a.ExpiresAt.Equal(e.clock.Now().Add(10 * time.Minute)) {
		t.Fatalf("expires %v", a.ExpiresAt)
	}
	if e.states.Len() != 2 {
		t.Fatalf("states stored: %d", e.states.Len())
	}
	// The stored state holds no state token, binding or code.
	st, err := e.states.Take(context.Background(), oauth.StateKey(a.StateToken))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(st)
	if strings.Contains(string(raw), a.StateToken) || strings.Contains(string(raw), a.BindingToken) {
		t.Fatalf("stored state leaks tokens: %s", raw)
	}
	if st.WorkspaceID != e.ws || st.UserID != e.user || st.Provider != fake.ID || st.Flow != oauth.FlowConnect {
		t.Fatalf("binding: %+v", st)
	}
	if _, err := e.flow.Begin(context.Background(), "google", e.ws, e.user); oauth.KindOf(err) != oauth.KindInvalidConfiguration {
		t.Fatalf("unknown provider: %v", err)
	}
	if _, err := e.flow.Begin(context.Background(), fake.ID, uuid.Nil, e.user); err == nil {
		t.Fatal("a flow needs a workspace")
	}
}

func TestCompleteSuccessThenStateIsSpent(t *testing.T) {
	e := newFlowEnv(t)
	cb := e.approve(t, e.begin(t))
	done, err := e.flow.Complete(context.Background(), fake.ID, cb)
	if err != nil {
		t.Fatal(err)
	}
	if done.Identity != ada || done.State.WorkspaceID != e.ws || done.State.UserID != e.user {
		t.Fatalf("completed: %+v", done)
	}
	if !e.provider.ValidAccessToken(done.Token.AccessToken.Reveal()) || done.Token.RefreshToken.Empty() ||
		done.Token.ProviderAccountID != "acct-1001" || !done.Token.Expiry.Equal(e.clock.Now().Add(time.Hour)) {
		t.Fatalf("token: %+v", done.Token)
	}
	if e.states.Len() != 0 {
		t.Fatal("the state is consumed")
	}
	// Replaying the same callback fails: single use.
	_, err = e.flow.Complete(context.Background(), fake.ID, cb)
	kindOf(t, err, oauth.KindInvalidState)
}

func TestCompleteRejections(t *testing.T) {
	ctx := context.Background()
	cases := map[string]struct {
		run  func(e *flowEnv, t *testing.T) error
		kind oauth.Kind
	}{
		"unknown state": {func(e *flowEnv, t *testing.T) error {
			_, err := e.flow.Complete(ctx, fake.ID, oauth.Callback{State: "forged", Code: "x", Binding: "y"})
			return err
		}, oauth.KindInvalidState},
		"no state": {func(e *flowEnv, t *testing.T) error {
			_, err := e.flow.Complete(ctx, fake.ID, oauth.Callback{Code: "x"})
			return err
		}, oauth.KindInvalidState},
		"expired state": {func(e *flowEnv, t *testing.T) error {
			cb := e.approve(t, e.begin(t))
			e.clock.Advance(10*time.Minute + time.Second)
			_, err := e.flow.Complete(ctx, fake.ID, cb)
			return err
		}, oauth.KindInvalidState},
		"wrong provider": {func(e *flowEnv, t *testing.T) error {
			cb := e.approve(t, e.begin(t))
			_, err := e.flow.Complete(ctx, "other_oauth", cb)
			return err
		}, oauth.KindInvalidState},
		"unknown provider": {func(e *flowEnv, t *testing.T) error {
			cb := e.approve(t, e.begin(t))
			_, err := e.flow.Complete(ctx, "google", cb)
			return err
		}, oauth.KindInvalidConfiguration},
		"another browser": {func(e *flowEnv, t *testing.T) error {
			cb := e.approve(t, e.begin(t))
			cb.Binding = e.begin(t).BindingToken // the attacker's own binding
			_, err := e.flow.Complete(ctx, fake.ID, cb)
			return err
		}, oauth.KindInvalidState},
		"no binding cookie": {func(e *flowEnv, t *testing.T) error {
			cb := e.approve(t, e.begin(t))
			cb.Binding = ""
			_, err := e.flow.Complete(ctx, fake.ID, cb)
			return err
		}, oauth.KindInvalidState},
		"access denied": {func(e *flowEnv, t *testing.T) error {
			s := e.begin(t)
			_, err := e.flow.Complete(ctx, fake.ID, oauth.Callback{State: s.StateToken, Error: "access_denied", Binding: s.BindingToken})
			return err
		}, oauth.KindAuthorizationDenied},
		"provider error": {func(e *flowEnv, t *testing.T) error {
			s := e.begin(t)
			_, err := e.flow.Complete(ctx, fake.ID, oauth.Callback{State: s.StateToken, Error: "server_error", Binding: s.BindingToken})
			return err
		}, oauth.KindAuthorizationDenied},
		"no code": {func(e *flowEnv, t *testing.T) error {
			s := e.begin(t)
			_, err := e.flow.Complete(ctx, fake.ID, oauth.Callback{State: s.StateToken, Binding: s.BindingToken})
			return err
		}, oauth.KindInvalidCode},
		"invalid code": {func(e *flowEnv, t *testing.T) error {
			cb := e.approve(t, e.begin(t))
			cb.Code = "code-forged"
			_, err := e.flow.Complete(ctx, fake.ID, cb)
			return err
		}, oauth.KindInvalidCode},
		"code of another flow (PKCE)": {func(e *flowEnv, t *testing.T) error {
			stolen := e.approve(t, e.begin(t)) // a code issued for someone else's challenge
			s := e.begin(t)
			_, err := e.flow.Complete(ctx, fake.ID, oauth.Callback{State: s.StateToken, Code: stolen.Code, Binding: s.BindingToken})
			return err
		}, oauth.KindInvalidCode},
		"provider unavailable": {func(e *flowEnv, t *testing.T) error {
			cb := e.approve(t, e.begin(t))
			e.provider.Fail = func(op string) error { return oauth.NewError(oauth.KindProviderUnavailable, "503") }
			_, err := e.flow.Complete(ctx, fake.ID, cb)
			return err
		}, oauth.KindProviderUnavailable},
		"unclassified exchange failure": {func(e *flowEnv, t *testing.T) error {
			cb := e.approve(t, e.begin(t))
			e.provider.Fail = func(op string) error { return errors.New("connection reset") }
			_, err := e.flow.Complete(ctx, fake.ID, cb)
			return err
		}, oauth.KindTokenExchangeFailed},
		"malformed token response": {func(e *flowEnv, t *testing.T) error {
			cb := e.approve(t, e.begin(t))
			e.provider.Malformed = true
			_, err := e.flow.Complete(ctx, fake.ID, cb)
			return err
		}, oauth.KindTokenExchangeFailed},
		"no account identity": {func(e *flowEnv, t *testing.T) error {
			s := e.begin(t)
			code, state, _ := e.provider.Approve(s.AuthorizationURL, oauth.Identity{Email: "no-id@example.test"})
			_, err := e.flow.Complete(ctx, fake.ID, oauth.Callback{State: state, Code: code, Binding: s.BindingToken})
			return err
		}, oauth.KindTokenExchangeFailed},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e := newFlowEnv(t)
			err := c.run(e, t)
			kindOf(t, err, c.kind)
			if c.kind.Retryable() != (c.kind == oauth.KindProviderUnavailable) {
				t.Fatal("only provider outages are transient")
			}
			if strings.Contains(err.Error(), "code-") || strings.Contains(err.Error(), "fake-access-") || strings.Contains(err.Error(), "connection reset") {
				t.Fatalf("error text leaks: %v", err)
			}
		})
	}
}

func TestConcurrentCallbacksConsumeTheStateOnce(t *testing.T) {
	e := newFlowEnv(t)
	cb := e.approve(t, e.begin(t))
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.flow.Complete(context.Background(), fake.ID, cb); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != 1 {
		t.Fatalf("%d callbacks succeeded, want exactly 1", ok)
	}
	if ex, _, _ := e.provider.Counts(); ex != 1 {
		t.Fatalf("%d code exchanges, want 1", ex)
	}
}

func TestTokenExpiryModel(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tok := credential.OAuthToken{AccessToken: credential.NewSecret("a"), Expiry: now.Add(time.Minute)}
	if tok.Expired(now) || !tok.Valid(now) || !tok.ExpiresWithin(now, 2*time.Minute) || tok.ExpiresWithin(now, 30*time.Second) {
		t.Fatal("valid, expiring soon")
	}
	if !tok.Expired(now.Add(time.Minute)) || tok.Valid(now.Add(time.Minute)) {
		t.Fatal("expired at expiry")
	}
	noExpiry := credential.OAuthToken{AccessToken: credential.NewSecret("a")}
	if noExpiry.Expired(now.Add(1000*time.Hour)) || noExpiry.ExpiresWithin(now, time.Hour) {
		t.Fatal("a token without expiry is not assumed to expire")
	}
	merged := credential.OAuthToken{RefreshToken: credential.NewSecret("r1"), Scopes: []string{"s"}, ProviderAccountID: "p", TokenType: "Bearer"}.
		Merge(credential.OAuthToken{AccessToken: credential.NewSecret("a2"), Expiry: now})
	if merged.RefreshToken.Reveal() != "r1" || merged.AccessToken.Reveal() != "a2" || merged.ProviderAccountID != "p" || len(merged.Scopes) != 1 || merged.TokenType != "Bearer" {
		t.Fatalf("merge must keep the refresh token when none is returned: %+v", merged)
	}
	rotated := credential.OAuthToken{RefreshToken: credential.NewSecret("r1")}.Merge(credential.OAuthToken{AccessToken: credential.NewSecret("a2"), RefreshToken: credential.NewSecret("r2")})
	if rotated.RefreshToken.Reveal() != "r2" {
		t.Fatal("a rotated refresh token replaces the old one")
	}
	b, _ := json.Marshal(merged)
	if strings.Contains(string(b), "r1") || strings.Contains(string(b), "a2") {
		t.Fatalf("tokens marshal: %s", b)
	}
}
