package execution

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// Lifecycle (Phase 8) errors.
var (
	ErrExecutionNotFound     = errors.New("execution: execution not found")
	ErrNodeExecutionNotFound = errors.New("execution: node execution not found")
	// ErrInvalidTransition is matched by every rejected lifecycle transition.
	ErrInvalidTransition = errors.New("execution: invalid state transition")
	// ErrExecutionNotClaimable is additionally matched when a Start (claim)
	// loses because the execution is no longer PENDING, e.g. another worker
	// claimed it first.
	ErrExecutionNotClaimable = errors.New("execution: execution is not claimable")
	// ErrTransitionConflict is returned by repositories when the conditional
	// update matched no row: the record is missing or not in the expected
	// status. The state machine converts it into a TransitionError.
	ErrTransitionConflict = errors.New("execution: conditional transition matched no row")
	// ErrInvalidExecution rejects malformed entities or transition payloads.
	ErrInvalidExecution = errors.New("execution: invalid execution data")
	// ErrWorkflowNotFound / ErrWorkflowVersionNotFound / ErrWorkflowVersionMismatch
	// report referential failures detected by the database on Create.
	ErrWorkflowNotFound        = errors.New("execution: workflow not found")
	ErrWorkflowVersionNotFound = errors.New("execution: workflow version not found")
	ErrWorkflowVersionMismatch = errors.New("execution: workflow version belongs to a different workflow")
)

// TransitionError describes a rejected execution transition.
type TransitionError struct {
	ExecutionID uuid.UUID
	// From is the status the record was actually in ("" when unknown).
	From ExecutionStatus
	To   ExecutionStatus
}

func (e *TransitionError) Error() string {
	if e.From == "" {
		return fmt.Sprintf("execution %s: transition to %s is not allowed", e.ExecutionID, e.To)
	}
	return fmt.Sprintf("execution %s: invalid transition %s -> %s", e.ExecutionID, e.From, e.To)
}

// Is matches ErrInvalidTransition, and ErrExecutionNotClaimable for claims.
func (e *TransitionError) Is(target error) bool {
	return target == ErrInvalidTransition || (target == ErrExecutionNotClaimable && e.To == StatusRunning)
}

// NodeTransitionError describes a rejected node execution transition.
type NodeTransitionError struct {
	NodeExecutionID uuid.UUID
	From            NodeExecutionStatus
	To              NodeExecutionStatus
}

func (e *NodeTransitionError) Error() string {
	if e.From == "" {
		return fmt.Sprintf("node execution %s: transition to %s is not allowed", e.NodeExecutionID, e.To)
	}
	return fmt.Sprintf("node execution %s: invalid transition %s -> %s", e.NodeExecutionID, e.From, e.To)
}

func (e *NodeTransitionError) Is(target error) bool { return target == ErrInvalidTransition }
