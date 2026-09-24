package queue

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"workflow-optimizer/internal/execution"
)

var errNilDependency = errors.New("queue: nil dependency")

// ExecutionDispatcher hands a persisted execution to the background workers.
type ExecutionDispatcher interface {
	Dispatch(ctx context.Context, executionID uuid.UUID) error
}

// QueueDispatcher dispatches by enqueueing a Job that carries only the
// execution's identity.
type QueueDispatcher struct {
	queue JobQueue
}

var _ ExecutionDispatcher = (*QueueDispatcher)(nil)

// NewDispatcher returns a dispatcher over q.
func NewDispatcher(q JobQueue) (*QueueDispatcher, error) {
	if q == nil {
		return nil, fmt.Errorf("%w: job queue", errNilDependency)
	}
	return &QueueDispatcher{queue: q}, nil
}

// Dispatch enqueues a job for the execution. Dispatching the same execution
// twice is safe: only one worker can claim it.
func (d *QueueDispatcher) Dispatch(ctx context.Context, executionID uuid.UUID) error {
	return d.queue.Enqueue(ctx, Job{ExecutionID: executionID})
}

// DispatchError reports that an execution was persisted (PENDING) but could
// not be handed to the queue. The execution is not lost: it can be
// dispatched again with its ID. It unwraps to the queue error.
type DispatchError struct {
	ExecutionID uuid.UUID
	Err         error
}

func (e *DispatchError) Error() string {
	return fmt.Sprintf("execution %s was created PENDING but could not be dispatched: %v", e.ExecutionID, e.Err)
}

func (e *DispatchError) Unwrap() error { return e.Err }

// Submitter is the request-path entry point for asynchronous execution:
// persist the execution as PENDING (Phase 8), then dispatch it. It never
// executes the workflow itself.
type Submitter struct {
	executions execution.ExecutionService
	dispatcher ExecutionDispatcher
}

// NewSubmitter wires the lifecycle service and the dispatcher.
func NewSubmitter(executions execution.ExecutionService, dispatcher ExecutionDispatcher) (*Submitter, error) {
	if executions == nil || dispatcher == nil {
		return nil, fmt.Errorf("%w: execution service and dispatcher are required", errNilDependency)
	}
	return &Submitter{executions: executions, dispatcher: dispatcher}, nil
}

// Submit creates the execution (PENDING) and enqueues it. If creation fails,
// nothing is enqueued. If enqueueing fails after the execution was persisted,
// the created execution is returned together with a *DispatchError, so the
// caller knows the ID and can dispatch it again.
func (s *Submitter) Submit(ctx context.Context, workflowID, versionID uuid.UUID, input map[string]any) (execution.Execution, error) {
	created, err := s.executions.Create(ctx, workflowID, versionID, input)
	if err != nil {
		return execution.Execution{}, err
	}
	if err := s.dispatcher.Dispatch(ctx, created.ID); err != nil {
		return created, &DispatchError{ExecutionID: created.ID, Err: err}
	}
	return created, nil
}
