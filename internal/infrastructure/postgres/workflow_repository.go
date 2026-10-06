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

	"workflow-optimizer/internal/workflow"
)

// WorkflowRepository stores projects, workflow metadata and workflow versions
// for the API (Phase 12). Deleted workflows (deleted_at, migration 000005)
// are invisible here; their versions and executions are kept.
type WorkflowRepository struct {
	pool *pgxpool.Pool
}

// NewWorkflowRepository uses the Store's existing connection pool.
func NewWorkflowRepository(store *Store) *WorkflowRepository {
	return &WorkflowRepository{pool: store.Pool}
}

// --- projects ---------------------------------------------------------------

const projectColumns = `id, workspace_id, name, description, created_at, updated_at`

func scanProject(row pgx.Row) (workflow.Project, error) {
	var p workflow.Project
	err := row.Scan(&p.ID, &p.WorkspaceID, &p.Name, &p.Description, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return workflow.Project{}, workflow.ErrProjectNotFound
	}
	if err != nil {
		return workflow.Project{}, fmt.Errorf("scan project: %w", err)
	}
	return p, nil
}

// CreateProject inserts a project.
func (r *WorkflowRepository) CreateProject(ctx context.Context, p workflow.Project) (workflow.Project, error) {
	return scanProject(r.pool.QueryRow(ctx, `
INSERT INTO projects (id, workspace_id, name, description) VALUES ($1, $2, $3, $4) RETURNING `+projectColumns,
		uuid.New(), p.WorkspaceID, p.Name, p.Description))
}

// FindProject loads a project.
func (r *WorkflowRepository) FindProject(ctx context.Context, id uuid.UUID) (workflow.Project, error) {
	return scanProject(r.pool.QueryRow(ctx, `SELECT `+projectColumns+` FROM projects WHERE id = $1`, id))
}

