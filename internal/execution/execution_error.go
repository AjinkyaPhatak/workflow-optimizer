package execution

import (
	"context"
	"errors"
	"fmt"
	"time"

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
	// Phase 10. CodeNodeTimeout: one node invocation exceeded its node
	// timeout (retryable; the execution deadline is CodeTimeout, which is
	// not). CodeWorkerLost: the worker owning the attempt stopped
	// heartbeating and its lease expired (retryable).
	CodeNodeTimeout = "NODE_TIMEOUT"
	CodeWorkerLost  = "WORKER_LOST"
)

// Error sources: which part of the system produced a failure.
const (
	SourceNode     = node.SourceNode
	SourceProvider = node.SourceProvider
	SourceWorker   = "worker"
	SourceDatabase = "database"
	SourceQueue    = "queue"
	SourceSystem   = "system"
)

// ExecutionError is the structured, persisted failure of an execution or node.
//
// Phase 10: the retry engine consumes Retryable; it never re-derives it.
// Source records where the failure came from (node, provider, worker,
// database, queue, system). RetryAfter carries a server's explicit delay
// (not persisted: the resulting schedule is).
//
// JSON field order is fixed by the struct, so serialization is deterministic.
type ExecutionError struct {
	Code       string        `json:"code"`
	Message    string        `json:"message"`
	NodeID     *string       `json:"node_id,omitempty"`
	Retryable  bool          `json:"retryable"`
	Source     string        `json:"source,omitempty"`
	RetryAfter time.Duration `json:"-"`
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

	out.Source = SourceSystem
	if nodeErr != nil {
		out.Source = SourceNode
	}

	var (
		typed   *node.NodeError
		timeout *NodeTimeoutError
	)
	switch {
	case errors.As(err, &timeout):
		// Checked before the context sentinels: a node timeout is not the
		// execution deadline.
		out.Code, out.Retryable, out.Source = CodeNodeTimeout, true, SourceNode
	case errors.As(err, &typed):
		out.Code = string(typed.Code)
		out.Retryable = typed.Retryable
		out.RetryAfter = typed.RetryAfter
		if typed.Source != "" {
			out.Source = typed.Source
		}
	case errors.Is(err, context.DeadlineExceeded):
		out.Code, out.Source = CodeTimeout, SourceSystem
	case errors.Is(err, context.Canceled):
		out.Code, out.Source = CodeCancelled, SourceSystem
	case errors.Is(err, ErrNodeImplementationMissing), errors.Is(err, ErrNodeDefinitionMissing):
		out.Code = CodeNodeNotRegistered
	case errors.Is(err, ErrGraphCycle), errors.Is(err, ErrInvalidPlanInput):
		out.Code = CodeInvalidWorkflow
	}

	// Side effects decide whether a transient failure may be repeated: a node
	// declaring unsafe side effects is re-run only when its error proves the
	// failed operation had no effect.
	if out.Retryable && nodeErr != nil && nodeErr.SideEffects == node.SideEffectsUnsafe &&
		(typed == nil || !typed.NotApplied) {
		out.Retryable = false
		out.RetryAfter = 0
		out.Message += " (not retried: the node has non-idempotent side effects and may have applied them)"
	}
	return out
}
