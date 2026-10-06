package execution_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/node"
)

// Phase 10 unit tests for the executor's node reliability (timeouts, in-place
// retries, resume) and for the shared retry decision.

// linearBackoff is a deterministic Backoff: attempt * step, never below
// retryAfter.
type linearBackoff struct{ step time.Duration }

func (b linearBackoff) Delay(failures int, retryAfter time.Duration) time.Duration {
	d := time.Duration(failures) * b.step
	if retryAfter > d {
		return retryAfter
	}
	return d
}

func nodeRetry(max int) execution.NodeRetry {
	return execution.NodeRetry{MaxInvocations: max, Backoff: linearBackoff{time.Millisecond}, MaxInlineDelay: time.Second}
}

// runOpts runs a validated in -> A -> out graph with options.
func runOpts(t *testing.T, e *env, ctx context.Context, opts execution.ExecuteOptions) (execution.ExecutionResult, error) {
	t.Helper()
	def := graph(nodes(entry("in"), probe("A"), exit("out")),
		edge("in", "data", "A", "in"), edge("A", "out", "out", "value"))
	e.mustValid(t, def)
	return e.exec.ExecuteWithOptions(ctx, def, map[string]any{}, opts)
}

func unavailable() error {
	return node.HTTPStatusError(503, 0, "service unavailable")
}

// ---------------------------------------------------------------------------
// Node timeout
// ---------------------------------------------------------------------------

func TestNodeTimeoutCancelsNodeAndIsStructured(t *testing.T) {
	e := newEnv(t)
	var sawDeadline atomic.Bool
	e.rec.hooks["A"] = func(ctx context.Context) error {
		if _, ok := ctx.Deadline(); ok {
			sawDeadline.Store(true)
		}
		<-ctx.Done() // a well-behaved node stops when its context ends
		return ctx.Err()
	}
	start := time.Now()
	_, err := runOpts(t, e, context.Background(), execution.ExecuteOptions{NodeTimeout: 30 * time.Millisecond})
	if time.Since(start) > 2*time.Second {
		t.Fatal("node timeout did not stop the node")
	}
	var nodeErr *execution.NodeExecutionError
	var timeout *execution.NodeTimeoutError
	if !errors.As(err, &nodeErr) || nodeErr.NodeID != "A" || !errors.As(err, &timeout) || !errors.Is(err, execution.ErrNodeTimeout) {
		t.Fatalf("err = %v", err)
	}
	if !sawDeadline.Load() {
		t.Fatal("node context carried no deadline")
	}
	ee := execution.ErrorFromExecution(err)
	if ee.Code != execution.CodeNodeTimeout || !ee.Retryable || ee.Source != execution.SourceNode || *ee.NodeID != "A" {
		t.Fatalf("classification = %+v", ee)
	}
}

