package phase10_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/infrastructure/postgres"
	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/workflow"
)

// Regression tests for the Phase 10 audit findings (F1 unsafe worker-loss
// recovery, F2 stale-worker node writes) and the mutations that survived the
// audit (M4 lease re-check under lock, M9/M10 node-write fences, M21 deadline
// branch). Interleavings are driven by explicit synchronization and by
// polling database state, never by guessing durations.

// waitLeaseExpired waits until the database considers the attempt's lease
// expired.
func (e *env) waitLeaseExpired(t *testing.T, id uuid.UUID) {
	t.Helper()
	eventually(t, 5*time.Second, "lease expiry", func() bool {
		return e.count(t, "SELECT count(*) FROM execution_leases WHERE execution_id = $1 AND expires_at < clock_timestamp()", id) == 1
	})
}

// waitRetryDue waits until the scheduled retry is due on the database clock.
func (e *env) waitRetryDue(t *testing.T, id uuid.UUID) {
	t.Helper()
	eventually(t, 5*time.Second, "retry to become due", func() bool {
		return e.count(t, "SELECT count(*) FROM executions WHERE id = $1 AND status = 'PENDING' AND next_attempt_at <= clock_timestamp()", id) == 1
	})
}

// unsafeFlaky is the scripted node registered as a node type whose
// definition declares unsafe side effects (an HTTP POST, a payment).
type unsafeFlaky struct{ flakyNode }

func (unsafeFlaky) Type() string { return "test.unsafe" }

// unsafeVersion publishes a version whose "mid" node is unsafe.
func (e *env) unsafeVersion(t *testing.T) uuid.UUID {
	t.Helper()
	def := passDefinition("test.unsafe")
	def.SideEffects = node.SideEffectsUnsafe
	if err := e.app.NodeRegistry.RegisterNode(unsafeFlaky{flakyNode{e.s}}, def); err != nil {
		t.Fatal(err)
	}
	wd := definition()
	for i := range wd.Nodes {
		if wd.Nodes[i].ID == "mid" {
			wd.Nodes[i].Type = "test.unsafe"
		}
	}
	raw, _ := json.Marshal(wd)
	v := uuid.New()
	e.mustExec(t, "INSERT INTO workflow_versions (id, workflow_id, version_number, definition, status) VALUES ($1, $2, 2, $3, 'PUBLISHED')", v, e.wf, raw)
	return v
}

// ---------------------------------------------------------------------------
// F1 — worker loss inside a node with unsafe side effects
// ---------------------------------------------------------------------------

// TEST A: worker A dies inside an unsafe node. The reaper must not retry the
// execution (the node may already have applied its effect): FAILED with an
// authoritative retry_unsafe dead letter naming the node, nothing left to
// dispatch or claim, and the unsafe node ran exactly once.
func TestUnsafeNodeInterruptedByWorkerLossIsNotRetried(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	v := e.unsafeVersion(t)
	key := uuid.NewString()
	e.s.set(key, "block", "ok")
	ex, err := e.service(3, time.Minute).Create(ctx, e.wf, v, map[string]any{"key": key})
	if err != nil {
		t.Fatal(err)
	}
	a := e.runner(t, runnerOpts{backoff: fast, keeper: deadKeeper{}, owner: "worker-A", lease: 100 * time.Millisecond})
	doneA := make(chan error, 1)
	go func() { _, err := a.Run(ctx, ex.ID); doneA <- err }()
	e.waitStarted(t, key, 1)

	// The side-effect declaration was persisted with the record.
	var effects string
	_ = e.raw.QueryRow(ctx, "SELECT side_effects FROM node_executions WHERE execution_id = $1 AND node_id = 'mid'", ex.ID).Scan(&effects)
	if effects != "unsafe" {
		t.Fatalf("persisted side effects of mid = %q", effects)
	}

	e.waitLeaseExpired(t, ex.ID)
	if n, err := e.reaper(t).RunOnce(ctx); err != nil || n != 1 {
		t.Fatalf("recovered %d %v", n, err)
	}
	got := e.get(t, ex.ID)
	if got.Status != execution.StatusFailed || got.Attempt != 1 || got.Error == nil ||
		got.Error.Code != execution.CodeWorkerLost || got.Error.Retryable ||
		got.Error.NodeID == nil || *got.Error.NodeID != "mid" {
		t.Fatalf("after recovery: %+v err=%+v", got, got.Error)
	}
	dl, err := e.rel.DeadLetter(ctx, ex.ID)
	if err != nil || dl == nil || dl.Reason != execution.DeadLetterRetryUnsafe || dl.Attempt != 1 ||
		dl.Error.NodeID == nil || *dl.Error.NodeID != "mid" {
		t.Fatalf("dead letter = %+v %v", dl, err)
	}
	if due, err := e.rel.ClaimDue(ctx, 10, time.Millisecond); err != nil || len(due) != 0 {
		t.Fatalf("dead-lettered execution is dispatchable: %+v %v", due, err)
	}
	if _, err := e.runner(t, runnerOpts{backoff: fast}).Run(ctx, ex.ID); !errors.Is(err, execution.ErrExecutionNotClaimable) {
		t.Fatalf("dead-lettered execution claimed again: %v", err)
	}
	if n := e.s.count(key); n != 1 {
		t.Fatalf("unsafe node invoked %d times, want exactly 1", n)
	}
	e.s.unblock(key)
	if err := <-doneA; !errors.Is(err, execution.ErrLeaseLost) && !errors.As(err, new(*execution.ExternallyFinalizedError)) {
		t.Fatalf("lost worker A returned %v", err)
	}
	if n := e.s.count(key); n != 1 {
		t.Fatalf("unsafe node invoked %d times after A woke up", n)
	}
	if e.get(t, ex.ID).Status != execution.StatusFailed {
		t.Fatal("the lost worker changed the dead-lettered execution")
	}
}

