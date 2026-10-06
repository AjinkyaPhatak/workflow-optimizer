package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/reliability"
)

// ReliabilityRepository implements the Phase 10 persistence operations:
// lease renewal (execution.LeaseStore), cancellation requests
// (execution.CancellationStore), recovery of orphaned attempts
// (reliability.RecoveryStore), atomic dispatch claims
// (reliability.DispatchStore) and dead-letter reads.
//
// Lock order is always executions row, then its lease row (the same order a
// lifecycle transition takes when its trigger drops the lease), so recovery,
// heartbeats and finalization serialize without deadlocking.
type ReliabilityRepository struct {
	pool   *pgxpool.Pool
	writer *lifecycleWriter
}

var (
	_ execution.LeaseStore        = (*ReliabilityRepository)(nil)
	_ execution.CancellationStore = (*ReliabilityRepository)(nil)
	_ reliability.RecoveryStore   = (*ReliabilityRepository)(nil)
	_ reliability.DispatchStore   = (*ReliabilityRepository)(nil)
	_ reliability.DeadLetterStore = (*ReliabilityRepository)(nil)
)

// NewReliabilityRepository uses the Store's existing connection pool.
func NewReliabilityRepository(store *Store) *ReliabilityRepository {
	return &ReliabilityRepository{pool: store.Pool, writer: &lifecycleWriter{pool: store.Pool}}
}

// renewLeaseSQL extends a lease only while it is still valid and still the
// execution's current RUNNING claim: an expired lease is never revived (it may
// be being recovered), and a finalized execution has no lease to renew.
const renewLeaseSQL = `
UPDATE execution_leases l
   SET expires_at = clock_timestamp() + $3::bigint * interval '1 millisecond'
  FROM executions e
 WHERE l.execution_id = $1 AND l.claim_token = $2 AND l.expires_at > clock_timestamp()
   AND e.id = l.execution_id AND e.status = 'RUNNING' AND e.claim_token = $2
RETURNING EXISTS (SELECT 1 FROM execution_cancel_requests c WHERE c.execution_id = $1)`

// Renew implements execution.LeaseStore.
func (r *ReliabilityRepository) Renew(ctx context.Context, executionID, claimToken uuid.UUID, d time.Duration) (execution.LeaseState, error) {
	if d <= 0 {
		return execution.LeaseState{}, fmt.Errorf("%w: lease duration must be positive", execution.ErrInvalidExecution)
	}
	var cancelRequested bool
	err := r.pool.QueryRow(ctx, renewLeaseSQL, executionID, claimToken, d.Milliseconds()).Scan(&cancelRequested)
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.LeaseState{Held: false}, nil
	}
	if err != nil {
		return execution.LeaseState{}, fmt.Errorf("renew lease of %s: %w", executionID, err)
	}
	return execution.LeaseState{Held: true, CancelRequested: cancelRequested}, nil
}

