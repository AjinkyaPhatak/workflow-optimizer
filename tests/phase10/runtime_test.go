package phase10_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/app"
	"workflow-optimizer/internal/config"
	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/node"
	"workflow-optimizer/internal/queue"
)

// reliabilityConfig is a fast configuration for end-to-end tests.
func (e *env) reliabilityConfig() config.Reliability {
	return config.Reliability{
		MaxAttempts: 3, InitialRetryDelay: 50 * time.Millisecond, MaxRetryDelay: 200 * time.Millisecond, BackoffMultiplier: 2,
		ExecutionTimeout: 20 * time.Second, NodeTimeout: 10 * time.Second, NodeMaxAttempts: 1,
		ReaperInterval: 50 * time.Millisecond, WorkerLeaseDuration: 400 * time.Millisecond,
		WorkerHeartbeatInterval: 50 * time.Millisecond, SchedulerInterval: 30 * time.Millisecond,
		DeadLetterQueue: e.keyPrefix + ":dead-letter",
	}
}

type runningRuntime struct {
	rt     *app.WorkerRuntime
	cancel context.CancelFunc
	done   chan error
}

// startRuntime starts a WorkerRuntime (workers + retry scheduler + reaper)
// on the test's schema and Redis keys; it is stopped on cleanup.
func (e *env) startRuntime(t *testing.T, workers int, rel config.Reliability) *runningRuntime {
	t.Helper()
	cfg := config.Config{
		DatabaseURL: e.dbURL, RedisURL: e.redisURL, RedisQueueName: e.keyPrefix + ":executions",
		WorkerCount: workers, WorkerShutdownTimeout: 10 * time.Second, Reliability: rel,
	}
	rt, err := app.NewWorkerRuntime(context.Background(), cfg, e.app, quiet)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &runningRuntime{rt: rt, cancel: cancel, done: make(chan error, 1)}
	go func() { r.done <- rt.Run(ctx) }()
	t.Cleanup(r.stop)
	return r
}

func (r *runningRuntime) stop() {
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(15 * time.Second):
	}
}

func (e *env) submit(t *testing.T, rt *runningRuntime, steps ...string) (uuid.UUID, string) {
	t.Helper()
	s, err := rt.rt.Submitter()
	if err != nil {
		t.Fatal(err)
	}
	key := uuid.NewString()
	e.s.set(key, steps...)
	ex, err := s.Submit(context.Background(), e.wf, e.v, map[string]any{"key": key})
	if err != nil {
		t.Fatal(err)
	}
	return ex.ID, key
}

func (e *env) waitTerminal(t *testing.T, id uuid.UUID, within time.Duration) execution.Execution {
	t.Helper()
	var ex execution.Execution
	eventually(t, within, "terminal status of "+id.String(), func() bool {
		ex = e.get(t, id)
		return ex.Status.IsTerminal()
	})
	return ex
}

