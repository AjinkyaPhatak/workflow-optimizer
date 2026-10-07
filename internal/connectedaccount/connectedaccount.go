// Package connectedaccount is the Connected Account domain (Phase C2): an
// external account (a Google account, a Discord user...) that a workspace
// authorized through OAuth. It is provider-neutral.
//
// An account holds metadata only. Its OAuth tokens live, encrypted, in the
// OAUTH2 credential it references (credential_id): workflows reference that
// credential, exactly like an API-key credential, and never see the account
// or its tokens.
package connectedaccount

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Status is an account's state.
type Status string

const (
	// StatusActive: usable.
	StatusActive Status = "ACTIVE"
	// StatusExpired: the access token expired and cannot be refreshed.
	StatusExpired Status = "EXPIRED"
	// StatusRevoked: the provider revoked the authorization.
	StatusRevoked Status = "REVOKED"
	// StatusError: refreshing failed for another lasting reason.
	StatusError Status = "ERROR"
	// StatusDisconnected: disconnected by a user; its tokens were removed.
	StatusDisconnected Status = "DISCONNECTED"
)

// Valid reports whether s is a known status.
func (s Status) Valid() bool {
	switch s {
	case StatusActive, StatusExpired, StatusRevoked, StatusError, StatusDisconnected:
		return true
	}
	return false
}

// Account is a connected account. ProviderAccountID is the stable external
// identity: reconnecting the same external account updates this account.
type Account struct {
	ID          uuid.UUID
	WorkspaceID uuid.UUID
	// UserID is who connected (or last reconnected) the account.
	UserID            uuid.UUID
	Provider          string
	ProviderAccountID string
	DisplayName       string
	Email             string
	CredentialID      uuid.UUID
	Status            Status
	// Scopes are the granted scopes (not secret).
	Scopes     []string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	LastUsedAt *time.Time
}

// Errors.
var (
	ErrNotFound  = errors.New("connected account: not found")
	ErrDuplicate = errors.New("connected account: already connected")
	ErrInvalid   = errors.New("connected account: invalid")
)

// MaxProviderAccountID bounds the external identity.
const MaxProviderAccountID = 255

// Validate checks an account before it is stored.
func (a Account) Validate() error {
	switch {
	case a.WorkspaceID == uuid.Nil:
		return fmt.Errorf("%w: workspace is required", ErrInvalid)
	case a.UserID == uuid.Nil:
		return fmt.Errorf("%w: user is required", ErrInvalid)
	case strings.TrimSpace(a.Provider) == "":
		return fmt.Errorf("%w: provider is required", ErrInvalid)
	case strings.TrimSpace(a.ProviderAccountID) == "" || len(a.ProviderAccountID) > MaxProviderAccountID:
		return fmt.Errorf("%w: a provider account ID of 1 to %d characters is required", ErrInvalid, MaxProviderAccountID)
	case a.CredentialID == uuid.Nil:
		return fmt.Errorf("%w: credential is required", ErrInvalid)
	case !a.Status.Valid():
		return fmt.Errorf("%w: unknown status %q", ErrInvalid, a.Status)
	}
	return nil
}

// Label is how the account is shown: "Google — ada@example.com".
func (a Account) Label(providerName string) string {
	who := a.Email
	if who == "" {
		who = a.DisplayName
	}
	if who == "" {
		who = a.ProviderAccountID
	}
	return providerName + " — " + who
}

// Repository persists accounts. The (workspace, provider, provider account)
// triple is unique; so is credential_id.
type Repository interface {
	// Create returns ErrDuplicate when the external account is already
	// connected to the workspace.
	Create(ctx context.Context, a Account) (Account, error)
	// Get returns ErrNotFound when missing.
	Get(ctx context.Context, id uuid.UUID) (Account, error)
	FindByIdentity(ctx context.Context, workspaceID uuid.UUID, provider, providerAccountID string) (Account, error)
	ListByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]Account, error)
	// Update stores user, display name, email, status and scopes.
	Update(ctx context.Context, a Account) (Account, error)
	// SetStatusByCredential changes the status of the account of a
	// credential (no-op when it has none).
	SetStatusByCredential(ctx context.Context, credentialID uuid.UUID, status Status) error
	// TouchByCredential records a use.
	TouchByCredential(ctx context.Context, credentialID uuid.UUID, at time.Time) error
}
