// Package workspace owns workspace and membership persistence concepts.
package workspace

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Role is a workspace membership role.
type Role string

const (
	RoleOwner  Role = "owner"
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
	RoleViewer Role = "viewer"
)

// Workspace is the durable workspace representation.
type Workspace struct {
	ID        uuid.UUID
	Name      string
	OwnerID   uuid.UUID
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Member is a durable workspace membership representation.
type Member struct {
	WorkspaceID uuid.UUID
	UserID      uuid.UUID
	Role        Role
	CreatedAt   time.Time
}

// Repository is the persistence boundary for workspaces and memberships.
type Repository interface {
	FindByID(context.Context, uuid.UUID) (Workspace, error)
	ListMembers(context.Context, uuid.UUID) ([]Member, error)
}
