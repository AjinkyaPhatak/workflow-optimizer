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
// A definition that cannot be decoded must be reported as
// ErrInvalidWorkflowDefinition; anything else is treated as transient.
type DefinitionLoader interface {
	LoadDefinition(ctx context.Context, versionID uuid.UUID) (workflow.Definition, error)
}

// RunnerConfig lists the Runner's collaborators. All but Persistence are
// required.
type RunnerConfig struct {
	Executions     ExecutionRepository
	NodeExecutions NodeExecutionRepository
	Definitions    DefinitionLoader
	Validator      workflow.Validator
	Graph          *GraphExecutor
	Persistence    PersistenceOptions
}

// Runner drives one claimed execution through its lifecycle:
//
//	Start (atomic claim) -> load exact version -> validate -> GraphExecutor
//	  -> COMPLETED | FAILED | CANCELLED
//
// GraphExecutor stays responsible only for graph execution; the Runner and
// LifecycleService own status, timestamps, history and node records.
//
// Ownership contract: once Start succeeds, Run drives the execution to a
// terminal status. Every lifecycle write runs on a context detached from the
// caller's cancellation and bounded by Persistence.WriteTimeout, so
// cancellation can neither abandon a write half-way nor block forever. The
// only way a claimed execution stays RUNNING is if the database itself cannot
// be written (Run then returns that error; the row carries the claim token for
// recovery, which is outside Phase 8).
type Runner struct {
	cfg     RunnerConfig
	service *LifecycleService
	machine ExecutionStateMachine
	nodes   NodeExecutionStateMachine
}

// NewRunner validates the configuration and constructs a Runner.
func NewRunner(cfg RunnerConfig) (*Runner, error) {
	if cfg.Executions == nil || cfg.NodeExecutions == nil || cfg.Definitions == nil || cfg.Validator == nil || cfg.Graph == nil {
		return nil, fmt.Errorf("%w: runner requires executions, node executions, definitions, validator and graph executor", ErrInvalidExecution)
	}
	service := NewLifecycleServiceWithOptions(cfg.Executions, cfg.Persistence)
	return &Runner{
		cfg:     cfg,
		service: service,
		machine: service.machine,
		nodes:   NewNodeExecutionStateMachineWithOptions(cfg.NodeExecutions, cfg.Persistence),
	}, nil
}

// Service exposes the lifecycle service the Runner uses (Create, Cancel, ...).
func (r *Runner) Service() ExecutionService { return r.service }

// Run claims and executes a PENDING execution. If the claim fails (another
// worker owns it, the caller was already cancelled, the database gave up)
// nothing else happens and the claim error is returned.
func (r *Runner) Run(ctx context.Context, executionID uuid.UUID) (ExecutionResult, error) {
	if err := r.service.Start(ctx, executionID); err != nil {
		return ExecutionResult{}, err
	}

	rctx, cancel := persistContext(ctx, r.cfg.Persistence)
	exec, err := r.cfg.Executions.Get(rctx, executionID)
	cancel()
	if err != nil {
		return ExecutionResult{}, r.fail(ctx, executionID, ExecutionError{
			Code: CodeExecutionFailed, Message: "load claimed execution: " + err.Error(), Retryable: true,
		}, interruptedByFailure, err)
	}

	lctx, cancel := persistContext(ctx, r.cfg.Persistence)
	def, err := r.cfg.Definitions.LoadDefinition(lctx, exec.WorkflowVersionID)
	cancel()
	if err != nil {
		execErr := ExecutionError{Code: CodeDefinitionLoadFailed, Message: "load workflow version: " + err.Error(), Retryable: true}
		if errors.Is(err, ErrInvalidWorkflowDefinition) {
			execErr = ExecutionError{Code: CodeInvalidWorkflow, Message: err.Error()}
		}
		return ExecutionResult{}, r.fail(ctx, executionID, execErr, interruptedByFailure, err)
	}
	if res := r.cfg.Validator.Validate(def); !res.Valid {
		verr := fmt.Errorf("%w: %s", ErrInvalidExecution, summarizeValidation(res))
		return ExecutionResult{}, r.fail(ctx, executionID, ExecutionError{Code: CodeInvalidWorkflow, Message: verr.Error()}, interruptedByFailure, verr)
	}

	recorder := &nodeRecorder{
		executionID: executionID,
		machine:     r.nodes,
		ids:         map[string]uuid.UUID{},
	}
	result, graphErr := r.cfg.Graph.ExecuteWithObserver(ctx, def, exec.Input, recorder)
	return result, r.finalize(ctx, executionID, result, graphErr)
}

