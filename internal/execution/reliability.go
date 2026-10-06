package execution

import (
	"time"

	"workflow-optimizer/internal/node"
)

// Backoff computes the pause before the next attempt after `failures` failed
// attempts, honouring a server's retryAfter. retry.Policy implements it.
type Backoff interface {
	Delay(failures int, retryAfter time.Duration) time.Duration
}

// NodeRetry is the in-place node retry policy: a failed node invocation may
// run again on the same worker, within the same execution attempt, when its
// failure is retryable, invocations remain and the pause is short. Longer
// pauses are not waited for here: the failure then goes to execution-level
// retry, which releases the worker and resumes later from completed nodes.
type NodeRetry struct {
	// MaxInvocations bounds invocations of one node per execution attempt
	// (1 = no in-place retry).
	MaxInvocations int
	Backoff        Backoff
	// MaxInlineDelay is the longest pause waited for in place.
	MaxInlineDelay time.Duration
}

// DefaultMaxInlineNodeDelay is the default longest in-place pause.
const DefaultMaxInlineNodeDelay = 5 * time.Second

var _ NodeRetryPolicy = NodeRetry{}

// NodeRetryDelay implements NodeRetryPolicy. Retryability comes from the
// error itself (including the node's side-effect declaration, applied by
// ErrorFromExecution); this only bounds and paces.
func (p NodeRetry) NodeRetryDelay(_ node.NodeDefinition, failures int, err ExecutionError) (time.Duration, bool) {
	if !err.Retryable || p.Backoff == nil || failures >= p.MaxInvocations {
		return 0, false
	}
	limit := p.MaxInlineDelay
	if limit <= 0 {
		limit = DefaultMaxInlineNodeDelay
	}
	d := p.Backoff.Delay(failures, err.RetryAfter)
	if d > limit {
		return 0, false
	}
	return d, true
}

// FailureAction is what happens to an attempt that failed.
type FailureAction int

const (
	// ActionFail makes the execution FAILED (terminal).
	ActionFail FailureAction = iota
	// ActionRetry schedules another attempt: RUNNING -> PENDING with
	// next_attempt_at = now + Delay.
	ActionRetry
	// ActionCancel makes the execution CANCELLED: cancellation beats retry.
	ActionCancel
)

// AttemptState is what the decision needs to know about the failed attempt.
type AttemptState struct {
	Attempt         int
	MaxAttempts     int
	DeadlineAt      *time.Time
	Now             time.Time
	CancelRequested bool
	// UnsafeNodeID names a node that was still running when the attempt was
	// lost and whose persisted side effects are unsafe or unknown: it may
	// have applied them, so the attempt must not be repeated.
	UnsafeNodeID *string
}

// FailureDecision is the outcome of DecideFailure.
type FailureDecision struct {
	Action FailureAction
	Delay  time.Duration
	// DeadLetter is set when a retryable failure stops being retried.
	DeadLetter DeadLetterReason
}

// DecideFailure is the single retry decision, shared by the Runner (a failed
// attempt) and the reaper (an attempt whose worker died). In order:
//
//  1. a cancellation request wins over everything;
//  2. without a backoff policy, nothing is retried (Phase 8 behaviour);
//  3. a non-retryable failure is never retried;
//  4. an attempt interrupted inside a node with unsafe (or unknown) side
//     effects is dead-lettered as retry_unsafe: re-running the node could
//     repeat an effect it may already have applied;
//  5. without attempts left the retryable failure is dead-lettered;
//  6. a retry that could not start before the execution deadline is
//     dead-lettered instead of scheduled;
//  7. otherwise the retry is scheduled after the backoff delay.
//
// The database enforces 1, 5 and 6 again when the retry is written.
func DecideFailure(s AttemptState, err ExecutionError, backoff Backoff) FailureDecision {
	switch {
	case s.CancelRequested:
		return FailureDecision{Action: ActionCancel}
	case backoff == nil, !err.Retryable:
		return FailureDecision{Action: ActionFail}
	case s.UnsafeNodeID != nil:
		return FailureDecision{Action: ActionFail, DeadLetter: DeadLetterRetryUnsafe}
	case s.Attempt >= s.MaxAttempts:
		return FailureDecision{Action: ActionFail, DeadLetter: DeadLetterAttemptsExhausted}
	}
	delay := backoff.Delay(s.Attempt, err.RetryAfter)
	if delay < 0 {
		delay = 0
	}
	if s.DeadlineAt != nil && !s.Now.Add(delay).Before(*s.DeadlineAt) {
		return FailureDecision{Action: ActionFail, DeadLetter: DeadLetterDeadlineExceeded}
	}
	return FailureDecision{Action: ActionRetry, Delay: delay}
}
