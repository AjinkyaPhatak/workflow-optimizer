package execution

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/workflow"
)

// DefinitionLoader loads the immutable definition of an exact workflow version.
type DefinitionLoader interface {
	LoadDefinition(ctx context.Context, versionID uuid.UUID) (workflow.Definition, error)
}

// RunnerConfig lists the Runner's collaborators. All are required.
type RunnerConfig struct {
	Executions     ExecutionRepository
	NodeExecutions NodeExecutionRepository
	Definitions    DefinitionLoader
	Validator      workflow.Validator
	Graph          *GraphExecutor
}

// Runner drives one claimed execution through its lifecycle:
//
//	Start (atomic claim) -> load exact version -> validate -> GraphExecutor
//	  -> COMPLETED | FAILED | CANCELLED
//
// GraphExecutor stays responsible only for graph execution; the Runner and
// LifecycleService own status, timestamps, history and node records. Once a
// claim succeeds, the Runner always drives the execution to a terminal state
// (lifecycle writes use context.WithoutCancel so cancellation of the run
// cannot prevent recording that it was cancelled).
type Runner struct {
	cfg     RunnerConfig
	service *LifecycleService
	nodes   NodeExecutionStateMachine
}

// NewRunner validates the configuration and constructs a Runner.
func NewRunner(cfg RunnerConfig) (*Runner, error) {
	if cfg.Executions == nil || cfg.NodeExecutions == nil || cfg.Definitions == nil || cfg.Validator == nil || cfg.Graph == nil {
		return nil, fmt.Errorf("%w: runner requires executions, node executions, definitions, validator and graph executor", ErrInvalidExecution)
	}
	return &Runner{
		cfg:     cfg,
		service: NewLifecycleService(cfg.Executions),
		nodes:   NewNodeExecutionStateMachine(cfg.NodeExecutions),
	}, nil
}

// Service exposes the lifecycle service the Runner uses (Create, Cancel, ...).
func (r *Runner) Service() ExecutionService { return r.service }

// Run claims and executes a PENDING execution. If the claim fails (e.g. another
// worker owns it) nothing else happens and the claim error is returned. After a
// successful claim the returned error is the graph error (or finalization
// error), and the execution has been moved to a terminal status.
func (r *Runner) Run(ctx context.Context, executionID uuid.UUID) (ExecutionResult, error) {
	if err := r.service.Start(ctx, executionID); err != nil {
		return ExecutionResult{}, err
	}
	lifecycleCtx := context.WithoutCancel(ctx)

	exec, err := r.cfg.Executions.Get(lifecycleCtx, executionID)
	if err != nil {
		return ExecutionResult{}, r.fail(lifecycleCtx, executionID, ExecutionError{Code: CodeExecutionFailed, Message: err.Error()}, err)
	}
	def, err := r.cfg.Definitions.LoadDefinition(lifecycleCtx, exec.WorkflowVersionID)
	if err != nil {
		return ExecutionResult{}, r.fail(lifecycleCtx, executionID, ExecutionError{Code: CodeInvalidWorkflow, Message: "load workflow version: " + err.Error()}, err)
	}
	if res := r.cfg.Validator.Validate(def); !res.Valid {
		verr := fmt.Errorf("%w: %s", ErrInvalidExecution, summarizeValidation(res))
		return ExecutionResult{}, r.fail(lifecycleCtx, executionID, ExecutionError{Code: CodeInvalidWorkflow, Message: verr.Error()}, verr)
	}

	recorder := &nodeRecorder{
		executionID: executionID,
		repo:        r.cfg.NodeExecutions,
		machine:     r.nodes,
		ids:         map[string]uuid.UUID{},
	}
	result, graphErr := r.cfg.Graph.ExecuteWithObserver(ctx, def, exec.Input, recorder)

	switch {
	case graphErr == nil:
		if err := r.service.Complete(lifecycleCtx, executionID, workflowOutput(result)); err != nil {
			return result, fmt.Errorf("record completion: %w", err)
		}
		return result, nil
	case errors.Is(ctx.Err(), context.Canceled) && errors.Is(graphErr, context.Canceled):
		// Caller cancellation is not a node failure: persist CANCELLED.
		if err := r.service.Cancel(lifecycleCtx, executionID); err != nil {
			return result, errors.Join(graphErr, fmt.Errorf("record cancellation: %w", err))
		}
		return result, graphErr
	default:
		return result, r.fail(lifecycleCtx, executionID, ErrorFromExecution(graphErr), graphErr)
	}
}

