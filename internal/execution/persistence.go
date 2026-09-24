package execution

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// IDs follow the repository-wide convention of uuid.UUID for durable records.

// Execution is one durable run of an exact, immutable workflow version.
//
// It is a read snapshot. Its Status changes only through ExecutionStateMachine
// (backed by ExecutionRepository.Transition, a conditional update); assigning
// the field on a value has no persistence path, and repositories expose no
// generic update/save method.
type Execution struct {
	ID                uuid.UUID
	WorkflowID        uuid.UUID
	WorkflowVersionID uuid.UUID

	Status ExecutionStatus

	Input  map[string]any
	Output map[string]any
	Error  *ExecutionError

	CreatedAt  time.Time
	UpdatedAt  time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
}

// NodeExecution records the execution of one workflow node inside an
// Execution. NodeType is required by the existing node_executions schema.
type NodeExecution struct {
	ID          uuid.UUID
	ExecutionID uuid.UUID
	NodeID      string
	NodeType    string

	Status NodeExecutionStatus

	Input  map[string]any
	Output map[string]any
	Error  *ExecutionError

	CreatedAt  time.Time
	UpdatedAt  time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
}

// StatusTransition is one append-only execution_status_history record.
// From is nil for the creation record (NULL -> PENDING).
type StatusTransition struct {
	ID          uuid.UUID
	ExecutionID uuid.UUID
	From        *ExecutionStatus
	To          ExecutionStatus
	CreatedAt   time.Time
	Metadata    map[string]any
}

// TransitionUpdate carries the data written atomically with a status change.
// Output is only accepted for COMPLETED and Error is required for FAILED.
type TransitionUpdate struct {
	Output   map[string]any
	Error    *ExecutionError
	Metadata map[string]any // stored on the history record
}

// NodeTransitionUpdate carries the data written atomically with a node status
// change: Input on RUNNING, Output on COMPLETED, Error on FAILED.
type NodeTransitionUpdate struct {
	Input  map[string]any
	Output map[string]any
	Error  *ExecutionError
}

// ExecutionRepository is the persistence boundary for executions.
type ExecutionRepository interface {
	// Create inserts a PENDING execution together with its creation history
	// record. Any other status is rejected.
	Create(ctx context.Context, execution Execution) error
	// Get returns ErrExecutionNotFound when the execution does not exist.
	Get(ctx context.Context, id uuid.UUID) (Execution, error)
	// Transition atomically moves the execution from -> to only if it is
	// currently in from, applies lifecycle timestamps and update, and appends
	// the history record in the same statement. It returns
	// ErrTransitionConflict when no row matched.
	Transition(ctx context.Context, id uuid.UUID, from, to ExecutionStatus, update TransitionUpdate) error
	// History returns the execution's status history, oldest first.
	History(ctx context.Context, id uuid.UUID) ([]StatusTransition, error)
}

// NodeExecutionRepository is the persistence boundary for node executions.
type NodeExecutionRepository interface {
	// Create inserts a PENDING node execution owned by an existing execution.
	Create(ctx context.Context, execution NodeExecution) error
	// Get returns ErrNodeExecutionNotFound when the record does not exist.
	Get(ctx context.Context, id uuid.UUID) (NodeExecution, error)
	// Transition is the node-level conditional update; see ExecutionRepository.
	Transition(ctx context.Context, id uuid.UUID, from, to NodeExecutionStatus, update NodeTransitionUpdate) error
	// ListByExecution returns an execution's node records, oldest first.
	ListByExecution(ctx context.Context, executionID uuid.UUID) ([]NodeExecution, error)
}