// The node context derives from the execution context: a node timeout longer
// than the remaining execution time never extends it, and the failure is then
// the execution's own deadline, not a (retryable) node timeout.
func TestNodeTimeoutNeverExtendsExecutionDeadline(t *testing.T) {
	e := newEnv(t)
	var nodeDeadline time.Time
	e.rec.hooks["A"] = func(ctx context.Context) error {
		nodeDeadline, _ = ctx.Deadline()
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	execDeadline, _ := ctx.Deadline()
	_, err := runOpts(t, e, ctx, execution.ExecuteOptions{NodeTimeout: time.Hour, NodeRetry: nodeRetry(5)})
	if nodeDeadline.After(execDeadline) {
		t.Fatalf("node deadline %v extends the execution deadline %v", nodeDeadline, execDeadline)
	}
	if errors.Is(err, execution.ErrNodeTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the execution deadline", err)
	}
	if got := e.rec.labels(); len(got) != 1 {
		t.Fatalf("node retried after the execution deadline: %v", got)
	}
}

// A node that ignores its context and returns a result after its timeout does
// not get that result accepted.
func TestLateResultAfterNodeTimeoutIsRejected(t *testing.T) {
	e := newEnv(t)
	e.rec.hooks["A"] = func(ctx context.Context) error {
		time.Sleep(60 * time.Millisecond) // ignores ctx
		return nil
	}
	_, err := runOpts(t, e, context.Background(), execution.ExecuteOptions{NodeTimeout: 10 * time.Millisecond})
	if !errors.Is(err, execution.ErrNodeTimeout) {
		t.Fatalf("late result accepted: %v", err)
	}
}

// ---------------------------------------------------------------------------
// In-place node retries
// ---------------------------------------------------------------------------

func TestRetryableNodeFailureIsRetriedInPlace(t *testing.T) {
	e := newEnv(t)
	var calls atomic.Int32
	var inputs []node.NodeInput
	e.rec.hooks["A"] = func(context.Context) error {
		if calls.Add(1) < 3 {
			return unavailable()
		}
		return nil
	}
	res, err := runOpts(t, e, context.Background(), execution.ExecuteOptions{
		NodeRetry: nodeRetry(3), OperationKey: "exec-1", PriorInvocations: map[string]int{"A": 4},
	})
	if err != nil || calls.Load() != 3 {
		t.Fatalf("err = %v, calls = %d", err, calls.Load())
	}
	for _, c := range e.rec.calls {
		inputs = append(inputs, c.Input)
	}
	for i, in := range inputs {
		if in.IdempotencyKey != "exec-1/A" {
			t.Fatalf("invocation %d key %q: must be stable across attempts", i, in.IdempotencyKey)
		}
		if in.Attempt != 4+i+1 {
			t.Fatalf("invocation %d attempt %d, want %d", i, in.Attempt, 4+i+1)
		}
	}
	if len(res.State.Completed) != 3 {
		t.Fatalf("completed = %v", res.State.Completed)
	}
}

func TestNodeRetriesAreBounded(t *testing.T) {
	e := newEnv(t)
	var calls atomic.Int32
	e.rec.hooks["A"] = func(context.Context) error { calls.Add(1); return unavailable() }
	_, err := runOpts(t, e, context.Background(), execution.ExecuteOptions{NodeRetry: nodeRetry(3)})
	if err == nil || calls.Load() != 3 {
		t.Fatalf("err = %v, calls = %d (want exactly 3)", err, calls.Load())
	}
	if ee := execution.ErrorFromExecution(err); !ee.Retryable || ee.Code != string(node.ErrCodeUnavailable) {
		t.Fatalf("final error must stay retryable for execution-level retry: %+v", ee)
	}
}

func TestNonRetryableNodeFailureFailsImmediately(t *testing.T) {
	for name, fail := range map[string]error{
		"invalid credentials": node.HTTPStatusError(401, 0, "bad key"),
		"invalid input":       node.NewNodeError(node.ErrCodeInvalidInput, "bad", false),
		"plain error":         errBoom,
		"panic":               nil,
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			var calls atomic.Int32
			e.rec.hooks["A"] = func(context.Context) error {
				calls.Add(1)
				if fail == nil {
					panic("boom")
				}
				return fail
			}
			_, err := runOpts(t, e, context.Background(), execution.ExecuteOptions{NodeRetry: nodeRetry(5)})
			if err == nil || calls.Load() != 1 {
				t.Fatalf("err = %v, calls = %d (want 1)", err, calls.Load())
			}
		})
	}
}

func TestCancellationPreventsNodeRetry(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	e.rec.hooks["A"] = func(context.Context) error {
		calls.Add(1)
		cancel()
		return unavailable()
	}
	_, err := runOpts(t, e, ctx, execution.ExecuteOptions{NodeRetry: nodeRetry(5)})
	if err == nil || calls.Load() != 1 {
		t.Fatalf("retried after cancellation: calls=%d err=%v", calls.Load(), err)
	}
}

