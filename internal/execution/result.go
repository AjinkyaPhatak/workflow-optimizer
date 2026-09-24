package execution

import "workflow-optimizer/internal/node"

// ExecutionState is the in-memory runtime state of one Phase 7 execution. It
// exists only for the duration of Execute and is never persisted; durable
// execution records (Execution, NodeExecution, Status) belong to Phase 8.
type ExecutionState struct {
	// Results maps workflow node ID to the NodeOutput it produced.
	Results map[string]node.NodeOutput
	// Completed lists node IDs in the order they finished successfully.
	Completed []string
}

func newExecutionState(size int) *ExecutionState {
	return &ExecutionState{
		Results:   make(map[string]node.NodeOutput, size),
		Completed: make([]string, 0, size),
	}
}

func (s *ExecutionState) record(nodeID string, out node.NodeOutput) {
	s.Results[nodeID] = out
	s.Completed = append(s.Completed, nodeID)
}

// ExecutionResult is the outcome of executing a workflow graph.
type ExecutionResult struct {
	// Outputs maps each exit-role (node.SemanticRoleExit) node ID to its
	// NodeOutput. It is only populated when execution succeeds.
	Outputs map[string]node.NodeOutput
	// OutputNodeIDs lists the keys of Outputs sorted by node ID, giving a
	// deterministic order when a workflow has several exit nodes.
	OutputNodeIDs []string
	// State holds every completed node result. On failure it contains the
	// nodes that completed before the failing node, for diagnostics only.
	State ExecutionState
}
