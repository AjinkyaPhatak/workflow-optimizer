package phase9_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/worker"
)

// End-to-end regression tests (real PostgreSQL + Redis) for the audit
// findings F1 (node panic) and F2 (pre-claim infrastructure failure).

type panicNode struct{}

func (panicNode) Type() string { return "test.panic" }
func (panicNode) Execute(context.Context, node.NodeInput) (node.NodeOutput, error) {
	var m map[string]int
	m["boom"] = 1 // a plain bug inside a node
	return node.NodeOutput{}, nil
}

// flakyReader fails Get according to fail(n) for the n-th call (1-based),
// then delegates to PostgreSQL.
type flakyReader struct {
	e     *env
	calls atomic.Int32
	fail  func(n int32) bool
}

func (f *flakyReader) Get(ctx context.Context, id uuid.UUID) (execution.Execution, error) {
	if f.fail(f.calls.Add(1)) {
		return execution.Execution{}, errors.New("read tcp: connection reset by peer")
	}
	return f.e.execs.Get(ctx, id)
}

// flakyRunner makes the first Run return as if the claim write had failed
// before being applied (nothing is written), then delegates.
type flakyRunner struct {
	real  *execution.Runner
	calls atomic.Int32
}

func (f *flakyRunner) Run(ctx context.Context, id uuid.UUID) (execution.ExecutionResult, error) {
	if f.calls.Add(1) == 1 {
		return execution.ExecutionResult{}, execution.ErrPersistenceTimeout
	}
	return f.real.Run(ctx, id)
}

func (e *env) startPoolWith(t *testing.T, n int, reader worker.ExecutionReader, runner worker.ExecutionRunner, pause time.Duration) *worker.Pool {
	t.Helper()
	p, err := worker.NewExecutionProcessor(reader, runner)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := worker.NewPool(n, e.queue, p, worker.Options{ErrorPause: pause})
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := pool.Stop(ctx); err != nil {
			t.Errorf("stop pool: %v", err)
		}
	})
	return pool
}

func stat(pool *worker.Pool, f func(worker.Stats) int) int {
	n := 0
	for _, w := range pool.Status().Workers {
		n += f(w.Stats)
	}
	return n
}

// F1: a panicking node fails its own execution through the Phase 8 failure
// path; the worker process survives and the other executions (on the same
// and on other workers) complete.
func TestNodePanicFailsOnlyItsExecution(t *testing.T) {
	e := newEnv(t, 20*time.Millisecond)
	if err := e.app.NodeRegistry.RegisterNode(panicNode{}, passDefinition("test.panic")); err != nil {
		t.Fatal(err)
	}
	pwf, pv := e.seed(t, "test.panic")
	bad, _ := e.createPending(t, pwf, pv)
	cwf, cv := e.seed(t, "test.count")
	var good []uuid.UUID
	var keys []string
	for i := 0; i < 6; i++ {
		ex, key := e.createPending(t, cwf, cv)
		good, keys = append(good, ex.ID), append(keys, key)
	}
	e.enqueue(t, good[:3]...)
	e.enqueue(t, bad.ID)
	e.enqueue(t, good[3:]...)
	pool := e.startPool(t, 2)

	eventually(t, 15*time.Second, "all jobs processed", func() bool { return processed(pool) == 7 })
	got := e.get(t, bad.ID)
	if got.Status != execution.StatusFailed || got.FinishedAt == nil || got.Error == nil ||
		got.Error.NodeID == nil || *got.Error.NodeID != "mid" || got.Error.Code != execution.CodeNodeFailed ||
		!strings.Contains(got.Error.Message, "panicked") || !strings.Contains(got.Error.Message, "test.panic") {
		t.Fatalf("panicking execution = %s %+v", got.Status, got.Error)
	}
	var midStatus string
	if err := e.raw.QueryRow(context.Background(),
		"SELECT status FROM node_executions WHERE execution_id = $1 AND node_id = 'mid'", bad.ID).Scan(&midStatus); err != nil || midStatus != "FAILED" {
		t.Fatalf("panicking node record = %q, %v", midStatus, err)
	}
	for i, id := range good {
		if s := e.get(t, id).Status; s != execution.StatusCompleted || e.counts.get(keys[i]) != 1 {
			t.Fatalf("execution %d = %s (runs %d)", i, s, e.counts.get(keys[i]))
		}
	}
	oc := outcomes(pool)
	if oc[worker.OutcomeFailed] != 1 || oc[worker.OutcomeCompleted] != 6 {
		t.Fatalf("outcomes = %v", oc)
	}
	// Handled by the executor boundary, not the worker's last-resort guard.
	if st := pool.Status(); st.Running != 2 || stat(pool, func(s worker.Stats) int { return s.Panics }) != 0 {
		t.Fatalf("pool status = %+v", st)
	}
}

