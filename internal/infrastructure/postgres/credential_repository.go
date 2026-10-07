package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"workflow-optimizer/internal/credential"
)

// CredentialRepository is the PostgreSQL credential.Repository on the
// existing credentials table. It stores and returns the encrypted envelope
// only (credentials.encrypted_data); it never sees plaintext.
type CredentialRepository struct {
	pool *pgxpool.Pool
}

var _ credential.Repository = (*CredentialRepository)(nil)

// NewCredentialRepository uses the Store's existing connection pool.
func NewCredentialRepository(store *Store) *CredentialRepository {
	return &CredentialRepository{pool: store.Pool}
}

const credentialColumns = `id, workspace_id, name, provider, credential_type, encrypted_data, created_at, updated_at`

func scanCredential(row pgx.Row) (credential.Credential, error) {
	var (
		c    credential.Credential
		typ  string
		data []byte
	)
	if err := row.Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.Provider, &typ, &data, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return credential.Credential{}, err
	}
	c.CredentialType = credential.Type(typ)
	c.EncryptedData = data
	return c, nil
}

// Create inserts the credential and returns the stored row.
func (r *CredentialRepository) Create(ctx context.Context, c credential.Credential) (credential.Credential, error) {
	if c.ID == uuid.Nil || c.WorkspaceID == uuid.Nil || len(c.EncryptedData) == 0 {
		return credential.Credential{}, fmt.Errorf("%w: id, workspace and encrypted data are required", credential.ErrInvalid)
	}
	created, err := scanCredential(r.pool.QueryRow(ctx, `
INSERT INTO credentials (id, workspace_id, name, provider, credential_type, encrypted_data)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING `+credentialColumns, c.ID, c.WorkspaceID, c.Name, c.Provider, string(c.CredentialType), []byte(c.EncryptedData)))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgForeignKeyViolation {
		return credential.Credential{}, fmt.Errorf("%w: workspace %s does not exist", credential.ErrInvalid, c.WorkspaceID)
	}
	if err != nil {
		return credential.Credential{}, fmt.Errorf("create credential: %w", err)
	}
	return created, nil
}

// Get loads one credential (ErrNotFound when missing).
func (r *CredentialRepository) Get(ctx context.Context, id uuid.UUID) (credential.Credential, error) {
	c, err := scanCredential(r.pool.QueryRow(ctx, `SELECT `+credentialColumns+` FROM credentials WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return credential.Credential{}, fmt.Errorf("%w: %s", credential.ErrNotFound, id)
	}
	if err != nil {
		return credential.Credential{}, fmt.Errorf("get credential %s: %w", id, err)
	}
	return c, nil
}

// Delete removes the workspace's credential; another workspace's credential
// is not found (and not deleted).
func (r *CredentialRepository) Delete(ctx context.Context, workspaceID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM credentials WHERE id = $1 AND workspace_id = $2`, id, workspaceID)
	var pgErr *pgconn.PgError
	// 23001 restrict_violation (ON DELETE RESTRICT) or 23503.
	if errors.As(err, &pgErr) && (pgErr.Code == "23001" || pgErr.Code == pgForeignKeyViolation) {
		return fmt.Errorf("%w: credential %s backs a connected account; disconnect it instead", credential.ErrInvalid, id)
	}
	if err != nil {
		return fmt.Errorf("delete credential %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", credential.ErrNotFound, id)
	}
	return nil
}

// UpdateData replaces the encrypted envelope of the workspace's credential
// (Phase C2: OAuth refresh, reconnect, disconnect).
func (r *CredentialRepository) UpdateData(ctx context.Context, workspaceID, id uuid.UUID, data json.RawMessage) error {
	if len(data) == 0 {
		return fmt.Errorf("%w: encrypted data is required", credential.ErrInvalid)
	}
	tag, err := r.pool.Exec(ctx, `UPDATE credentials SET encrypted_data = $3, updated_at = now() WHERE id = $1 AND workspace_id = $2`,
		id, workspaceID, []byte(data))
	if err != nil {
		return fmt.Errorf("update credential %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", credential.ErrNotFound, id)
	}
	return nil
}

// ListByWorkspace returns the workspace's credentials, by name.
func (r *CredentialRepository) ListByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]credential.Credential, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+credentialColumns+` FROM credentials WHERE workspace_id = $1 ORDER BY name, id`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list credentials: %w", err)
	}
	defer rows.Close()
	var out []credential.Credential
	for rows.Next() {
		c, err := scanCredential(rows)
		if err != nil {
			return nil, fmt.Errorf("scan credential: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
