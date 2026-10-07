package execution

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/workflow"
)

// EntryInputPort is the runtime port on which entry-role nodes
// (node.SemanticRoleEntry) receive the workflow's initial input, as a JSON
// Value. The executor only delivers the payload; interpreting it is the entry
// node's own responsibility.
const EntryInputPort = "data"

// NodeResolver supplies nodes to the execution layer through the node contract.
// node.Registry satisfies it.
type NodeResolver interface {
	Resolve(nodeType string) (node.Node, bool)
}

// Executor executes a workflow definition and returns its result.
type Executor interface {
	Execute(ctx context.Context, definition workflow.Definition, input map[string]any) (ExecutionResult, error)
}

// NodeObserver receives node lifecycle events from GraphExecutor. It lets an
// outer layer (the Phase 8 lifecycle Runner) record node executions without
// GraphExecutor depending on persistence. Returning an error stops execution
// fail-fast. Observers are called sequentially, on the executing goroutine.
type NodeObserver interface {
	// NodeStarted is called with the fully resolved runtime input immediately
	// before the node's Execute. An error prevents the node from running.
	NodeStarted(ctx context.Context, nodeID, nodeType string, input node.NodeInput) error
	// NodeFinished is called after Execute returns, with its output or error.
	NodeFinished(ctx context.Context, nodeID string, output node.NodeOutput, err error) error
	// NodeFailedBeforeExecute is called when a node's configuration or inputs
	// cannot be resolved, so the failing node is observable even though its
	// Execute never ran. err is the resolution error for stage.
	NodeFailedBeforeExecute(ctx context.Context, nodeID, nodeType string, stage Stage, err error) error
}

// NodeProgressObserver may additionally be implemented by a NodeObserver
// (Phase 14) to learn what the executor decides between invocations: a node
// skipped because it completed in an earlier attempt (its output is reused),
// and an in-place retry scheduled after delay. It only observes: it cannot
// influence either decision.
type NodeProgressObserver interface {
	NodeReused(ctx context.Context, nodeID, nodeType string)
	NodeRetryScheduled(ctx context.Context, nodeID string, failures int, delay time.Duration, failure error)
}

// GraphExecutor is the Phase 7 sequential DAG executor. It assumes the
// definition has passed Phase 6 executable validation; it only guards against
// impossible states instead of re-validating the graph.
//
// It knows nothing about individual node types: implementations and metadata
// are looked up through the canonical node.Registry, and graph-boundary
// behaviour is driven by NodeDefinition.Role.
type GraphExecutor struct {
	registry node.Registry
}

var _ Executor = (*GraphExecutor)(nil)

// NewGraphExecutor constructs an executor backed by the canonical registry.
func NewGraphExecutor(registry node.Registry) *GraphExecutor {
	return &GraphExecutor{registry: registry}
}

// SideEffects returns the registered side-effect declaration of nodeType, or
// node.SideEffectsUnsafe when the type has no definition (unknown effects
// must never be assumed safe to repeat).
func (e *GraphExecutor) SideEffects(nodeType string) node.SideEffects {
	def, err := e.registry.GetDefinition(nodeType)
	if err != nil {
		return node.SideEffectsUnsafe
	}
	return def.SideEffects
}

// boundNode pairs a workflow node instance with its registered implementation
// and definition.
type boundNode struct {
	spec workflow.Node
	impl node.Node
	def  node.NodeDefinition
}

// Execute runs every node of the definition in deterministic dependency order.
//
// Semantics:
//   - Sequential: one node at a time, no goroutines.
//   - Fail-fast: the first error stops execution; no later node runs and
//     nothing is retried.
//   - Cancellation: ctx is checked before every node and passed unchanged to
//     every node's Execute; cancellation errors are wrapped, not swallowed.
//   - Nothing is persisted; the returned State is in-memory only.
func (e *GraphExecutor) Execute(ctx context.Context, definition workflow.Definition, input map[string]any) (ExecutionResult, error) {
	return e.ExecuteWithObserver(ctx, definition, input, nil)
}

// ExecuteWithObserver is Execute with an optional NodeObserver (nil allowed).
func (e *GraphExecutor) ExecuteWithObserver(ctx context.Context, definition workflow.Definition, input map[string]any, observer NodeObserver) (ExecutionResult, error) {
	return e.ExecuteWithOptions(ctx, definition, input, ExecuteOptions{Observer: observer})
}

// NodeRetryPolicy decides whether a failed node invocation runs again in
// place (within the same execution attempt, on the same worker).
type NodeRetryPolicy interface {
	// NodeRetryDelay is consulted after `failures` failed invocations of a
	// node with definition def, the last failing with err. It returns the
	// pause before invoking the node again, or false to stop.
	NodeRetryDelay(def node.NodeDefinition, failures int, err ExecutionError) (time.Duration, bool)
}

