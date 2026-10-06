package phase10_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/node"
)

// Attempt 1 -> 503, attempt 2 -> success: the retry is persisted, refused
// before it is due, then resumes without re-running completed nodes.
func TestRetryIsPersistedScheduledAndResumes(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, key := e.create(t, 3, time.Minute, "503", "ok")
	r := e.runner(t, runnerOpts{backoff: fast})

	_, err := r.Run(ctx, id)
	var scheduled *execution.RetryScheduledError
	if !errors.As(err, &scheduled) || scheduled.Attempt != 1 {
		t.Fatalf("attempt 1: %v", err)
	}
	ex := e.get(t, id)
	if ex.Status != execution.StatusPending || ex.Attempt != 1 || ex.NextAttemptAt == nil || ex.ClaimToken != nil ||
		ex.LastError == nil || ex.LastError.Code != string(node.ErrCodeUnavailable) || !ex.LastError.Retryable ||
		ex.LastError.Source != execution.SourceProvider || ex.DeadlineAt == nil {
		t.Fatalf("after attempt 1: %+v last=%+v", ex, ex.LastError)
	}
	if n := e.count(t, "SELECT count(*) FROM execution_dispatch WHERE execution_id = $1 AND attempt = 1 AND dispatched_at IS NULL", id); n != 1 {
		t.Fatal("the retry must be waiting for the scheduler")
	}

	// A stale or duplicate job before the retry is due: the database refuses.
	if _, err := r.Run(ctx, id); !errors.Is(err, execution.ErrRetryNotDue) {
		t.Fatalf("early claim: %v", err)
	}
	if e.s.count(key) != 1 || e.get(t, id).Attempt != 1 {
		t.Fatal("an early claim ran something or consumed an attempt")
	}

	time.Sleep(time.Until(*ex.NextAttemptAt) + 20*time.Millisecond)
	if _, err := r.Run(ctx, id); err != nil {
		t.Fatalf("attempt 2: %v", err)
	}
	firstDeadline := *ex.DeadlineAt
	ex = e.get(t, id)
	if ex.Status != execution.StatusCompleted || ex.Attempt != 2 || ex.Output == nil {
		t.Fatalf("after attempt 2: %+v", ex)
	}
	// The execution deadline was fixed by the first claim; a retry never moves it.
	if ex.DeadlineAt == nil || !ex.DeadlineAt.Equal(firstDeadline) {
		t.Fatalf("deadline moved on retry: %v -> %v", firstDeadline, ex.DeadlineAt)
	}
	want := []string{"NULL->PENDING@0", "PENDING->RUNNING@1", "RUNNING->PENDING@1", "PENDING->RUNNING@2", "RUNNING->COMPLETED@2"}
	if got := e.history(t, id); !equal(got, want) {
		t.Fatalf("history = %v", got)
	}
	// Completed nodes were not re-run; only the failed one was.
	if e.s.preCount(key) != 1 || e.s.count(key) != 2 {
		t.Fatalf("pre ran %d times, mid %d times", e.s.preCount(key), e.s.count(key))
	}
	calls := e.s.invocations(key)
	if calls[0].IdempotencyKey == "" || calls[0].IdempotencyKey != calls[1].IdempotencyKey || calls[0].Attempt != 1 || calls[1].Attempt != 2 {
		t.Fatalf("idempotency key / attempt: %+v %+v", calls[0], calls[1])
	}
	if n := e.count(t, "SELECT count(*) FROM node_executions WHERE execution_id = $1 AND node_id = 'pre'", id); n != 1 {
		t.Fatalf("pre has %d records", n)
	}
	if n := e.count(t, `SELECT count(*) FROM node_executions WHERE execution_id = $1 AND node_id = 'mid'
		AND ((attempt = 1 AND execution_attempt = 1 AND status = 'FAILED') OR (attempt = 2 AND execution_attempt = 2 AND status = 'COMPLETED'))`, id); n != 2 {
		t.Fatal("mid records do not show one failed and one completed invocation")
	}
}

// Attempt 1 -> invalid credentials: FAILED at attempt 1, never retried.
func TestNonRetryableFailureIsNeverRetried(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, key := e.create(t, 3, time.Minute, "401", "ok")
	r := e.runner(t, runnerOpts{backoff: fast})
	if _, err := r.Run(ctx, id); err == nil {
		t.Fatal("expected failure")
	}
	ex := e.get(t, id)
	if ex.Status != execution.StatusFailed || ex.Attempt != 1 || ex.Error == nil ||
		ex.Error.Code != string(node.ErrCodeInvalidCredentials) || ex.Error.Retryable {
		t.Fatalf("execution = %+v err=%+v", ex, ex.Error)
	}
	if dl, _ := e.rel.DeadLetter(ctx, id); dl != nil {
		t.Fatal("a non-retryable failure is not dead-lettered (it never retried)")
	}
	due, _ := e.rel.ClaimDue(ctx, 10, time.Millisecond)
	if _, err := r.Run(ctx, id); !errors.Is(err, execution.ErrExecutionNotClaimable) || len(due) != 0 || e.s.count(key) != 1 {
		t.Fatalf("terminal execution became runnable: %v due=%v calls=%d", err, due, e.s.count(key))
	}
}

