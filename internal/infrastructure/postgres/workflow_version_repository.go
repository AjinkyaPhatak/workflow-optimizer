package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/workflow"
)

// ErrWorkflowVersionNotFound is returned when a workflow version is missing.
// It is the execution package's sentinel so lifecycle callers can match it.
var ErrWorkflowVersionNotFound = execution.ErrWorkflowVersionNotFound

// WorkflowVersionRepository implements the existing (Phase 2)
// workflow.VersionRepository and adapts it to execution.DefinitionLoader so
// a run always executes the exact persisted version.
type WorkflowVersionRepository struct {
	pool *pgxpool.Pool
}

var (
	_ workflow.VersionRepository = (*WorkflowVersionRepository)(nil)
	_ execution.DefinitionLoader = (*WorkflowVersionRepository)(nil)
	_ execution.WorkspaceLookup  = (*WorkflowVersionRepository)(nil)
)

// NewWorkflowVersionRepository uses the Store's existing connection pool.
func NewWorkflowVersionRepository(store *Store) *WorkflowVersionRepository {
	return &WorkflowVersionRepository{pool: store.Pool}
}

const versionColumns = `id, workflow_id, version_number, definition, status, created_by, created_at, published_at`

func scanVersion(row pgx.Row) (workflow.Version, error) {
	var (
		v      workflow.Version
		status string
		def    []byte
	)
	if err := row.Scan(&v.ID, &v.WorkflowID, &v.VersionNumber, &def, &status, &v.CreatedBy, &v.CreatedAt, &v.PublishedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return workflow.Version{}, ErrWorkflowVersionNotFound
		}
		return workflow.Version{}, fmt.Errorf("scan workflow version: %w", err)
	}
	v.Definition = json.RawMessage(def)
	v.Status = workflow.VersionStatus(status)
	return v, nil
}

// FindByID loads a workflow version by ID.
func (r *WorkflowVersionRepository) FindByID(ctx context.Context, id uuid.UUID) (workflow.Version, error) {
	return scanVersion(r.pool.QueryRow(ctx, `SELECT `+versionColumns+` FROM workflow_versions WHERE id = $1`, id))
}

// FindByWorkflowAndNumber loads a workflow version by workflow and number.
func (r *WorkflowVersionRepository) FindByWorkflowAndNumber(ctx context.Context, workflowID uuid.UUID, number int) (workflow.Version, error) {
	return scanVersion(r.pool.QueryRow(ctx, `SELECT `+versionColumns+` FROM workflow_versions WHERE workflow_id = $1 AND version_number = $2`, workflowID, number))
}

// WorkspaceOf implements execution.WorkspaceLookup: the workspace that owns
// the workflow (workflows -> projects -> workspaces).
func (r *WorkflowVersionRepository) WorkspaceOf(ctx context.Context, workflowID uuid.UUID) (uuid.UUID, error) {
	var ws uuid.UUID
	err := r.pool.QueryRow(ctx, `
SELECT p.workspace_id FROM workflows w JOIN projects p ON p.id = w.project_id WHERE w.id = $1`, workflowID).Scan(&ws)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("%w: %s", execution.ErrWorkflowNotFound, workflowID)
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("find workspace of workflow %s: %w", workflowID, err)
	}
	return ws, nil
}

// LoadDefinition decodes the version's canonical workflow definition. The
// database guarantees the definition cannot change once the version is
// published or referenced by an execution (migration 000002).
func (r *WorkflowVersionRepository) LoadDefinition(ctx context.Context, versionID uuid.UUID) (workflow.Definition, error) {
	v, err := r.FindByID(ctx, versionID)
	if err != nil {
		return workflow.Definition{}, err
	}
	var def workflow.Definition
	if err := json.Unmarshal(v.Definition, &def); err != nil {
		return workflow.Definition{}, fmt.Errorf("%w: decode workflow version %s definition: %v", execution.ErrInvalidWorkflowDefinition, versionID, err)
	}
	return def, nil
}