// ExecuteOptions extends Execute (Phase 10). The zero value is Execute.
type ExecuteOptions struct {
	// Observer receives node lifecycle events (nil allowed).
	Observer NodeObserver
	// Completed holds the outputs of nodes that completed in an earlier
	// attempt of the same execution. They are not run again; their outputs
	// feed downstream nodes exactly as if they had just run.
	Completed map[string]node.NodeOutput
	// PriorInvocations counts earlier invocations per node, so
	// NodeInput.Attempt keeps growing across attempts.
	PriorInvocations map[string]int
	// OperationKey, when set, makes every node receive the stable
	// NodeInput.IdempotencyKey OperationKey + "/" + nodeID.
	OperationKey string
	// NodeTimeout bounds each node invocation (0 = no node timeout). The node
	// context derives from ctx, so a node timeout never extends ctx's deadline.
	NodeTimeout time.Duration
	// NodeRetry, when set, may re-invoke a node after a failure (bounded by
	// the policy, ctx's deadline and cancellation).
	NodeRetry NodeRetryPolicy
	// Scope (Phase 11) is passed to every node as NodeInput.Scope; the
	// executor does not interpret it.
	Scope node.Scope
}

// ExecuteWithOptions is Execute with Phase 10 reliability options.
func (e *GraphExecutor) ExecuteWithOptions(ctx context.Context, definition workflow.Definition, input map[string]any, opts ExecuteOptions) (ExecutionResult, error) {
	observer := opts.Observer
	if ctx == nil {
		return ExecutionResult{}, ErrNilContext
	}
	if e == nil || e.registry == nil {
		return ExecutionResult{}, ErrNilRegistry
	}
	if err := ctx.Err(); err != nil {
		return ExecutionResult{}, fmt.Errorf("execution cancelled before start: %w", err)
	}

	plan, err := NewPlan(definition)
	if err != nil {
		return ExecutionResult{}, err
	}
	bound, err := e.bind(&plan)
	if err != nil {
		return ExecutionResult{}, err
	}

	progress, _ := observer.(NodeProgressObserver)
	state := newExecutionState(len(plan.Order))
	for _, id := range plan.Order {
		if out, done := opts.Completed[id]; done {
			// Completed in an earlier attempt: reuse its output, do not re-run.
			if out.Ports == nil {
				out = node.NewNodeOutput(nil)
			}
			state.record(id, out)
			if progress != nil {
				progress.NodeReused(ctx, id, bound[id].spec.Type)
			}
			continue
		}
		if err := ctx.Err(); err != nil {
			return ExecutionResult{State: *state}, fmt.Errorf("execution cancelled before node %q: %w", id, err)
		}
		b := bound[id]
		fail := func(stage Stage, cause error) (ExecutionResult, error) {
			return ExecutionResult{State: *state}, &NodeExecutionError{NodeID: id, NodeType: b.spec.Type, Stage: stage, Err: cause, SideEffects: b.def.SideEffects}
		}
		failBeforeExecute := func(stage Stage, cause error) (ExecutionResult, error) {
			if observer != nil {
				if obsErr := observer.NodeFailedBeforeExecute(ctx, id, b.spec.Type, stage, cause); obsErr != nil {
					cause = errors.Join(cause, obsErr)
				}
			}
			return fail(stage, cause)
		}

		cfg, err := resolveConfig(b.spec.Config, variableScope{nodeID: id, plan: &plan, results: state.Results, input: input})
		if err != nil {
			return failBeforeExecute(StageResolveConfig, err)
		}
		ports, err := resolveInputs(id, b.def, &plan, state, bound)
		if err != nil {
			return failBeforeExecute(StageResolveInputs, err)
		}
		if b.def.Role == node.SemanticRoleEntry && input != nil {
			if _, wired := ports[EntryInputPort]; !wired {
				ports[EntryInputPort] = node.NewJSONValue(deepCopy(input))
			}
		}

		key := ""
		if opts.OperationKey != "" {
			key = opts.OperationKey + "/" + id
		}
		for failures := 0; ; failures++ {
			nodeInput := node.NodeInput{Ports: ports, Config: cfg, IdempotencyKey: key,
				Attempt: opts.PriorInvocations[id] + failures + 1, Scope: opts.Scope}
			if observer != nil {
				if err := observer.NodeStarted(ctx, id, b.spec.Type, nodeInput); err != nil {
					return fail(StageObserve, err)
				}
			}
			out, err := invokeNodeWithTimeout(ctx, b.impl, nodeInput, opts.NodeTimeout)
			if observer != nil {
				if obsErr := observer.NodeFinished(ctx, id, out, err); obsErr != nil {
					if err != nil {
						return fail(StageExecute, errors.Join(err, obsErr))
					}
					return fail(StageObserve, obsErr)
				}
			}
			if err == nil {
				if out.Ports == nil {
					out = node.NewNodeOutput(nil)
				}
				state.record(id, out)
				break
			}
			failure := &NodeExecutionError{NodeID: id, NodeType: b.spec.Type, Stage: StageExecute, Err: err, SideEffects: b.def.SideEffects}
			onScheduled := func(time.Duration) {}
			if progress != nil {
				onScheduled = func(d time.Duration) { progress.NodeRetryScheduled(ctx, id, failures+1, d, failure) }
			}
			if !waitForNodeRetry(ctx, opts.NodeRetry, b.def, failures+1, failure, onScheduled) {
				return fail(StageExecute, err)
			}
		}
	}

	return buildResult(&plan, bound, state), nil
}