// F2: a transient PostgreSQL failure while loading the execution (before any
// claim) returns the job to Redis; the execution then runs exactly once.
func TestTransientLoadFailureReturnsJob(t *testing.T) {
	e := newEnv(t, 0)
	wf, v := e.seed(t, "test.count")
	ex, key := e.createPending(t, wf, v)
	e.enqueue(t, ex.ID)
	reader := &flakyReader{e: e, fail: func(n int32) bool { return n == 1 }}
	pool := e.startPoolWith(t, 1, reader, e.runner(t), 50*time.Millisecond)

	eventually(t, 15*time.Second, "COMPLETED", func() bool { return e.get(t, ex.ID).Status == execution.StatusCompleted })
	eventually(t, 5*time.Second, "outcomes recorded", func() bool { return processed(pool) == 2 })
	oc := outcomes(pool)
	if oc[worker.OutcomeError] != 1 || oc[worker.OutcomeCompleted] != 1 || e.counts.get(key) != 1 {
		t.Fatalf("outcomes = %v runs=%d", oc, e.counts.get(key))
	}
	if stat(pool, func(s worker.Stats) int { return s.Requeued }) != 1 || e.queueLen(t) != 0 {
		t.Fatal("the job must have been returned exactly once and consumed")
	}
}

// F2: a claim that was not applied (execution still PENDING) returns the job.
func TestUnappliedClaimReturnsJob(t *testing.T) {
	e := newEnv(t, 0)
	wf, v := e.seed(t, "test.count")
	ex, key := e.createPending(t, wf, v)
	e.enqueue(t, ex.ID)
	pool := e.startPoolWith(t, 1, e.execs, &flakyRunner{real: e.runner(t)}, 50*time.Millisecond)

	eventually(t, 15*time.Second, "COMPLETED", func() bool { return e.get(t, ex.ID).Status == execution.StatusCompleted })
	eventually(t, 5*time.Second, "outcomes recorded", func() bool { return processed(pool) == 2 })
	oc := outcomes(pool)
	if oc[worker.OutcomeNotAttempted] != 1 || oc[worker.OutcomeCompleted] != 1 || e.counts.get(key) != 1 ||
		e.historyCount(t, ex.ID, execution.StatusRunning) != 1 {
		t.Fatalf("outcomes = %v runs=%d", oc, e.counts.get(key))
	}
	if stat(pool, func(s worker.Stats) int { return s.Requeued }) != 1 {
		t.Fatal("the unclaimed job must have been returned once")
	}
}