func TestNodeRetryNeverWaitsPastTheDeadline(t *testing.T) {
	e := newEnv(t)
	var calls atomic.Int32
	e.rec.hooks["A"] = func(context.Context) error {
		calls.Add(1)
		return node.HTTPStatusError(503, 10*time.Second, "later") // Retry-After beyond the deadline
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := runOpts(t, e, ctx, execution.ExecuteOptions{
		NodeRetry: execution.NodeRetry{MaxInvocations: 5, Backoff: linearBackoff{time.Millisecond}, MaxInlineDelay: time.Minute},
	})
	if err == nil || calls.Load() != 1 || time.Since(start) > time.Second {
		t.Fatalf("calls=%d err=%v after %v", calls.Load(), err, time.Since(start))
	}
}

// A pause longer than MaxInlineDelay is not waited for on the worker: the
// failure is returned (for execution-level retry, which releases the worker).
func TestLongNodeBackoffIsNotWaitedInPlace(t *testing.T) {
	e := newEnv(t)
	var calls atomic.Int32
	e.rec.hooks["A"] = func(context.Context) error {
		calls.Add(1)
		return node.HTTPStatusError(429, time.Minute, "slow down")
	}
	start := time.Now()
	_, err := runOpts(t, e, context.Background(), execution.ExecuteOptions{NodeRetry: nodeRetry(5)})
	if calls.Load() != 1 || time.Since(start) > time.Second {
		t.Fatalf("waited in place: calls=%d after %v", calls.Load(), time.Since(start))
	}
	if ee := execution.ErrorFromExecution(err); !ee.Retryable || ee.RetryAfter != time.Minute {
		t.Fatalf("Retry-After must reach the execution-level decision: %+v", ee)
	}
}

// ---------------------------------------------------------------------------
// Resume: completed nodes are not re-run
// ---------------------------------------------------------------------------

func TestCompletedNodesAreNotRerun(t *testing.T) {
	e := newEnv(t)
	def := graph(nodes(entry("in"), probe("A"), probe("B"), exit("out")),
		edge("in", "data", "A", "in"), edge("A", "out", "B", "in"), edge("B", "out", "out", "value"))
	e.mustValid(t, def)
	priorA := node.NewNodeOutput(map[string]node.Value{"out": node.NewJSONValue(map[string]any{"label": "A-from-attempt-1"})})
	priorIn := node.NewNodeOutput(map[string]node.Value{"data": node.NewJSONValue(map[string]any{})})
	res, err := e.exec.ExecuteWithOptions(context.Background(), def, map[string]any{}, execution.ExecuteOptions{
		Completed: map[string]node.NodeOutput{"in": priorIn, "A": priorA},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := e.rec.labels(); !equalStrings(got, []string{"B"}) {
		t.Fatalf("executed %v, want only B", got)
	}
	// B received A's persisted output exactly as if A had just run.
	if l := labelOf(t, portData(t, e.rec.call(t, "B"), "in")); l != "A-from-attempt-1" {
		t.Fatalf("B saw %q", l)
	}
	if !equalStrings(res.State.Completed, []string{"in", "A", "B", "out"}) {
		t.Fatalf("completed = %v", res.State.Completed)
	}
}

// ---------------------------------------------------------------------------
// Error classification (Section 1): the error carries it
// ---------------------------------------------------------------------------

func TestErrorClassificationBelongsToTheError(t *testing.T) {
	nodeFail := func(err error, se node.SideEffects) error {
		return &execution.NodeExecutionError{NodeID: "n", NodeType: "t", Stage: execution.StageExecute, Err: err, SideEffects: se}
	}
	cases := []struct {
		name      string
		err       error
		code      string
		retryable bool
		source    string
	}{
		{"429", nodeFail(node.HTTPStatusError(429, 0, "x"), ""), string(node.ErrCodeRateLimited), true, execution.SourceProvider},
		{"500", nodeFail(node.HTTPStatusError(500, 0, "x"), ""), string(node.ErrCodeUnavailable), true, execution.SourceProvider},
		{"502", nodeFail(node.HTTPStatusError(502, 0, "x"), ""), string(node.ErrCodeUnavailable), true, execution.SourceProvider},
		{"503", nodeFail(node.HTTPStatusError(503, 0, "x"), ""), string(node.ErrCodeUnavailable), true, execution.SourceProvider},
		{"408", nodeFail(node.HTTPStatusError(408, 0, "x"), ""), string(node.ErrCodeTimeout), true, execution.SourceProvider},
		{"401 credentials", nodeFail(node.HTTPStatusError(401, 0, "x"), ""), string(node.ErrCodeInvalidCredentials), false, execution.SourceProvider},
		{"403 credentials", nodeFail(node.HTTPStatusError(403, 0, "x"), ""), string(node.ErrCodeInvalidCredentials), false, execution.SourceProvider},
		{"400 input", nodeFail(node.HTTPStatusError(400, 0, "x"), ""), string(node.ErrCodeInvalidInput), false, execution.SourceProvider},
		{"501 unsupported", nodeFail(node.HTTPStatusError(501, 0, "x"), ""), string(node.ErrCodeUnsupported), false, execution.SourceProvider},
		{"node config", nodeFail(node.NewNodeError(node.ErrCodeConfiguration, "x", false), ""), string(node.ErrCodeConfiguration), false, execution.SourceNode},
		{"node timeout", nodeFail(&execution.NodeTimeoutError{Timeout: time.Second, Err: context.DeadlineExceeded}, ""), execution.CodeNodeTimeout, true, execution.SourceNode},
		{"execution deadline", fmtWrap(context.DeadlineExceeded), execution.CodeTimeout, false, execution.SourceSystem},
		{"cancelled", fmtWrap(context.Canceled), execution.CodeCancelled, false, execution.SourceSystem},
		{"invalid workflow", fmtWrap(execution.ErrGraphCycle), execution.CodeInvalidWorkflow, false, execution.SourceSystem},
		// Side effects: an unsafe node's transient failure is not repeated...
		{"unsafe 503", nodeFail(node.HTTPStatusError(503, 0, "x"), node.SideEffectsUnsafe), string(node.ErrCodeUnavailable), false, execution.SourceProvider},
		// ...unless the error proves the operation was not applied.
		{"unsafe 429", nodeFail(node.HTTPStatusError(429, 0, "x"), node.SideEffectsUnsafe), string(node.ErrCodeRateLimited), true, execution.SourceProvider},
		{"idempotent 503", nodeFail(node.HTTPStatusError(503, 0, "x"), node.SideEffectsIdempotent), string(node.ErrCodeUnavailable), true, execution.SourceProvider},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ee := execution.ErrorFromExecution(tc.err)
			if ee.Code != tc.code || ee.Retryable != tc.retryable || ee.Source != tc.source {
				t.Fatalf("got %+v, want code=%s retryable=%v source=%s", ee, tc.code, tc.retryable, tc.source)
			}
		})
	}
	// errors.Is / As survive classification.
	wrapped := nodeFail(node.WrapNodeError(node.ErrCodeUnavailable, "x", true, errBoom), "")
	if !errors.Is(wrapped, errBoom) {
		t.Fatal("wrapping chain broken")
	}
	ee := execution.ErrorFromExecution(nodeFail(node.HTTPStatusError(503, 0, "x"), node.SideEffectsUnsafe))
	if !strings.Contains(ee.Message, "non-idempotent") {
		t.Fatalf("unsafe message = %q", ee.Message)
	}
}

func fmtWrap(err error) error { return errors.Join(errors.New("context"), err) }

func TestRetryAfterParsing(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		d  time.Duration
		ok bool
	}{
		"120":                           {2 * time.Minute, true},
		" 0 ":                           {0, true},
		"Thu, 01 Jan 2026 12:00:30 GMT": {30 * time.Second, true},
		"Thu, 01 Jan 2026 11:00:00 GMT": {0, true},
		"":                              {0, false},
		"-5":                            {0, false},
		"soon":                          {0, false},
	}
	for in, want := range cases {
		d, ok := node.ParseRetryAfter(in, now)
		if d != want.d || ok != want.ok {
			t.Errorf("ParseRetryAfter(%q) = %s, %v", in, d, ok)
		}
	}
	if d, _ := node.ParseRetryAfter("99999999", now); d != 24*time.Hour {
		t.Fatalf("absurd Retry-After not capped: %s", d)
	}
	// The HTTP-date form is capped the same way.
	if d, ok := node.ParseRetryAfter("Fri, 01 Jan 2027 12:00:00 GMT", now); d != node.MaxRetryAfter || !ok {
		t.Fatalf("far HTTP-date Retry-After not capped: %s %v", d, ok)
	}
}

