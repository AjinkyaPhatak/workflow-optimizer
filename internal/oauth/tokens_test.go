package oauth_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/integration"
	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/oauth"
	"workflow-optimizer/internal/oauth/fake"
)

// statusLog records what the token manager reports about accounts.
type statusLog struct {
	mu        sync.Mutex
	refreshed int
	lost      []oauth.Kind
	used      int
}

func (s *statusLog) TokenRefreshed(context.Context, uuid.UUID) {
	s.mu.Lock()
	s.refreshed++
	s.mu.Unlock()
}

func (s *statusLog) AuthorizationLost(_ context.Context, _ uuid.UUID, k oauth.Kind) {
	s.mu.Lock()
	s.lost = append(s.lost, k)
	s.mu.Unlock()
}

func (s *statusLog) CredentialUsed(context.Context, uuid.UUID) {
	s.mu.Lock()
	s.used++
	s.mu.Unlock()
}

type tokenEnv struct {
	clock    *clock
	provider *fake.Provider
	creds    *credential.Service
	repo     *memRepo
	status   *statusLog
	logs     *bytes.Buffer
	manager  *oauth.TokenManager
	ws       uuid.UUID
	credID   uuid.UUID
	initial  credential.OAuthToken
}

// newTokenEnv connects ada's fake account and stores its tokens in an
// OAUTH2 credential, as the callback would.
func newTokenEnv(t *testing.T) *tokenEnv {
	t.Helper()
	c := newClock()
	p := fakeProvider(c)
	creds, repo := newCredentialService(t)
	e := &tokenEnv{clock: c, provider: p, creds: creds, repo: repo, status: &statusLog{}, logs: &bytes.Buffer{}, ws: uuid.New()}
	flow := oauth.NewFlow(registry(t, p), oauth.NewMemoryStateStore(), 0).WithClock(c.Now)
	s, err := flow.Begin(context.Background(), fake.ID, e.ws, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	code, state, _ := p.Approve(s.AuthorizationURL, ada)
	done, err := flow.Complete(context.Background(), fake.ID, oauth.Callback{State: state, Code: code, Binding: s.BindingToken})
	if err != nil {
		t.Fatal(err)
	}
	cred, err := creds.CreateOAuth(context.Background(), credential.CreateOAuthInput{WorkspaceID: e.ws, Name: "Fake — ada", Provider: fake.ID, Token: done.Token})
	if err != nil {
		t.Fatal(err)
	}
	e.credID, e.initial = cred.ID, done.Token
	e.manager = oauth.NewTokenManager(creds, registry(t, p), e.status, slog.New(slog.NewJSONHandler(e.logs, nil))).WithClock(c.Now)
	return e
}

func (e *tokenEnv) resolve(ctx context.Context) (credential.ResolvedCredential, error) {
	return e.manager.Resolve(ctx, e.ws, e.credID, fake.ID)
}

func (e *tokenEnv) stored(t *testing.T) credential.OAuthToken {
	t.Helper()
	_, tok, err := e.creds.ResolveOAuth(context.Background(), e.ws, e.credID, fake.ID)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// noTokens asserts that no token ever issued appears in s.
func (e *tokenEnv) noTokens(t *testing.T, where, s string) {
	t.Helper()
	if strings.Contains(s, "fake-access-") || strings.Contains(s, "fake-refresh-") || strings.Contains(s, "code-") {
		t.Fatalf("a token leaked into %s: %s", where, s)
	}
}

func TestValidTokenIsUsedWithoutRefresh(t *testing.T) {
	e := newTokenEnv(t)
	r, err := e.resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Secret.Reveal() != e.initial.AccessToken.Reveal() || r.Type != credential.TypeOAuth2 || r.Provider != fake.ID {
		t.Fatalf("resolved: %+v", r)
	}
	if _, refreshes, _ := e.provider.Counts(); refreshes != 0 {
		t.Fatal("no refresh for a valid token")
	}
	if e.status.used != 1 {
		t.Fatal("use recorded")
	}
}

func TestExpiringTokenIsRefreshedAndStored(t *testing.T) {
	e := newTokenEnv(t)
	e.clock.Advance(time.Hour - time.Minute) // inside the 2-minute refresh window
	r, err := e.resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Secret.Reveal() == e.initial.AccessToken.Reveal() || !e.provider.ValidAccessToken(r.Secret.Reveal()) {
		t.Fatal("a new, valid access token is returned")
	}
	st := e.stored(t)
	if st.AccessToken.Reveal() != r.Secret.Reveal() || !st.Expiry.Equal(e.clock.Now().Add(time.Hour)) {
		t.Fatalf("the refreshed token is stored: %+v", st)
	}
	// The provider omitted the refresh token: the existing one is kept.
	if st.RefreshToken.Reveal() != e.initial.RefreshToken.Reveal() || st.ProviderAccountID != "acct-1001" || len(st.Scopes) != 2 {
		t.Fatalf("refresh token, account and scopes must be preserved: %+v", st)
	}
	if e.status.refreshed != 1 {
		t.Fatal("refresh reported")
	}
	// Encrypted at rest: no token in the stored record.
	e.noTokens(t, "stored credential", e.repo.raw(e.credID))
	e.noTokens(t, "logs", e.logs.String())
	if !strings.Contains(e.logs.String(), "connected_account_refreshed") {
		t.Fatal("refresh is observable")
	}
}

func TestRefreshTokenRotation(t *testing.T) {
	e := newTokenEnv(t)
	e.provider.RotateRefreshTokens = true
	e.clock.Advance(2 * time.Hour)
	if _, err := e.resolve(context.Background()); err != nil {
		t.Fatal(err)
	}
	rotated := e.stored(t).RefreshToken.Reveal()
	if rotated == e.initial.RefreshToken.Reveal() || rotated == "" {
		t.Fatal("the rotated refresh token is stored")
	}
	// The old refresh token is dead at the provider; the stored one works.
	e.clock.Advance(2 * time.Hour)
	if _, err := e.resolve(context.Background()); err != nil {
		t.Fatalf("second refresh with the rotated token: %v", err)
	}
	if _, refreshes, _ := e.provider.Counts(); refreshes != 2 {
		t.Fatalf("refreshes: %d", refreshes)
	}
}

func TestConcurrentResolutionsRefreshOnce(t *testing.T) {
	e := newTokenEnv(t)
	e.provider.RotateRefreshTokens = true
	e.provider.RefreshDelay = 50 * time.Millisecond
	e.clock.Advance(2 * time.Hour)
	var wg sync.WaitGroup
	results := make([]string, 20)
	errs := make([]error, 20)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := e.resolve(context.Background())
			results[i], errs[i] = r.Secret.Reveal(), err
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("resolution %d: %v", i, err)
		}
		if results[i] != results[0] || !e.provider.ValidAccessToken(results[i]) {
			t.Fatal("every resolution gets the one refreshed token")
		}
	}
	if _, refreshes, _ := e.provider.Counts(); refreshes != 1 {
		t.Fatalf("%d refresh requests, want 1", refreshes)
	}
}