// Successfully claimed, terminal and RUNNING executions are never returned
// to the queue.
func TestClaimedAndNonPendingJobsAreNotRequeued(t *testing.T) {
	e := newEnv(t, 0)
	wf, v := e.seed(t, "test.count")
	ok, _ := e.createPending(t, wf, v)
	done, _ := e.createPending(t, wf, v)
	running, _ := e.createPending(t, wf, v)
	fwf, fv := e.seed(t, "test.fail")
	failed, _ := e.createPending(t, fwf, fv)
	ctx := context.Background()
	for _, id := range []uuid.UUID{done.ID, running.ID} {
		if err := e.svc.Start(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.svc.Complete(ctx, done.ID, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	e.enqueue(t, ok.ID, failed.ID, done.ID, running.ID)
	pool := e.startPool(t, 2)

	eventually(t, 15*time.Second, "all jobs processed", func() bool { return processed(pool) == 4 })
	time.Sleep(1500 * time.Millisecond) // longer than one BRPOP poll: a requeued job would reappear
	oc := outcomes(pool)
	if processed(pool) != 4 || oc[worker.OutcomeCompleted] != 1 || oc[worker.OutcomeFailed] != 1 || oc[worker.OutcomeSkipped] != 2 {
		t.Fatalf("outcomes = %v", oc)
	}
	if stat(pool, func(s worker.Stats) int { return s.Requeued }) != 0 || e.queueLen(t) != 0 {
		t.Fatal("no job may be returned to the queue")
	}
}

// The invariant survives requeue churn: duplicate jobs plus intermittent
// load failures still run every execution exactly once.
func TestDuplicatesWithTransientFailuresExecuteOnce(t *testing.T) {
	e := newEnv(t, 30*time.Millisecond)
	wf, v := e.seed(t, "test.count")
	const n = 10
	ids := make([]uuid.UUID, n)
	keys := make([]string, n)
	for i := range ids {
		ex, key := e.createPending(t, wf, v)
		ids[i], keys[i] = ex.ID, key
	}
	for r := 0; r < 3; r++ {
		e.enqueue(t, ids...)
	}
	reader := &flakyReader{e: e, fail: func(n int32) bool { return n%3 == 0 }}
	pool := e.startPoolWith(t, 4, reader, e.runner(t), 20*time.Millisecond)

	eventually(t, 30*time.Second, "all executions completed", func() bool {
		for _, id := range ids {
			if e.get(t, id).Status != execution.StatusCompleted {
				return false
			}
		}
		return true
	})
	eventually(t, 15*time.Second, "queue drained", func() bool { return e.queueLen(t) == 0 })
	for i, id := range ids {
		if e.counts.get(keys[i]) != 1 || e.historyCount(t, id, execution.StatusRunning) != 1 {
			t.Fatalf("execution %d ran %d times", i, e.counts.get(keys[i]))
		}
	}
	requeued := stat(pool, func(s worker.Stats) int { return s.Requeued })
	if requeued == 0 {
		t.Fatal("the failure path was not exercised")
	}
	// Every job is accounted for: the 3n originals plus one extra handling per
	// returned job, and nothing was lost. (A claimed run whose status re-read
	// failed is reported as an error, not requeued: the reader also fails
	// those reads.)
	eventually(t, 10*time.Second, "all handlings recorded", func() bool { return processed(pool) == 3*n+requeued })
	if lost := stat(pool, func(s worker.Stats) int { return s.RequeueFailed }); lost != 0 {
		t.Fatalf("%d jobs lost", lost)
	}
}

// A persistent outage neither busy-loops nor loses the job, and shutdown
// still returns promptly with the job left in Redis.
func TestPersistentLoadFailureKeepsJobWithoutHotLoop(t *testing.T) {
	e := newEnv(t, 0)
	wf, v := e.seed(t, "test.count")
	ex, key := e.createPending(t, wf, v)
	e.enqueue(t, ex.ID)
	reader := &flakyReader{e: e, fail: func(int32) bool { return true }}
	p, _ := worker.NewExecutionProcessor(reader, e.runner(t))
	pool, _ := worker.NewPool(2, e.queue, p, worker.Options{ErrorPause: 200 * time.Millisecond})
	_ = pool.Start(context.Background())

	time.Sleep(2 * time.Second)
	attempts := reader.calls.Load()
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := pool.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("shutdown took %v", d)
	}
	// One job, paced at 200ms: ~10 attempts in 2s. A hot loop would be thousands.
	if attempts < 3 || attempts > 25 {
		t.Fatalf("%d load attempts in 2s", attempts)
	}
	if e.get(t, ex.ID).Status != execution.StatusPending || e.counts.get(key) != 0 {
		t.Fatal("nothing may be claimed or run while loads fail")
	}
	if e.queueLen(t) != 1 {
		t.Fatalf("the job must still be in Redis after the outage; len=%d", e.queueLen(t))
	}
}
