package integration

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"workflow-optimizer/internal/node"
)

// Kind is the normalized category of an integration failure. Its value is
// the error code that reaches the execution record (node.NodeError.Code).
type Kind string

const (
	KindAuthentication      Kind = "AUTHENTICATION_FAILED" // expired or rejected credentials (401)
	KindPermissionDenied    Kind = "PERMISSION_DENIED"     // authenticated but not allowed (403)
	KindRateLimited         Kind = "RATE_LIMITED"          // 429 (= node.ErrCodeRateLimited)
	KindNotFound            Kind = "RESOURCE_NOT_FOUND"    // 404, 410
	KindInvalidRequest      Kind = "INVALID_REQUEST"       // other 4xx, unsupported operations
	KindProviderUnavailable Kind = "PROVIDER_UNAVAILABLE"  // 5xx, connection failures
	KindTimeout             Kind = "TIMEOUT"               // 408, the client's own timeout (= node.ErrCodeTimeout)
	KindMalformedResponse   Kind = "MALFORMED_RESPONSE"    // the provider answered something unreadable
	KindUnknown             Kind = "INTEGRATION_ERROR"
)

// Retryable reports whether a failure of this kind is transient. It is a
// classification, not a decision: the Phase 10 engine decides whether to
// retry, and never re-runs an action with unsafe side effects unless the
// error is NotApplied.
//
// Unknown failures are not retryable: when it is not known what happened,
// repeating a call to an external system is not assumed to be safe.
func (k Kind) Retryable() bool {
	switch k {
	case KindRateLimited, KindProviderUnavailable, KindTimeout:
		return true
	}
	return false
}

// Error is an integration failure normalized to platform semantics. The
// provider-specific client classifies (Kind, RetryAfter, NotApplied); it
// writes Message itself and must never put secrets, request headers or raw
// response bodies into it. Err is an optional non-sensitive cause; it is
// available to errors.Is/As but never part of Error().
type Error struct {
	Integration string
	Action      string
	Kind        Kind
	Message     string
	StatusCode  int // HTTP status, 0 when none
	// RetryAfter is the provider's explicit delay (Retry-After), if any.
	RetryAfter time.Duration
	// NotApplied states the provider refused the request without processing
	// it, so even an unsafe action may be repeated (e.g. 429).
	NotApplied bool
	Err        error
}

func (e *Error) Error() string {
	msg := string(e.Kind)
	if e.Integration != "" {
		msg = NodeType(e.Integration, e.Action) + ": " + msg
	}
	if e.StatusCode != 0 {
		msg += fmt.Sprintf(" (HTTP %d)", e.StatusCode)
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

// NewError builds an Error of the given kind.
func NewError(kind Kind, message string) *Error {
	return &Error{Kind: kind, Message: message}
}

// FromHTTPStatus classifies an unsuccessful HTTP response. Provider clients
// call it with the status and the parsed Retry-After (node.ParseRetryAfter);
// they may reclassify provider-specific bodies (e.g. a 403 that means rate
// limiting) before returning.
//
//	401          -> authentication failure, not retryable
//	403          -> permission denied, not retryable
//	404, 410     -> resource not found, not retryable
//	408          -> timeout, retryable
//	429          -> rate limited, retryable, not applied
//	501          -> invalid request (unsupported), not retryable
//	other 5xx    -> provider unavailable, retryable
//	other 4xx    -> invalid request, not retryable
func FromHTTPStatus(status int, retryAfter time.Duration, message string) *Error {
	e := &Error{StatusCode: status, Message: message}
	switch {
	case status == 401:
		e.Kind = KindAuthentication
	case status == 403:
		e.Kind = KindPermissionDenied
	case status == 404 || status == 410:
		e.Kind = KindNotFound
	case status == 408:
		e.Kind = KindTimeout
	case status == 429:
		e.Kind, e.NotApplied = KindRateLimited, true
	case status == 501:
		e.Kind = KindInvalidRequest
	case status >= 500 && status <= 599:
		e.Kind = KindProviderUnavailable
	case status >= 400 && status <= 499:
		e.Kind = KindInvalidRequest
	default:
		e.Kind = KindUnknown
	}
	if e.Kind.Retryable() && retryAfter > 0 {
		e.RetryAfter = retryAfter
	}
	return e
}

// ToNodeError converts an action's failure into what the execution engine
// reads (*node.NodeError, Source "provider"):
//
//   - cancellation of ctx passes through unchanged, so the engine sees a
//     cancelled run (or its deadline) as such;
//   - a *node.NodeError (e.g. a configuration or credential problem) is kept;
//   - an *Error keeps its kind, retryability, Retry-After and NotApplied;
//   - a timeout that is not ctx's (the client's own) becomes KindTimeout;
//   - a network error becomes KindProviderUnavailable;
//   - anything else is KindUnknown.
//
// The resulting message never includes the text of an unclassified cause
// (Error.Error omits Err), only the client-written classified message.
func ToNodeError(ctx context.Context, integrationID, actionID string, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return err
	}
	var typed *node.NodeError
	if errors.As(err, &typed) {
		return err
	}
	var ie *Error
	if !errors.As(err, &ie) {
		ie = &Error{Kind: KindUnknown, Message: "the request failed", Err: err}
		var netErr net.Error
		switch {
		case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
			ie.Kind, ie.Message = KindTimeout, "the request timed out"
		case errors.As(err, &netErr):
			ie.Kind, ie.Message = KindProviderUnavailable, "the service could not be reached"
		}
	}
	if ie.Integration == "" {
		cp := *ie
		cp.Integration, cp.Action = integrationID, actionID
		ie = &cp
	}
	ne := node.WrapNodeError(node.ErrorCode(ie.Kind), "integration request failed", ie.Kind.Retryable(), ie)
	ne.Source = node.SourceProvider
	ne.RetryAfter = ie.RetryAfter
	ne.NotApplied = ie.NotApplied
	ne.Details = map[string]any{"integration": ie.Integration, "action": ie.Action, "kind": string(ie.Kind)}
	if ie.StatusCode != 0 {
		ne.Details["status_code"] = ie.StatusCode
	}
	return ne
}
