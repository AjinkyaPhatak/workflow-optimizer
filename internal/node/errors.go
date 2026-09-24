package node

import (
	"errors"
	"fmt"
)

// Sentinel registry errors.
var (
	ErrNodeAlreadyRegistered       = errors.New("node already registered")
	ErrNodeNotFound                = errors.New("node not found")
	ErrDefinitionAlreadyRegistered = errors.New("node definition already registered")
	ErrDefinitionNotFound          = errors.New("node definition not found")
	ErrInvalidNode                 = errors.New("invalid node")
	ErrInvalidDefinition           = errors.New("invalid node definition")
	ErrInconsistentType            = errors.New("node and definition types do not match")
	ErrMissingDefinition           = errors.New("missing node definition for registered node")
	ErrMissingNode                 = errors.New("missing executable node for registered definition")
	ErrRegistryValidation          = errors.New("registry validation failed")
)

// ErrorCode identifies the classification of an operational node failure.
type ErrorCode string

const (
	ErrCodeExecutionFailed ErrorCode = "EXECUTION_FAILED"
	ErrCodeInvalidInput    ErrorCode = "INVALID_INPUT"
	ErrCodeTimeout         ErrorCode = "TIMEOUT"
	ErrCodeRateLimited     ErrorCode = "RATE_LIMITED"
	ErrCodeUnavailable     ErrorCode = "UNAVAILABLE"
	ErrCodeConfiguration  ErrorCode = "CONFIGURATION_ERROR"
)

// NodeError communicates a structured failure from node execution.
// It signals whether an error is retryable to the execution engine.
// IMPORTANT: Nodes must NEVER perform workflow-level retries themselves;
// retry policies are owned strictly by the execution engine.
type NodeError struct {
	Code      ErrorCode      `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details,omitempty"`
	Err       error          `json:"-"`
}

// Error implements the standard error interface.
func (e *NodeError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

// Unwrap supports Go error wrapping chains (errors.Is and errors.As).
func (e *NodeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// NewNodeError constructs a NodeError without a wrapped cause.
func NewNodeError(code ErrorCode, message string, retryable bool) *NodeError {
	return &NodeError{
		Code:      code,
		Message:   message,
		Retryable: retryable,
	}
}

// WrapNodeError constructs a NodeError wrapping an underlying cause.
func WrapNodeError(code ErrorCode, message string, retryable bool, cause error) *NodeError {
	return &NodeError{
		Code:      code,
		Message:   message,
		Retryable: retryable,
		Err:       cause,
	}
}

// WithDetails attaches diagnostic metadata to the NodeError.
func (e *NodeError) WithDetails(details map[string]any) *NodeError {
	if e != nil {
		e.Details = details
	}
	return e
}

// IsRetryable checks whether the given error (or any wrapped error in its chain)
// indicates that the operation may be retried by the execution engine.
func IsRetryable(err error) bool {
	var nodeErr *NodeError
	if errors.As(err, &nodeErr) {
		return nodeErr.Retryable
	}
	return false
}
