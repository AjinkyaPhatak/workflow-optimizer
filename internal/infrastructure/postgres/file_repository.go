package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"workflow-optimizer/internal/files"
)

// FileRepository is the PostgreSQL files.Repository (Phase D1, files).
// Every read and delete is scoped to a workspace; only Locate (used to
// authorize by the file's workspace) reads by ID alone.
type FileRepository struct {
	pool *pgxpool.Pool
}

var _ files.Repository = (*FileRepository)(nil)

// NewFileRepository uses the Store's pool.
func NewFileRepository(store *Store) *FileRepository { return &FileRepository{pool: store.Pool} }

const fileColumns = `id, workspace_id, name, content_type, size_bytes, sha256, created_by, created_at`

func scanFile(row pgx.Row, extra ...any) (files.File, error) {
	var f files.File
	err := row.Scan(append([]any{&f.ID, &f.WorkspaceID, &f.Name, &f.ContentType, &f.Size, &f.SHA256, &f.CreatedBy, &f.CreatedAt}, extra...)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return files.File{}, files.ErrNotFound
	}
	return f, err
}

// Create stores a file and its bytes.
func (r *FileRepository) Create(ctx context.Context, f files.File, data []byte) (files.File, error) {
	out, err := scanFile(r.pool.QueryRow(ctx, `INSERT INTO files (id, workspace_id, name, content_type, size_bytes, sha256, data, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING `+fileColumns,
		f.ID, f.WorkspaceID, f.Name, f.ContentType, f.Size, f.SHA256, data, f.CreatedBy))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == pgForeignKeyViolation || pgErr.Code == pgCheckViolation) {
		return files.File{}, fmt.Errorf("%w: %s", files.ErrInvalid, pgErr.ConstraintName)
	}
	if err != nil {
		return files.File{}, fmt.Errorf("create file: %w", err)
	}
	return out, nil
}

// Get returns a file's metadata in a workspace.
func (r *FileRepository) Get(ctx context.Context, workspaceID, id uuid.UUID) (files.File, error) {
	return scanFile(r.pool.QueryRow(ctx, `SELECT `+fileColumns+` FROM files WHERE id = $1 AND workspace_id = $2`, id, workspaceID))
}

// Content returns a file of a workspace with its bytes.
func (r *FileRepository) Content(ctx context.Context, workspaceID, id uuid.UUID) (files.File, []byte, error) {
	var data []byte
	f, err := scanFile(r.pool.QueryRow(ctx, `SELECT `+fileColumns+`, data FROM files WHERE id = $1 AND workspace_id = $2`, id, workspaceID), &data)
	if err != nil {
		return files.File{}, nil, err
	}
	return f, data, nil
}

// Locate returns a file's metadata by ID alone.
func (r *FileRepository) Locate(ctx context.Context, id uuid.UUID) (files.File, error) {
	return scanFile(r.pool.QueryRow(ctx, `SELECT `+fileColumns+` FROM files WHERE id = $1`, id))
}

// List returns a workspace's files, newest first.
func (r *FileRepository) List(ctx context.Context, workspaceID uuid.UUID) ([]files.File, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+fileColumns+` FROM files WHERE workspace_id = $1 ORDER BY created_at DESC, id`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list files: %w", err)
	}
	defer rows.Close()
	out := []files.File{}
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Delete removes a file of a workspace.
func (r *FileRepository) Delete(ctx context.Context, workspaceID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM files WHERE id = $1 AND workspace_id = $2`, id, workspaceID)
	if err != nil {
		return fmt.Errorf("delete file: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return files.ErrNotFound
	}
	return nil
}
