// Package workspace owns workspace and membership persistence concepts.
// Workspace membership is the tenant boundary (Phase 12).
package workspace

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrNotMember is returned when a user has no membership in a workspace
// (including when the workspace does not exist).
var ErrNotMember = errors.New("workspace: user is not a member")

// Role is a workspace membership role.
type Role string

const (
	RoleOwner  Role = "owner"
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
	RoleViewer Role = "viewer"
)

// Action is something a member may do inside a workspace.
type Action string

const (
	// ActionRead: read workflows, versions, executions, projects and
	// credential metadata.
	ActionRead Action = "read"
	// ActionWrite: create and edit workflows and create versions.
	ActionWrite Action = "write"
	// ActionDelete: delete workflows.
	ActionDelete Action = "delete"
	// ActionPublish: publish workflow versions.
	ActionPublish Action = "publish"
	// ActionExecute: start and cancel executions.
	ActionExecute Action = "execute"
	// ActionManageCredentials: create and delete credentials.
	ActionManageCredentials Action = "manage_credentials"
	// ActionManageProjects: create projects.
	ActionManageProjects Action = "manage_projects"
)

// permissions is the whole authorization model: a fixed role -> actions
// table. Owners and admins may do everything defined here (no
// ownership-specific operation exists yet); members build and run workflows
// but do not delete, publish or manage credentials and projects; viewers
// only read.
var permissions = map[Role]map[Action]bool{
	RoleOwner: {ActionRead: true, ActionWrite: true, ActionDelete: true, ActionPublish: true,
		ActionExecute: true, ActionManageCredentials: true, ActionManageProjects: true},
	RoleAdmin: {ActionRead: true, ActionWrite: true, ActionDelete: true, ActionPublish: true,
		ActionExecute: true, ActionManageCredentials: true, ActionManageProjects: true},
	RoleMember: {ActionRead: true, ActionWrite: true, ActionExecute: true},
	RoleViewer: {ActionRead: true},
}

// Can reports whether the role permits the action.
func (r Role) Can(a Action) bool { return permissions[r][a] }

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

// Membership is a workspace seen by one of its members.
type Membership struct {
	Workspace Workspace
	Role      Role
}

// Repository is the persistence boundary for workspaces and memberships.
type Repository interface {
	FindByID(context.Context, uuid.UUID) (Workspace, error)
	ListMembers(context.Context, uuid.UUID) ([]Member, error)
}
