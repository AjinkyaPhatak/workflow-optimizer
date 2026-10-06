package execution

import (
	"errors"
	"fmt"
	"time"

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
	// ErrWorkflowVersionNotExecutable rejects executions of versions that are
	// not PUBLISHED (drafts are mutable; archived versions are retired).
	ErrWorkflowVersionNotExecutable = errors.New("execution: workflow version is not published")
	// ErrInvalidWorkflowDefinition marks a stored definition that cannot be
	// decoded (as opposed to a transient failure to load it).
	ErrInvalidWorkflowDefinition = errors.New("execution: stored workflow definition is invalid")

	// ErrExecutionNotRunning rejects node writes whose parent execution is not
	// RUNNING (for example because another process cancelled it).
	ErrExecutionNotRunning = errors.New("execution: execution is not running")
	// ErrExecutionCancelled is matched when an execution was cancelled by
	// another actor while this worker was running it.
	ErrExecutionCancelled = errors.New("execution: execution was cancelled")
	// ErrExecutionHasRunningNodes rejects completing an execution that still
	// has RUNNING node records.
	ErrExecutionHasRunningNodes = errors.New("execution: execution has running node executions")

	// ErrPersistenceTimeout: the database gave up (lock or statement timeout)
	// before the client did, so nothing was written.
	ErrPersistenceTimeout = errors.New("execution: database operation timed out; nothing was written")
	// ErrConcurrentUpdate: the database aborted the write because of a
	// serialization failure or deadlock; nothing was written.
	ErrConcurrentUpdate = errors.New("execution: concurrent update conflict; nothing was written")
	// ErrWriteNotApplied: the connection failed mid-write, the database side
	// was fenced, and the write is known not to have been committed.
	ErrWriteNotApplied = errors.New("execution: write was not applied")
	// ErrOutcomeUnknown: the connection failed mid-write and the database could
	// not be reached to establish whether the write committed. The record may
	// be in either state; ownership is identifiable through its claim token.
	ErrOutcomeUnknown = errors.New("execution: write outcome unknown")

	// Phase 10.

	// ErrRetryNotDue rejects claiming a PENDING execution whose scheduled
	// retry is not due yet.
	ErrRetryNotDue = errors.New("execution: scheduled retry is not due yet")
	// ErrAttemptsExhausted rejects claiming or rescheduling an execution that
	// has used all its attempts.
	ErrAttemptsExhausted = errors.New("execution: no attempts left")
	// ErrRetryAfterDeadline rejects a retry that would start after the
	// execution deadline.
	ErrRetryAfterDeadline = errors.New("execution: retry would start after the execution deadline")
	// ErrCancelRequested rejects rescheduling an execution whose cancellation
	// was requested, and is the cancellation cause of a run whose
	// cancellation was requested while it ran.
	ErrCancelRequested = errors.New("execution: cancellation requested")
	// ErrLeaseLost: this worker no longer owns the attempt (its lease expired
	// and the attempt may have been recovered, or another actor finalized the
	// execution). A worker that lost its lease writes nothing more.
	ErrLeaseLost = errors.New("execution: ownership of the attempt was lost")
)

// RetryScheduledError reports that a failed attempt was not the end: the
// execution is PENDING again with a persisted retry schedule. It unwraps to
// the attempt's failure.
type RetryScheduledError struct {
	ExecutionID uuid.UUID
	Attempt     int
	Delay       time.Duration
	Err         error
}

func (e *RetryScheduledError) Error() string {
	return fmt.Sprintf("execution %s attempt %d failed; retry scheduled in %s: %v", e.ExecutionID, e.Attempt, e.Delay, e.Err)
}

func (e *RetryScheduledError) Unwrap() error { return e.Err }

// ExternallyFinalizedError reports that the execution reached a terminal
// status through another actor (e.g. an operator's Cancel) while this worker
// was running it. It matches ErrExecutionNotRunning, and ErrExecutionCancelled
// when that status is CANCELLED.
type ExternallyFinalizedError struct {
	ExecutionID uuid.UUID
	Status      ExecutionStatus
	Err         error
}

func (e *ExternallyFinalizedError) Error() string {
	return fmt.Sprintf("execution %s was finalized as %s by another actor: %v", e.ExecutionID, e.Status, e.Err)
}

func (e *ExternallyFinalizedError) Unwrap() error { return e.Err }

func (e *ExternallyFinalizedError) Is(target error) bool {
	return target == ErrExecutionNotRunning || (target == ErrExecutionCancelled && e.Status == StatusCancelled)
}

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

// DeadLetteredError reports that a retryable failure stopped being retried
// and the execution was dead-lettered (FAILED, with an authoritative
// dead-letter record written in the same transaction). It unwraps to the
// attempt's failure.
type DeadLetteredError struct {
	ExecutionID uuid.UUID
	Attempt     int
	Reason      DeadLetterReason
	Err         error
}

func (e *DeadLetteredError) Error() string {
	return fmt.Sprintf("execution %s dead-lettered after attempt %d (%s): %v", e.ExecutionID, e.Attempt, e.Reason, e.Err)
}

func (e *DeadLetteredError) Unwrap() error { return e.Err }
