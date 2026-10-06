package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"workflow-optimizer/internal/execution"
)

// lifecycleWriter executes lifecycle writes so that their outcome is never
// ambiguous to the caller.
//
// The failure it prevents: a client gives up on a statement (context deadline,
// broken connection) while PostgreSQL still executes and commits it, so the
// caller believes "not written" while the row changed. Three layers close it:
//
//  1. Explicit transactions. A write commits only when the client sends
//     COMMIT; a client that goes away mid-transaction cannot have its work
//     committed behind its back (an autocommit statement could).
//  2. Server-side budgets. lock_timeout, statement_timeout and
//     idle_in_transaction_session_timeout are set to a fraction of the
//     client's remaining deadline, so PostgreSQL gives up (and reports a
//     definite error) before the client does.
//  3. Fencing. If the outcome is still unknown (the connection broke after
//     COMMIT was sent, or the reply was lost), the server backend that ran the
//     write is terminated and awaited; afterwards the write is final, and a
//     caller-supplied marker query decides whether it committed.
//
// Transactions run at READ COMMITTED explicitly: the lifecycle's
// compare-and-set statements rely on its row-lock re-check semantics, whatever
// default_transaction_isolation the database is configured with.
type lifecycleWriter struct {
	pool *pgxpool.Pool
}

const (
	defaultServerBudget = 30 * time.Second
	minServerBudget     = 20 * time.Millisecond
	fenceTimeout        = 10 * time.Second
)

// serverBudget returns how long PostgreSQL may spend: three quarters of the
// client's remaining time, leaving the rest for the reply to arrive.
func serverBudget(ctx context.Context) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return defaultServerBudget
	}
	budget := time.Until(deadline) * 3 / 4
	if budget < minServerBudget {
		budget = minServerBudget
	}
	return budget
}

// notApplied marks an error returned by write callbacks after deciding, inside
// the transaction, not to write (e.g. a failed compare-and-set). The
// transaction is rolled back and the outcome is definite.
type notApplied struct{ err error }

func (n notApplied) Error() string { return n.err.Error() }
func (n notApplied) Unwrap() error { return n.err }

// write runs fn in a bounded READ COMMITTED transaction. settings are
// transaction-local configuration values (e.g. the history metadata).
// committed reports, after fencing, whether the write took effect; it is only
// consulted when the outcome is otherwise ambiguous.
func (w *lifecycleWriter) write(
	ctx context.Context,
	settings map[string]string,
	fn func(ctx context.Context, tx pgx.Tx) error,
	committed func(ctx context.Context) (bool, error),
) error {
	conn, err := w.pool.Acquire(ctx)
	if err != nil {
		// Nothing reached the server.
		return fmt.Errorf("%w: acquire connection: %v", execution.ErrWriteNotApplied, err)
	}
	pid := conn.Conn().PgConn().PID()
	err = runTx(ctx, conn.Conn(), settings, fn)
	conn.Release() // pgx discards connections that broke mid-operation
	if err == nil {
		return nil
	}
	var na notApplied
	if errors.As(err, &na) {
		return na.err
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		// The server reported the failure: the transaction was rolled back.
		return mapWriteError(err)
	}
	if pgconn.SafeToRetry(err) {
		return fmt.Errorf("%w: %v", execution.ErrWriteNotApplied, err)
	}
	return w.resolve(ctx, pid, err, committed)
}

func runTx(ctx context.Context, conn *pgx.Conn, settings map[string]string, fn func(context.Context, pgx.Tx) error) error {
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	rollback := func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_ = tx.Rollback(rctx)
	}
	budget := fmt.Sprintf("%dms", serverBudget(ctx).Milliseconds())
	query := "SELECT set_config('lock_timeout', $1, true), set_config('statement_timeout', $1, true), " +
		"set_config('idle_in_transaction_session_timeout', $1, true)"
	args := []any{budget}
	for name, value := range settings {
		args = append(args, name, value)
		query += fmt.Sprintf(", set_config($%d, $%d, true)", len(args)-1, len(args))
	}
	if _, err := tx.Exec(ctx, query, args...); err != nil {
		rollback()
		return err
	}
	if err := fn(ctx, tx); err != nil {
		rollback()
		return err
	}
	return tx.Commit(ctx)
}

