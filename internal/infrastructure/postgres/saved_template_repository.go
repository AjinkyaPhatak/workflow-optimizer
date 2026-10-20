package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"workflow-optimizer/internal/templates"
)

// SavedTemplateRepository is the PostgreSQL templates.SavedRepository
// (Phase K, saved_templates).
type SavedTemplateRepository struct {
	pool *pgxpool.Pool
}

var _ templates.SavedRepository = (*SavedTemplateRepository)(nil)

// NewSavedTemplateRepository uses the Store's pool.
func NewSavedTemplateRepository(store *Store) *SavedTemplateRepository {
	return &SavedTemplateRepository{pool: store.Pool}
}

const savedTemplateColumns = `id, workspace_id, name, description, definition, created_by, created_at`

func scanSavedTemplate(row pgx.Row) (templates.Saved, error) {
	var s templates.Saved
	var def []byte
	err := row.Scan(&s.ID, &s.WorkspaceID, &s.Name, &s.Description, &def, &s.CreatedBy, &s.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return templates.Saved{}, templates.ErrSavedNotFound
	}
	if err != nil {
		return templates.Saved{}, err
	}
	if err := json.Unmarshal(def, &s.Definition); err != nil {
		return templates.Saved{}, fmt.Errorf("decode saved template %s: %w", s.ID, err)
	}
	return s, nil
}

// Create stores a saved template.
func (r *SavedTemplateRepository) Create(ctx context.Context, s templates.Saved) (templates.Saved, error) {
	def, err := json.Marshal(s.Definition)
	if err != nil {
		return templates.Saved{}, err
	}
	out, err := scanSavedTemplate(r.pool.QueryRow(ctx, `INSERT INTO saved_templates (workspace_id, name, description, definition, created_by)
		VALUES ($1, $2, $3, $4, $5) RETURNING `+savedTemplateColumns, s.WorkspaceID, s.Name, s.Description, def, s.CreatedBy))
	if err != nil {
		return templates.Saved{}, fmt.Errorf("create saved template: %w", err)
	}
	return out, nil
}

// List returns a workspace's saved templates, newest first.
func (r *SavedTemplateRepository) List(ctx context.Context, workspaceID uuid.UUID) ([]templates.Saved, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+savedTemplateColumns+` FROM saved_templates WHERE workspace_id = $1 ORDER BY created_at DESC, id`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list saved templates: %w", err)
	}
	defer rows.Close()
	out := []templates.Saved{}
	for rows.Next() {
		s, err := scanSavedTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Locate returns a saved template by ID alone.
func (r *SavedTemplateRepository) Locate(ctx context.Context, id uuid.UUID) (templates.Saved, error) {
	return scanSavedTemplate(r.pool.QueryRow(ctx, `SELECT `+savedTemplateColumns+` FROM saved_templates WHERE id = $1`, id))
}

// Delete removes a saved template of a workspace.
func (r *SavedTemplateRepository) Delete(ctx context.Context, workspaceID, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM saved_templates WHERE id = $1 AND workspace_id = $2`, id, workspaceID)
	if err != nil {
		return fmt.Errorf("delete saved template: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return templates.ErrSavedNotFound
	}
	return nil
}
