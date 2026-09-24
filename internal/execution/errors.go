package execution

import (
	"errors"
	"fmt"
)

// Sentinel errors returned by the Phase 7 graph executor. They classify
// defensive failures for states that Phase 6 executable validation should make
// impossible, plus runtime failures that only exist once values flow.
var (
	// ErrNilRegistry is returned when the executor has no node registry.
	ErrNilRegistry = errors.New("execution: node registry is nil")
	// ErrNilContext is returned when Execute is called with a nil context.
	ErrNilContext = errors.New("execution: context is nil")
	// ErrInvalidPlanInput marks a graph the planner cannot order safely
	// (duplicate/empty node IDs, edges to unknown nodes).
	ErrInvalidPlanInput = errors.New("execution: invalid graph for planning")
	// ErrGraphCycle marks a cyclic graph reaching the planner.
	ErrGraphCycle = errors.New("execution: graph contains a cycle")
	// ErrNodeImplementationMissing marks a node type with no registered executable node.
	ErrNodeImplementationMissing = errors.New("execution: node implementation not registered")
	// ErrNodeDefinitionMissing marks a node type with no registered NodeDefinition.
	ErrNodeDefinitionMissing = errors.New("execution: node definition not registered")
	// ErrMissingUpstreamResult marks an edge whose source node has no result.
	ErrMissingUpstreamResult = errors.New("execution: upstream node result missing")
	// ErrMissingSourcePort marks an edge whose source output port was declared
	// required but was not produced by the source node.
	ErrMissingSourcePort = errors.New("execution: upstream output port missing")
	// ErrInvalidConnection marks an edge that cannot be mapped onto the target
	// node's declared inputs.
	ErrInvalidConnection = errors.New("execution: input connection cannot be resolved")
	// ErrUnresolvedReference marks a {{variable}} reference with no runtime value.
	ErrUnresolvedReference = errors.New("execution: unresolved variable reference")
)

// Stage identifies which step of a node's execution failed.
type Stage string

const (
	StageResolveConfig Stage = "resolve_config"
	StageResolveInputs Stage = "resolve_inputs"
	StageExecute       Stage = "execute"
	// StageObserve marks a failure reported by a NodeObserver (e.g. the
	// lifecycle layer could not record the node execution).
	StageObserve Stage = "observe"
)

// NodeExecutionError reports a failure attributable to one workflow node. It
// unwraps to the underlying cause so errors.Is/As work for node errors,
// context cancellation, and the sentinels above.
type NodeExecutionError struct {
	NodeID   string
	NodeType string
	Stage    Stage
	Err      error
}

func (e *NodeExecutionError) Error() string {
	return fmt.Sprintf("node %q (type %q) failed during %s: %v", e.NodeID, e.NodeType, e.Stage, e.Err)
}

func (e *NodeExecutionError) Unwrap() error { return e.Err }
