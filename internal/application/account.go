package application

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/auth"
	"workflow-optimizer/internal/usage"
	"workflow-optimizer/internal/workspace"
)

// Account settings and usage (Phase M): a person changes their own name and
// password; owners and admins rename the workspace; every member sees the
// workspace's usage.

// AccountStore updates users and workspaces.
type AccountStore interface {
	FindByID(ctx context.Context, id uuid.UUID) (auth.User, error)
	UpdateUserName(ctx context.Context, id uuid.UUID, name string) (auth.User, error)
	UpdatePasswordHash(ctx context.Context, id uuid.UUID, hash string) error
	RenameWorkspace(ctx context.Context, id uuid.UUID, name string) (workspace.Workspace, error)
}

// AccountService serves the settings pages.
type AccountService struct {
	access *Access
	store  AccountStore
	usage  usage.Repository
	now    func() time.Time
	// attempts limits wrong current passwords (a stolen session must not
	// be able to guess the password).
	attempts *attemptGuard
}

// NewAccountService wires the service.
func NewAccountService(access *Access, store AccountStore, reports usage.Repository) *AccountService {
	return &AccountService{access: access, store: store, usage: reports, now: time.Now, attempts: newAttemptGuard()}
}

// MaxNameLength bounds a user's and a workspace's name.
const MaxNameLength = 255

func cleanName(name, what string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > MaxNameLength {
		return "", &InvalidError{Message: what + " is required (at most 255 characters)"}
	}
	return name, nil
}

// UpdateProfile changes the user's name.
func (s *AccountService) UpdateProfile(ctx context.Context, user uuid.UUID, name string) (auth.User, error) {
	name, err := cleanName(name, "name")
	if err != nil {
		return auth.User{}, err
	}
	u, err := s.store.UpdateUserName(ctx, user, name)
	if errors.Is(err, auth.ErrUserNotFound) {
		return auth.User{}, notFound("user")
	}
	return u, err
}

// ChangePassword replaces the user's password after checking the current
// one (a wrong current password is ErrInvalidCredentials). Tokens issued
// before stay valid until they expire.
func (s *AccountService) ChangePassword(ctx context.Context, user uuid.UUID, current, next string) error {
	u, err := s.store.FindByID(ctx, user)
	if errors.Is(err, auth.ErrUserNotFound) {
		return notFound("user")
	}
	if err != nil {
		return err
	}
	key := "password:" + user.String()
	if s.attempts.locked(key) {
		return ErrTooManyAttempts
	}
	if !auth.CheckPassword(u.PasswordHash, current) {
		s.attempts.fail(key)
		return &InvalidError{Message: "the current password is not correct"}
	}
	s.attempts.reset(key)
	if current == next {
		return &InvalidError{Message: "the new password must be different"}
	}
	hash, err := auth.HashPassword(next)
	if err != nil {
		return &InvalidError{Message: "the new password must be 8 to 72 bytes"}
	}
	return s.store.UpdatePasswordHash(ctx, user, hash)
}

// RenameWorkspace changes the workspace's name (owners and admins).
func (s *AccountService) RenameWorkspace(ctx context.Context, user, workspaceID uuid.UUID, name string) (workspace.Workspace, workspace.Role, error) {
	name, err := cleanName(name, "name")
	if err != nil {
		return workspace.Workspace{}, "", err
	}
	role, err := s.access.Require(ctx, user, workspaceID, workspace.ActionManageWorkspace, "workspace")
	if err != nil {
		return workspace.Workspace{}, "", err
	}
	ws, err := s.store.RenameWorkspace(ctx, workspaceID, name)
	if errors.Is(err, workspace.ErrNotMember) {
		return workspace.Workspace{}, "", notFound("workspace")
	}
	return ws, role, err
}

// Usage is the workspace's usage over the last days (1 to usage.MaxDays).
func (s *AccountService) Usage(ctx context.Context, user, workspaceID uuid.UUID, days int) (usage.Report, error) {
	if days < 1 || days > usage.MaxDays {
		return usage.Report{}, &InvalidError{Message: "days must be between 1 and 90"}
	}
	if _, err := s.access.Require(ctx, user, workspaceID, workspace.ActionRead, "workspace"); err != nil {
		return usage.Report{}, err
	}
	since := s.now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -(days - 1))
	return s.usage.WorkspaceUsage(ctx, workspaceID, since)
}
