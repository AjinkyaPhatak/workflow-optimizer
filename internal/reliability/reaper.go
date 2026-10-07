package reliability

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"workflow-optimizer/internal/execution"
)

// DefaultReaperInterval is how often the reaper looks for orphaned attempts.
const DefaultReaperInterval = 15 * time.Second

// Reaper recovers RUNNING attempts that no live worker owns:
//
//	worker heartbeat -> PostgreSQL <- reaper -> recovery
//
// A candidate is an attempt whose lease expired (its worker died or lost
// PostgreSQL) or that overran its execution deadline. Recovery is one
// transaction that re-checks the candidate under lock and is fenced by the
// attempt's claim token, so two reapers never recover the same attempt and a
// worker whose heartbeat committed first is never recovered. What happens is
// decided by execution.DecideFailure, exactly as for a failed attempt:
//
//   - cancellation requested        -> CANCELLED
//   - deadline passed               -> FAILED (EXECUTION_TIMEOUT)
//   - lease expired inside a node whose persisted side effects are unsafe
//     (or unknown) -> FAILED + dead letter retry_unsafe (never re-run)
//   - lease expired, attempts left  -> PENDING, retried after backoff
//     (the worker loss is a retryable WORKER_LOST failure)
//   - lease expired, no attempts or no time left -> FAILED + dead letter
type Reaper struct {
	Store    RecoveryStore
	Backoff  execution.Backoff
	Interval time.Duration
	Batch    int
	Logger   *slog.Logger
	// Notify, when set, is told about each dead-lettered execution (e.g. to
	// publish a non-authoritative Redis dead-letter notice).
	Notify func(ctx context.Context, n DeadLetterNotice)
	// Observer, when set, is told about each applied recovery (Phase 14).
	Observer execution.ExecutionObserver
}

// NewReaper validates the configuration. backoff may be nil (worker losses
// are then failed instead of retried).
func NewReaper(store RecoveryStore, backoff execution.Backoff, interval time.Duration, logger *slog.Logger) (*Reaper, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: reaper requires a recovery store", ErrInvalidConfig)
	}
	if interval <= 0 {
		return nil, fmt.Errorf("%w: reaper interval must be positive", ErrInvalidConfig)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Reaper{Store: store, Backoff: backoff, Interval: interval, Batch: DefaultBatchSize, Logger: logger}, nil
}

// Plan returns the recovery decision for candidate c. Exported for tests.
func (r *Reaper) Plan(c RecoveryCandidate) func(RecoverySnapshot) RecoveryPlan {
	return func(s RecoverySnapshot) RecoveryPlan {
		if s.CancelRequested {
			return RecoveryPlan{To: execution.StatusCancelled}
		}
		if c.Reason == RecoverDeadlineExceeded {
			return RecoveryPlan{To: execution.StatusFailed, Error: execution.ExecutionError{
				Code: execution.CodeTimeout, Source: execution.SourceSystem,
				Message: fmt.Sprintf("attempt %d was still running after the execution deadline", s.Attempt),
			}}
		}
		lost := execution.ExecutionError{
			Code: execution.CodeWorkerLost, Retryable: true, Source: execution.SourceWorker,
			Message: fmt.Sprintf("worker %q stopped heartbeating during attempt %d; its lease expired", c.Owner, s.Attempt),
		}
		d := execution.DecideFailure(s.AttemptState, lost, r.Backoff)
		switch d.Action {
		case execution.ActionRetry:
			return RecoveryPlan{To: execution.StatusPending, Error: lost, RetryDelay: d.Delay}
		case execution.ActionCancel:
			return RecoveryPlan{To: execution.StatusCancelled}
		}
		if d.DeadLetter == execution.DeadLetterRetryUnsafe {
			// The lost worker was inside a node that may already have
			// applied its side effects: not retried.
			lost.Retryable = false
			lost.NodeID = s.UnsafeNodeID
			lost.Message = fmt.Sprintf("worker %q stopped heartbeating during attempt %d while node %q (unsafe or unknown side effects) was running; "+
				"it may have applied them, so the execution is not retried", c.Owner, s.Attempt, *s.UnsafeNodeID)
		}
		return RecoveryPlan{To: execution.StatusFailed, Error: lost, DeadLetter: d.DeadLetter}
	}
}

// RunOnce recovers the current candidates and returns how many were
// recovered.
func (r *Reaper) RunOnce(ctx context.Context) (int, error) {
	batch := r.Batch
	if batch <= 0 {
		batch = DefaultBatchSize
	}
	candidates, err := r.Store.Candidates(ctx, batch)
	if err != nil {
		return 0, err
	}
	recovered := 0
	for _, c := range candidates {
		res, err := r.Store.Recover(ctx, c, r.Plan(c))
		if err != nil {
			r.Logger.Error("recovery failed", "execution_id", c.ExecutionID, "attempt", c.Attempt, "error", err)
			continue
		}
		if !res.Recovered {
			continue // renewed, finished or recovered elsewhere meanwhile
		}
		recovered++
		r.Logger.Warn("recovered orphaned attempt", "execution_id", c.ExecutionID, "attempt", c.Attempt,
			"owner", c.Owner, "reason", c.Reason, "to", res.Plan.To, "retry_in", res.Plan.RetryDelay, "dead_letter", res.Plan.DeadLetter)
		r.observe(ctx, c, res.Plan)
		if res.Plan.DeadLetter != "" && r.Notify != nil {
			r.Notify(ctx, DeadLetterNotice{ExecutionID: c.ExecutionID, Attempt: c.Attempt, Reason: res.Plan.DeadLetter, Code: res.Plan.Error.Code})
		}
	}
	return recovered, nil
}

// Run recovers every Interval until ctx is cancelled.
func (r *Reaper) Run(ctx context.Context) {
	loop(ctx, r.Interval, func(ctx context.Context) {
		if _, err := r.RunOnce(ctx); err != nil && ctx.Err() == nil {
			r.Logger.Error("reaper pass failed", "error", err)
		}
	})
}

// observe reports an applied recovery as the matching execution event.
func (r *Reaper) observe(ctx context.Context, c RecoveryCandidate, p RecoveryPlan) {
	if r.Observer == nil {
		return
	}
	switch p.To {
	case execution.StatusPending:
		r.Observer.RetryScheduled(ctx, c.ExecutionID, nil, c.Attempt+1, p.RetryDelay, p.Error)
	case execution.StatusFailed:
		r.Observer.ExecutionFailed(ctx, c.ExecutionID, c.Attempt, p.Error, p.DeadLetter)
	case execution.StatusCancelled:
		r.Observer.ExecutionCancelled(ctx, c.ExecutionID, c.Attempt, "cancel_requested")
	}
}