// RequestCancel implements execution.CancellationStore (idempotent).
func (r *ReliabilityRepository) RequestCancel(ctx context.Context, executionID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO execution_cancel_requests (execution_id) VALUES ($1) ON CONFLICT DO NOTHING`, executionID)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgForeignKeyViolation {
		return fmt.Errorf("%w: %s", execution.ErrExecutionNotFound, executionID)
	}
	if err != nil {
		return fmt.Errorf("request cancellation of %s: %w", executionID, err)
	}
	return nil
}

const candidatesSQL = `
(SELECT l.execution_id, l.claim_token, l.attempt, l.owner, 'lease_expired'
   FROM execution_leases l
   JOIN executions e ON e.id = l.execution_id
  WHERE l.expires_at < clock_timestamp() AND e.status = 'RUNNING' AND e.claim_token = l.claim_token
  ORDER BY l.expires_at
  LIMIT $1)
UNION ALL
(SELECT e.id, e.claim_token, e.attempt, COALESCE(l.owner, ''), 'deadline_exceeded'
   FROM executions e
   LEFT JOIN execution_leases l ON l.execution_id = e.id
  WHERE e.status = 'RUNNING' AND e.deadline_at < clock_timestamp() AND e.claim_token IS NOT NULL
    AND (l.execution_id IS NULL OR l.expires_at >= clock_timestamp())
  ORDER BY e.deadline_at
  LIMIT $1)`

// Candidates implements reliability.RecoveryStore.
func (r *ReliabilityRepository) Candidates(ctx context.Context, limit int) ([]reliability.RecoveryCandidate, error) {
	if limit <= 0 {
		limit = 1
	}
	rows, err := r.pool.Query(ctx, candidatesSQL, limit)
	if err != nil {
		return nil, fmt.Errorf("list recovery candidates: %w", err)
	}
	defer rows.Close()
	var out []reliability.RecoveryCandidate
	for rows.Next() {
		var c reliability.RecoveryCandidate
		var reason string
		if err := rows.Scan(&c.ExecutionID, &c.ClaimToken, &c.Attempt, &c.Owner, &reason); err != nil {
			return nil, fmt.Errorf("scan recovery candidate: %w", err)
		}
		c.Reason = reliability.RecoveryReason(reason)
		out = append(out, c)
	}
	return out, rows.Err()
}

// unsafeInterruptedNodeSQL finds a RUNNING node record whose side effects are
// unsafe, or unknown (written before they were recorded).
const unsafeInterruptedNodeSQL = `
SELECT node_id FROM node_executions
 WHERE execution_id = $1 AND status = 'RUNNING' AND (side_effects IS NULL OR side_effects = 'unsafe')
 ORDER BY started_at, node_id
 LIMIT 1`

// errNotRecoverable marks a candidate that no longer qualifies.
var errNotRecoverable = errors.New("postgres: candidate no longer recoverable")

// interruptedByRecovery is stamped on node records still RUNNING in an
// attempt whose worker was lost.
var interruptedByRecovery = execution.ExecutionError{Code: execution.CodeNodeInterrupted,
	Message: "the worker running this node was lost before recording its outcome", Source: execution.SourceWorker}

// Recover implements reliability.RecoveryStore. Everything happens in one
// transaction: the execution row is locked and must still be RUNNING under
// the candidate's claim; for an expired lease, the lease row is locked and must
// still be expired (a heartbeat that committed first wins, so a healthy worker
// is never recovered); for an overrun deadline, the deadline must have passed.
// Only then is the plan computed from the locked state and written, fenced by
// the claim.
func (r *ReliabilityRepository) Recover(ctx context.Context, c reliability.RecoveryCandidate, plan func(reliability.RecoverySnapshot) reliability.RecoveryPlan) (reliability.RecoveryResult, error) {
	transitionID := uuid.New()
	meta, err := json.Marshal(map[string]any{"reason": "recovered_" + string(c.Reason), "owner": c.Owner, "attempt": c.Attempt})
	if err != nil {
		return reliability.RecoveryResult{}, err
	}
	interrupted, err := json.Marshal(interruptedByRecovery)
	if err != nil {
		return reliability.RecoveryResult{}, err
	}
	settings := map[string]string{
		"workflow.transition_id":       transitionID.String(),
		"workflow.transition_metadata": string(meta),
	}
	var result reliability.RecoveryResult
	err = r.writer.write(ctx, settings,
		func(ctx context.Context, tx pgx.Tx) error {
			var (
				status   string
				token    *uuid.UUID
				snapshot reliability.RecoverySnapshot
			)
			err := tx.QueryRow(ctx, `
SELECT status, claim_token, attempt, max_attempts, deadline_at, clock_timestamp(),
       EXISTS (SELECT 1 FROM execution_cancel_requests c WHERE c.execution_id = executions.id)
  FROM executions WHERE id = $1 FOR UPDATE`, c.ExecutionID).
				Scan(&status, &token, &snapshot.Attempt, &snapshot.MaxAttempts, &snapshot.DeadlineAt, &snapshot.Now, &snapshot.CancelRequested)
			if errors.Is(err, pgx.ErrNoRows) {
				return notApplied{errNotRecoverable}
			}
			if err != nil {
				return err
			}
			if status != string(execution.StatusRunning) || token == nil || *token != c.ClaimToken {
				return notApplied{errNotRecoverable}
			}
			switch c.Reason {
			case reliability.RecoverLeaseExpired:
				var expired bool
				err := tx.QueryRow(ctx, `
SELECT expires_at < clock_timestamp() FROM execution_leases
 WHERE execution_id = $1 AND claim_token = $2 FOR UPDATE`, c.ExecutionID, c.ClaimToken).Scan(&expired)
				if errors.Is(err, pgx.ErrNoRows) || (err == nil && !expired) {
					return notApplied{errNotRecoverable}
				}
				if err != nil {
					return err
				}
			case reliability.RecoverDeadlineExceeded:
				if snapshot.DeadlineAt == nil || !snapshot.DeadlineAt.Before(snapshot.Now) {
					return notApplied{errNotRecoverable}
				}
			default:
				return notApplied{fmt.Errorf("%w: unknown recovery reason %q", execution.ErrInvalidExecution, c.Reason)}
			}
			// A node still RUNNING was interrupted; if its persisted side
			// effects are unsafe or unknown, it may have applied them.
			// (Node writers share-lock the execution, so this is stable.)
			var unsafeNode string
			err = tx.QueryRow(ctx, unsafeInterruptedNodeSQL, c.ExecutionID).Scan(&unsafeNode)
			switch {
			case err == nil:
				snapshot.UnsafeNodeID = &unsafeNode
			case !errors.Is(err, pgx.ErrNoRows):
				return err
			}

			p := plan(snapshot)
			var execErr, lastErr []byte
			switch p.To {
			case execution.StatusPending:
				if lastErr, err = json.Marshal(p.Error); err != nil {
					return err
				}
			case execution.StatusFailed, execution.StatusCancelled:
				if p.To == execution.StatusFailed || p.Error.Code != "" {
					if execErr, err = json.Marshal(p.Error); err != nil {
						return err
					}
				}
			default:
				return notApplied{fmt.Errorf("%w: recovery cannot move an execution to %s", execution.ErrInvalidExecution, p.To)}
			}
			if _, err := tx.Exec(ctx, sweepRunningNodesSQL, c.ExecutionID, interrupted); err != nil {
				return err
			}
			tag, err := tx.Exec(ctx, transitionExecutionSQL, c.ExecutionID, string(execution.StatusRunning), string(p.To),
				nil, execErr, nil, c.ClaimToken, p.RetryDelay.Milliseconds(), lastErr)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return notApplied{errNotRecoverable}
			}
			if p.To == execution.StatusFailed && p.DeadLetter != "" {
				if _, err := tx.Exec(ctx, deadLetterSQL, c.ExecutionID, string(p.DeadLetter)); err != nil {
					return err
				}
			}
			result = reliability.RecoveryResult{Recovered: true, Plan: p}
			return nil
		},
		func(ctx context.Context) (bool, error) {
			var exists bool
			err := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM execution_status_history WHERE id = $1)`, transitionID).Scan(&exists)
			return exists, err
		})
	if errors.Is(err, errNotRecoverable) {
		return reliability.RecoveryResult{Recovered: false}, nil
	}
	if err != nil {
		return reliability.RecoveryResult{}, err
	}
	return result, nil
}