func TestWaitingForARefreshHonoursCancellation(t *testing.T) {
	e := newTokenEnv(t)
	e.provider.RefreshDelay = 500 * time.Millisecond
	e.clock.Advance(2 * time.Hour)
	go func() { _, _ = e.resolve(context.Background()) }()
	time.Sleep(50 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := e.resolve(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if time.Since(start) > 300*time.Millisecond {
		t.Fatal("the waiter did not stop on its deadline")
	}
}

// classify is what an integration node reports for a resolution error.
func classify(err error) *node.NodeError {
	var ne *node.NodeError
	errors.As(integration.ClassifyCredentialError(err), &ne)
	return ne
}

func TestRefreshFailures(t *testing.T) {
	t.Run("revoked grant", func(t *testing.T) {
		e := newTokenEnv(t)
		e.provider.RevokeAccount("acct-1001")
		e.clock.Advance(2 * time.Hour)
		_, err := e.resolve(context.Background())
		if !errors.Is(err, credential.ErrRevoked) {
			t.Fatalf("got %v", err)
		}
		if ne := classify(err); ne.Code != integration.ErrCodeCredentialRevoked || ne.Retryable {
			t.Fatalf("classified: %+v", ne)
		}
		if len(e.status.lost) != 1 || e.status.lost[0] != oauth.KindRevoked {
			t.Fatalf("status: %+v", e.status.lost)
		}
		// The dead tokens are removed: no further provider calls.
		if _, _, err := e.creds.ResolveOAuth(context.Background(), e.ws, e.credID, fake.ID); !errors.Is(err, credential.ErrRevoked) {
			t.Fatalf("tokens must be removed: %v", err)
		}
		_, before, _ := e.provider.Counts()
		if _, err := e.resolve(context.Background()); !errors.Is(err, credential.ErrRevoked) {
			t.Fatal(err)
		}
		if _, after, _ := e.provider.Counts(); after != before {
			t.Fatal("a revoked credential must not call the provider again")
		}
		e.noTokens(t, "error", err.Error())
	})
	t.Run("outage while the token is still valid", func(t *testing.T) {
		e := newTokenEnv(t)
		e.provider.Fail = func(string) error { return oauth.NewError(oauth.KindProviderUnavailable, "503") }
		e.clock.Advance(time.Hour - time.Minute)
		r, err := e.resolve(context.Background())
		if err != nil || r.Secret.Reveal() != e.initial.AccessToken.Reveal() {
			t.Fatalf("the still-valid token is used: %v", err)
		}
	})
	t.Run("outage after expiry is retryable", func(t *testing.T) {
		e := newTokenEnv(t)
		e.provider.Fail = func(string) error { return oauth.NewError(oauth.KindProviderUnavailable, "503") }
		e.clock.Advance(2 * time.Hour)
		_, err := e.resolve(context.Background())
		if err == nil || credential.IsCredentialError(err) {
			t.Fatalf("got %v", err)
		}
		if ne := classify(err); ne.Code != node.ErrCodeUnavailable || !ne.Retryable {
			t.Fatalf("classified: %+v", ne)
		}
		if len(e.status.lost) != 0 {
			t.Fatal("an outage does not change the account's status")
		}
	})
	t.Run("malformed refresh response after expiry", func(t *testing.T) {
		e := newTokenEnv(t)
		e.provider.Malformed = true
		e.clock.Advance(2 * time.Hour)
		_, err := e.resolve(context.Background())
		if !errors.Is(err, credential.ErrInvalid) || classify(err).Retryable {
			t.Fatalf("got %v", err)
		}
		if len(e.status.lost) != 1 || e.status.lost[0] != oauth.KindTokenRefreshFailed {
			t.Fatalf("status: %+v", e.status.lost)
		}
	})
	t.Run("expired without refresh token", func(t *testing.T) {
		e := newTokenEnv(t)
		tok := e.stored(t)
		tok.RefreshToken = credential.Secret{}
		if err := e.creds.ReplaceOAuth(context.Background(), e.ws, e.credID, &tok); err != nil {
			t.Fatal(err)
		}
		e.clock.Advance(2 * time.Hour)
		_, err := e.resolve(context.Background())
		if !errors.Is(err, credential.ErrRevoked) || len(e.status.lost) != 1 || e.status.lost[0] != oauth.KindTokenExpired {
			t.Fatalf("got %v %+v", err, e.status.lost)
		}
	})
}

func TestResolutionIsolationAndPassThrough(t *testing.T) {
	e := newTokenEnv(t)
	ctx := context.Background()
	if _, err := e.manager.Resolve(ctx, uuid.New(), e.credID, fake.ID); !errors.Is(err, credential.ErrNotFound) {
		t.Fatalf("another workspace: %v", err)
	}
	if _, err := e.manager.Resolve(ctx, e.ws, e.credID, "openai"); !errors.Is(err, credential.ErrProviderMismatch) {
		t.Fatalf("another provider: %v", err)
	}
	// API keys are resolved exactly as before.
	key, err := e.creds.Create(ctx, credential.CreateInput{WorkspaceID: e.ws, Name: "OpenAI", Provider: "openai", Type: credential.TypeAPIKey, Secret: credential.NewSecret("sk-api-key-0001")})
	if err != nil {
		t.Fatal(err)
	}
	r, err := e.manager.Resolve(ctx, e.ws, key.ID, "openai")
	if err != nil || r.Secret.Reveal() != "sk-api-key-0001" {
		t.Fatalf("api key: %v", err)
	}
	// Disconnected: tokens removed, resolution refused.
	if err := e.creds.ReplaceOAuth(ctx, e.ws, e.credID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.resolve(ctx); !errors.Is(err, credential.ErrRevoked) {
		t.Fatalf("disconnected: %v", err)
	}
}

func TestCredentialServiceOAuthMaterial(t *testing.T) {
	creds, repo := newCredentialService(t)
	ctx := context.Background()
	ws := uuid.New()
	tok := credential.OAuthToken{AccessToken: credential.NewSecret("fake-access-PLAINTEXT"), RefreshToken: credential.NewSecret("fake-refresh-PLAINTEXT"),
		TokenType: "Bearer", Expiry: time.Now().Add(time.Hour), Scopes: []string{"a"}, ProviderAccountID: "acct"}
	c, err := creds.CreateOAuth(ctx, credential.CreateOAuthInput{WorkspaceID: ws, Name: "X", Provider: "p", Token: tok})
	if err != nil {
		t.Fatal(err)
	}
	if c.CredentialType != credential.TypeOAuth2 || strings.Contains(repo.raw(c.ID), "PLAINTEXT") {
		t.Fatalf("stored: %s", repo.raw(c.ID))
	}
	r, err := creds.Resolve(ctx, ws, c.ID, "p")
	if err != nil || r.Secret.Reveal() != "fake-access-PLAINTEXT" {
		t.Fatalf("resolve: %v", err)
	}
	expired := tok
	expired.Expiry = time.Now().Add(-time.Minute)
	if err := creds.ReplaceOAuth(ctx, ws, c.ID, &expired); err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Resolve(ctx, ws, c.ID, "p"); !errors.Is(err, credential.ErrInvalid) {
		t.Fatalf("without a token manager an expired token is not returned: %v", err)
	}
	if _, err := creds.Create(ctx, credential.CreateInput{WorkspaceID: ws, Name: "x", Provider: "p", Type: credential.TypeOAuth2, Secret: credential.NewSecret("x")}); !errors.Is(err, credential.ErrInvalid) {
		t.Fatal("OAuth credentials are only created from an authorization")
	}
	if err := creds.ReplaceOAuth(ctx, uuid.New(), c.ID, &tok); !errors.Is(err, credential.ErrNotFound) {
		t.Fatal("another workspace cannot replace the tokens")
	}
	if _, err := creds.CreateOAuth(ctx, credential.CreateOAuthInput{WorkspaceID: ws, Name: "X", Provider: "p"}); !errors.Is(err, credential.ErrInvalid) {
		t.Fatal("an access token is required")
	}
}
