package execution

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// ExecutionStateMachine is the only component that changes an execution's
// status. All lifecycle rules live in status.go and are enforced here and,
// independently, by the database triggers of migration 000002.
type ExecutionStateMachine interface {
	Transition(ctx context.Context, executionID uuid.UUID, target ExecutionStatus, update TransitionUpdate) error
}

// NodeExecutionStateMachine is the node-level counterpart.
type NodeExecutionStateMachine interface {
	// Begin records that a node started: PENDING -> RUNNING atomically.
	Begin(ctx context.Context, record NodeExecution, input map[string]any) error
	Transition(ctx context.Context, nodeExecutionID uuid.UUID, target NodeExecutionStatus, update NodeTransitionUpdate) error
}

type executionStateMachine struct {
	repo ExecutionRepository
	opts PersistenceOptions
}

// NewExecutionStateMachine returns the persisted execution state machine.
func NewExecutionStateMachine(repo ExecutionRepository) ExecutionStateMachine {
	return NewExecutionStateMachineWithOptions(repo, PersistenceOptions{})
}

// NewExecutionStateMachineWithOptions configures the write timeout.
func NewExecutionStateMachineWithOptions(repo ExecutionRepository, opts PersistenceOptions) ExecutionStateMachine {
	return &executionStateMachine{repo: repo, opts: opts}
}

// Transition validates target and payload, then performs one atomic
// compare-and-set from target's unique legal source status. The guard is
// inside the database write (UPDATE ... WHERE status = source), so there is no
// read-then-write race; the current state is loaded only after a rejected
// write, to report what the execution actually was.
//
// The write runs on a context detached from the caller's cancellation but
// bounded by the configured write timeout: once issued, a transition is
// always resolved (applied, not applied, or ErrOutcomeUnknown), never
// abandoned half-way by a cancelled caller.
func (m *executionStateMachine) Transition(ctx context.Context, id uuid.UUID, target ExecutionStatus, update TransitionUpdate) error {
	from, ok := sourceStatus(target)
	if !ok {
		return &TransitionError{ExecutionID: id, To: target}
	}
	if err := validateExecutionUpdate(target, update); err != nil {
		return err
	}
	if update.TransitionID == uuid.Nil {
		update.TransitionID = uuid.New()
	}
	wctx, cancel := persistContext(ctx, m.opts)
	defer cancel()
	err := m.repo.Transition(wctx, id, from, target, update)
	if err == nil || (!errors.Is(err, ErrTransitionConflict) && !errors.Is(err, ErrConcurrentUpdate)) {
		return err
	}
	rctx, rcancel := persistContext(ctx, m.opts)
	defer rcancel()
	current, getErr := m.repo.Get(rctx, id)
	if getErr != nil {
		return getErr
	}
	if current.Status == from {
		// Nobody else moved the execution: the write failed for a database
		// reason (e.g. deadlock victim), which must not be hidden.
		return err
	}
	return &TransitionError{ExecutionID: id, From: current.Status, To: target}
}

func validateExecutionUpdate(target ExecutionStatus, u TransitionUpdate) error {
	if u.Output != nil && target != StatusCompleted {
		return fmt.Errorf("%w: output may only be written on %s", ErrInvalidExecution, StatusCompleted)
	}
	if (target == StatusRunning) != (u.ClaimToken != uuid.Nil) {
		return fmt.Errorf("%w: a claim token is required for, and only for, %s", ErrInvalidExecution, StatusRunning)
	}
	if u.InterruptedNodeError != nil {
		if target != StatusFailed && target != StatusCancelled {
			return fmt.Errorf("%w: interrupted-node errors apply only to %s or %s", ErrInvalidExecution, StatusFailed, StatusCancelled)
		}
		if err := u.InterruptedNodeError.Validate(); err != nil {
			return err
		}
	}
	if target == StatusFailed {
		if u.Error == nil {
			return fmt.Errorf("%w: %s requires an execution error", ErrInvalidExecution, StatusFailed)
		}
		if err := u.Error.Validate(); err != nil {
			return err
		}
	} else if u.Error != nil && target != StatusCancelled {
		return fmt.Errorf("%w: error may only be written on %s or %s", ErrInvalidExecution, StatusFailed, StatusCancelled)
	}
	return nil
}

type nodeExecutionStateMachine struct {
	repo NodeExecutionRepository
	opts PersistenceOptions
}

// NewNodeExecutionStateMachine returns the persisted node state machine.
func NewNodeExecutionStateMachine(repo NodeExecutionRepository) NodeExecutionStateMachine {
	return NewNodeExecutionStateMachineWithOptions(repo, PersistenceOptions{})
}

// NewNodeExecutionStateMachineWithOptions configures the write timeout.
func NewNodeExecutionStateMachineWithOptions(repo NodeExecutionRepository, opts PersistenceOptions) NodeExecutionStateMachine {
	return &nodeExecutionStateMachine{repo: repo, opts: opts}
}

func (m *nodeExecutionStateMachine) Begin(ctx context.Context, record NodeExecution, input map[string]any) error {
	if record.Status != NodeStatusPending {
		return fmt.Errorf("%w: node executions begin from %s", ErrInvalidExecution, NodeStatusPending)
	}
	wctx, cancel := persistContext(ctx, m.opts)
	defer cancel()
	return m.repo.Begin(wctx, record, input)
}

func (m *nodeExecutionStateMachine) Transition(ctx context.Context, id uuid.UUID, target NodeExecutionStatus, update NodeTransitionUpdate) error {
	from, ok := nodeSourceStatus(target)
	if !ok {
		return &NodeTransitionError{NodeExecutionID: id, To: target}
	}
	if update.Input != nil && target != NodeStatusRunning {
		return fmt.Errorf("%w: node input may only be written on %s", ErrInvalidExecution, NodeStatusRunning)
	}
	if update.Output != nil && target != NodeStatusCompleted {
		return fmt.Errorf("%w: node output may only be written on %s", ErrInvalidExecution, NodeStatusCompleted)
	}
	if target == NodeStatusFailed {
		if update.Error == nil {
			return fmt.Errorf("%w: %s requires an execution error", ErrInvalidExecution, NodeStatusFailed)
		}
		if err := update.Error.Validate(); err != nil {
			return err
		}
	} else if update.Error != nil {
		return fmt.Errorf("%w: node error may only be written on %s", ErrInvalidExecution, NodeStatusFailed)
	}
	wctx, cancel := persistContext(ctx, m.opts)
	defer cancel()
	err := m.repo.Transition(wctx, id, from, target, update)
	if err == nil || (!errors.Is(err, ErrTransitionConflict) && !errors.Is(err, ErrConcurrentUpdate)) {
		return err
	}
	rctx, rcancel := persistContext(ctx, m.opts)
	defer rcancel()
	current, getErr := m.repo.Get(rctx, id)
	if getErr != nil {
		return getErr
	}
	if current.Status == from {
		return err
	}
	return &NodeTransitionError{NodeExecutionID: id, From: current.Status, To: target}
}
