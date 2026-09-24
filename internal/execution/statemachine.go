package execution

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// ExecutionStateMachine is the only component that changes an execution's
// status. All lifecycle rules live in status.go and are enforced here (and,
// independently, by database triggers).
type ExecutionStateMachine interface {
	Transition(ctx context.Context, executionID uuid.UUID, target ExecutionStatus, update TransitionUpdate) error
}

// NodeExecutionStateMachine is the node-level counterpart.
type NodeExecutionStateMachine interface {
	Transition(ctx context.Context, nodeExecutionID uuid.UUID, target NodeExecutionStatus, update NodeTransitionUpdate) error
}

type executionStateMachine struct {
	repo ExecutionRepository
}

// NewExecutionStateMachine returns the persisted execution state machine.
func NewExecutionStateMachine(repo ExecutionRepository) ExecutionStateMachine {
	return &executionStateMachine{repo: repo}
}

// Transition validates target and payload, then performs one atomic
// compare-and-set from target's unique legal source status. Because the
// guard is inside the UPDATE ... WHERE status = source, there is no
// read-then-write race; the current state is loaded only after a rejected
// compare-and-set, to report what the execution actually was.
func (m *executionStateMachine) Transition(ctx context.Context, id uuid.UUID, target ExecutionStatus, update TransitionUpdate) error {
	from, ok := sourceStatus(target)
	if !ok {
		return &TransitionError{ExecutionID: id, To: target}
	}
	if err := validateExecutionUpdate(target, update); err != nil {
		return err
	}
	err := m.repo.Transition(ctx, id, from, target, update)
	if !errors.Is(err, ErrTransitionConflict) {
		return err
	}
	current, getErr := m.repo.Get(ctx, id)
	if getErr != nil {
		return getErr
	}
	return &TransitionError{ExecutionID: id, From: current.Status, To: target}
}

func validateExecutionUpdate(target ExecutionStatus, u TransitionUpdate) error {
	if u.Output != nil && target != StatusCompleted {
		return fmt.Errorf("%w: output may only be written on %s", ErrInvalidExecution, StatusCompleted)
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
}

// NewNodeExecutionStateMachine returns the persisted node state machine.
func NewNodeExecutionStateMachine(repo NodeExecutionRepository) NodeExecutionStateMachine {
	return &nodeExecutionStateMachine{repo: repo}
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
	err := m.repo.Transition(ctx, id, from, target, update)
	if !errors.Is(err, ErrTransitionConflict) {
		return err
	}
	current, getErr := m.repo.Get(ctx, id)
	if getErr != nil {
		return getErr
	}
	return &NodeTransitionError{NodeExecutionID: id, From: current.Status, To: target}
}
