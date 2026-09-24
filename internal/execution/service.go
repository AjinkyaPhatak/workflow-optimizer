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
}

var _ ExecutionService = (*LifecycleService)(nil)

// NewLifecycleService wires the service to its repository and state machine.
func NewLifecycleService(repo ExecutionRepository) *LifecycleService {
	return &LifecycleService{repo: repo, machine: NewExecutionStateMachine(repo)}
}

func (s *LifecycleService) Create(ctx context.Context, workflowID, versionID uuid.UUID, input map[string]any) (Execution, error) {
	if workflowID == uuid.Nil || versionID == uuid.Nil {
		return Execution{}, fmt.Errorf("%w: workflow and workflow version IDs are required", ErrInvalidExecution)
	}
	e := Execution{
		ID:                uuid.New(),
		WorkflowID:        workflowID,
		WorkflowVersionID: versionID,
		Status:            StatusPending,
		Input:             input,
	}
	if err := s.repo.Create(ctx, e); err != nil {
		return Execution{}, err
	}
	return s.repo.Get(ctx, e.ID)
}

func (s *LifecycleService) Start(ctx context.Context, id uuid.UUID) error {
	return s.machine.Transition(ctx, id, StatusRunning, TransitionUpdate{})
}

func (s *LifecycleService) Complete(ctx context.Context, id uuid.UUID, output map[string]any) error {
	if output == nil {
		output = map[string]any{}
	}
	return s.machine.Transition(ctx, id, StatusCompleted, TransitionUpdate{Output: output})
}

func (s *LifecycleService) Fail(ctx context.Context, id uuid.UUID, executionError ExecutionError) error {
	meta := map[string]any{"error_code": executionError.Code}
	if executionError.NodeID != nil {
		meta["node_id"] = *executionError.NodeID
	}
	return s.machine.Transition(ctx, id, StatusFailed, TransitionUpdate{Error: &executionError, Metadata: meta})
}

func (s *LifecycleService) Cancel(ctx context.Context, id uuid.UUID) error {
	return s.machine.Transition(ctx, id, StatusCancelled, TransitionUpdate{Metadata: map[string]any{"reason": "cancelled"}})
}