// ListProjects returns a workspace's projects, oldest first.
func (r *WorkflowRepository) ListProjects(ctx context.Context, workspaceID uuid.UUID) ([]workflow.Project, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+projectColumns+` FROM projects WHERE workspace_id = $1 ORDER BY created_at, id`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()
	out := []workflow.Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// --- workflows --------------------------------------------------------------

const workflowColumns = `w.id, w.project_id, w.name, w.description, w.active_version_id, w.created_at, w.updated_at`

func scanWorkflow(row pgx.Row, extra ...any) (workflow.Workflow, error) {
	var wf workflow.Workflow
	dst := append([]any{&wf.ID, &wf.ProjectID, &wf.Name, &wf.Description, &wf.ActiveVersionID, &wf.CreatedAt, &wf.UpdatedAt}, extra...)
	err := row.Scan(dst...)
	if errors.Is(err, pgx.ErrNoRows) {
		return workflow.Workflow{}, workflow.ErrWorkflowNotFound
	}
	if err != nil {
		return workflow.Workflow{}, fmt.Errorf("scan workflow: %w", err)
	}
	return wf, nil
}

// CreateWorkflow inserts workflow metadata (no version).
func (r *WorkflowRepository) CreateWorkflow(ctx context.Context, wf workflow.Workflow) (workflow.Workflow, error) {
	created, err := scanWorkflow(r.pool.QueryRow(ctx, `
INSERT INTO workflows AS w (id, project_id, name, description) VALUES ($1, $2, $3, $4) RETURNING `+workflowColumns,
		uuid.New(), wf.ProjectID, wf.Name, wf.Description))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgForeignKeyViolation {
		return workflow.Workflow{}, workflow.ErrProjectNotFound
	}
	return created, err
}

// FindWorkflow loads a live workflow and the workspace that owns it.
func (r *WorkflowRepository) FindWorkflow(ctx context.Context, id uuid.UUID) (workflow.Workflow, uuid.UUID, error) {
	var ws uuid.UUID
	wf, err := scanWorkflow(r.pool.QueryRow(ctx, `
SELECT `+workflowColumns+`, p.workspace_id
  FROM workflows w JOIN projects p ON p.id = w.project_id
 WHERE w.id = $1 AND w.deleted_at IS NULL`, id), &ws)
	return wf, ws, err
}

// ListWorkflows returns one page of a project's live workflows (newest
// first) and the total count.
func (r *WorkflowRepository) ListWorkflows(ctx context.Context, projectID uuid.UUID, limit, offset int) ([]workflow.Workflow, int, error) {
	var total int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM workflows WHERE project_id = $1 AND deleted_at IS NULL`, projectID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count workflows: %w", err)
	}
	rows, err := r.pool.Query(ctx, `
SELECT `+workflowColumns+` FROM workflows w
 WHERE w.project_id = $1 AND w.deleted_at IS NULL
 ORDER BY w.created_at DESC, w.id
 LIMIT $2 OFFSET $3`, projectID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list workflows: %w", err)
	}
	defer rows.Close()
	out := []workflow.Workflow{}
	for rows.Next() {
		wf, err := scanWorkflow(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, wf)
	}
	return out, total, rows.Err()
}

// UpdateWorkflow writes the workflow's name and description.
func (r *WorkflowRepository) UpdateWorkflow(ctx context.Context, wf workflow.Workflow) (workflow.Workflow, error) {
	return scanWorkflow(r.pool.QueryRow(ctx, `
UPDATE workflows w SET name = $2, description = $3, updated_at = now()
 WHERE w.id = $1 AND w.deleted_at IS NULL
RETURNING `+workflowColumns, wf.ID, wf.Name, wf.Description))
}

// DeleteWorkflow hides the workflow. Its versions and executions are kept.
func (r *WorkflowRepository) DeleteWorkflow(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `UPDATE workflows SET deleted_at = now(), updated_at = now() WHERE id = $1 AND deleted_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("delete workflow: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return workflow.ErrWorkflowNotFound
	}
	return nil
}

// --- versions ---------------------------------------------------------------

const versionListColumns = `id, workflow_id, version_number, status, created_by, created_at, published_at`

// lockLiveWorkflow locks a live workflow row for the rest of tx.
func lockLiveWorkflow(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	var one int
	err := tx.QueryRow(ctx, `SELECT 1 FROM workflows WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, id).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return workflow.ErrWorkflowNotFound
	}
	return err
}

// CreateVersion stores a new DRAFT version numbered after the workflow's
// latest one. Numbering is serialized by locking the workflow row.
func (r *WorkflowRepository) CreateVersion(ctx context.Context, workflowID uuid.UUID, definition json.RawMessage, createdBy uuid.UUID) (workflow.Version, error) {
	var v workflow.Version
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := lockLiveWorkflow(ctx, tx, workflowID); err != nil {
			return err
		}
		var err error
		v, err = scanVersion(tx.QueryRow(ctx, `
INSERT INTO workflow_versions (id, workflow_id, version_number, definition, status, created_by)
SELECT $1, $2, COALESCE(MAX(version_number), 0) + 1, $3, 'DRAFT', $4 FROM workflow_versions WHERE workflow_id = $2
RETURNING `+versionColumns, uuid.New(), workflowID, []byte(definition), createdBy))
		return err
	})
	if err != nil {
		return workflow.Version{}, err
	}
	return v, nil
}

// FindVersion loads a version of the workflow (with its definition).
func (r *WorkflowRepository) FindVersion(ctx context.Context, workflowID, versionID uuid.UUID) (workflow.Version, error) {
	v, err := scanVersion(r.pool.QueryRow(ctx, `SELECT `+versionColumns+` FROM workflow_versions WHERE id = $1 AND workflow_id = $2`, versionID, workflowID))
	if errors.Is(err, ErrWorkflowVersionNotFound) {
		return workflow.Version{}, workflow.ErrVersionNotFound
	}
	return v, err
}

// ListVersions returns one page of the workflow's versions (newest first,
// without definitions) and the total count.
func (r *WorkflowRepository) ListVersions(ctx context.Context, workflowID uuid.UUID, limit, offset int) ([]workflow.Version, int, error) {
	var total int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM workflow_versions WHERE workflow_id = $1`, workflowID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count workflow versions: %w", err)
	}
	rows, err := r.pool.Query(ctx, `SELECT `+versionListColumns+` FROM workflow_versions WHERE workflow_id = $1
 ORDER BY version_number DESC LIMIT $2 OFFSET $3`, workflowID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list workflow versions: %w", err)
	}
	defer rows.Close()
	out := []workflow.Version{}
	for rows.Next() {
		var (
			v      workflow.Version
			status string
		)
		if err := rows.Scan(&v.ID, &v.WorkflowID, &v.VersionNumber, &status, &v.CreatedBy, &v.CreatedAt, &v.PublishedAt); err != nil {
			return nil, 0, fmt.Errorf("scan workflow version: %w", err)
		}
		v.Status = workflow.VersionStatus(status)
		out = append(out, v)
	}
	return out, total, rows.Err()
}

// PublishVersion publishes the version and makes it the workflow's active
// version, atomically. check runs on the locked version (its definition can
// no longer change underneath) and aborts the publish when it returns an
// error. Publishing an already PUBLISHED version only re-activates it; an
// ARCHIVED version is refused. The database keeps published versions
// immutable (migration 000002).
func (r *WorkflowRepository) PublishVersion(ctx context.Context, workflowID, versionID uuid.UUID, check func(workflow.Version) error) (workflow.Version, error) {
	var v workflow.Version
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := lockLiveWorkflow(ctx, tx, workflowID); err != nil {
			return err
		}
		var err error
		v, err = scanVersion(tx.QueryRow(ctx, `SELECT `+versionColumns+` FROM workflow_versions WHERE id = $1 AND workflow_id = $2 FOR UPDATE`, versionID, workflowID))
		if errors.Is(err, ErrWorkflowVersionNotFound) {
			return workflow.ErrVersionNotFound
		}
		if err != nil {
			return err
		}
		if v.Status == workflow.VersionStatusArchived {
			return workflow.ErrVersionArchived
		}
		if err := check(v); err != nil {
			return err
		}
		if v.Status == workflow.VersionStatusDraft {
			if v, err = scanVersion(tx.QueryRow(ctx, `UPDATE workflow_versions SET status = 'PUBLISHED' WHERE id = $1 RETURNING `+versionColumns, versionID)); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `UPDATE workflows SET active_version_id = $2, updated_at = now() WHERE id = $1`, workflowID, versionID)
		return err
	})
	if err != nil {
		return workflow.Version{}, err
	}
	return v, nil
}
