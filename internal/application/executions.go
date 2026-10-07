package application

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"

	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/queue"
	"workflow-optimizer/internal/workflow"
	"workflow-optimizer/internal/workspace"
)

// ExecutionSubmitter is the existing queue.Submitter: persist PENDING, then
// enqueue the execution ID.
type ExecutionSubmitter interface {
	Submit(ctx context.Context, workflowID, versionID uuid.UUID, input map[string]any) (execution.Execution, error)
}

// ExecutionReader reads executions and their node records.
type ExecutionReader interface {
	Get(ctx context.Context, id uuid.UUID) (execution.Execution, error)
}

// NodeExecutionReader lists an execution's node records.
type NodeExecutionReader interface {
	ListByExecution(ctx context.Context, executionID uuid.UUID) ([]execution.NodeExecution, error)
}

// Canceller is the existing lifecycle cancellation (Phase 8/10
// LifecycleService.RequestCancel).
type Canceller interface {
	RequestCancel(ctx context.Context, id uuid.UUID) error
}

// WorkflowWorkspaces resolves the workspace of any workflow, including a
// deleted one (its executions stay readable).
type WorkflowWorkspaces interface {
	WorkspaceOf(ctx context.Context, workflowID uuid.UUID) (uuid.UUID, error)
}

// ExecutionService starts, reads and cancels executions. It never runs a
// workflow: Execute persists a PENDING execution and enqueues it; workers do
// the rest.
type ExecutionService struct {
	workflows  *WorkflowService
	submitter  ExecutionSubmitter
	executions ExecutionReader
	nodes      NodeExecutionReader
	canceller  Canceller
	workspaces WorkflowWorkspaces
	logger     *slog.Logger
}

// ExecutionDeps are ExecutionService's collaborators.
type ExecutionDeps struct {
	Workflows  *WorkflowService
	Submitter  ExecutionSubmitter
	Executions ExecutionReader
	Nodes      NodeExecutionReader
	Canceller  Canceller
	Workspaces WorkflowWorkspaces
	Logger     *slog.Logger
}

// NewExecutionService wires the service.
func NewExecutionService(d ExecutionDeps) *ExecutionService {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &ExecutionService{workflows: d.Workflows, submitter: d.Submitter, executions: d.Executions,
		nodes: d.Nodes, canceller: d.Canceller, workspaces: d.Workspaces, logger: d.Logger}
}

// Execute starts an execution of a PUBLISHED version (the workflow's active
// version when versionID is nil): it validates the graph in executable
// mode, persists the execution PENDING and enqueues it, then returns.
func (s *ExecutionService) Execute(ctx context.Context, user, workflowID uuid.UUID, versionID *uuid.UUID, input map[string]any) (execution.Execution, error) {
	wf, _, err := s.workflows.workflow(ctx, user, workflowID, workspace.ActionExecute)
	if err != nil {
		return execution.Execution{}, err
	}
	if versionID == nil {
		if wf.ActiveVersionID == nil {
			return execution.Execution{}, &ConflictError{Code: "NO_ACTIVE_VERSION", Message: "Workflow has no published version"}
		}
		versionID = wf.ActiveVersionID
	}
	v, err := s.workflows.version(ctx, workflowID, *versionID)
	if err != nil {
		return execution.Execution{}, err
	}
	if v.Status != workflow.VersionStatusPublished {
		return execution.Execution{}, &ConflictError{Code: "VERSION_NOT_PUBLISHED", Message: "Only published versions can be executed"}
	}
	res, err := s.workflows.validateStored(v)
	if err != nil {
		return execution.Execution{}, err
	}
	if !res.Valid {
		return execution.Execution{}, &ValidationFailedError{Errors: res.Errors}
	}
	if input == nil {
		input = map[string]any{}
	}
	created, err := s.submitter.Submit(ctx, workflowID, v.ID, input)
	if err == nil || created.ID != uuid.Nil {
		// Correlates the request (request_id, added by the context logger)
		// with the execution the workers will report on.
		s.logger.InfoContext(ctx, "execution submitted", "event", "execution_submitted",
			"execution_id", created.ID.String(), "workflow_id", workflowID.String(), "version_id", v.ID.String())
	}
	var dispatchErr *queue.DispatchError
	if errors.As(err, &dispatchErr) {
		// Persisted PENDING; the retry scheduler re-dispatches executions
		// left undelivered, so the request still succeeded.
		s.logger.Warn("execution persisted but not enqueued; the scheduler will re-dispatch it",
			"execution_id", created.ID, "error", err)
		return created, nil
	}
	if errors.Is(err, execution.ErrWorkflowVersionNotExecutable) {
		return execution.Execution{}, &ConflictError{Code: "VERSION_NOT_PUBLISHED", Message: "Only published versions can be executed"}
	}
	return created, err
}

// execution loads an execution the user may act on.
func (s *ExecutionService) execution(ctx context.Context, user, id uuid.UUID, action workspace.Action) (execution.Execution, error) {
	e, err := s.executions.Get(ctx, id)
	if errors.Is(err, execution.ErrExecutionNotFound) {
		return execution.Execution{}, notFound("execution")
	}
	if err != nil {
		return execution.Execution{}, err
	}
	ws, err := s.workspaces.WorkspaceOf(ctx, e.WorkflowID)
	if errors.Is(err, execution.ErrWorkflowNotFound) {
		return execution.Execution{}, notFound("execution")
	}
	if err != nil {
		return execution.Execution{}, err
	}
	if _, err := s.workflows.access.Require(ctx, user, ws, action, "execution"); err != nil {
		return execution.Execution{}, err
	}
	return e, nil
}

// Cancel requests cancellation through the lifecycle service: a RUNNING
// execution is cancelled at once (its worker stops at its next heartbeat),
// a PENDING one is cancelled instead of being claimed.
func (s *ExecutionService) Cancel(ctx context.Context, user, id uuid.UUID) (execution.Execution, error) {
	if _, err := s.execution(ctx, user, id, workspace.ActionExecute); err != nil {
		return execution.Execution{}, err
	}
	if err := s.canceller.RequestCancel(ctx, id); err != nil {
		var te *execution.TransitionError
		if errors.As(err, &te) && te.From.IsTerminal() {
			return execution.Execution{}, &ConflictError{Code: "EXECUTION_FINISHED", Message: "Execution has already finished"}
		}
		if errors.Is(err, execution.ErrExecutionNotFound) {
			return execution.Execution{}, notFound("execution")
		}
		return execution.Execution{}, err
	}
	return s.executions.Get(ctx, id)
}
