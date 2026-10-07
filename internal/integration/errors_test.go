package integration_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/integration"
	"workflow-optimizer/internal/node"
)

func TestFromHTTPStatusClassification(t *testing.T) {
	cases := []struct {
		status     int
		kind       integration.Kind
		retryable  bool
		notApplied bool
	}{
		{401, integration.KindAuthentication, false, false},
		{403, integration.KindPermissionDenied, false, false},
		{404, integration.KindNotFound, false, false},
		{410, integration.KindNotFound, false, false},
		{400, integration.KindInvalidRequest, false, false},
		{422, integration.KindInvalidRequest, false, false},
		{409, integration.KindInvalidRequest, false, false},
		{501, integration.KindInvalidRequest, false, false},
		{408, integration.KindTimeout, true, false},
		{429, integration.KindRateLimited, true, true},
		{500, integration.KindProviderUnavailable, true, false},
		{502, integration.KindProviderUnavailable, true, false},
		{503, integration.KindProviderUnavailable, true, false},
		{504, integration.KindProviderUnavailable, true, false},
		{302, integration.KindUnknown, false, false},
	}
	for _, c := range cases {
		e := integration.FromHTTPStatus(c.status, 7*time.Second, "msg")
		if e.Kind != c.kind || e.Kind.Retryable() != c.retryable || e.NotApplied != c.notApplied || e.StatusCode != c.status {
			t.Errorf("%d: %+v (retryable %v)", c.status, e, e.Kind.Retryable())
		}
		if c.retryable && e.RetryAfter != 7*time.Second || !c.retryable && e.RetryAfter != 0 {
			t.Errorf("%d: RetryAfter %v", c.status, e.RetryAfter)
		}
	}
	if integration.KindMalformedResponse.Retryable() || integration.KindUnknown.Retryable() {
		t.Fatal("malformed and unknown failures are not transient")
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

var _ net.Error = timeoutErr{}

func nodeErr(t *testing.T, err error) *node.NodeError {
	t.Helper()
	var ne *node.NodeError
	if !errors.As(err, &ne) {
		t.Fatalf("not a NodeError: %v", err)
	}
	return ne
}

func TestToNodeError(t *testing.T) {
	ctx := context.Background()

	ne := nodeErr(t, integration.ToNodeError(ctx, "gmail", "search", integration.FromHTTPStatus(429, 3*time.Second, "slow down")))
	if ne.Code != "RATE_LIMITED" || !ne.Retryable || !ne.NotApplied || ne.RetryAfter != 3*time.Second || ne.Source != node.SourceProvider {
		t.Fatalf("rate limited: %+v", ne)
	}
	if ne.Details["integration"] != "gmail" || ne.Details["action"] != "search" || ne.Details["kind"] != "RATE_LIMITED" || ne.Details["status_code"] != 429 {
		t.Fatalf("details: %v", ne.Details)
	}
	var ie *integration.Error
	if !errors.As(ne, &ie) || ie.Kind != integration.KindRateLimited {
		t.Fatal("the classified error stays in the chain")
	}

	for name, c := range map[string]struct {
		err       error
		code      string
		retryable bool
	}{
		"auth":         {integration.FromHTTPStatus(401, 0, "expired"), "AUTHENTICATION_FAILED", false},
		"permission":   {integration.FromHTTPStatus(403, 0, "no scope"), "PERMISSION_DENIED", false},
		"not found":    {integration.FromHTTPStatus(404, 0, "gone"), "RESOURCE_NOT_FOUND", false},
		"invalid":      {integration.FromHTTPStatus(400, 0, "bad"), "INVALID_REQUEST", false},
		"unavailable":  {integration.FromHTTPStatus(503, 0, "down"), "PROVIDER_UNAVAILABLE", true},
		"malformed":    {integration.NewError(integration.KindMalformedResponse, "not JSON"), "MALFORMED_RESPONSE", false},
		"own timeout":  {fmt.Errorf("call: %w", context.DeadlineExceeded), "TIMEOUT", true},
		"net timeout":  {&net.OpError{Op: "read", Err: timeoutErr{}}, "TIMEOUT", true},
		"net failure":  {&net.OpError{Op: "dial", Err: errors.New("connection refused")}, "PROVIDER_UNAVAILABLE", true},
		"unclassified": {errors.New("weird"), "INTEGRATION_ERROR", false},
	} {
		ne := nodeErr(t, integration.ToNodeError(ctx, "gmail", "search", c.err))
		if string(ne.Code) != c.code || ne.Retryable != c.retryable {
			t.Errorf("%s: code %s retryable %v, want %s %v", name, ne.Code, ne.Retryable, c.code, c.retryable)
		}
	}

	// A node error (configuration, credentials) is kept as is.
	cfg := node.NewNodeError(node.ErrCodeConfiguration, "bad config", false)
	if got := integration.ToNodeError(ctx, "gmail", "search", cfg); got != error(cfg) {
		t.Fatalf("node error replaced: %v", got)
	}

	// Cancellation of the run passes through untouched, so the engine sees
	// a cancelled (or timed-out) execution, not an integration failure.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if got := integration.ToNodeError(cctx, "gmail", "search", fmt.Errorf("x: %w", context.Canceled)); !errors.Is(got, context.Canceled) || errors.As(got, new(*node.NodeError)) {
		t.Fatalf("cancellation: %v", got)
	}
	if integration.ToNodeError(ctx, "gmail", "search", nil) != nil {
		t.Fatal("nil error")
	}
}

func TestErrorTextNeverIncludesUnclassifiedCauses(t *testing.T) {
	const secret = "ya29.SECRET-ACCESS-TOKEN-0123456789"
	cause := errors.New("POST https://api.example.test failed; Authorization: Bearer " + secret)
	err := integration.ToNodeError(context.Background(), "gmail", "send", cause)
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "Authorization") {
		t.Fatalf("cause leaked into the error text: %v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatal("the cause stays reachable for errors.Is")
	}
	ee := execution.ErrorFromExecution(&execution.NodeExecutionError{NodeID: "n", NodeType: "gmail.send", Stage: execution.StageExecute, Err: err})
	if strings.Contains(ee.Message, secret) {
		t.Fatalf("persisted execution error leaks: %s", ee.Message)
	}
	wrapped := &integration.Error{Kind: integration.KindProviderUnavailable, Message: "upstream failed", Err: cause}
	if strings.Contains(wrapped.Error(), secret) {
		t.Fatalf("Error() includes Err: %v", wrapped)
	}
}

// The integration classifies; the existing Phase 10 engine decides. A
// transient failure of an unsafe action (send) is not repeated unless the
// provider refused it without applying it (429).
func TestClassificationFeedsTheExistingRetryEngine(t *testing.T) {
	ctx := context.Background()
	failed := func(side node.SideEffects, cause error) execution.ExecutionError {
		return execution.ErrorFromExecution(&execution.NodeExecutionError{
			NodeID: "n", NodeType: "gmail.x", Stage: execution.StageExecute, SideEffects: side,
			Err: integration.ToNodeError(ctx, "gmail", "x", cause),
		})
	}
	backoff := fixedBackoff(time.Second)
	decide := func(e execution.ExecutionError) execution.FailureAction {
		return execution.DecideFailure(execution.AttemptState{Attempt: 1, MaxAttempts: 3, Now: time.Now()}, e, backoff).Action
	}
	unavailable := integration.FromHTTPStatus(503, 0, "down")
	if e := failed(node.SideEffectsNone, unavailable); !e.Retryable || e.Source != node.SourceProvider || decide(e) != execution.ActionRetry {
		t.Fatalf("read + 503 should be retried: %+v", e)
	}
	if e := failed(node.SideEffectsUnsafe, unavailable); e.Retryable || decide(e) != execution.ActionFail {
		t.Fatalf("send + 503 must not be repeated: %+v", e)
	}
	if e := failed(node.SideEffectsUnsafe, integration.FromHTTPStatus(429, 2*time.Second, "slow")); !e.Retryable || e.RetryAfter != 2*time.Second || decide(e) != execution.ActionRetry {
		t.Fatalf("send + 429 (not applied) may be retried: %+v", e)
	}
	if e := failed(node.SideEffectsNone, integration.FromHTTPStatus(401, 0, "expired")); e.Retryable || e.Code != "AUTHENTICATION_FAILED" || decide(e) != execution.ActionFail {
		t.Fatalf("401 is final: %+v", e)
	}
	if e := failed(node.SideEffectsIdempotent, integration.FromHTTPStatus(502, 0, "bad gateway")); !e.Retryable {
		t.Fatalf("idempotent + 502 may be retried: %+v", e)
	}
}

type fixedBackoff time.Duration

func (f fixedBackoff) Delay(int, time.Duration) time.Duration { return time.Duration(f) }