// 503 three times with three attempts: FAILED, dead-lettered, no attempt 4.
func TestExhaustedRetriesAreDeadLettered(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, key := e.create(t, 3, time.Minute, "503", "503", "503", "ok")
	r := e.runner(t, runnerOpts{backoff: fast})
	for attempt := 1; attempt <= 3; attempt++ {
		ex := e.get(t, id)
		if ex.NextAttemptAt != nil {
			time.Sleep(time.Until(*ex.NextAttemptAt) + 20*time.Millisecond)
		}
		_, err := r.Run(ctx, id)
		var scheduled *execution.RetryScheduledError
		var dead *execution.DeadLetteredError
		switch {
		case attempt < 3 && !errors.As(err, &scheduled):
			t.Fatalf("attempt %d: %v", attempt, err)
		case attempt == 3 && (!errors.As(err, &dead) || dead.Reason != execution.DeadLetterAttemptsExhausted):
			t.Fatalf("attempt 3: %v", err)
		}
	}
	ex := e.get(t, id)
	if ex.Status != execution.StatusFailed || ex.Attempt != 3 || ex.Error.Code != string(node.ErrCodeUnavailable) {
		t.Fatalf("execution = %+v", ex)
	}
	dl, err := e.rel.DeadLetter(ctx, id)
	if err != nil || dl == nil || dl.Attempt != 3 || dl.Reason != execution.DeadLetterAttemptsExhausted ||
		dl.Error.Code != string(node.ErrCodeUnavailable) || dl.CreatedAt.IsZero() || dl.ID.String() == "" {
		t.Fatalf("dead letter = %+v, %v", dl, err)
	}
	// No attempt 4, no requeue: not claimable, not due, not runnable.
	time.Sleep(300 * time.Millisecond)
	due, _ := e.rel.ClaimDue(ctx, 10, time.Millisecond)
	if _, err := r.Run(ctx, id); !errors.Is(err, execution.ErrExecutionNotClaimable) || len(due) != 0 {
		t.Fatalf("exhausted execution runnable again: %v due=%v", err, due)
	}
	if e.s.count(key) != 3 || e.get(t, id).Attempt != 3 {
		t.Fatalf("mid ran %d times", e.s.count(key))
	}
	// The dead-letter record is append-only.
	if _, err := e.raw.Exec(ctx, "DELETE FROM execution_dead_letters WHERE execution_id = $1", id); err == nil {
		t.Fatal("dead letter deleted")
	}
}

// The database itself refuses to exceed the attempt limit, whatever a writer
// does.
func TestDatabaseEnforcesAttemptLimit(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	retry := func(id uuid.UUID) error {
		_, err := e.raw.Exec(ctx, `UPDATE executions SET status = 'PENDING', next_attempt_at = clock_timestamp() + interval '1 second',
			last_error = '{"code":"X","message":"m","retryable":true}' WHERE id = $1`, id)
		return err
	}
	// Control: with attempts left the same raw retry is accepted, so the
	// refusal below is the attempt limit and nothing else.
	ok, _ := e.create(t, 2, time.Minute, "block")
	e.mustExec(t, "UPDATE executions SET status = 'RUNNING', claim_token = gen_random_uuid() WHERE id = $1", ok)
	if err := retry(ok); err != nil {
		t.Fatalf("control retry refused: %v", err)
	}

	id, _ := e.create(t, 1, time.Minute, "block")
	e.mustExec(t, "UPDATE executions SET status = 'RUNNING', claim_token = gen_random_uuid() WHERE id = $1", id)
	limit := map[string]bool{"lifecycle_attempts_exhausted": true, "executions_attempt_check": true}
	var pgErr *pgconn.PgError
	if err := retry(id); !errors.As(err, &pgErr) || !limit[pgErr.ConstraintName] {
		t.Fatalf("a retry beyond max attempts was not refused by the attempt limit: %v", err)
	}
	if got := e.get(t, id); got.Status != execution.StatusRunning || got.Attempt != 1 {
		t.Fatalf("state changed: %+v", got)
	}
	// Even with the lifecycle triggers switched off, the table constraint
	// keeps an exhausted execution from ever being PENDING (claimable) again.
	e.mustExec(t, "ALTER TABLE executions DISABLE TRIGGER USER")
	_, err := e.raw.Exec(ctx, "UPDATE executions SET status = 'PENDING', claim_token = NULL WHERE id = $1", id)
	e.mustExec(t, "ALTER TABLE executions ENABLE TRIGGER USER")
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != "executions_attempt_check" {
		t.Fatalf("exhausted execution made PENDING: %v", err)
	}
}