func (r *Runner) fail(ctx context.Context, id uuid.UUID, execErr ExecutionError, cause error) error {
	if err := r.service.Fail(ctx, id, execErr); err != nil {
		return errors.Join(cause, fmt.Errorf("record failure: %w", err))
	}
	return cause
}

func summarizeValidation(res workflow.ValidationResult) string {
	parts := make([]string, 0, len(res.Errors))
	for _, e := range res.Errors {
		p := string(e.Code)
		if e.NodeID != "" {
			p += " node=" + e.NodeID
		}
		parts = append(parts, p)
	}
	return "workflow failed executable validation: " + strings.Join(parts, ", ")
}

// nodeRecorder is the NodeObserver that persists one NodeExecution per node
// actually dispatched by GraphExecutor: PENDING -> RUNNING -> COMPLETED|FAILED.
// The ids map is per-run bookkeeping (node ID -> record ID), not a lock.
type nodeRecorder struct {
	executionID uuid.UUID
	repo        NodeExecutionRepository
	machine     NodeExecutionStateMachine
	ids         map[string]uuid.UUID
}

var _ NodeObserver = (*nodeRecorder)(nil)

func (n *nodeRecorder) NodeStarted(ctx context.Context, nodeID, nodeType string, input node.NodeInput) error {
	ctx = context.WithoutCancel(ctx)
	rec := NodeExecution{
		ID:          uuid.New(),
		ExecutionID: n.executionID,
		NodeID:      nodeID,
		NodeType:    nodeType,
		Status:      NodeStatusPending,
	}
	if err := n.repo.Create(ctx, rec); err != nil {
		return err
	}
	n.ids[nodeID] = rec.ID
	return n.machine.Transition(ctx, rec.ID, NodeStatusRunning, NodeTransitionUpdate{Input: nodeInputMap(input)})
}

func (n *nodeRecorder) NodeFinished(ctx context.Context, nodeID string, output node.NodeOutput, err error) error {
	ctx = context.WithoutCancel(ctx)
	id, ok := n.ids[nodeID]
	if !ok {
		return fmt.Errorf("%w: node %q finished without a started record", ErrNodeExecutionNotFound, nodeID)
	}
	if err == nil {
		return n.machine.Transition(ctx, id, NodeStatusCompleted, NodeTransitionUpdate{Output: portsMap(output.Ports)})
	}
	nodeErr := nodeFailure(nodeID, err)
	return n.machine.Transition(ctx, id, NodeStatusFailed, NodeTransitionUpdate{Error: &nodeErr})
}

// nodeFailure builds the node-level ExecutionError, keeping node-reported
// codes and distinguishing cancellation/timeouts from ordinary failures.
func nodeFailure(nodeID string, err error) ExecutionError {
	e := ErrorFromExecution(err)
	if e.Code == CodeExecutionFailed {
		e.Code = CodeNodeFailed
	}
	id := nodeID
	e.NodeID = &id
	return e
}

func portsMap(ports map[string]node.Value) map[string]any {
	out := make(map[string]any, len(ports))
	for name, v := range ports {
		out[name] = v.Data
	}
	return out
}

func nodeInputMap(in node.NodeInput) map[string]any {
	m := map[string]any{"ports": portsMap(in.Ports)}
	if in.Config != nil {
		m["config"] = in.Config
	}
	return m
}

// workflowOutput maps exit-node results to {exitNodeID: {port: data}}.
func workflowOutput(res ExecutionResult) map[string]any {
	out := make(map[string]any, len(res.Outputs))
	for id, o := range res.Outputs {
		out[id] = portsMap(o.Ports)
	}
	return out
}
