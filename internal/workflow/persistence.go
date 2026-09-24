package workflow

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// VersionStatus is a workflow-version lifecycle label.
type VersionStatus string

const (
	VersionStatusDraft     VersionStatus = "DRAFT"
	VersionStatusPublished VersionStatus = "PUBLISHED"
	VersionStatusArchived  VersionStatus = "ARCHIVED"
)

// Project is the durable representation of a workspace project.
type Project struct {
	ID          uuid.UUID
	WorkspaceID uuid.UUID
	Name        string
	Description *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Workflow is the durable workflow record. ActiveVersionID may be absent.
type Workflow struct {
	ID              uuid.UUID
	ProjectID       uuid.UUID
	Name            string
	Description     *string
	ActiveVersionID *uuid.UUID
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Version is an immutable workflow-definition snapshot once published.
type Version struct {
	ID            uuid.UUID
	WorkflowID    uuid.UUID
	VersionNumber int
	Definition    json.RawMessage
	Status        VersionStatus
	CreatedBy     *uuid.UUID
	CreatedAt     time.Time
	PublishedAt   *time.Time
}

// ProjectRepository is the persistence boundary for projects.
type ProjectRepository interface {
	FindByID(context.Context, uuid.UUID) (Project, error)
}

// Repository is the persistence boundary for workflows.
type Repository interface {
	FindByID(context.Context, uuid.UUID) (Workflow, error)
}

// VersionRepository is the persistence boundary for workflow versions.
type VersionRepository interface {
	FindByID(context.Context, uuid.UUID) (Version, error)
	FindByWorkflowAndNumber(context.Context, uuid.UUID, int) (Version, error)
}
