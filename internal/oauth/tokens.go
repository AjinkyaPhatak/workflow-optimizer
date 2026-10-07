package oauth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/credential"
)

// DefaultRefreshBefore is how long before expiry an access token is
// refreshed, so it does not expire in the middle of an action.
const DefaultRefreshBefore = 2 * time.Minute

// AccountStatus reports token lifecycle facts to whoever tracks connected
// accounts (internal/connectedaccount). Implementations must not fail the
// resolution: errors are theirs to log.
type AccountStatus interface {
	// TokenRefreshed: the credential's tokens were refreshed and stored.
	TokenRefreshed(ctx context.Context, credentialID uuid.UUID)
	// AuthorizationLost: the credential can no longer be used without
	// reconnecting (kind says why: revoked, token_refresh_failed...).
	AuthorizationLost(ctx context.Context, credentialID uuid.UUID, kind Kind)
	// CredentialUsed: the credential was resolved for use.
	CredentialUsed(ctx context.Context, credentialID uuid.UUID)
}

// TokenManager is the runtime credential.Resolver (Phase C2). Integration
// nodes keep calling Resolve exactly as in C1; for OAuth credentials of a
// registered provider it returns a valid access token, refreshing it through
// the provider when it expires within RefreshBefore:
//
//	Resolve -> credential.Service.ResolveOAuth (decrypt) -> valid? -> access token
//	                                                     -> no: lock(credential) -> re-read
//	                                                        -> Provider.Refresh -> Merge -> ReplaceOAuth (encrypt)
//
// Everything else (API keys, unknown providers) goes to the credential
// service unchanged.
//
// Refresh coordination is per process: concurrent resolutions of one
// credential wait for a single refresh and then re-read the stored tokens.
// Several processes (API, workers) can still refresh the same credential at
// about the same time; with providers that rotate refresh tokens one of
// them may then fail. Cross-process coordination is deferred.
type TokenManager struct {
	creds         *credential.Service
	providers     *Registry
	status        AccountStatus
	logger        *slog.Logger
	now           func() time.Time
	refreshBefore time.Duration
	locks         keyedLock
}

var _ credential.Resolver = (*TokenManager)(nil)

// NewTokenManager wraps the credential service. status and logger may be nil.
func NewTokenManager(creds *credential.Service, providers *Registry, status AccountStatus, logger *slog.Logger) *TokenManager {
	if logger == nil {
		logger = slog.Default()
	}
	return &TokenManager{creds: creds, providers: providers, status: status, logger: logger, now: time.Now,
		refreshBefore: DefaultRefreshBefore, locks: keyedLock{m: map[uuid.UUID]*lockEntry{}}}
}

// WithClock replaces the clock (tests).
func (m *TokenManager) WithClock(now func() time.Time) *TokenManager {
	m.now = now
	return m
}

// Resolve implements credential.Resolver.
func (m *TokenManager) Resolve(ctx context.Context, workspaceID, id uuid.UUID, provider string) (credential.ResolvedCredential, error) {
	p, isOAuth := m.providers.Get(provider)
	if !isOAuth {
		return m.creds.Resolve(ctx, workspaceID, id, provider)
	}
	c, err := m.creds.Get(ctx, workspaceID, id)
	if err != nil {
		return credential.ResolvedCredential{}, err
	}
	if c.CredentialType != credential.TypeOAuth2 {
		return m.creds.Resolve(ctx, workspaceID, id, provider)
	}
	c, tok, err := m.creds.ResolveOAuth(ctx, workspaceID, id, provider)
	if err != nil {
		return credential.ResolvedCredential{}, err
	}
	if !tok.ExpiresWithin(m.now(), m.refreshBefore) {
		return m.use(ctx, c, tok), nil
	}

	unlock, err := m.locks.lock(ctx, id)
	if err != nil {
		return credential.ResolvedCredential{}, err
	}
	defer unlock()
	// Another resolution may have refreshed while this one waited.
	c, tok, err = m.creds.ResolveOAuth(ctx, workspaceID, id, provider)
	if err != nil {
		return credential.ResolvedCredential{}, err
	}
	if !tok.ExpiresWithin(m.now(), m.refreshBefore) {
		return m.use(ctx, c, tok), nil
	}
	if tok.RefreshToken.Empty() {
		if !tok.Expired(m.now()) {
			return m.use(ctx, c, tok), nil
		}
		m.lost(ctx, c, KindTokenExpired, "the access token expired and there is no refresh token")
		return credential.ResolvedCredential{}, fmt.Errorf("%w: the access token of credential %s expired and cannot be refreshed; reconnect the account", credential.ErrRevoked, id)
	}

	refreshed, err := p.Refresh(ctx, tok)
	if err == nil && refreshed.AccessToken.Empty() {
		err = newError(KindTokenRefreshFailed, "the provider returned no access token")
	}
	if err != nil {
		return m.refreshFailed(ctx, c, tok, err)
	}
	merged := tok.Merge(refreshed)
	if err := m.creds.ReplaceOAuth(ctx, workspaceID, id, &merged); err != nil {
		// The new token works now; without it stored, the next refresh uses
		// the previous refresh token.
		m.logger.ErrorContext(ctx, "refreshed OAuth token could not be stored", "event", "oauth_token_persist_failed",
			"credential_id", id.String(), "provider", provider, "error", err)
	} else {
		m.logger.InfoContext(ctx, "connected account refreshed", "event", "connected_account_refreshed",
			"credential_id", id.String(), "provider", provider)
		if m.status != nil {
			m.status.TokenRefreshed(ctx, id)
		}
	}
	return m.use(ctx, c, merged), nil
}