// resolve settles an ambiguous write: terminate the backend that ran it (its
// uncommitted work rolls back; a committed transaction stays committed), then
// ask the marker query what happened.
func (w *lifecycleWriter) resolve(ctx context.Context, pid uint32, cause error, committed func(context.Context) (bool, error)) error {
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fenceTimeout)
	defer cancel()
	if err := w.fence(fctx, pid); err != nil {
		return fmt.Errorf("%w: %v (fencing backend %d: %v)", execution.ErrOutcomeUnknown, cause, pid, err)
	}
	ok, err := committed(fctx)
	if err != nil {
		return fmt.Errorf("%w: %v (checking commit marker: %v)", execution.ErrOutcomeUnknown, cause, err)
	}
	if ok {
		return nil
	}
	return fmt.Errorf("%w: %v", execution.ErrWriteNotApplied, cause)
}

// fence terminates backend pid and waits until it is gone.
func (w *lifecycleWriter) fence(ctx context.Context, pid uint32) error {
	waitMs := int64(fenceTimeout / time.Millisecond / 2)
	var terminated bool
	if err := w.pool.QueryRow(ctx, "SELECT pg_terminate_backend($1, $2)", int32(pid), waitMs).Scan(&terminated); err != nil {
		return err
	}
	if terminated {
		return nil
	}
	var alive bool
	if err := w.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE pid = $1)", int32(pid)).Scan(&alive); err != nil {
		return err
	}
	if alive {
		return fmt.Errorf("backend %d did not terminate", pid)
	}
	return nil // already gone
}

// PostgreSQL error classes used by the lifecycle.
const (
	pgForeignKeyViolation  = "23503"
	pgUniqueViolation      = "23505"
	pgCheckViolation       = "23514"
	pgSerializationFailure = "40001"
	pgDeadlockDetected     = "40P01"
	pgLockNotAvailable     = "55P03"
	pgQueryCanceled        = "57014"
	pgIdleInTxTimeout      = "25P03"
)

// mapWriteError translates a server-reported error (the transaction was
// rolled back) into lifecycle sentinels, keeping the original error wrapped.
func mapWriteError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	wrap := func(sentinel error) error { return fmt.Errorf("%w: %w", sentinel, err) }
	switch pgErr.Code {
	case pgForeignKeyViolation:
		switch pgErr.ConstraintName {
		case "executions_workflow_id_fkey":
			return wrap(execution.ErrWorkflowNotFound)
		case "executions_workflow_version_id_fkey":
			return wrap(execution.ErrWorkflowVersionNotFound)
		case "executions_version_belongs_to_workflow_fkey":
			return wrap(execution.ErrWorkflowVersionMismatch)
		case "node_executions_execution_id_fkey":
			return wrap(execution.ErrExecutionNotFound)
		}
	case pgUniqueViolation:
		return wrap(execution.ErrInvalidExecution)
	case pgCheckViolation:
		switch pgErr.ConstraintName {
		case "lifecycle_parent_not_running":
			return wrap(execution.ErrExecutionNotRunning)
		case "lifecycle_running_nodes":
			return wrap(execution.ErrExecutionHasRunningNodes)
		case "lifecycle_version_not_published":
			return wrap(execution.ErrWorkflowVersionNotExecutable)
		case "lifecycle_insert_requires_pending":
			return wrap(execution.ErrInvalidExecution)
		// Phase 10.
		case "lifecycle_retry_not_due":
			return wrap(execution.ErrRetryNotDue)
		case "lifecycle_attempts_exhausted":
			return wrap(execution.ErrAttemptsExhausted)
		case "lifecycle_retry_after_deadline":
			return wrap(execution.ErrRetryAfterDeadline)
		case "lifecycle_retry_cancel_requested":
			return wrap(execution.ErrCancelRequested)
		case "lifecycle_stale_attempt", "lifecycle_stale_owner", "lifecycle_lease_not_owner":
			return wrap(execution.ErrLeaseLost)
		case "lifecycle_retry_requires_schedule":
			return wrap(execution.ErrInvalidExecution)
		}
		return wrap(execution.ErrInvalidTransition)
	case pgSerializationFailure, pgDeadlockDetected:
		return wrap(execution.ErrConcurrentUpdate)
	case pgLockNotAvailable, pgQueryCanceled, pgIdleInTxTimeout:
		return wrap(execution.ErrPersistenceTimeout)
	}
	return fmt.Errorf("lifecycle write: %w", err)
}
