package execution

import (
	"context"
	"errors"
	"fmt"
	"sort"

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

	state := newExecutionState(len(plan.Order))
	for _, id := range plan.Order {
		if err := ctx.Err(); err != nil {
			return ExecutionResult{State: *state}, fmt.Errorf("execution cancelled before node %q: %w", id, err)
		}
		b := bound[id]
		fail := func(stage Stage, cause error) (ExecutionResult, error) {
			return ExecutionResult{State: *state}, &NodeExecutionError{NodeID: id, NodeType: b.spec.Type, Stage: stage, Err: cause}
		}

		cfg, err := resolveConfig(b.spec.Config, variableScope{nodeID: id, plan: &plan, results: state.Results, input: input})
		if err != nil {
			return fail(StageResolveConfig, err)
		}
		ports, err := resolveInputs(id, b.def, &plan, state, bound)
		if err != nil {
			return fail(StageResolveInputs, err)
		}
		if b.def.Role == node.SemanticRoleEntry && input != nil {
			if _, wired := ports[EntryInputPort]; !wired {
				ports[EntryInputPort] = node.NewJSONValue(deepCopy(input))
			}
		}

		nodeInput := node.NodeInput{Ports: ports, Config: cfg}
		if observer != nil {
			if err := observer.NodeStarted(ctx, id, b.spec.Type, nodeInput); err != nil {
				return fail(StageObserve, err)
			}
		}
		out, err := b.impl.Execute(ctx, nodeInput)
		if observer != nil {
			if obsErr := observer.NodeFinished(ctx, id, out, err); obsErr != nil {
				if err != nil {
					return fail(StageExecute, errors.Join(err, obsErr))
				}
				return fail(StageObserve, obsErr)
			}
		}
		if err != nil {
			return fail(StageExecute, err)
		}
		if out.Ports == nil {
			out = node.NewNodeOutput(nil)
		}
		state.record(id, out)
	}

	return buildResult(&plan, bound, state), nil
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
