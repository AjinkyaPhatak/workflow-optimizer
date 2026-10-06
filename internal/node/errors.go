package node

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
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
	ErrCodeConfiguration   ErrorCode = "CONFIGURATION_ERROR"
	// ErrCodeInvalidCredentials: the credentials were rejected (never
	// retried: repeating the call cannot succeed).
	ErrCodeInvalidCredentials ErrorCode = "INVALID_CREDENTIALS"
	// ErrCodeUnsupported: the operation is not supported by the target.
	ErrCodeUnsupported ErrorCode = "UNSUPPORTED_OPERATION"
)

// Error sources: which part of the system a failure came from.
const (
	SourceNode     = "node"
	SourceProvider = "provider"
)

// NodeError communicates a structured failure from node execution.
// It signals whether an error is retryable to the execution engine.
// IMPORTANT: Nodes must NEVER perform workflow-level retries themselves;
// retry policies are owned strictly by the execution engine.
//
// Phase 10: the classification travels with the error. Retryable says the
// failure is transient; RetryAfter carries a server's explicit delay (e.g. an
// HTTP Retry-After header); Source says where it came from (SourceNode by
// default, SourceProvider for external services); NotApplied states that the
// failed operation is known to have had no external effect (e.g. the request
// was rejected with 429 before being processed), which makes re-running safe
// even for nodes whose definition declares unsafe side effects.
type NodeError struct {
	Code       ErrorCode      `json:"code"`
	Message    string         `json:"message"`
	Retryable  bool           `json:"retryable"`
	Details    map[string]any `json:"details,omitempty"`
	Err        error          `json:"-"`
	Source     string         `json:"source,omitempty"`
	RetryAfter time.Duration  `json:"-"`
	NotApplied bool           `json:"-"`
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

// HTTPStatusError classifies an unsuccessful HTTP response from an external
// service. It is the single place where status codes map to retryability, so
// integration nodes never re-implement it:
//
//	408, 429, 500, 502, 503, 504 and other 5xx  -> retryable
//	401, 403                                     -> INVALID_CREDENTIALS, never retried
//	501                                          -> UNSUPPORTED_OPERATION, never retried
//	other 4xx                                    -> INVALID_INPUT, never retried
//
// 429 is marked NotApplied: the server refused the request without processing
// it. retryAfter (from the Retry-After header, see ParseRetryAfter) is kept.
func HTTPStatusError(status int, retryAfter time.Duration, message string) *NodeError {
	e := &NodeError{Message: message, Source: SourceProvider, Details: map[string]any{"status_code": status}}
	switch {
	case status == 429:
		e.Code, e.Retryable, e.NotApplied = ErrCodeRateLimited, true, true
	case status == 408:
		e.Code, e.Retryable = ErrCodeTimeout, true
	case status == 401 || status == 403:
		e.Code = ErrCodeInvalidCredentials
	case status == 501:
		e.Code = ErrCodeUnsupported
	case status >= 500 && status <= 599:
		e.Code, e.Retryable = ErrCodeUnavailable, true
	case status >= 400 && status <= 499:
		e.Code = ErrCodeInvalidInput
	default:
		e.Code = ErrCodeExecutionFailed
	}
	if e.Retryable && retryAfter > 0 {
		e.RetryAfter = retryAfter
	}
	return e
}

// retryAfterDateLayout is the HTTP-date format (RFC 7231 IMF-fixdate).
const retryAfterDateLayout = "Mon, 02 Jan 2006 15:04:05 GMT"

// MaxRetryAfter caps a server's Retry-After, in either form.
const MaxRetryAfter = 24 * time.Hour

// ParseRetryAfter parses an HTTP Retry-After value: delay-seconds or an
// HTTP-date relative to now. It reports false for empty or malformed values;
// a date in the past yields zero. Both forms are capped at MaxRetryAfter.
func ParseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if secs, err := strconv.ParseInt(value, 10, 64); err == nil {
		if secs < 0 {
			return 0, false
		}
		if secs > int64(MaxRetryAfter/time.Second) {
			secs = int64(MaxRetryAfter / time.Second)
		}
		return time.Duration(secs) * time.Second, true
	}
	t, err := time.Parse(retryAfterDateLayout, value)
	if err != nil {
		return 0, false
	}
	d := t.Sub(now)
	switch {
	case d <= 0:
		return 0, true
	case d > MaxRetryAfter:
		return MaxRetryAfter, true
	}
	return d, true
}
