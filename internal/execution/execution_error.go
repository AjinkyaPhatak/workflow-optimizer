package execution

import (
	"context"
	"errors"
	"fmt"

	"workflow-optimizer/internal/node"
)

// Execution error codes produced by the lifecycle layer. Node-reported codes
// (node.ErrorCode) are preserved verbatim.
const (
	CodeExecutionFailed    = "EXECUTION_FAILED"
	CodeNodeFailed         = "NODE_EXECUTION_FAILED"
	CodeVariableResolution = "VARIABLE_RESOLUTION_FAILED"
	CodeInputResolution    = "INPUT_RESOLUTION_FAILED"
	CodeNodeObservation    = "NODE_RECORDING_FAILED"
	CodeInvalidWorkflow    = "INVALID_WORKFLOW"
	CodeNodeNotRegistered  = "NODE_NOT_REGISTERED"
	CodeTimeout            = "EXECUTION_TIMEOUT"
	CodeCancelled          = "EXECUTION_CANCELLED"
	// CodeDefinitionLoadFailed: the version definition could not be loaded
	// (transient). CodeNodeInterrupted: a node was still RUNNING when its
	// execution was finalized, so its outcome was not recorded.
	CodeDefinitionLoadFailed = "DEFINITION_LOAD_FAILED"
	CodeNodeInterrupted      = "NODE_INTERRUPTED"
)

// ExecutionError is the structured, persisted failure of an execution or node.
// Retryable is metadata only; Phase 8 never retries.
//
// JSON field order is fixed by the struct, so serialization is deterministic.
type ExecutionError struct {
	Code      string  `json:"code"`
	Message   string  `json:"message"`
	NodeID    *string `json:"node_id,omitempty"`
	Retryable bool    `json:"retryable"`
}

func (e *ExecutionError) Error() string {
	if e.NodeID != nil {
		return fmt.Sprintf("[%s] node %q: %s", e.Code, *e.NodeID, e.Message)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

// Validate requires a code and a message.
func (e ExecutionError) Validate() error {
	if e.Code == "" || e.Message == "" {
		return fmt.Errorf("%w: execution error requires code and message", ErrInvalidExecution)
	}
	return nil
}

// ErrorFromExecution converts a Phase 7 GraphExecutor (or lifecycle) error into
// a structured ExecutionError, preserving the failing node ID and any
// node-reported code and retryability.
func ErrorFromExecution(err error) ExecutionError {
	out := ExecutionError{Code: CodeExecutionFailed, Message: err.Error()}

	var nodeErr *NodeExecutionError
	if errors.As(err, &nodeErr) {
		id := nodeErr.NodeID
		out.NodeID = &id
		switch nodeErr.Stage {
		case StageResolveConfig:
			out.Code = CodeVariableResolution
		case StageResolveInputs:
			out.Code = CodeInputResolution
		case StageObserve:
			out.Code = CodeNodeObservation
		default:
			out.Code = CodeNodeFailed
		}
	}

	var typed *node.NodeError
	switch {
	case errors.As(err, &typed):
		out.Code = string(typed.Code)
		out.Retryable = typed.Retryable
	case errors.Is(err, context.DeadlineExceeded):
		out.Code = CodeTimeout
	case errors.Is(err, context.Canceled):
		out.Code = CodeCancelled
	case errors.Is(err, ErrNodeImplementationMissing), errors.Is(err, ErrNodeDefinitionMissing):
		out.Code = CodeNodeNotRegistered
	case errors.Is(err, ErrGraphCycle), errors.Is(err, ErrInvalidPlanInput):
		out.Code = CodeInvalidWorkflow
	}
	return out
}
