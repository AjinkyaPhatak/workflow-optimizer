// Package reliability runs the Phase 10 background machinery around the
// execution lifecycle:
//
//   - LeaseKeeper heartbeats the lease of each attempt a worker runs and stops
//     the attempt when ownership is lost or cancellation is requested;
//   - Scheduler hands due PENDING executions (retries, and deliveries that
//     were lost) to the queue after claiming them atomically in PostgreSQL;
//   - Reaper recovers RUNNING attempts whose worker stopped heartbeating (and
//     attempts that overran their execution deadline), atomically and fenced
//     by the lease, deciding retry / fail / cancel with the same rules as the
//     Runner (execution.DecideFailure).
//
// PostgreSQL is the only authority. Redis only delivers execution IDs; a
// Redis job is never created for work PostgreSQL has not made due.
package reliability

import (
	"context"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/execution"
)

// RecoveryReason says why a RUNNING attempt is being recovered.
type RecoveryReason string

const (
	// RecoverLeaseExpired: the owner stopped heartbeating; its lease ran out.
	RecoverLeaseExpired RecoveryReason = "lease_expired"
	// RecoverDeadlineExceeded: the attempt is still RUNNING after the
	// execution deadline (its owner did not stop in time).
	RecoverDeadlineExceeded RecoveryReason = "deadline_exceeded"
)

// RecoveryCandidate is a RUNNING attempt that looked recoverable when listed.
// Recovery re-checks every condition atomically.
type RecoveryCandidate struct {
	ExecutionID uuid.UUID
	ClaimToken  uuid.UUID
	Attempt     int
	Owner       string
	Reason      RecoveryReason
}

// RecoverySnapshot is the attempt's state read under lock during recovery.
type RecoverySnapshot struct {
	execution.AttemptState
}

// RecoveryPlan is the transition recovery writes.
type RecoveryPlan struct {
	// To is PENDING (retry), FAILED or CANCELLED.
	To         execution.ExecutionStatus
	Error      execution.ExecutionError
	RetryDelay time.Duration
	DeadLetter execution.DeadLetterReason
}

// RecoveryResult reports what recovery did.
type RecoveryResult struct {
	// Recovered is false when the candidate no longer qualified (a heartbeat
	// renewed the lease, the owner finished, another reaper got there first).
	Recovered bool
	Plan      RecoveryPlan
}

// RecoveryStore finds and recovers orphaned attempts.
type RecoveryStore interface {
	// Candidates lists up to limit RUNNING attempts whose lease has expired
	// or whose execution deadline has passed.
	Candidates(ctx context.Context, limit int) ([]RecoveryCandidate, error)
	// Recover atomically re-checks the candidate (still RUNNING under the
	// same claim, and still expired / overdue), asks plan for the transition
	// and writes it fenced by the claim, in one transaction.
	Recover(ctx context.Context, c RecoveryCandidate, plan func(RecoverySnapshot) RecoveryPlan) (RecoveryResult, error)
}

// DueExecution is a PENDING execution claimed for dispatch.
type DueExecution struct {
	ExecutionID uuid.UUID
	Attempt     int
}

// DispatchStore claims due PENDING executions for dispatch.
type DispatchStore interface {
	// ClaimDue atomically marks up to limit due PENDING executions as
	// dispatched and returns them. Due means: the retry schedule has passed
	// (or cancellation was requested), and the current attempt was never
	// dispatched, or was dispatched more than redispatchAfter ago without
	// being claimed. Concurrent callers never receive the same row.
	ClaimDue(ctx context.Context, limit int, redispatchAfter time.Duration) ([]DueExecution, error)
}

// DeadLetterStore reads the authoritative dead-letter records.
type DeadLetterStore interface {
	DeadLetter(ctx context.Context, executionID uuid.UUID) (*execution.DeadLetter, error)
}
