package application

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"workflow-optimizer/internal/workspace"
)

// MembershipStore resolves a user's role in a workspace.
type MembershipStore interface {
	// MemberRole returns workspace.ErrNotMember when the user is not a member.
	MemberRole(ctx context.Context, workspaceID, userID uuid.UUID) (workspace.Role, error)
}

// Access enforces the tenant boundary: authenticated user -> workspace
// membership -> role permission.
type Access struct {
	members MembershipStore
}

// NewAccess returns the authorizer over members.
func NewAccess(members MembershipStore) *Access { return &Access{members: members} }

// Require checks that user may perform action in the workspace that owns a
// resource. A non-member gets the resource's NotFoundError (existence is not
// revealed); a member whose role lacks the action gets ErrForbidden.
func (a *Access) Require(ctx context.Context, user, workspaceID uuid.UUID, action workspace.Action, resource string) (workspace.Role, error) {
	role, err := a.members.MemberRole(ctx, workspaceID, user)
	if errors.Is(err, workspace.ErrNotMember) {
		return "", notFound(resource)
	}
	if err != nil {
		return "", err
	}
	if !role.Can(action) {
		return role, ErrForbidden
	}
	return role, nil
}