// claimDueSQL is the scheduler's atomic claim: FOR UPDATE SKIP LOCKED gives
// concurrent schedulers disjoint rows, and the row lock re-checks the
// conditions on the latest row version, so a row one scheduler just marked
// dispatched is not claimed again by another.
const claimDueSQL = `
UPDATE execution_dispatch d
   SET dispatched_at = clock_timestamp(), dispatch_count = d.dispatch_count + 1
  FROM (SELECT d2.execution_id
          FROM execution_dispatch d2
          JOIN executions e ON e.id = d2.execution_id
         WHERE e.status = 'PENDING' AND d2.attempt = e.attempt
           AND (e.next_attempt_at IS NULL OR e.next_attempt_at <= clock_timestamp()
                OR EXISTS (SELECT 1 FROM execution_cancel_requests c WHERE c.execution_id = e.id))
           AND (d2.dispatched_at IS NULL
                OR d2.dispatched_at <= clock_timestamp() - $2::bigint * interval '1 millisecond')
         ORDER BY COALESCE(e.next_attempt_at, e.created_at)
         LIMIT $1
           FOR UPDATE OF d2 SKIP LOCKED) due
 WHERE d.execution_id = due.execution_id
RETURNING d.execution_id, d.attempt`

// ClaimDue implements reliability.DispatchStore.
func (r *ReliabilityRepository) ClaimDue(ctx context.Context, limit int, redispatchAfter time.Duration) ([]reliability.DueExecution, error) {
	if limit <= 0 {
		limit = 1
	}
	if redispatchAfter <= 0 {
		return nil, fmt.Errorf("%w: re-dispatch interval must be positive", execution.ErrInvalidExecution)
	}
	rows, err := r.pool.Query(ctx, claimDueSQL, limit, redispatchAfter.Milliseconds())
	if err != nil {
		return nil, fmt.Errorf("claim due executions: %w", err)
	}
	defer rows.Close()
	var out []reliability.DueExecution
	for rows.Next() {
		var d reliability.DueExecution
		if err := rows.Scan(&d.ExecutionID, &d.Attempt); err != nil {
			return nil, fmt.Errorf("scan due execution: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeadLetter implements reliability.DeadLetterStore (nil when none).
func (r *ReliabilityRepository) DeadLetter(ctx context.Context, executionID uuid.UUID) (*execution.DeadLetter, error) {
	var (
		d      execution.DeadLetter
		raw    []byte
		reason string
	)
	err := r.pool.QueryRow(ctx, `
SELECT id, execution_id, attempt, error, reason, created_at FROM execution_dead_letters WHERE execution_id = $1`, executionID).
		Scan(&d.ID, &d.ExecutionID, &d.Attempt, &raw, &reason, &d.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get dead letter of %s: %w", executionID, err)
	}
	if err := json.Unmarshal(raw, &d.Error); err != nil {
		return nil, fmt.Errorf("decode dead letter error: %w", err)
	}
	d.Reason = execution.DeadLetterReason(reason)
	return &d, nil
}