// A server's Retry-After is honoured; a retry that could only start after
// the execution deadline is never scheduled.
func TestRetryAfterAndDeadlinePreventingRetry(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	r := e.runner(t, runnerOpts{backoff: fast})

	honoured, _ := e.create(t, 3, time.Minute, "429:600ms", "ok")
	if _, err := r.Run(ctx, honoured); !errors.As(err, new(*execution.RetryScheduledError)) {
		t.Fatalf("429: %v", err)
	}
	ex := e.get(t, honoured)
	if gap := ex.NextAttemptAt.Sub(ex.UpdatedAt); gap < 590*time.Millisecond {
		t.Fatalf("Retry-After 600ms not honoured: retry after %v (max backoff is 200ms)", gap)
	}

	late, key := e.create(t, 3, 2*time.Second, "429:30s", "ok")
	_, err := r.Run(ctx, late)
	var dead *execution.DeadLetteredError
	if !errors.As(err, &dead) || dead.Reason != execution.DeadLetterDeadlineExceeded {
		t.Fatalf("retry past deadline: %v", err)
	}
	if ex := e.get(t, late); ex.Status != execution.StatusFailed || ex.Attempt != 1 || e.s.count(key) != 1 {
		t.Fatalf("execution = %+v", ex)
	}

	// The database refuses such a retry from any writer.
	raw, _ := e.create(t, 3, time.Second, "block")
	e.mustExec(t, "UPDATE executions SET status = 'RUNNING', claim_token = gen_random_uuid() WHERE id = $1", raw)
	if _, err := e.raw.Exec(ctx, `UPDATE executions SET status = 'PENDING', next_attempt_at = now() + interval '1 hour',
		last_error = '{"code":"X","message":"m","retryable":true}' WHERE id = $1`, raw); err == nil {
		t.Fatal("database accepted a retry after the deadline")
	}
}

// The execution deadline cancels the running node, is persisted as
// EXECUTION_TIMEOUT and is never retried, even with attempts left.
func TestExecutionTimeout(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, key := e.create(t, 3, 300*time.Millisecond, "sleep:10s", "ok")
	r := e.runner(t, runnerOpts{backoff: fast})
	start := time.Now()
	_, err := r.Run(ctx, id)
	if time.Since(start) > 3*time.Second || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("run returned %v after %v", err, time.Since(start))
	}
	ex := e.get(t, id)
	if ex.Status != execution.StatusFailed || ex.Error.Code != execution.CodeTimeout || ex.Error.Retryable || ex.Attempt != 1 {
		t.Fatalf("execution = %+v err=%+v", ex, ex.Error)
	}
	if ex.DeadlineAt == nil || ex.StartedAt == nil || ex.DeadlineAt.Sub(*ex.StartedAt) != 300*time.Millisecond {
		t.Fatalf("deadline not persisted as start + timeout: %v %v", ex.DeadlineAt, ex.StartedAt)
	}
	var code string
	_ = e.raw.QueryRow(ctx, "SELECT error->>'code' FROM node_executions WHERE execution_id = $1 AND node_id = 'mid'", id).Scan(&code)
	if code != execution.CodeTimeout {
		t.Fatalf("node record code = %q", code)
	}
	if dl, _ := e.rel.DeadLetter(ctx, id); dl != nil || e.s.count(key) != 1 {
		t.Fatal("a timed-out execution must not be retried")
	}
}

// A node timeout cancels the node and is a retryable NODE_TIMEOUT: retried in
// place (bounded), then by the execution, and every invocation is persisted.
func TestNodeTimeoutIsRetriedAndPersisted(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, key := e.create(t, 3, time.Minute, "sleep:10s", "sleep:10s", "ok")
	r := e.runner(t, runnerOpts{backoff: fast, nodeRetries: 2, nodeTimeout: 100 * time.Millisecond})
	start := time.Now()
	if _, err := r.Run(ctx, id); !errors.As(err, new(*execution.RetryScheduledError)) || time.Since(start) > 3*time.Second {
		t.Fatalf("attempt 1: %v after %v", err, time.Since(start))
	}
	ex := e.get(t, id)
	if ex.LastError.Code != execution.CodeNodeTimeout || !ex.LastError.Retryable {
		t.Fatalf("last error = %+v", ex.LastError)
	}
	time.Sleep(time.Until(*ex.NextAttemptAt) + 20*time.Millisecond)
	if _, err := r.Run(ctx, id); err != nil {
		t.Fatalf("attempt 2: %v", err)
	}
	if ex := e.get(t, id); ex.Status != execution.StatusCompleted || ex.Attempt != 2 || e.s.count(key) != 3 {
		t.Fatalf("execution = %+v, mid ran %d", ex, e.s.count(key))
	}
	if n := e.count(t, `SELECT count(*) FROM node_executions WHERE execution_id = $1 AND node_id = 'mid'
		AND status = 'FAILED' AND execution_attempt = 1 AND error->>'code' = 'NODE_TIMEOUT'`, id); n != 2 {
		t.Fatalf("%d persisted node timeouts, want 2", n)
	}
}

