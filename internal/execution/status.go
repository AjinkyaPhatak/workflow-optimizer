package execution

// ExecutionStatus is the durable lifecycle status of a workflow execution.
type ExecutionStatus string

const (
	StatusPending   ExecutionStatus = "PENDING"
	StatusRunning   ExecutionStatus = "RUNNING"
	StatusCompleted ExecutionStatus = "COMPLETED"
	StatusFailed    ExecutionStatus = "FAILED"
	StatusCancelled ExecutionStatus = "CANCELLED"
)

// NodeExecutionStatus is the durable lifecycle status of one node execution.
type NodeExecutionStatus string

const (
	NodeStatusPending   NodeExecutionStatus = "PENDING"
	NodeStatusRunning   NodeExecutionStatus = "RUNNING"
	NodeStatusCompleted NodeExecutionStatus = "COMPLETED"
	NodeStatusFailed    NodeExecutionStatus = "FAILED"
	// NodeStatusSkipped is persisted-compatible for future conditional
	// execution; Phase 8 never produces it.
	NodeStatusSkipped NodeExecutionStatus = "SKIPPED"
)

// executionTransitions is the single source of truth for execution lifecycle
// rules. The database triggers in migration 000002 mirror this table.
var executionTransitions = map[ExecutionStatus][]ExecutionStatus{
	StatusPending:   {StatusRunning},
	StatusRunning:   {StatusCompleted, StatusFailed, StatusCancelled},
	StatusCompleted: nil,
	StatusFailed:    nil,
	StatusCancelled: nil,
}

// nodeTransitions is the single source of truth for node lifecycle rules.
var nodeTransitions = map[NodeExecutionStatus][]NodeExecutionStatus{
	NodeStatusPending:   {NodeStatusRunning},
	NodeStatusRunning:   {NodeStatusCompleted, NodeStatusFailed, NodeStatusSkipped},
	NodeStatusCompleted: nil,
	NodeStatusFailed:    nil,
	NodeStatusSkipped:   nil,
}

// Valid reports whether s is a defined execution status.
func (s ExecutionStatus) Valid() bool { _, ok := executionTransitions[s]; return ok }

// IsTerminal reports whether s is COMPLETED, FAILED or CANCELLED.
func (s ExecutionStatus) IsTerminal() bool { return s.Valid() && len(executionTransitions[s]) == 0 }

// CanTransitionTo reports whether s -> target is a legal lifecycle transition.
func (s ExecutionStatus) CanTransitionTo(target ExecutionStatus) bool {
	for _, t := range executionTransitions[s] {
		if t == target {
			return true
		}
	}
	return false
}

// sourceStatus returns the unique status from which target may be entered.
// Every reachable status has exactly one legal predecessor, which lets a
// transition be a single compare-and-set without reading state first.
func sourceStatus(target ExecutionStatus) (ExecutionStatus, bool) {
	for from, targets := range executionTransitions {
		for _, t := range targets {
			if t == target {
				return from, true
			}
		}
	}
	return "", false
}

// Valid reports whether s is a defined node execution status.
func (s NodeExecutionStatus) Valid() bool { _, ok := nodeTransitions[s]; return ok }

// IsTerminal reports whether s is COMPLETED, FAILED or SKIPPED.
func (s NodeExecutionStatus) IsTerminal() bool { return s.Valid() && len(nodeTransitions[s]) == 0 }

// CanTransitionTo reports whether s -> target is a legal node transition.
func (s NodeExecutionStatus) CanTransitionTo(target NodeExecutionStatus) bool {
	for _, t := range nodeTransitions[s] {
		if t == target {
			return true
		}
	}
	return false
}

func nodeSourceStatus(target NodeExecutionStatus) (NodeExecutionStatus, bool) {
	for from, targets := range nodeTransitions {
		for _, t := range targets {
			if t == target {
				return from, true
			}
		}
	}
	return "", false
}