// finalize records the terminal status. Order matters:
//  1. success -> COMPLETED (work finished; a late cancel does not erase it);
//  2. a node write rejected because another actor already finalized the
//     execution -> report that status, write nothing;
//  3. the caller cancelled -> CANCELLED, even if a node ignored its context and
//     returned an unrelated error (that error is kept in history metadata and
//     on the node record);
//  4. the caller's deadline passed -> FAILED with EXECUTION_TIMEOUT;
//  5. otherwise -> FAILED with the structured graph error.
func (r *Runner) finalize(ctx context.Context, id uuid.UUID, result ExecutionResult, graphErr error) error {
	switch {
	case graphErr == nil:
		if err := r.service.Complete(ctx, id, workflowOutput(result)); err != nil {
			return r.explainFinalizationFailure(ctx, id, fmt.Errorf("record completion: %w", err))
		}
		return nil
	case errors.Is(graphErr, ErrExecutionNotRunning):
		return r.explainFinalizationFailure(ctx, id, graphErr)
	case errors.Is(ctx.Err(), context.Canceled):
		cause := fmt.Errorf("execution cancelled by caller: %w", errors.Join(ctx.Err(), graphErr))
		meta := map[string]any{"reason": "caller_cancelled"}
		if !errors.Is(graphErr, context.Canceled) {
			meta["graph_error"] = graphErr.Error()
		}
		if err := r.service.cancel(ctx, id, meta); err != nil {
			return r.explainFinalizationFailure(ctx, id, errors.Join(cause, fmt.Errorf("record cancellation: %w", err)))
		}
		return cause
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		execErr := ErrorFromExecution(graphErr)
		execErr.Code = CodeTimeout
		return r.fail(ctx, id, execErr, interruptedByFailure, graphErr)
	default:
		interrupted := interruptedByFailure
		var nodeErr *NodeExecutionError
		if errors.As(graphErr, &nodeErr) && nodeErr.Stage == StageObserve {
			interrupted = ExecutionError{Code: CodeNodeObservation,
				Message: "node executed but its result could not be recorded: " + nodeErr.Err.Error()}
		}
		return r.fail(ctx, id, ErrorFromExecution(graphErr), interrupted, graphErr)
	}
}

func (r *Runner) fail(ctx context.Context, id uuid.UUID, execErr ExecutionError, interrupted ExecutionError, cause error) error {
	if err := r.service.fail(ctx, id, execErr, interrupted); err != nil {
		return r.explainFinalizationFailure(ctx, id, errors.Join(cause, fmt.Errorf("record failure: %w", err)))
	}
	return cause
}

// explainFinalizationFailure distinguishes "another actor already finalized
// this execution" (typed error) from a genuine persistence failure.
func (r *Runner) explainFinalizationFailure(ctx context.Context, id uuid.UUID, err error) error {
	rctx, cancel := persistContext(ctx, r.cfg.Persistence)
	defer cancel()
	current, getErr := r.cfg.Executions.Get(rctx, id)
	if getErr == nil && current.Status.IsTerminal() {
		return &ExternallyFinalizedError{ExecutionID: id, Status: current.Status, Err: err}
	}
	return err
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
// GraphExecutor reaches. A started node is created RUNNING atomically
// (PENDING -> RUNNING in one transaction). A write rejected because the
// execution is no longer RUNNING stops the graph with ErrExecutionNotRunning.
// The ids map is per-run bookkeeping (node ID -> record ID), not a lock.
type nodeRecorder struct {
	executionID uuid.UUID
	machine     NodeExecutionStateMachine
	ids         map[string]uuid.UUID
}

var _ NodeObserver = (*nodeRecorder)(nil)

func (n *nodeRecorder) begin(ctx context.Context, nodeID, nodeType string, input map[string]any) error {
	rec := NodeExecution{
		ID:          uuid.New(),
		ExecutionID: n.executionID,
		NodeID:      nodeID,
		NodeType:    nodeType,
		Status:      NodeStatusPending,
	}
	if err := n.machine.Begin(ctx, rec, input); err != nil {
		return err
	}
	n.ids[nodeID] = rec.ID
	return nil
}

func (n *nodeRecorder) NodeStarted(ctx context.Context, nodeID, nodeType string, input node.NodeInput) error {
	return n.begin(ctx, nodeID, nodeType, nodeInputMap(input))
}

func (n *nodeRecorder) NodeFinished(ctx context.Context, nodeID string, output node.NodeOutput, err error) error {
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

// NodeFailedBeforeExecute records a node whose configuration or inputs could
// not be resolved: it was reached (RUNNING) and failed before Execute.
func (n *nodeRecorder) NodeFailedBeforeExecute(ctx context.Context, nodeID, nodeType string, stage Stage, err error) error {
	if beginErr := n.begin(ctx, nodeID, nodeType, nil); beginErr != nil {
		return beginErr
	}
	nodeErr := nodeFailure(nodeID, &NodeExecutionError{NodeID: nodeID, NodeType: nodeType, Stage: stage, Err: err})
	return n.machine.Transition(ctx, n.ids[nodeID], NodeStatusFailed, NodeTransitionUpdate{Error: &nodeErr})
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