// ---------------------------------------------------------------------------
// The shared retry decision
// ---------------------------------------------------------------------------

func TestDecideFailure(t *testing.T) {
	now := time.Now()
	soon := now.Add(5 * time.Second)
	later := now.Add(time.Hour)
	retryable := execution.ExecutionError{Code: "UNAVAILABLE", Message: "m", Retryable: true}
	permanent := execution.ExecutionError{Code: "INVALID_CREDENTIALS", Message: "m"}
	backoff := linearBackoff{time.Second}
	unsafeNode := "post"
	cases := []struct {
		name   string
		state  execution.AttemptState
		err    execution.ExecutionError
		b      execution.Backoff
		action execution.FailureAction
		dead   execution.DeadLetterReason
		delay  time.Duration
	}{
		{"retry", execution.AttemptState{Attempt: 1, MaxAttempts: 3, Now: now, DeadlineAt: &later}, retryable, backoff, execution.ActionRetry, "", time.Second},
		{"backoff grows", execution.AttemptState{Attempt: 2, MaxAttempts: 3, Now: now}, retryable, backoff, execution.ActionRetry, "", 2 * time.Second},
		{"non-retryable", execution.AttemptState{Attempt: 1, MaxAttempts: 3, Now: now}, permanent, backoff, execution.ActionFail, "", 0},
		{"exhausted", execution.AttemptState{Attempt: 3, MaxAttempts: 3, Now: now}, retryable, backoff, execution.ActionFail, execution.DeadLetterAttemptsExhausted, 0},
		{"beyond max", execution.AttemptState{Attempt: 4, MaxAttempts: 3, Now: now}, retryable, backoff, execution.ActionFail, execution.DeadLetterAttemptsExhausted, 0},
		{"deadline", execution.AttemptState{Attempt: 1, MaxAttempts: 3, Now: now, DeadlineAt: &soon}, execution.ExecutionError{Code: "X", Message: "m", Retryable: true, RetryAfter: time.Minute}, backoff, execution.ActionFail, execution.DeadLetterDeadlineExceeded, 0},
		{"deadline passed", execution.AttemptState{Attempt: 1, MaxAttempts: 3, Now: later, DeadlineAt: &soon}, retryable, backoff, execution.ActionFail, execution.DeadLetterDeadlineExceeded, 0},
		{"cancel beats retry", execution.AttemptState{Attempt: 1, MaxAttempts: 3, Now: now, CancelRequested: true}, retryable, backoff, execution.ActionCancel, "", 0},
		{"cancel beats failure", execution.AttemptState{Attempt: 3, MaxAttempts: 3, Now: now, CancelRequested: true}, permanent, backoff, execution.ActionCancel, "", 0},
		{"retries disabled", execution.AttemptState{Attempt: 1, MaxAttempts: 3, Now: now}, retryable, nil, execution.ActionFail, "", 0},
		{"retry-after honoured", execution.AttemptState{Attempt: 1, MaxAttempts: 3, Now: now, DeadlineAt: &later}, execution.ExecutionError{Code: "X", Message: "m", Retryable: true, RetryAfter: time.Minute}, backoff, execution.ActionRetry, "", time.Minute},
		{"unsafe interrupted node", execution.AttemptState{Attempt: 1, MaxAttempts: 3, Now: now, DeadlineAt: &later, UnsafeNodeID: &unsafeNode}, retryable, backoff, execution.ActionFail, execution.DeadLetterRetryUnsafe, 0},
		{"unsafe beats exhausted", execution.AttemptState{Attempt: 3, MaxAttempts: 3, Now: now, UnsafeNodeID: &unsafeNode}, retryable, backoff, execution.ActionFail, execution.DeadLetterRetryUnsafe, 0},
		{"cancel beats unsafe", execution.AttemptState{Attempt: 1, MaxAttempts: 3, Now: now, CancelRequested: true, UnsafeNodeID: &unsafeNode}, retryable, backoff, execution.ActionCancel, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := execution.DecideFailure(tc.state, tc.err, tc.b)
			if d.Action != tc.action || d.DeadLetter != tc.dead || d.Delay != tc.delay {
				t.Fatalf("got %+v, want action=%v dead=%q delay=%s", d, tc.action, tc.dead, tc.delay)
			}
		})
	}
}

func TestNodeRetryPolicyIsBoundedAndClassified(t *testing.T) {
	p := nodeRetry(3)
	retryable := execution.ExecutionError{Code: "X", Message: "m", Retryable: true}
	if _, ok := p.NodeRetryDelay(node.NodeDefinition{}, 1, retryable); !ok {
		t.Fatal("first failure must be retried")
	}
	if _, ok := p.NodeRetryDelay(node.NodeDefinition{}, 3, retryable); ok {
		t.Fatal("retried beyond MaxInvocations")
	}
	if _, ok := p.NodeRetryDelay(node.NodeDefinition{}, 1, execution.ExecutionError{Code: "X", Message: "m"}); ok {
		t.Fatal("non-retryable failure retried")
	}
	retryable.RetryAfter = time.Hour
	if _, ok := p.NodeRetryDelay(node.NodeDefinition{}, 1, retryable); ok {
		t.Fatal("long pause waited in place")
	}
}