func (m *TokenManager) use(ctx context.Context, c credential.Credential, tok credential.OAuthToken) credential.ResolvedCredential {
	if m.status != nil {
		m.status.CredentialUsed(ctx, c.ID)
	}
	return credential.ResolvedCredential{ID: c.ID, Provider: c.Provider, Type: c.CredentialType, Secret: tok.AccessToken}
}

func (m *TokenManager) lost(ctx context.Context, c credential.Credential, kind Kind, reason string) {
	m.logger.WarnContext(ctx, "connected account needs reconnecting", "event", "oauth_authorization_lost",
		"credential_id", c.ID.String(), "provider", c.Provider, "kind", string(kind), "reason", reason)
	if m.status != nil {
		m.status.AuthorizationLost(ctx, c.ID, kind)
	}
}

// refreshFailed maps a refresh failure onto the credential error model the
// integration layer classifies:
//
//   - cancellation passes through;
//   - revoked grant: the stored tokens are removed (they can never work
//     again), the account is marked, and credential.ErrRevoked is returned
//     (never retried);
//   - transient provider failure: the current token is used while it is
//     still valid; otherwise a plain (retryable) error is returned;
//   - anything else: the current token is used while valid; otherwise
//     credential.ErrInvalid (never retried).
func (m *TokenManager) refreshFailed(ctx context.Context, c credential.Credential, tok credential.OAuthToken, err error) (credential.ResolvedCredential, error) {
	if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return credential.ResolvedCredential{}, err
	}
	kind := KindOf(err)
	if kind == "" {
		kind = KindProviderUnavailable
	}
	m.logger.WarnContext(ctx, "OAuth token refresh failed", "event", "oauth_token_refresh_failed",
		"credential_id", c.ID.String(), "provider", c.Provider, "kind", string(kind))
	switch {
	case kind == KindRevoked:
		if rerr := m.creds.ReplaceOAuth(ctx, c.WorkspaceID, c.ID, nil); rerr != nil {
			m.logger.ErrorContext(ctx, "revoked OAuth tokens could not be removed", "event", "oauth_token_persist_failed",
				"credential_id", c.ID.String(), "error", rerr)
		}
		m.lost(ctx, c, kind, "the provider revoked the authorization")
		return credential.ResolvedCredential{}, fmt.Errorf("%w: the authorization of credential %s was revoked; reconnect the account", credential.ErrRevoked, c.ID)
	case !tok.Expired(m.now()):
		return m.use(ctx, c, tok), nil
	case kind.Retryable():
		return credential.ResolvedCredential{}, fmt.Errorf("oauth: the access token of credential %s could not be refreshed: %s", c.ID, kind)
	default:
		m.lost(ctx, c, kind, "the token could not be refreshed")
		return credential.ResolvedCredential{}, fmt.Errorf("%w: the access token of credential %s could not be refreshed (%s)", credential.ErrInvalid, c.ID, kind)
	}
}

// keyedLock is a per-credential mutex whose waits honour ctx.
type keyedLock struct {
	mu sync.Mutex
	m  map[uuid.UUID]*lockEntry
}

type lockEntry struct {
	ch   chan struct{}
	refs int
}

func (k *keyedLock) lock(ctx context.Context, id uuid.UUID) (func(), error) {
	k.mu.Lock()
	e, ok := k.m[id]
	if !ok {
		e = &lockEntry{ch: make(chan struct{}, 1)}
		k.m[id] = e
	}
	e.refs++
	k.mu.Unlock()

	release := func() {
		k.mu.Lock()
		e.refs--
		if e.refs == 0 {
			delete(k.m, id)
		}
		k.mu.Unlock()
	}
	select {
	case e.ch <- struct{}{}:
		return func() { <-e.ch; release() }, nil
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	}
}
