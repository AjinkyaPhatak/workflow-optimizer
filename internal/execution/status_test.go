package execution_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/node"
)

var allExecutionStatuses = []execution.ExecutionStatus{
	execution.StatusPending, execution.StatusRunning, execution.StatusCompleted,
	execution.StatusFailed, execution.StatusCancelled,
}

var allNodeStatuses = []execution.NodeExecutionStatus{
	execution.NodeStatusPending, execution.NodeStatusRunning, execution.NodeStatusCompleted,
	execution.NodeStatusFailed, execution.NodeStatusSkipped,
}

func TestExecutionTransitionMatrix(t *testing.T) {
	valid := map[[2]execution.ExecutionStatus]bool{
		{execution.StatusPending, execution.StatusRunning}:   true,
		{execution.StatusRunning, execution.StatusCompleted}: true,
		{execution.StatusRunning, execution.StatusFailed}:    true,
		{execution.StatusRunning, execution.StatusCancelled}: true,
		// Phase 10: a failed attempt may be scheduled for retry (or recovered
		// after its worker died); the execution waits PENDING again.
		{execution.StatusRunning, execution.StatusPending}: true,
	}
	// Exhaustive 5x5 matrix: exactly the five legal transitions.
	for _, from := range allExecutionStatuses {
		for _, to := range allExecutionStatuses {
			if got, want := from.CanTransitionTo(to), valid[[2]execution.ExecutionStatus{from, to}]; got != want {
				t.Errorf("%s -> %s: CanTransitionTo = %v, want %v", from, to, got, want)
			}
		}
	}
	if execution.ExecutionStatus("BOGUS").Valid() || execution.ExecutionStatus("BOGUS").CanTransitionTo(execution.StatusRunning) {
		t.Fatal("unknown status must be invalid")
	}
}

func TestExecutionTerminalStatesAreImmutable(t *testing.T) {
	for _, s := range allExecutionStatuses {
		terminal := s == execution.StatusCompleted || s == execution.StatusFailed || s == execution.StatusCancelled
		if s.IsTerminal() != terminal {
			t.Errorf("%s.IsTerminal() = %v", s, s.IsTerminal())
		}
		if terminal {
			for _, to := range allExecutionStatuses {
				if s.CanTransitionTo(to) {
					t.Errorf("terminal %s may not transition to %s", s, to)
				}
			}
		}
	}
}

func TestNodeTransitionMatrix(t *testing.T) {
	valid := map[[2]execution.NodeExecutionStatus]bool{
		{execution.NodeStatusPending, execution.NodeStatusRunning}:   true,
		{execution.NodeStatusRunning, execution.NodeStatusCompleted}: true,
		{execution.NodeStatusRunning, execution.NodeStatusFailed}:    true,
		{execution.NodeStatusRunning, execution.NodeStatusSkipped}:   true,
	}
	for _, from := range allNodeStatuses {
		for _, to := range allNodeStatuses {
			if got, want := from.CanTransitionTo(to), valid[[2]execution.NodeExecutionStatus{from, to}]; got != want {
				t.Errorf("%s -> %s: CanTransitionTo = %v, want %v", from, to, got, want)
			}
		}
		terminal := from == execution.NodeStatusCompleted || from == execution.NodeStatusFailed || from == execution.NodeStatusSkipped
		if from.IsTerminal() != terminal {
			t.Errorf("%s.IsTerminal() = %v", from, from.IsTerminal())
		}
	}
}

func TestExecutionErrorSerializationIsDeterministic(t *testing.T) {
	id := "llm_1"
	e := execution.ExecutionError{Code: "RATE_LIMITED", Message: "slow down", NodeID: &id, Retryable: true}
	want := `{"code":"RATE_LIMITED","message":"slow down","node_id":"llm_1","retryable":true}`
	for i := 0; i < 20; i++ {
		b, err := json.Marshal(e)
		if err != nil || string(b) != want {
			t.Fatalf("marshal = %s, %v; want %s", b, err, want)
		}
	}
	noNode, _ := json.Marshal(execution.ExecutionError{Code: "X", Message: "m"})
	if string(noNode) != `{"code":"X","message":"m","retryable":false}` {
		t.Fatalf("marshal without node = %s", noNode)
	}
	var back execution.ExecutionError
	if err := json.Unmarshal([]byte(want), &back); err != nil || *back.NodeID != "llm_1" || !back.Retryable {
		t.Fatalf("round trip = %+v, %v", back, err)
	}
	if err := (execution.ExecutionError{Code: "X"}).Validate(); !errors.Is(err, execution.ErrInvalidExecution) {
		t.Fatalf("missing message must be invalid: %v", err)
	}
}

func TestErrorFromExecutionPreservesPhase7Details(t *testing.T) {
	nodeErr := node.NewNodeError(node.ErrCodeRateLimited, "429", true)
	cases := []struct {
		name      string
		err       error
		code      string
		nodeID    string
		retryable bool
	}{
		{"node error keeps code and retryable", &execution.NodeExecutionError{NodeID: "llm_1", NodeType: "llm", Stage: execution.StageExecute, Err: nodeErr},
			string(node.ErrCodeRateLimited), "llm_1", true},
		{"plain node failure", &execution.NodeExecutionError{NodeID: "B", Stage: execution.StageExecute, Err: errors.New("boom")},
			execution.CodeNodeFailed, "B", false},
		{"variable resolution", &execution.NodeExecutionError{NodeID: "C", Stage: execution.StageResolveConfig, Err: execution.ErrUnresolvedReference},
			execution.CodeVariableResolution, "C", false},
		{"input resolution", &execution.NodeExecutionError{NodeID: "D", Stage: execution.StageResolveInputs, Err: execution.ErrMissingSourcePort},
			execution.CodeInputResolution, "D", false},
		{"cancellation", fmt.Errorf("execution cancelled before node %q: %w", "E", context.Canceled), execution.CodeCancelled, "", false},
		{"node timeout", &execution.NodeExecutionError{NodeID: "F", Stage: execution.StageExecute, Err: context.DeadlineExceeded},
			execution.CodeTimeout, "F", false},
		{"missing registration", fmt.Errorf("%w: x", execution.ErrNodeImplementationMissing), execution.CodeNodeNotRegistered, "", false},
		{"cycle", fmt.Errorf("%w: x", execution.ErrGraphCycle), execution.CodeInvalidWorkflow, "", false},
		{"other", errors.New("?"), execution.CodeExecutionFailed, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := execution.ErrorFromExecution(tc.err)
			if got.Code != tc.code || got.Retryable != tc.retryable || got.Message == "" {
				t.Fatalf("got %+v", got)
			}
			if tc.nodeID == "" && got.NodeID != nil || tc.nodeID != "" && (got.NodeID == nil || *got.NodeID != tc.nodeID) {
				t.Fatalf("node id = %v, want %q", got.NodeID, tc.nodeID)
			}
		})
	}
}