// Cancellation beats retry: a cancel request makes a waiting retry CANCELLED
// at its next claim, without running anything.
func TestCancellationBeatsWaitingRetry(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, key := e.create(t, 3, time.Minute, "503", "ok")
	r := e.runner(t, runnerOpts{backoff: fast})
	if _, err := r.Run(ctx, id); !errors.As(err, new(*execution.RetryScheduledError)) {
		t.Fatal(err)
	}
	if err := e.service(3, time.Minute).RequestCancel(ctx, id); err != nil {
		t.Fatalf("request cancel: %v", err)
	}
	// Due at once (cancellation does not wait for the backoff).
	due, err := e.rel.ClaimDue(ctx, 10, time.Hour)
	if err != nil || len(due) != 1 || due[0].ExecutionID != id {
		t.Fatalf("cancelled retry not dispatched: %v %v", due, err)
	}
	if _, err := r.Run(ctx, id); !errors.Is(err, execution.ErrCancelRequested) {
		t.Fatalf("run: %v", err)
	}
	if ex := e.get(t, id); ex.Status != execution.StatusCancelled || e.s.count(key) != 1 {
		t.Fatalf("execution = %+v, mid ran %d", ex, e.s.count(key))
	}
}

// The database refuses a retry once cancellation was requested.
func TestDatabaseRefusesRetryAfterCancelRequest(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, _ := e.create(t, 3, time.Minute, "block")
	e.mustExec(t, "UPDATE executions SET status = 'RUNNING', claim_token = gen_random_uuid() WHERE id = $1", id)
	if err := e.rel.RequestCancel(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := e.raw.Exec(ctx, `UPDATE executions SET status = 'PENDING', next_attempt_at = now(),
		last_error = '{"code":"X","message":"m","retryable":true}' WHERE id = $1`, id); err == nil {
		t.Fatal("retry accepted after a cancellation request")
	}
}

// Cancellation of a running attempt propagates into the running node through
// the worker's lease heartbeat.
func TestCancellationPropagatesIntoRunningNode(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, key := e.create(t, 3, time.Minute, "sleep:30s", "ok")
	r := e.runner(t, runnerOpts{backoff: fast, keeper: e.heartbeat(t, 400*time.Millisecond, 50*time.Millisecond)})
	done := make(chan error, 1)
	go func() { _, err := r.Run(ctx, id); done <- err }()
	<-e.s.started
	if err := e.service(3, time.Minute).RequestCancel(ctx, id); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, execution.ErrLeaseLost) {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the running node did not observe the cancellation")
	}
	if ex := e.get(t, id); ex.Status != execution.StatusCancelled || e.s.count(key) != 1 {
		t.Fatalf("execution = %+v", ex)
	}
	time.Sleep(200 * time.Millisecond)
	if due, _ := e.rel.ClaimDue(ctx, 10, time.Millisecond); len(due) != 0 {
		t.Fatal("a cancelled execution was scheduled")
	}
}

// The HTTP node performs external side effects without idempotency support,
// so it is declared unsafe: its retryable failures are never retried unless
// the provider reported that the request was not applied.
func TestHTTPNodeIsDeclaredUnsafeToRetry(t *testing.T) {
	e := newEnv(t)
	def, err := e.app.NodeRegistry.GetDefinition("http")
	if err != nil {
		t.Fatal(err)
	}
	if def.SideEffects != node.SideEffectsUnsafe {
		t.Fatalf("http side effects = %q", def.SideEffects)
	}
	unsafe := &execution.NodeExecutionError{NodeID: "h", NodeType: "http", Stage: execution.StageExecute,
		SideEffects: node.SideEffectsUnsafe, Err: node.HTTPStatusError(503, 0, "down")}
	if got := execution.ErrorFromExecution(unsafe); got.Retryable {
		t.Fatalf("unsafe 503 classified retryable: %+v", got)
	}
	unsafe.Err = node.HTTPStatusError(429, 0, "slow down")
	if got := execution.ErrorFromExecution(unsafe); !got.Retryable {
		t.Fatalf("unsafe 429 (not applied) classified non-retryable: %+v", got)
	}
}
