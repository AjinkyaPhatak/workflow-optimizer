package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"workflow-optimizer/internal/auth"
	"workflow-optimizer/internal/workspace"
)

// IdentityRepository stores users, workspaces and memberships (Phase 12) on
// the existing users, workspaces and workspace_members tables.
type IdentityRepository struct {
	pool *pgxpool.Pool
}

var _ auth.UserRepository = (*IdentityRepository)(nil)

// NewIdentityRepository uses the Store's existing connection pool.
func NewIdentityRepository(store *Store) *IdentityRepository {
	return &IdentityRepository{pool: store.Pool}
}

const userColumns = `id, email, name, password_hash, created_at, updated_at`

func scanUser(row pgx.Row) (auth.User, error) {
	var u auth.User
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.PasswordHash, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.User{}, auth.ErrUserNotFound
	}
	if err != nil {
		return auth.User{}, fmt.Errorf("scan user: %w", err)
	}
	return u, nil
}

// FindByID implements auth.UserRepository.
func (r *IdentityRepository) FindByID(ctx context.Context, id uuid.UUID) (auth.User, error) {
	return scanUser(r.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id))
}

// FindByEmail implements auth.UserRepository (emails are stored normalized).
func (r *IdentityRepository) FindByEmail(ctx context.Context, email string) (auth.User, error) {
	return scanUser(r.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE email = $1`, email))
}

// CreateUserWithWorkspace creates the user, a workspace they own, and their
// owner membership in one transaction.
func (r *IdentityRepository) CreateUserWithWorkspace(ctx context.Context, u auth.User, workspaceName string) (auth.User, workspace.Workspace, error) {
	var ws workspace.Workspace
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var err error
		u, err = scanUser(tx.QueryRow(ctx, `
INSERT INTO users (id, email, name, password_hash) VALUES ($1, $2, $3, $4) RETURNING `+userColumns,
			uuid.New(), u.Email, u.Name, u.PasswordHash))
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
INSERT INTO workspaces (id, name, owner_id) VALUES ($1, $2, $3) RETURNING id, name, owner_id, created_at, updated_at`,
			uuid.New(), workspaceName, u.ID).Scan(&ws.ID, &ws.Name, &ws.OwnerID, &ws.CreatedAt, &ws.UpdatedAt); err != nil {
			return fmt.Errorf("create workspace: %w", err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, $3)`,
			ws.ID, u.ID, string(workspace.RoleOwner))
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
		return auth.User{}, workspace.Workspace{}, auth.ErrEmailTaken
	}
	if err != nil {
		return auth.User{}, workspace.Workspace{}, fmt.Errorf("create user: %w", err)
	}
	return u, ws, nil
}

// MemberRole returns the user's role in the workspace, or
// workspace.ErrNotMember.
func (r *IdentityRepository) MemberRole(ctx context.Context, workspaceID, userID uuid.UUID) (workspace.Role, error) {
	var role string
	err := r.pool.QueryRow(ctx, `SELECT role FROM workspace_members WHERE workspace_id = $1 AND user_id = $2`,
		workspaceID, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", workspace.ErrNotMember
	}
	if err != nil {
		return "", fmt.Errorf("find membership: %w", err)
	}
	return workspace.Role(role), nil
}

// ListMemberships returns the workspaces the user belongs to, oldest first.
func (r *IdentityRepository) ListMemberships(ctx context.Context, userID uuid.UUID) ([]workspace.Membership, error) {
	rows, err := r.pool.Query(ctx, `
SELECT w.id, w.name, w.owner_id, w.created_at, w.updated_at, m.role
  FROM workspace_members m JOIN workspaces w ON w.id = m.workspace_id
 WHERE m.user_id = $1
 ORDER BY w.created_at, w.id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list memberships: %w", err)
	}
	defer rows.Close()
	out := []workspace.Membership{}
	for rows.Next() {
		var (
			m    workspace.Membership
			role string
		)
		if err := rows.Scan(&m.Workspace.ID, &m.Workspace.Name, &m.Workspace.OwnerID, &m.Workspace.CreatedAt, &m.Workspace.UpdatedAt, &role); err != nil {
			return nil, fmt.Errorf("scan membership: %w", err)
		}
		m.Role = workspace.Role(role)
		out = append(out, m)
	}
	return out, rows.Err()
}
