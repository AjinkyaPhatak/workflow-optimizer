package execution

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Phase 14: execution observability.
//
// Observability describes execution; it never controls it. The lifecycle
// reports what happened to an ExecutionObserver only AFTER the corresponding
// durable state change succeeded (the execution row and node records stay
// the source of truth). Observer methods return nothing: an observer that
// cannot record an event (database down) must not fail, retry or otherwise
// alter the execution.

// EventType is an execution event kind.
type EventType string

const (
	EventExecutionStarted   EventType = "EXECUTION_STARTED"
	EventExecutionCompleted EventType = "EXECUTION_COMPLETED"
	EventExecutionFailed    EventType = "EXECUTION_FAILED"
	EventExecutionCancelled EventType = "EXECUTION_CANCELLED"
	EventNodeStarted        EventType = "NODE_STARTED"
	EventNodeCompleted      EventType = "NODE_COMPLETED"
	EventNodeFailed         EventType = "NODE_FAILED"
	// EventNodeSkipped: the node did not run in this attempt because it
	// already completed in an earlier attempt (its output is reused).
	EventNodeSkipped EventType = "NODE_SKIPPED"
	// EventRetryScheduled / EventRetryStarted describe both retry levels:
	// with a node ID, an in-place node retry; without, an execution retry
	// (a new attempt).
	EventRetryScheduled EventType = "RETRY_SCHEDULED"
	EventRetryStarted   EventType = "RETRY_STARTED"
)

// EventTypes lists every event type.
var EventTypes = []EventType{EventExecutionStarted, EventExecutionCompleted, EventExecutionFailed, EventExecutionCancelled,
	EventNodeStarted, EventNodeCompleted, EventNodeFailed, EventNodeSkipped, EventRetryScheduled, EventRetryStarted}

// ExecutionEvent is one append-only historical record. Data holds only
// identifiers, codes, counters and durations: never node inputs/outputs,
// provider payloads or credentials.
type ExecutionEvent struct {
	ID          uuid.UUID
	ExecutionID uuid.UUID
	NodeID      *string
	Type        EventType
	// Timestamp is assigned by the store when the event is appended (one
	// clock for all processes), so ordering is deterministic.
	Timestamp time.Time
	Data      map[string]any
}

// EventQuery selects one page of an execution's events, oldest first.
type EventQuery struct {
	Page     int // 1-based
	PageSize int
}

// ExecutionEventRepository stores events. It is append-only: there is no
// update or delete operation.
type ExecutionEventRepository interface {
	// Append stores the event and returns it with its ID and timestamp.
	Append(ctx context.Context, event ExecutionEvent) (ExecutionEvent, error)
	// ListByExecution returns one page in chronological order, and the total.
	ListByExecution(ctx context.Context, executionID uuid.UUID, q EventQuery) ([]ExecutionEvent, int, error)
}

// NodeRef identifies one invocation of a workflow node.
type NodeRef struct {
	ID   string
	Type string
	// Invocation counts the node's invocations across the execution's
	// attempts (1 = first).
	Invocation int
}

// ExecutionObserver receives lifecycle facts after they are durable.
type ExecutionObserver interface {
	// ExecutionStarted: attempt 1 was claimed. queued is the time it waited
	// between creation and the claim.
	ExecutionStarted(ctx context.Context, executionID uuid.UUID, attempt, maxAttempts int, queued time.Duration)
	ExecutionCompleted(ctx context.Context, executionID uuid.UUID, attempt int, ran time.Duration)
	ExecutionFailed(ctx context.Context, executionID uuid.UUID, attempt int, err ExecutionError, deadLetter DeadLetterReason)
	ExecutionCancelled(ctx context.Context, executionID uuid.UUID, attempt int, reason string)

	NodeStarted(ctx context.Context, executionID uuid.UUID, n NodeRef)
	NodeCompleted(ctx context.Context, executionID uuid.UUID, n NodeRef, ran time.Duration)
	NodeFailed(ctx context.Context, executionID uuid.UUID, n NodeRef, err ExecutionError, ran time.Duration)
	NodeSkipped(ctx context.Context, executionID uuid.UUID, nodeID, reason string)

	// RetryScheduled: nodeID nil = a new execution attempt is scheduled after
	// delay; otherwise the node runs again in place after delay.
	RetryScheduled(ctx context.Context, executionID uuid.UUID, nodeID *string, attempt int, delay time.Duration, cause ExecutionError)
	// RetryStarted: nodeID nil = attempt (> 1) was claimed; otherwise the
	// node's next in-place invocation begins.
	RetryStarted(ctx context.Context, executionID uuid.UUID, nodeID *string, attempt int)
}

// NopObserver ignores everything (the default).
type NopObserver struct{}

var _ ExecutionObserver = NopObserver{}

func (NopObserver) ExecutionStarted(context.Context, uuid.UUID, int, int, time.Duration) {}
func (NopObserver) ExecutionCompleted(context.Context, uuid.UUID, int, time.Duration)    {}
func (NopObserver) ExecutionFailed(context.Context, uuid.UUID, int, ExecutionError, DeadLetterReason) {
}
func (NopObserver) ExecutionCancelled(context.Context, uuid.UUID, int, string)                    {}
func (NopObserver) NodeStarted(context.Context, uuid.UUID, NodeRef)                               {}
func (NopObserver) NodeCompleted(context.Context, uuid.UUID, NodeRef, time.Duration)              {}
func (NopObserver) NodeFailed(context.Context, uuid.UUID, NodeRef, ExecutionError, time.Duration) {}
func (NopObserver) NodeSkipped(context.Context, uuid.UUID, string, string)                        {}
func (NopObserver) RetryScheduled(context.Context, uuid.UUID, *string, int, time.Duration, ExecutionError) {
}
func (NopObserver) RetryStarted(context.Context, uuid.UUID, *string, int) {}

func observerOrNop(o ExecutionObserver) ExecutionObserver {
	if o == nil {
		return NopObserver{}
	}
	return o
}
