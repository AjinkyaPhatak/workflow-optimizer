package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"workflow-optimizer/internal/connectedaccount"
)

// ConnectedAccountRepository is the PostgreSQL connectedaccount.Repository
// (Phase C2, connected_accounts). It holds metadata only: tokens are in the
// referenced credential's encrypted envelope.
type ConnectedAccountRepository struct {
	pool *pgxpool.Pool
}

var _ connectedaccount.Repository = (*ConnectedAccountRepository)(nil)

// NewConnectedAccountRepository uses the Store's pool.
func NewConnectedAccountRepository(store *Store) *ConnectedAccountRepository {
	return &ConnectedAccountRepository{pool: store.Pool}
}

const connectedAccountColumns = `id, workspace_id, user_id, provider, provider_account_id, display_name, email,
	credential_id, status, scopes, created_at, updated_at, last_used_at`

func scanConnectedAccount(row pgx.Row) (connectedaccount.Account, error) {
	var (
		a      connectedaccount.Account
		status string
	)
	err := row.Scan(&a.ID, &a.WorkspaceID, &a.UserID, &a.Provider, &a.ProviderAccountID, &a.DisplayName, &a.Email,
		&a.CredentialID, &status, &a.Scopes, &a.CreatedAt, &a.UpdatedAt, &a.LastUsedAt)
	a.Status = connectedaccount.Status(status)
	if a.Scopes == nil {
		a.Scopes = []string{}
	}
	return a, err
}

func connectedAccountError(err error, what string) error {
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("%w: %s", connectedaccount.ErrNotFound, what)
	case errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation:
		return fmt.Errorf("%w: %s", connectedaccount.ErrDuplicate, what)
	case errors.As(err, &pgErr) && (pgErr.Code == pgForeignKeyViolation || pgErr.Code == pgCheckViolation):
		return fmt.Errorf("%w: %s (%s)", connectedaccount.ErrInvalid, what, pgErr.ConstraintName)
	}
	return fmt.Errorf("%s: %w", what, err)
}

func scopesOf(a connectedaccount.Account) []string {
	if a.Scopes == nil {
		return []string{}
	}
	return a.Scopes
}

// Create inserts an account.
func (r *ConnectedAccountRepository) Create(ctx context.Context, a connectedaccount.Account) (connectedaccount.Account, error) {
	if err := a.Validate(); err != nil {
		return connectedaccount.Account{}, err
	}
	out, err := scanConnectedAccount(r.pool.QueryRow(ctx, `
INSERT INTO connected_accounts (workspace_id, user_id, provider, provider_account_id, display_name, email, credential_id, status, scopes)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING `+connectedAccountColumns,
		a.WorkspaceID, a.UserID, a.Provider, a.ProviderAccountID, a.DisplayName, a.Email, a.CredentialID, string(a.Status), scopesOf(a)))
	if err != nil {
		return connectedaccount.Account{}, connectedAccountError(err, "create connected account")
	}
	return out, nil
}

// Get loads one account.
func (r *ConnectedAccountRepository) Get(ctx context.Context, id uuid.UUID) (connectedaccount.Account, error) {
	a, err := scanConnectedAccount(r.pool.QueryRow(ctx, `SELECT `+connectedAccountColumns+` FROM connected_accounts WHERE id = $1`, id))
	if err != nil {
		return connectedaccount.Account{}, connectedAccountError(err, "connected account "+id.String())
	}
	return a, nil
}

// FindByIdentity loads the workspace's account of an external identity.
func (r *ConnectedAccountRepository) FindByIdentity(ctx context.Context, workspaceID uuid.UUID, provider, providerAccountID string) (connectedaccount.Account, error) {
	a, err := scanConnectedAccount(r.pool.QueryRow(ctx, `SELECT `+connectedAccountColumns+`
FROM connected_accounts WHERE workspace_id = $1 AND provider = $2 AND provider_account_id = $3`, workspaceID, provider, providerAccountID))
	if err != nil {
		return connectedaccount.Account{}, connectedAccountError(err, "connected account")
	}
	return a, nil
}

// ListByWorkspace returns the workspace's accounts by provider, then age.
func (r *ConnectedAccountRepository) ListByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]connectedaccount.Account, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+connectedAccountColumns+`
FROM connected_accounts WHERE workspace_id = $1 ORDER BY provider, created_at, id`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list connected accounts: %w", err)
	}
	defer rows.Close()
	out := []connectedaccount.Account{}
	for rows.Next() {
		a, err := scanConnectedAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("scan connected account: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Update stores the mutable fields of the workspace's account.
func (r *ConnectedAccountRepository) Update(ctx context.Context, a connectedaccount.Account) (connectedaccount.Account, error) {
	if err := a.Validate(); err != nil {
		return connectedaccount.Account{}, err
	}
	out, err := scanConnectedAccount(r.pool.QueryRow(ctx, `
UPDATE connected_accounts SET user_id = $3, display_name = $4, email = $5, status = $6, scopes = $7, updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING `+connectedAccountColumns, a.ID, a.WorkspaceID, a.UserID, a.DisplayName, a.Email, string(a.Status), scopesOf(a)))
	if err != nil {
		return connectedaccount.Account{}, connectedAccountError(err, "update connected account "+a.ID.String())
	}
	return out, nil
}

// SetStatusByCredential changes the status of a credential's account. A
// disconnected account stays disconnected.
func (r *ConnectedAccountRepository) SetStatusByCredential(ctx context.Context, credentialID uuid.UUID, status connectedaccount.Status) error {
	if !status.Valid() {
		return fmt.Errorf("%w: unknown status %q", connectedaccount.ErrInvalid, status)
	}
	_, err := r.pool.Exec(ctx, `UPDATE connected_accounts SET status = $2, updated_at = now()
WHERE credential_id = $1 AND status <> 'DISCONNECTED' AND status <> $2`, credentialID, string(status))
	if err != nil {
		return fmt.Errorf("set connected account status: %w", err)
	}
	return nil
}

// TouchByCredential records the last use of a credential's account.
func (r *ConnectedAccountRepository) TouchByCredential(ctx context.Context, credentialID uuid.UUID, at time.Time) error {
	_, err := r.pool.Exec(ctx, `UPDATE connected_accounts SET last_used_at = $2 WHERE credential_id = $1`, credentialID, at)
	if err != nil {
		return fmt.Errorf("touch connected account: %w", err)
	}
	return nil
}
