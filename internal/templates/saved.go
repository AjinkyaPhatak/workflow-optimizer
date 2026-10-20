package templates

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/workflow"
)

// ErrSavedNotFound reports an unknown saved template (or one of another
// workspace).
var ErrSavedNotFound = errors.New("saved template not found")

// SavedCategory is the gallery category of templates people save.
const SavedCategory = "Saved"

// Saved is a template saved from a workflow (Phase K, "Save as template"):
// a copy of the workflow's definition, shared with the workspace.
type Saved struct {
	ID          uuid.UUID
	WorkspaceID uuid.UUID
	Name        string
	Description string
	Definition  workflow.Definition
	CreatedBy   uuid.UUID
	CreatedAt   time.Time
}

// SavedRepository stores saved templates. Reads and deletes are scoped to a
// workspace, except Locate (used to authorize by the template's workspace).
type SavedRepository interface {
	Create(ctx context.Context, s Saved) (Saved, error)
	List(ctx context.Context, workspaceID uuid.UUID) ([]Saved, error)
	Locate(ctx context.Context, id uuid.UUID) (Saved, error)
	Delete(ctx context.Context, workspaceID, id uuid.UUID) error
}