func (e *env) deadLetterNotices(t *testing.T) []map[string]any {
	t.Helper()
	raw, err := e.rawRedis.LRange(context.Background(), e.keyPrefix+":dead-letter", 0, -1).Result()
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, r := range raw {
		var m map[string]any
		if err := json.Unmarshal([]byte(r), &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

// End to end through Redis and the worker runtime: a retryable failure is
// retried by the scheduler and completes on attempt 2; a permanent failure
// fails on attempt 1; a failure that keeps happening stops after
// DEFAULT_MAX_ATTEMPTS and is dead-lettered (PostgreSQL + Redis notice) and
// never returns to the queue; an execution over its timeout fails as
// EXECUTION_TIMEOUT.
func TestRuntimeEndToEndRetryOutcomes(t *testing.T) {
	e := newEnv(t)
	rel := e.reliabilityConfig()
	rt := e.startRuntime(t, 3, rel)

	retried, rkey := e.submit(t, rt, "503", "ok")
	permanent, pkey := e.submit(t, rt, "401", "ok")
	exhausted, xkey := e.submit(t, rt, "503")

	ex := e.waitTerminal(t, retried, 15*time.Second)
	if ex.Status != execution.StatusCompleted || ex.Attempt != 2 || e.s.count(rkey) != 2 || e.s.preCount(rkey) != 1 {
		t.Fatalf("retried: %+v mid=%d pre=%d", ex, e.s.count(rkey), e.s.preCount(rkey))
	}
	ex = e.waitTerminal(t, permanent, 15*time.Second)
	if ex.Status != execution.StatusFailed || ex.Attempt != 1 || ex.Error.Code != string(node.ErrCodeInvalidCredentials) || e.s.count(pkey) != 1 {
		t.Fatalf("permanent: %+v err=%+v", ex, ex.Error)
	}
	ex = e.waitTerminal(t, exhausted, 15*time.Second)
	if ex.Status != execution.StatusFailed || ex.Attempt != 3 || e.s.count(xkey) != 3 {
		t.Fatalf("exhausted: %+v calls=%d", ex, e.s.count(xkey))
	}
	dl, err := e.rel.DeadLetter(context.Background(), exhausted)
	if err != nil || dl == nil || dl.Reason != execution.DeadLetterAttemptsExhausted || dl.Attempt != 3 {
		t.Fatalf("dead letter: %+v %v", dl, err)
	}
	notices := e.deadLetterNotices(t)
	if len(notices) != 1 || notices[0]["execution_id"] != exhausted.String() || notices[0]["attempt"] != float64(3) {
		t.Fatalf("redis notices = %v", notices)
	}
	// Give the scheduler and reaper time: nothing brings it back.
	time.Sleep(300 * time.Millisecond)
	if ex := e.get(t, exhausted); ex.Attempt != 3 || e.s.count(xkey) != 3 {
		t.Fatal("an exhausted execution ran again")
	}
	if n := e.count(t, "SELECT count(*) FROM execution_dispatch WHERE execution_id = ANY($1)", []uuid.UUID{retried, permanent, exhausted}); n != 0 {
		t.Fatal("terminal executions are still dispatchable")
	}
}

func TestRuntimeEndToEndExecutionTimeout(t *testing.T) {
	e := newEnv(t)
	rel := e.reliabilityConfig()
	rel.ExecutionTimeout = 300 * time.Millisecond
	rt := e.startRuntime(t, 2, rel)
	id, key := e.submit(t, rt, "sleep:10s", "ok")
	ex := e.waitTerminal(t, id, 10*time.Second)
	if ex.Status != execution.StatusFailed || ex.Error.Code != execution.CodeTimeout || ex.Attempt != 1 || e.s.count(key) != 1 {
		t.Fatalf("%+v err=%+v", ex, ex.Error)
	}
	if ex.FinishedAt == nil || ex.FinishedAt.After(ex.DeadlineAt.Add(2*time.Second)) {
		t.Fatalf("timeout recorded late: completed %v deadline %v", ex.FinishedAt, ex.DeadlineAt)
	}
}

// Worker crash recovery end to end: a worker claims an execution and dies
// (no heartbeat, never finishes). The running runtime's reaper recovers it,
// its scheduler re-enqueues it, and its workers complete attempt 2.
func TestRuntimeRecoversCrashedWorker(t *testing.T) {
	e := newEnv(t)
	rel := e.reliabilityConfig()
	id, key := e.create(t, 3, time.Minute, "block", "ok")
	crashed := e.runner(t, runnerOpts{backoff: fast, keeper: deadKeeper{}, owner: "crashed-worker", lease: 200 * time.Millisecond})
	done := make(chan error, 1)
	go func() { _, err := crashed.Run(context.Background(), id); done <- err }()
	e.waitStarted(t, key, 1)

	e.startRuntime(t, 2, rel)
	ex := e.waitTerminal(t, id, 15*time.Second)
	if ex.Status != execution.StatusCompleted || ex.Attempt != 2 || e.s.preCount(key) != 1 {
		t.Fatalf("%+v pre=%d", ex, e.s.preCount(key))
	}
	want := []string{"NULL->PENDING@0", "PENDING->RUNNING@1", "RUNNING->PENDING@1", "PENDING->RUNNING@2", "RUNNING->COMPLETED@2"}
	if got := e.history(t, id); !equal(got, want) {
		t.Fatalf("history = %v", got)
	}
	e.s.unblock(key)
	<-done
	if got := e.get(t, id); got.Status != execution.StatusCompleted || got.Attempt != 2 {
		t.Fatal("the crashed worker overwrote the recovered execution")
	}
}

// ---------------------------------------------------------------------------
// Concurrency
// ---------------------------------------------------------------------------

// 100 executions, 5 workers in two runtimes (so two retry schedulers and two
// reapers run concurrently), retryable and permanent failures, Retry-After,
// exhausted retries, every job duplicated in Redis, and crashed workers
// holding claims. Afterwards: every execution is terminal with the expected
// outcome, no attempt ran twice, attempts never exceed the limit, histories
// are legal, and nothing is left leased, dispatched or queued.
func TestConcurrentExecutionsWithFailuresDuplicatesAndCrashes(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	rel := e.reliabilityConfig()

	type class struct {
		steps    []string
		status   execution.ExecutionStatus
		attempt  int
		calls    int
		dead     bool
		crashing bool
	}
	classes := []class{
		{steps: []string{"sleep:10ms"}, status: execution.StatusCompleted, attempt: 1, calls: 1},
		{steps: []string{"503", "sleep:10ms"}, status: execution.StatusCompleted, attempt: 2, calls: 2},
		{steps: []string{"503", "503", "ok"}, status: execution.StatusCompleted, attempt: 3, calls: 3},
		{steps: []string{"429:80ms", "ok"}, status: execution.StatusCompleted, attempt: 2, calls: 2},
		{steps: []string{"401"}, status: execution.StatusFailed, attempt: 1, calls: 1},
		{steps: []string{"503"}, status: execution.StatusFailed, attempt: 3, calls: 3, dead: true},
		{steps: []string{"block", "ok"}, status: execution.StatusCompleted, attempt: 2, calls: 2, crashing: true},
	}
	const total = 100
	type item struct {
		id  uuid.UUID
		key string
		c   class
	}
	items := make([]item, 0, total)
	var crashed sync.WaitGroup
	var crashedKeys []string
	t.Cleanup(func() {
		for _, k := range crashedKeys {
			func() { defer func() { _ = recover() }(); e.s.unblock(k) }()
		}
		crashed.Wait()
	})
	// Crashed workers first: they hold claims with no heartbeat.
	for i := 0; i < total; i++ {
		c := classes[i%len(classes)]
		id, key := e.create(t, rel.MaxAttempts, rel.ExecutionTimeout, c.steps...)
		items = append(items, item{id, key, c})
		if c.crashing {
			r := e.runner(t, runnerOpts{backoff: fast, keeper: deadKeeper{}, owner: "crashed-" + key[:6], lease: 200 * time.Millisecond})
			crashed.Add(1)
			crashedKeys = append(crashedKeys, key)
			go func() { defer crashed.Done(); _, _ = r.Run(ctx, id) }()
			e.waitStarted(t, key, 1)
		}
	}
	// Every job is enqueued three times.
	for _, it := range items {
		for d := 0; d < 3; d++ {
			if err := e.queue.Enqueue(ctx, queue.Job{ExecutionID: it.id}); err != nil {
				t.Fatal(err)
			}
		}
	}
	e.startRuntime(t, 3, rel)
	e.startRuntime(t, 2, rel)

	for _, it := range items {
		e.waitTerminal(t, it.id, 60*time.Second)
	}

	legal := map[string]bool{
		"NULL->PENDING": true, "PENDING->RUNNING": true, "RUNNING->PENDING": true,
		"RUNNING->COMPLETED": true, "RUNNING->FAILED": true, "RUNNING->CANCELLED": true, "PENDING->CANCELLED": true,
	}
	deadLettered := 0
	for _, it := range items {
		ex := e.get(t, it.id)
		if ex.Status != it.c.status || ex.Attempt != it.c.attempt || ex.Attempt > ex.MaxAttempts {
			t.Fatalf("%v: status %s attempt %d, want %s attempt %d (err %+v)", it.c.steps, ex.Status, ex.Attempt, it.c.status, it.c.attempt, ex.Error)
		}
		// No attempt ran twice despite duplicate jobs and two schedulers.
		if got := e.s.count(it.key); got != it.c.calls {
			t.Fatalf("%v: node invoked %d times, want %d", it.c.steps, got, it.c.calls)
		}
		if got := e.s.preCount(it.key); got != 1 {
			t.Fatalf("%v: completed node ran %d times", it.c.steps, got)
		}
		// History: legal transitions, one claim per attempt, attempts in order.
		h, err := e.execs.History(ctx, it.id)
		if err != nil {
			t.Fatal(err)
		}
		claims := 0
		for _, tr := range h {
			from := "NULL"
			if tr.From != nil {
				from = string(*tr.From)
			}
			if !legal[from+"->"+string(tr.To)] {
				t.Fatalf("illegal transition %s->%s", from, tr.To)
			}
			if tr.To == execution.StatusRunning {
				claims++
				if tr.Attempt != claims {
					t.Fatalf("claim %d recorded as attempt %d", claims, tr.Attempt)
				}
			}
		}
		if claims != it.c.attempt {
			t.Fatalf("%v: %d claims for %d attempts", it.c.steps, claims, it.c.attempt)
		}
		dl, err := e.rel.DeadLetter(ctx, it.id)
		if err != nil {
			t.Fatal(err)
		}
		if (dl != nil) != it.c.dead {
			t.Fatalf("%v: dead letter = %+v", it.c.steps, dl)
		}
		if dl != nil {
			deadLettered++
		}
	}
	// Database-level checks.
	if n := e.count(t, "SELECT count(*) FROM node_executions WHERE status = 'RUNNING'"); n != 0 {
		t.Fatalf("%d node records left RUNNING", n)
	}
	if n := e.count(t, `SELECT count(*) FROM (SELECT execution_id, node_id FROM node_executions WHERE status = 'COMPLETED'
		GROUP BY execution_id, node_id HAVING count(*) > 1) d`); n != 0 {
		t.Fatalf("%d nodes completed more than once", n)
	}
	if n := e.count(t, "SELECT count(*) FROM execution_leases"); n != 0 {
		t.Fatalf("%d leases left", n)
	}
	if n := e.count(t, "SELECT count(*) FROM execution_dispatch"); n != 0 {
		t.Fatalf("%d dispatch rows left", n)
	}
	if n := e.count(t, "SELECT count(*) FROM execution_dead_letters"); n != deadLettered {
		t.Fatalf("dead letters %d, want %d", n, deadLettered)
	}
	eventually(t, 5*time.Second, "queue to drain", func() bool {
		n, _ := e.rawRedis.LLen(ctx, e.keyPrefix+":executions").Result()
		return n == 0
	})
	if notices := e.deadLetterNotices(t); len(notices) != deadLettered {
		t.Fatalf("%d redis notices for %d dead letters", len(notices), deadLettered)
	}
	t.Logf("100 executions: %d dead-lettered, %d crashed-and-recovered", deadLettered, len(crashedKeys))
}