// invokeNode is the single boundary between the executor and a node
// implementation. A panic inside Execute is recovered here and returned as a
// *NodePanicError, so it follows the ordinary node-failure path (observer
// NodeFinished, fail-fast NodeExecutionError at StageExecute) instead of
// unwinding through the executor and killing the calling goroutine/process.
// Only the node's own code runs under this recover.
func invokeNode(ctx context.Context, impl node.Node, in node.NodeInput) (out node.NodeOutput, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, err = node.NodeOutput{}, &NodePanicError{Value: r}
		}
	}()
	return impl.Execute(ctx, in)
}

// invokeNodeWithTimeout runs one node invocation under a context derived
// from ctx and bounded by timeout (0 = ctx only). The derived context can only
// end earlier than ctx, so a node timeout never extends the execution
// deadline. When the node's own timeout (not ctx) ended it, the failure is a
// *NodeTimeoutError.
func invokeNodeWithTimeout(ctx context.Context, impl node.Node, in node.NodeInput, timeout time.Duration) (node.NodeOutput, error) {
	if timeout <= 0 {
		return invokeNode(ctx, impl, in)
	}
	nodeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := invokeNode(nodeCtx, impl, in)
	if ctx.Err() == nil && errors.Is(nodeCtx.Err(), context.DeadlineExceeded) {
		if err == nil {
			// Returned a result only after its time was up: not accepted.
			err = nodeCtx.Err()
		}
		return node.NodeOutput{}, &NodeTimeoutError{Timeout: timeout, Err: err}
	}
	return out, err
}

// waitForNodeRetry decides, from the failure's own classification, whether
// the node runs again in place, and waits for the policy's delay. It never
// retries once ctx is done (cancellation and the execution deadline win) and
// never schedules a retry that would start after ctx's deadline.
func waitForNodeRetry(ctx context.Context, policy NodeRetryPolicy, def node.NodeDefinition, failures int, failure error, onScheduled func(time.Duration)) bool {
	if policy == nil || ctx.Err() != nil {
		return false
	}
	delay, ok := policy.NodeRetryDelay(def, failures, ErrorFromExecution(failure))
	if !ok {
		return false
	}
	if deadline, has := ctx.Deadline(); has && time.Until(deadline) <= delay {
		return false
	}
	onScheduled(delay)
	if delay <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// bind resolves every node's implementation and definition through the
// registry before any node runs, so a missing registration never leaves a
// partially executed workflow.
func (e *GraphExecutor) bind(plan *Plan) (map[string]boundNode, error) {
	bound := make(map[string]boundNode, len(plan.Order))
	for _, id := range plan.Order {
		spec, _ := plan.Node(id)
		impl, err := e.registry.Get(spec.Type)
		if err != nil {
			return nil, fmt.Errorf("%w: node %q type %q: %w", ErrNodeImplementationMissing, id, spec.Type, err)
		}
		if impl == nil {
			return nil, fmt.Errorf("%w: node %q type %q resolved to nil", ErrNodeImplementationMissing, id, spec.Type)
		}
		def, err := e.registry.GetDefinition(spec.Type)
		if err != nil {
			return nil, fmt.Errorf("%w: node %q type %q: %w", ErrNodeDefinitionMissing, id, spec.Type, err)
		}
		bound[id] = boundNode{spec: spec, impl: impl, def: def}
	}
	return bound, nil
}

func buildResult(plan *Plan, bound map[string]boundNode, state *ExecutionState) ExecutionResult {
	outputs := make(map[string]node.NodeOutput)
	ids := make([]string, 0)
	for _, id := range plan.Order {
		if bound[id].def.Role == node.SemanticRoleExit {
			outputs[id] = state.Results[id]
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ExecutionResult{Outputs: outputs, OutputNodeIDs: ids, State: *state}
}
