package execution

import (
	"errors"
	"fmt"
	"time"

	"workflow-optimizer/internal/node"
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
	// ErrNodePanicked marks a node whose Execute panicked. The panic is
	// recovered at the node invocation boundary and reported as a node failure.
	ErrNodePanicked = errors.New("execution: node panicked")
	// ErrNodeTimeout marks a node invocation that exceeded its node timeout.
	ErrNodeTimeout = errors.New("execution: node timed out")
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
	// SideEffects is the failing node's declaration; it decides whether a
	// transient failure may be repeated (see ErrorFromExecution).
	SideEffects node.SideEffects
}

func (e *NodeExecutionError) Error() string {
	return fmt.Sprintf("node %q (type %q) failed during %s: %v", e.NodeID, e.NodeType, e.Stage, e.Err)
}

func (e *NodeExecutionError) Unwrap() error { return e.Err }

// NodePanicError is the cause recorded when a node's Execute panics. It is
// wrapped in a NodeExecutionError (Stage == StageExecute), which carries the
// node ID and type. It matches ErrNodePanicked. It deliberately does not
// unwrap to the panic value: a panic is always a node failure, never
// reclassified (e.g. panic(context.Canceled) must not read as a cancellation).
// The raw value stays available through errors.As.
type NodePanicError struct {
	Value any
}

func (e *NodePanicError) Error() string {
	return fmt.Sprintf("node panicked: %v", e.Value)
}

// Is matches ErrNodePanicked.
func (e *NodePanicError) Is(target error) bool { return target == ErrNodePanicked }

// NodeTimeoutError is the cause recorded when one node invocation exceeds its
// node timeout while the execution itself is still within its deadline. It
// matches ErrNodeTimeout and unwraps to what the node returned.
type NodeTimeoutError struct {
	Timeout time.Duration
	Err     error
}

func (e *NodeTimeoutError) Error() string {
	return fmt.Sprintf("node exceeded its %s timeout: %v", e.Timeout, e.Err)
}

func (e *NodeTimeoutError) Is(target error) bool { return target == ErrNodeTimeout }

func (e *NodeTimeoutError) Unwrap() error { return e.Err }
