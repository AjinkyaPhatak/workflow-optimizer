package application

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"

	"workflow-optimizer/internal/connectedaccount"
	"workflow-optimizer/internal/oauth"
	"workflow-optimizer/internal/workspace"
)

// ConnectedAccountService authorizes connected-account operations (Phase
// C2) and runs the OAuth flow:
//
//	Authorize: membership + manage_credentials -> oauth.Flow.Begin
//	Callback:  oauth.Flow.Complete (state single-use, browser-bound, code exchange)
//	           -> the state's user may still manage credentials in its workspace
//	           -> connectedaccount.Service.Connect (encrypted credential + account)
//
// Reading accounts needs membership; connecting and disconnecting need
// manage_credentials (owners, admins), like credentials. Another
// workspace's account is reported as not found. Nothing returned holds a
// token.
type ConnectedAccountService struct {
	access    *Access
	accounts  *connectedaccount.Service
	flow      *oauth.Flow
	providers *oauth.Registry
	logger    *slog.Logger
}

// NewConnectedAccountService wires the service. logger may be nil.
func NewConnectedAccountService(access *Access, accounts *connectedaccount.Service, flow *oauth.Flow, providers *oauth.Registry, logger *slog.Logger) *ConnectedAccountService {
	if logger == nil {
		logger = slog.Default()
	}
	return &ConnectedAccountService{access: access, accounts: accounts, flow: flow, providers: providers, logger: logger}
}

// Providers lists the configured OAuth providers (public metadata only:
// callers must not expose ClientSecret, and no response type has a field
// for it).
func (s *ConnectedAccountService) Providers() []oauth.ProviderConfig { return s.providers.List() }

// ProviderName is a provider's display name (its ID when unknown).
func (s *ConnectedAccountService) ProviderName(id string) string {
	if p, ok := s.providers.Get(id); ok {
		return p.Config().Name
	}
	return id
}

// Authorize starts connecting an account of provider to the workspace.
func (s *ConnectedAccountService) Authorize(ctx context.Context, user, workspaceID uuid.UUID, provider string) (oauth.Started, error) {
	if _, err := s.access.Require(ctx, user, workspaceID, workspace.ActionManageCredentials, "workspace"); err != nil {
		return oauth.Started{}, err
	}
	if _, ok := s.providers.Get(provider); !ok {
		return oauth.Started{}, notFound("oauth provider")
	}
	started, err := s.flow.Begin(ctx, provider, workspaceID, user)
	if err != nil {
		return oauth.Started{}, err
	}
	s.logger.InfoContext(ctx, "OAuth authorization started", "event", "oauth_authorization_started",
		"provider", provider, "workspace_id", workspaceID.String(), "user_id", user.String())
	return started, nil
}

// Callback completes an authorization. Its errors are *oauth.Error (the
// kind is what the browser is told); the account is returned on success.
func (s *ConnectedAccountService) Callback(ctx context.Context, provider string, cb oauth.Callback) (connectedaccount.Account, error) {
	done, err := s.flow.Complete(ctx, provider, cb)
	if err != nil {
		s.failed(ctx, provider, done.State, err)
		return connectedaccount.Account{}, err
	}
	st := done.State
	// The user who started the flow must still be allowed to connect
	// accounts in that workspace.
	if _, err := s.access.Require(ctx, st.UserID, st.WorkspaceID, workspace.ActionManageCredentials, "workspace"); err != nil {
		err = &oauth.Error{Kind: oauth.KindInvalidState, Message: "the user may no longer connect accounts in this workspace", Err: err}
		s.failed(ctx, provider, st, err)
		return connectedaccount.Account{}, err
	}
	acct, created, err := s.accounts.Connect(ctx, connectedaccount.ConnectInput{WorkspaceID: st.WorkspaceID, UserID: st.UserID,
		Provider: provider, Identity: done.Identity, Token: done.Token})
	if err != nil {
		s.logger.ErrorContext(ctx, "connected account could not be stored", "event", "oauth_authorization_failed",
			"provider", provider, "workspace_id", st.WorkspaceID.String(), "error", err)
		return connectedaccount.Account{}, &oauth.Error{Kind: oauth.KindTokenExchangeFailed, Message: "the account could not be saved", Err: err}
	}
	event := "connected_account_reconnected"
	if created {
		event = "connected_account_created"
	}
	s.logger.InfoContext(ctx, "OAuth authorization completed", "event", "oauth_authorization_completed",
		"provider", provider, "workspace_id", st.WorkspaceID.String(), "account_id", acct.ID.String(), "result", event)
	return acct, nil
}

func (s *ConnectedAccountService) failed(ctx context.Context, provider string, st oauth.State, err error) {
	attrs := []any{"event", "oauth_authorization_failed", "provider", provider, "kind", string(oauth.KindOf(err))}
	if st.WorkspaceID != uuid.Nil {
		attrs = append(attrs, "workspace_id", st.WorkspaceID.String())
	}
	s.logger.WarnContext(ctx, "OAuth authorization failed", attrs...)
}

// List returns the workspace's connected accounts (any member: workflow
// authors pick accounts for their nodes).
func (s *ConnectedAccountService) List(ctx context.Context, user, workspaceID uuid.UUID) ([]connectedaccount.Account, error) {
	if _, err := s.access.Require(ctx, user, workspaceID, workspace.ActionRead, "workspace"); err != nil {
		return nil, err
	}
	return s.accounts.List(ctx, workspaceID)
}

func (s *ConnectedAccountService) account(ctx context.Context, user, id uuid.UUID, action workspace.Action) (connectedaccount.Account, error) {
	a, err := s.accounts.Get(ctx, id)
	if errors.Is(err, connectedaccount.ErrNotFound) {
		return connectedaccount.Account{}, notFound("connected account")
	}
	if err != nil {
		return connectedaccount.Account{}, err
	}
	if _, err := s.access.Require(ctx, user, a.WorkspaceID, action, "connected account"); err != nil {
		return connectedaccount.Account{}, err
	}
	return a, nil
}

// Get returns one account of a workspace the user belongs to.
func (s *ConnectedAccountService) Get(ctx context.Context, user, id uuid.UUID) (connectedaccount.Account, error) {
	return s.account(ctx, user, id, workspace.ActionRead)
}

// Disconnect disconnects an account (owners and admins).
func (s *ConnectedAccountService) Disconnect(ctx context.Context, user, id uuid.UUID) (connectedaccount.Account, error) {
	a, err := s.account(ctx, user, id, workspace.ActionManageCredentials)
	if err != nil {
		return connectedaccount.Account{}, err
	}
	out, _, err := s.accounts.Disconnect(ctx, a)
	return out, err
}
