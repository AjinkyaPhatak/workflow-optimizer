package execution

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// ExecutionService coordinates the execution lifecycle. It owns no graph
// traversal; see Runner for the integration with GraphExecutor.
type ExecutionService interface {
	// Create persists a new PENDING execution and nothing else.
	Create(ctx context.Context, workflowID, versionID uuid.UUID, input map[string]any) (Execution, error)
	// Start atomically claims a PENDING execution (PENDING -> RUNNING). Exactly
	// one concurrent caller succeeds; the others get ErrExecutionNotClaimable.
	Start(ctx context.Context, executionID uuid.UUID) error
	Complete(ctx context.Context, executionID uuid.UUID, output map[string]any) error
	Fail(ctx context.Context, executionID uuid.UUID, executionError ExecutionError) error
	Cancel(ctx context.Context, executionID uuid.UUID) error
}

// LifecycleService is the persisted ExecutionService.
type LifecycleService struct {
	repo    ExecutionRepository
	machine ExecutionStateMachine
	opts    PersistenceOptions
}

var _ ExecutionService = (*LifecycleService)(nil)

// NewLifecycleService wires the service to its repository and state machine.
func NewLifecycleService(repo ExecutionRepository) *LifecycleService {
	return NewLifecycleServiceWithOptions(repo, PersistenceOptions{})
}

// NewLifecycleServiceWithOptions configures the persistence write timeout.
func NewLifecycleServiceWithOptions(repo ExecutionRepository, opts PersistenceOptions) *LifecycleService {
	return &LifecycleService{repo: repo, machine: NewExecutionStateMachineWithOptions(repo, opts), opts: opts}
}

// Default errors stamped on nodes that are still RUNNING when an execution is
// failed or cancelled; the node really started, so it is never shown as
// "never ran", and its outcome is explicitly unknown.
var (
	interruptedByFailure = ExecutionError{Code: CodeNodeInterrupted,
		Message: "execution failed while this node was running; the node's outcome was not recorded"}
	interruptedByCancel = ExecutionError{Code: CodeCancelled,
		Message: "execution was cancelled while this node was running; the node's outcome was not recorded"}
)

// Create refuses to start a write for an already-cancelled caller; once
// issued, the insert is resolved on a bounded, detached context and the
// persisted row is returned from the same statement (no separate read that
// could fail after a successful insert).
func (s *LifecycleService) Create(ctx context.Context, workflowID, versionID uuid.UUID, input map[string]any) (Execution, error) {
	if workflowID == uuid.Nil || versionID == uuid.Nil {
		return Execution{}, fmt.Errorf("%w: workflow and workflow version IDs are required", ErrInvalidExecution)
	}
	if err := ctx.Err(); err != nil {
		return Execution{}, fmt.Errorf("create not attempted: %w", err)
	}
	wctx, cancel := persistContext(ctx, s.opts)
	defer cancel()
	return s.repo.Create(wctx, Execution{
		ID:                uuid.New(),
		WorkflowID:        workflowID,
		WorkflowVersionID: versionID,
		Status:            StatusPending,
		Input:             input,
	})
}

// Start claims the execution under a fresh claim token. A caller whose
// context is already done never issues a claim; a claim that was issued is
// always resolved, so a caller is never told "not claimed" for a claim the
// database committed.
func (s *LifecycleService) Start(ctx context.Context, id uuid.UUID) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("claim not attempted: %w", err)
	}
	return s.machine.Transition(ctx, id, StatusRunning, TransitionUpdate{ClaimToken: uuid.New()})
}

func (s *LifecycleService) Complete(ctx context.Context, id uuid.UUID, output map[string]any) error {
	if output == nil {
		output = map[string]any{}
	}
	return s.machine.Transition(ctx, id, StatusCompleted, TransitionUpdate{Output: output})
}

func (s *LifecycleService) Fail(ctx context.Context, id uuid.UUID, executionError ExecutionError) error {
	return s.fail(ctx, id, executionError, interruptedByFailure)
}

func (s *LifecycleService) fail(ctx context.Context, id uuid.UUID, executionError ExecutionError, interrupted ExecutionError) error {
	meta := map[string]any{"error_code": executionError.Code}
	if executionError.NodeID != nil {
		meta["node_id"] = *executionError.NodeID
	}
	return s.machine.Transition(ctx, id, StatusFailed, TransitionUpdate{
		Error: &executionError, Metadata: meta, InterruptedNodeError: &interrupted,
	})
}

func (s *LifecycleService) Cancel(ctx context.Context, id uuid.UUID) error {
	return s.cancel(ctx, id, map[string]any{"reason": "cancelled"})
}

func (s *LifecycleService) cancel(ctx context.Context, id uuid.UUID, meta map[string]any) error {
	interrupted := interruptedByCancel
	return s.machine.Transition(ctx, id, StatusCancelled, TransitionUpdate{Metadata: meta, InterruptedNodeError: &interrupted})
}
