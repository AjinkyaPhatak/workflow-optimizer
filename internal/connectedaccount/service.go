package connectedaccount

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/oauth"
)

// Service manages connected accounts and their OAuth credentials:
//
//	connect:    token -> credential.Service (encrypt) -> credential row -> account row
//	reconnect:  same external account -> same account and credential, new tokens, ACTIVE
//	disconnect: Provider.Revoke (best effort) -> tokens removed from the credential -> DISCONNECTED
//
// It does not authorize users: the application layer does, before calling
// it. It never returns tokens. It implements oauth.AccountStatus, so the
// token manager can report refreshes and lost authorizations.
type Service struct {
	repo      Repository
	creds     *credential.Service
	providers *oauth.Registry
	logger    *slog.Logger
	now       func() time.Time
}

var _ oauth.AccountStatus = (*Service)(nil)

// NewService wires the service. logger may be nil.
func NewService(repo Repository, creds *credential.Service, providers *oauth.Registry, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{repo: repo, creds: creds, providers: providers, logger: logger, now: time.Now}
}

// ConnectInput is a completed authorization.
type ConnectInput struct {
	WorkspaceID uuid.UUID
	UserID      uuid.UUID
	Provider    string
	Identity    oauth.Identity
	Token       credential.OAuthToken
}

func (s *Service) providerName(id string) string {
	if p, ok := s.providers.Get(id); ok {
		return p.Config().Name
	}
	return id
}

// Connect stores a completed authorization. When the external account is
// already connected to the workspace (same provider_account_id), its
// credential gets the new tokens and the account becomes ACTIVE again
// (created is false); otherwise a credential and an account are created.
func (s *Service) Connect(ctx context.Context, in ConnectInput) (acct Account, created bool, err error) {
	in.Identity.ProviderAccountID = strings.TrimSpace(in.Identity.ProviderAccountID)
	draft := Account{WorkspaceID: in.WorkspaceID, UserID: in.UserID, Provider: in.Provider,
		ProviderAccountID: in.Identity.ProviderAccountID, DisplayName: strings.TrimSpace(in.Identity.DisplayName),
		Email: strings.TrimSpace(in.Identity.Email), Status: StatusActive, Scopes: in.Token.Scopes,
		CredentialID: uuid.New()} // placeholder for validation
	if err := draft.Validate(); err != nil {
		return Account{}, false, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		existing, err := s.repo.FindByIdentity(ctx, in.WorkspaceID, in.Provider, draft.ProviderAccountID)
		if err == nil {
			return s.reconnect(ctx, existing, draft, in.Token)
		}
		if !errors.Is(err, ErrNotFound) {
			return Account{}, false, err
		}
		c, err := s.creds.CreateOAuth(ctx, credential.CreateOAuthInput{WorkspaceID: in.WorkspaceID,
			Name: draft.Label(s.providerName(in.Provider)), Provider: in.Provider, Token: in.Token})
		if err != nil {
			return Account{}, false, err
		}
		draft.CredentialID = c.ID
		acct, err := s.repo.Create(ctx, draft)
		if err == nil {
			return acct, true, nil
		}
		// Not stored: the credential must not stay behind with the tokens.
		if derr := s.creds.Delete(ctx, in.WorkspaceID, c.ID); derr != nil {
			s.logger.ErrorContext(ctx, "orphaned OAuth credential could not be removed", "credential_id", c.ID.String(), "error", derr)
		}
		if !errors.Is(err, ErrDuplicate) {
			return Account{}, false, err
		}
		// Connected concurrently: reconnect the account that won.
	}
	return Account{}, false, fmt.Errorf("%w: concurrent connections of the same account", ErrDuplicate)
}

func (s *Service) reconnect(ctx context.Context, existing, draft Account, tok credential.OAuthToken) (Account, bool, error) {
	if err := s.creds.ReplaceOAuth(ctx, existing.WorkspaceID, existing.CredentialID, &tok); err != nil {
		return Account{}, false, err
	}
	existing.UserID = draft.UserID
	existing.DisplayName, existing.Email, existing.Scopes = draft.DisplayName, draft.Email, draft.Scopes
	existing.Status = StatusActive
	acct, err := s.repo.Update(ctx, existing)
	return acct, false, err
}

// Get returns an account (authorization is the caller's).
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Account, error) { return s.repo.Get(ctx, id) }

// List returns the workspace's accounts.
func (s *Service) List(ctx context.Context, workspaceID uuid.UUID) ([]Account, error) {
	return s.repo.ListByWorkspace(ctx, workspaceID)
}

// Disconnect revokes the authorization at the provider when it can (a
// failure there does not stop the disconnect), removes the tokens from the
// credential, and marks the account DISCONNECTED. The credential row stays,
// so workflows that reference it fail with "revoked" until the account is
// connected again, and execution history is untouched. revoked reports
// whether the provider confirmed the revocation.
func (s *Service) Disconnect(ctx context.Context, a Account) (out Account, revoked bool, err error) {
	if p, ok := s.providers.Get(a.Provider); ok {
		_, tok, err := s.creds.ResolveOAuth(ctx, a.WorkspaceID, a.CredentialID, a.Provider)
		switch {
		case err == nil:
			if rerr := p.Revoke(ctx, tok); rerr == nil {
				revoked = true
			} else {
				s.logger.WarnContext(ctx, "provider revocation failed; disconnecting locally", "event", "oauth_revocation_failed",
					"account_id", a.ID.String(), "provider", a.Provider, "kind", string(oauth.KindOf(rerr)))
			}
		case !errors.Is(err, credential.ErrRevoked):
			s.logger.WarnContext(ctx, "tokens could not be read for revocation", "account_id", a.ID.String(), "error", err)
		}
	}
	if err := s.creds.ReplaceOAuth(ctx, a.WorkspaceID, a.CredentialID, nil); err != nil && !errors.Is(err, credential.ErrNotFound) {
		return Account{}, revoked, err
	}
	a.Status = StatusDisconnected
	out, err = s.repo.Update(ctx, a)
	if err != nil {
		return Account{}, revoked, err
	}
	s.logger.InfoContext(ctx, "connected account disconnected", "event", "connected_account_disconnected",
		"account_id", a.ID.String(), "provider", a.Provider, "revoked_at_provider", revoked)
	return out, revoked, nil
}

// TokenRefreshed implements oauth.AccountStatus.
func (s *Service) TokenRefreshed(ctx context.Context, credentialID uuid.UUID) {
	if err := s.repo.SetStatusByCredential(ctx, credentialID, StatusActive); err != nil {
		s.logger.ErrorContext(ctx, "connected account status could not be updated", "credential_id", credentialID.String(), "error", err)
	}
}

// AuthorizationLost implements oauth.AccountStatus.
func (s *Service) AuthorizationLost(ctx context.Context, credentialID uuid.UUID, kind oauth.Kind) {
	status := StatusError
	switch kind {
	case oauth.KindRevoked:
		status = StatusRevoked
	case oauth.KindTokenExpired:
		status = StatusExpired
	}
	if err := s.repo.SetStatusByCredential(ctx, credentialID, status); err != nil {
		s.logger.ErrorContext(ctx, "connected account status could not be updated", "credential_id", credentialID.String(), "error", err)
	}
}

// CredentialUsed implements oauth.AccountStatus.
func (s *Service) CredentialUsed(ctx context.Context, credentialID uuid.UUID) {
	if err := s.repo.TouchByCredential(ctx, credentialID, s.now()); err != nil {
		s.logger.WarnContext(ctx, "connected account use could not be recorded", "credential_id", credentialID.String(), "error", err)
	}
}