// TEST B: the same worker loss inside a node without side effects keeps the
// normal recovery: a retryable WORKER_LOST failure, retried after backoff.
func TestSafeNodeInterruptedByWorkerLossIsRetried(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, key := e.create(t, 3, time.Minute, "block", "ok")
	a := e.runner(t, runnerOpts{backoff: fast, keeper: deadKeeper{}, owner: "worker-A", lease: 100 * time.Millisecond})
	doneA := make(chan error, 1)
	go func() { _, err := a.Run(ctx, id); doneA <- err }()
	t.Cleanup(func() { e.s.unblock(key); <-doneA })
	e.waitStarted(t, key, 1)
	var effects string
	_ = e.raw.QueryRow(ctx, "SELECT side_effects FROM node_executions WHERE execution_id = $1 AND node_id = 'mid'", id).Scan(&effects)
	if effects != "none" {
		t.Fatalf("persisted side effects of mid = %q", effects)
	}
	e.waitLeaseExpired(t, id)
	if n, err := e.reaper(t).RunOnce(ctx); err != nil || n != 1 {
		t.Fatalf("recovered %d %v", n, err)
	}
	got := e.get(t, id)
	if got.Status != execution.StatusPending || got.LastError == nil || got.LastError.Code != execution.CodeWorkerLost ||
		!got.LastError.Retryable || got.NextAttemptAt == nil {
		t.Fatalf("after recovery: %+v last=%+v", got, got.LastError)
	}
	if dl, _ := e.rel.DeadLetter(ctx, id); dl != nil {
		t.Fatalf("safe worker loss dead-lettered: %+v", dl)
	}
	e.waitRetryDue(t, id)
	if _, err := e.runner(t, runnerOpts{backoff: fast}).Run(ctx, id); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := e.get(t, id); got.Status != execution.StatusCompleted || got.Attempt != 2 {
		t.Fatalf("final: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// F2 — a stale worker never writes into a newer attempt
// ---------------------------------------------------------------------------

// gatedLoader pauses the first LoadDefinition (after the claim, before any
// node write) until the test releases it.
type gatedLoader struct {
	execution.DefinitionLoader
	once    sync.Once
	hit     chan struct{}
	release chan struct{}
}

func (g *gatedLoader) LoadDefinition(ctx context.Context, v uuid.UUID) (workflow.Definition, error) {
	g.once.Do(func() { close(g.hit); <-g.release })
	return g.DefinitionLoader.LoadDefinition(ctx, v)
}

// Worker A claims attempt 1 and pauses before writing anything. Its lease
// expires, the reaper recovers the attempt and worker B claims attempt 2 and
// is still RUNNING it (blocked inside "mid"). When A resumes, it must not
// adopt attempt 2, reuse B's completed nodes or write any node record: every
// write is refused as stale and A ends with ErrLeaseLost. B stays the sole
// owner and completes alone.
func TestStaleWorkerCannotWriteIntoNewerRunningAttempt(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, key := e.create(t, 3, time.Minute, "block", "ok")

	gate := &gatedLoader{DefinitionLoader: postgres.NewWorkflowVersionRepository(e.store), hit: make(chan struct{}), release: make(chan struct{})}
	a, err := execution.NewRunner(execution.RunnerConfig{
		Executions: e.execs, NodeExecutions: e.nodes, Definitions: gate,
		Validator: workflow.NewValidator(e.app.NodeRegistry), Graph: execution.NewGraphExecutor(e.app.NodeRegistry),
		Backoff: fast,
		// A's heartbeat has not noticed anything yet: the database fence is
		// what must stop it.
		Leases: deadKeeper{}, Lease: &execution.LeaseGrant{Owner: "worker-A", Duration: 100 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	doneA := make(chan error, 1)
	go func() { _, err := a.Run(ctx, id); doneA <- err }()
	<-gate.hit // A owns attempt 1 and is paused before any node write
	if got := e.get(t, id); got.Status != execution.StatusRunning || got.Attempt != 1 {
		t.Fatalf("A's claim: %+v", got)
	}

	e.waitLeaseExpired(t, id)
	if n, err := e.reaper(t).RunOnce(ctx); err != nil || n != 1 {
		t.Fatalf("recovered %d %v", n, err)
	}
	e.waitRetryDue(t, id)
	b := e.runner(t, runnerOpts{backoff: fast, keeper: deadKeeper{}, owner: "worker-B", lease: time.Minute})
	doneB := make(chan error, 1)
	go func() { _, err := b.Run(ctx, id); doneB <- err }()
	e.waitStarted(t, key, 1) // B is RUNNING attempt 2, blocked inside "mid"
	bLease, ok := e.lease(t, id)
	if !ok || bLease.owner != "worker-B" || bLease.attempt != 2 {
		t.Fatalf("B's lease: %+v %v", bLease, ok)
	}

	close(gate.release) // A resumes while attempt 2 is still RUNNING
	errA := <-doneA
	if !errors.Is(errA, execution.ErrLeaseLost) {
		t.Fatalf("stale worker A returned %v, want ErrLeaseLost", errA)
	}

	// B is still the sole owner of attempt 2.
	if got := e.get(t, id); got.Status != execution.StatusRunning || got.Attempt != 2 || got.ClaimToken == nil || *got.ClaimToken != bLease.token {
		t.Fatalf("after A resumed: %+v", got)
	}
	// Every node record belongs to B's claim of attempt 2; A wrote nothing
	// and ran nothing.
	if n := e.count(t, "SELECT count(*) FROM node_executions WHERE execution_id = $1", id); n != 3 {
		t.Fatalf("%d node records, want B's in, pre and mid", n)
	}
	if n := e.count(t, "SELECT count(*) FROM node_executions WHERE execution_id = $1 AND (execution_attempt <> 2 OR claim_token IS DISTINCT FROM $2)", id, bLease.token); n != 0 {
		t.Fatalf("%d node records not owned by B's claim of attempt 2", n)
	}
	if n := e.count(t, "SELECT count(*) FROM node_executions WHERE execution_id = $1 AND node_id = 'mid' AND status = 'COMPLETED'", id); n != 0 {
		t.Fatal("mid marked completed while B is still running it")
	}
	if e.s.preCount(key) != 1 || e.s.count(key) != 1 {
		t.Fatalf("pre ran %d times, mid %d times: the stale worker executed nodes", e.s.preCount(key), e.s.count(key))
	}

	e.s.unblock(key)
	if err := <-doneB; err != nil {
		t.Fatalf("worker B: %v", err)
	}
	if got := e.get(t, id); got.Status != execution.StatusCompleted || got.Attempt != 2 {
		t.Fatalf("final: %+v", got)
	}
	if n := e.count(t, "SELECT count(*) FROM node_executions WHERE execution_id = $1 AND node_id = 'mid' AND status = 'COMPLETED'", id); n != 1 {
		t.Fatalf("mid completed %d times", n)
	}
}

// gatedReads pauses the first read of an execution (the claim's snapshot
// read, right after the claim committed) until the test releases it.
type gatedReads struct {
	execution.ExecutionRepository
	once    sync.Once
	hit     chan struct{}
	release chan struct{}
}

func (g *gatedReads) Get(ctx context.Context, id uuid.UUID) (execution.Execution, error) {
	g.once.Do(func() { close(g.hit); <-g.release })
	return g.ExecutionRepository.Get(ctx, id)
}

// countingLoader counts definition loads (a Run loads the definition only
// once it owns an attempt).
type countingLoader struct {
	execution.DefinitionLoader
	mu    sync.Mutex
	loads int
}

func (c *countingLoader) LoadDefinition(ctx context.Context, v uuid.UUID) (workflow.Definition, error) {
	c.mu.Lock()
	c.loads++
	c.mu.Unlock()
	return c.DefinitionLoader.LoadDefinition(ctx, v)
}

// The claim's snapshot is bound to the claim: worker A claims attempt 1 and
// pauses before it has read what it claimed; meanwhile attempt 1 is recovered
// and worker B claims attempt 2 (still RUNNING). When A reads, it sees an
// attempt under another claim and must stop with ErrLeaseLost at once: it
// never treats attempt 2 as its own (no definition load, no resume, no node
// write attempt).
func TestClaimSnapshotIsBoundToTheClaim(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, key := e.create(t, 3, time.Minute, "block", "ok")

	reads := &gatedReads{ExecutionRepository: e.execs, hit: make(chan struct{}), release: make(chan struct{})}
	loads := &countingLoader{DefinitionLoader: postgres.NewWorkflowVersionRepository(e.store)}
	a, err := execution.NewRunner(execution.RunnerConfig{
		Executions: reads, NodeExecutions: e.nodes, Definitions: loads,
		Validator: workflow.NewValidator(e.app.NodeRegistry), Graph: execution.NewGraphExecutor(e.app.NodeRegistry),
		Backoff: fast,
		Leases:  deadKeeper{}, Lease: &execution.LeaseGrant{Owner: "worker-A", Duration: 100 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	doneA := make(chan error, 1)
	go func() { _, err := a.Run(ctx, id); doneA <- err }()
	<-reads.hit // A's claim of attempt 1 committed; A has not read it yet

	e.waitLeaseExpired(t, id)
	if n, err := e.reaper(t).RunOnce(ctx); err != nil || n != 1 {
		t.Fatalf("recovered %d %v", n, err)
	}
	e.waitRetryDue(t, id)
	b := e.runner(t, runnerOpts{backoff: fast, keeper: deadKeeper{}, owner: "worker-B", lease: time.Minute})
	doneB := make(chan error, 1)
	go func() { _, err := b.Run(ctx, id); doneB <- err }()
	e.waitStarted(t, key, 1) // B runs attempt 2

	close(reads.release)
	errA := <-doneA
	if !errors.Is(errA, execution.ErrLeaseLost) {
		t.Fatalf("A returned %v, want ErrLeaseLost", errA)
	}
	if loads.loads != 0 {
		t.Fatalf("A loaded the definition %d times: it adopted attempt 2", loads.loads)
	}
	if n := e.count(t, "SELECT count(*) FROM node_executions WHERE execution_id = $1", id); n != 3 {
		t.Fatalf("%d node records, want only B's", n)
	}
	e.s.unblock(key)
	if err := <-doneB; err != nil {
		t.Fatalf("worker B: %v", err)
	}
	if got := e.get(t, id); got.Status != execution.StatusCompleted || got.Attempt != 2 {
		t.Fatalf("final: %+v", got)
	}
}

// The database itself refuses node writes from a claim that no longer owns
// the execution, even when the writer names the current attempt, and refuses
// changes to a record written under an earlier claim.
func TestDatabaseRejectsNodeWritesFromStaleClaim(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, _ := e.create(t, 3, time.Minute)
	first, second := uuid.New(), uuid.New()
	e.mustExec(t, "UPDATE executions SET status = 'RUNNING', claim_token = $2 WHERE id = $1", id, first)

	// The owning claim writes; the record is stamped with claim and attempt.
	owned := uuid.New()
	e.mustExec(t, "INSERT INTO node_executions (id, execution_id, node_id, node_type, status, claim_token) VALUES ($1, $2, 'a', 'text', 'PENDING', $3)", owned, id, first)
	// Another token is refused, also when it names the current attempt.
	stale := func(sql string, args ...any) {
		t.Helper()
		_, err := e.raw.Exec(ctx, sql, args...)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.ConstraintName != "lifecycle_stale_owner" {
			t.Fatalf("%s: err = %v, want lifecycle_stale_owner", sql, err)
		}
	}
	stale("INSERT INTO node_executions (id, execution_id, node_id, node_type, status, claim_token) VALUES ($1, $2, 'b', 'text', 'PENDING', $3)", uuid.New(), id, second)
	stale("INSERT INTO node_executions (id, execution_id, node_id, node_type, status, claim_token, execution_attempt) VALUES ($1, $2, 'b', 'text', 'PENDING', $3, 1)", uuid.New(), id, second)
	// Through the repository it is ErrLeaseLost.
	err := e.nodes.Create(ctx, execution.NodeExecution{ID: uuid.New(), ExecutionID: id, NodeID: "c", NodeType: "text",
		Status: execution.NodeStatusPending, ClaimToken: second})
	if !errors.Is(err, execution.ErrLeaseLost) {
		t.Fatalf("repository write from a stale claim: %v", err)
	}

	// The attempt is retried and re-claimed by another claim (attempt 2):
	// the record written under the first claim can no longer change, and the
	// first claim can no longer write, although attempt numbers are not given.
	e.mustExec(t, `UPDATE executions SET status = 'PENDING', next_attempt_at = clock_timestamp(),
	                last_error = '{"code":"X","message":"m","retryable":true}' WHERE id = $1`, id)
	e.mustExec(t, "UPDATE executions SET status = 'RUNNING', claim_token = $2 WHERE id = $1", id, second)
	stale("UPDATE node_executions SET status = 'RUNNING' WHERE id = $1", owned)
	stale("INSERT INTO node_executions (id, execution_id, node_id, node_type, status, claim_token) VALUES ($1, $2, 'd', 'text', 'PENDING', $3)", uuid.New(), id, first)
	// The new owner writes normally.
	e.mustExec(t, "INSERT INTO node_executions (id, execution_id, node_id, node_type, status, claim_token) VALUES ($1, $2, 'e', 'text', 'PENDING', $3)", uuid.New(), id, second)
	var attempt int
	var token uuid.UUID
	_ = e.raw.QueryRow(ctx, "SELECT execution_attempt, claim_token FROM node_executions WHERE execution_id = $1 AND node_id = 'e'", id).Scan(&attempt, &token)
	if attempt != 2 || token != second {
		t.Fatalf("new owner's record: attempt %d token %s", attempt, token)
	}
	// The fencing columns are immutable.
	fenced := uuid.New()
	e.mustExec(t, "INSERT INTO node_executions (id, execution_id, node_id, node_type, status, claim_token, side_effects) VALUES ($1, $2, 'f', 'text', 'PENDING', $3, 'unsafe')", fenced, id, second)
	e.mustFail(t, "UPDATE node_executions SET status = 'RUNNING', claim_token = $2 WHERE id = $1", fenced, first)
	e.mustFail(t, "UPDATE node_executions SET status = 'RUNNING', side_effects = 'none' WHERE id = $1", fenced)
}

func (e *env) mustFail(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.raw.Exec(context.Background(), sql, args...); err == nil {
		t.Fatalf("accepted: %s", sql)
	}
}

// ---------------------------------------------------------------------------
// M4 — a lease renewed while recovery waits is not recovered
// ---------------------------------------------------------------------------

// The heartbeat/reaper race: a renewal holds the lease row (its UPDATE is in
// flight) when the lease's old expiry passes. The reaper lists the attempt
// (its snapshot shows the old, expired lease), then blocks on the lease row
// in Recover. The renewal commits a new expiry first. Recovery must re-check
// the lease under the lock and leave the healthy worker alone.
func TestRecoveryRechecksLeaseAfterConcurrentRenewal(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, key := e.create(t, 3, time.Minute, "block")
	a := e.runner(t, runnerOpts{backoff: fast, keeper: deadKeeper{}, lease: 100 * time.Millisecond})
	doneA := make(chan error, 1)
	go func() { _, err := a.Run(ctx, id); doneA <- err }()
	e.waitStarted(t, key, 1)

	// The in-flight renewal: lease row locked, new expiry not yet committed.
	renewal, err := e.raw.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = renewal.Rollback(ctx) }()
	var renewalPID int
	if err := renewal.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&renewalPID); err != nil {
		t.Fatal(err)
	}
	if _, err := renewal.Exec(ctx, "SELECT 1 FROM execution_leases WHERE execution_id = $1 FOR UPDATE", id); err != nil {
		t.Fatal(err)
	}
	e.waitLeaseExpired(t, id)
	candidates, err := e.rel.Candidates(ctx, 10)
	if err != nil || len(candidates) != 1 || candidates[0].Reason != "lease_expired" {
		t.Fatalf("candidates = %+v %v", candidates, err)
	}
	if _, err := renewal.Exec(ctx, "UPDATE execution_leases SET expires_at = clock_timestamp() + interval '1 minute' WHERE execution_id = $1", id); err != nil {
		t.Fatal(err)
	}

	reaper := e.reaper(t)
	recovered := make(chan bool, 1)
	failed := make(chan error, 1)
	go func() {
		res, err := e.rel.Recover(ctx, candidates[0], reaper.Plan(candidates[0]))
		if err != nil {
			failed <- err
			return
		}
		recovered <- res.Recovered
	}()
	// Recovery is now waiting for the renewal's lock.
	eventually(t, 5*time.Second, "recovery to wait for the renewal", func() bool {
		return e.count(t, "SELECT count(*) FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid))", renewalPID) == 1
	})
	if err := renewal.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-recovered:
		if r {
			t.Fatal("a lease renewed before recovery got its lock was recovered (healthy worker reaped)")
		}
	case err := <-failed:
		t.Fatalf("recover: %v", err)
	}
	if got := e.get(t, id); got.Status != execution.StatusRunning || got.Attempt != 1 {
		t.Fatalf("healthy attempt changed: %+v", got)
	}
	e.s.unblock(key)
	if err := <-doneA; err != nil {
		t.Fatalf("healthy worker: %v", err)
	}
	if got := e.get(t, id); got.Status != execution.StatusCompleted || got.Attempt != 1 {
		t.Fatalf("final: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// M21 — the execution deadline decides, whatever the node reports
// ---------------------------------------------------------------------------

// Normal timeout (the node honours its context) and a node that reports a
// retryable 503 at the very moment the deadline ends it: both are the
// execution's timeout. FAILED with EXECUTION_TIMEOUT, not retryable, no retry
// scheduled and no dead letter (the retryable 503 must not turn the timeout
// into a retry or a deadline_exceeded dead letter).
func TestExecutionDeadlineWinsOverRetryableError(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, steps := range [][]string{{"sleep:30s"}, {"503-at-end"}} {
		id, key := e.create(t, 3, 300*time.Millisecond, steps...)
		_, err := e.runner(t, runnerOpts{backoff: fast}).Run(ctx, id)
		if err == nil || errors.As(err, new(*execution.RetryScheduledError)) || errors.As(err, new(*execution.DeadLetteredError)) {
			t.Fatalf("%v: run returned %v", steps, err)
		}
		got := e.get(t, id)
		if got.Status != execution.StatusFailed || got.Attempt != 1 || got.Error == nil ||
			got.Error.Code != execution.CodeTimeout || got.Error.Retryable || got.NextAttemptAt != nil {
			t.Fatalf("%v: %+v err=%+v", steps, got, got.Error)
		}
		if dl, _ := e.rel.DeadLetter(ctx, id); dl != nil {
			t.Fatalf("%v: timeout dead-lettered: %+v", steps, dl)
		}
		if n := e.s.count(key); n != 1 {
			t.Fatalf("%v: node invoked %d times", steps, n)
		}
	}
}
