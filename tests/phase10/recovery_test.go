package phase10_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/execution"
	redisinfra "workflow-optimizer/internal/infrastructure/redis"
	"workflow-optimizer/internal/reliability"
)

func (e *env) reaper(t *testing.T) *reliability.Reaper {
	t.Helper()
	r, err := reliability.NewReaper(e.rel, fast, time.Hour, quiet)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// waitStarted waits until the scripted node for key has been invoked n times.
func (e *env) waitStarted(t *testing.T, key string, n int) {
	t.Helper()
	eventually(t, 5*time.Second, "node invocation", func() bool { return e.s.count(key) >= n })
}

type leaseRow struct {
	owner   string
	attempt int
	token   uuid.UUID
	expires time.Time
	beat    time.Time
}

func (e *env) lease(t *testing.T, id uuid.UUID) (leaseRow, bool) {
	t.Helper()
	var l leaseRow
	err := e.raw.QueryRow(context.Background(),
		"SELECT owner, attempt, claim_token, expires_at, heartbeat_at FROM execution_leases WHERE execution_id = $1", id).
		Scan(&l.owner, &l.attempt, &l.token, &l.expires, &l.beat)
	if err != nil {
		return leaseRow{}, false
	}
	return l, true
}

// A running attempt holds a lease named after its worker; the heartbeat keeps
// extending it past its initial duration; it disappears when the attempt ends,
// and only the owning claim can renew it.
func TestLeaseAndHeartbeatLifecycle(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, key := e.create(t, 3, time.Minute, "sleep:900ms")
	const lease = 300 * time.Millisecond
	r := e.runner(t, runnerOpts{backoff: fast, keeper: e.heartbeat(t, lease, 50*time.Millisecond), owner: "worker-A", lease: lease})
	done := make(chan error, 1)
	go func() { _, err := r.Run(ctx, id); done <- err }()
	e.waitStarted(t, key, 1)

	first, ok := e.lease(t, id)
	if !ok || first.owner != "worker-A" || first.attempt != 1 {
		t.Fatalf("lease = %+v ok=%v", first, ok)
	}
	ex := e.get(t, id)
	if ex.ClaimToken == nil || *ex.ClaimToken != first.token {
		t.Fatal("lease is not bound to the execution's claim")
	}
	// Another claim token cannot renew it.
	if st, err := e.rel.Renew(ctx, id, uuid.New(), time.Minute); err != nil || st.Held {
		t.Fatalf("foreign renew: %+v %v", st, err)
	}
	time.Sleep(lease + 150*time.Millisecond)
	later, ok := e.lease(t, id)
	if !ok || !later.expires.After(first.expires) || !later.beat.After(first.beat) {
		t.Fatalf("heartbeat did not extend the lease: %+v -> %+v", first, later)
	}
	if !later.expires.After(time.Now()) {
		t.Fatal("a heartbeating worker's lease expired")
	}
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, ok := e.lease(t, id); ok {
		t.Fatal("lease survived the end of the attempt")
	}
	if st, err := e.rel.Renew(ctx, id, first.token, time.Minute); err != nil || st.Held {
		t.Fatalf("renew after completion: %+v %v", st, err)
	}
	if e.get(t, id).Status != execution.StatusCompleted {
		t.Fatal("not completed")
	}
}

// An expired lease is never revived by a late heartbeat.
func TestExpiredLeaseIsNotRenewed(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, key := e.create(t, 3, time.Minute, "block")
	r := e.runner(t, runnerOpts{backoff: fast, keeper: deadKeeper{}, lease: 100 * time.Millisecond})
	done := make(chan error, 1)
	go func() { _, err := r.Run(ctx, id); done <- err }()
	e.waitStarted(t, key, 1)
	l, _ := e.lease(t, id)
	time.Sleep(150 * time.Millisecond)
	if st, err := e.rel.Renew(ctx, id, l.token, time.Minute); err != nil || st.Held {
		t.Fatalf("expired lease renewed: %+v %v", st, err)
	}
	if n, _ := e.reaper(t).RunOnce(ctx); n != 1 {
		t.Fatalf("recovered %d", n)
	}
	e.s.unblock(key)
	if err := <-done; !errors.Is(err, execution.ErrLeaseLost) {
		t.Fatalf("stale worker: %v", err)
	}
}

// Worker crash recovery: worker A claims attempt 1 and stops heartbeating
// (frozen: its node ignores cancellation). The reaper recovers the attempt as
// a retryable WORKER_LOST failure; worker B runs attempt 2 to completion
// without re-running completed nodes; when A wakes up, every write it tries
// is fenced out and the execution is untouched.
func TestReaperRecoversDeadWorkerAndFencesIt(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, key := e.create(t, 3, time.Minute, "block", "ok")
	a := e.runner(t, runnerOpts{backoff: fast, keeper: deadKeeper{}, owner: "worker-A", lease: 150 * time.Millisecond})
	doneA := make(chan error, 1)
	go func() { _, err := a.Run(ctx, id); doneA <- err }()
	e.waitStarted(t, key, 1)

	reaper := e.reaper(t)
	// Not yet expired: nothing to recover.
	if n, err := reaper.RunOnce(ctx); err != nil || n != 0 {
		t.Fatalf("live lease recovered: n=%d err=%v", n, err)
	}
	time.Sleep(200 * time.Millisecond)
	if n, err := reaper.RunOnce(ctx); err != nil || n != 1 {
		t.Fatalf("recovered n=%d err=%v", n, err)
	}
	ex := e.get(t, id)
	if ex.Status != execution.StatusPending || ex.Attempt != 1 || ex.LastError == nil ||
		ex.LastError.Code != execution.CodeWorkerLost || !ex.LastError.Retryable || ex.LastError.Source != execution.SourceWorker || ex.NextAttemptAt == nil {
		t.Fatalf("after recovery: %+v last=%+v", ex, ex.LastError)
	}
	if _, ok := e.lease(t, id); ok {
		t.Fatal("recovered attempt kept its lease")
	}
	var code string
	_ = e.raw.QueryRow(ctx, "SELECT error->>'code' FROM node_executions WHERE execution_id = $1 AND node_id = 'mid' AND execution_attempt = 1", id).Scan(&code)
	if code != execution.CodeNodeInterrupted {
		t.Fatalf("interrupted node record code = %q", code)
	}
	// Recovery happens once.
	if n, _ := reaper.RunOnce(ctx); n != 0 {
		t.Fatal("recovered twice")
	}

	time.Sleep(time.Until(*ex.NextAttemptAt) + 20*time.Millisecond)
	b := e.runner(t, runnerOpts{backoff: fast, keeper: e.heartbeat(t, time.Second, 100*time.Millisecond), owner: "worker-B"})
	if _, err := b.Run(ctx, id); err != nil {
		t.Fatalf("worker B: %v", err)
	}

	e.s.unblock(key) // worker A wakes up
	errA := <-doneA
	if !errors.Is(errA, execution.ErrLeaseLost) {
		t.Fatalf("stale worker A returned %v", errA)
	}
	ex = e.get(t, id)
	if ex.Status != execution.StatusCompleted || ex.Attempt != 2 {
		t.Fatalf("final: %+v", ex)
	}
	want := []string{"NULL->PENDING@0", "PENDING->RUNNING@1", "RUNNING->PENDING@1", "PENDING->RUNNING@2", "RUNNING->COMPLETED@2"}
	if got := e.history(t, id); !equal(got, want) {
		t.Fatalf("history = %v", got)
	}
	if e.s.preCount(key) != 1 {
		t.Fatalf("completed node re-run: %d", e.s.preCount(key))
	}
	if n := e.count(t, "SELECT count(*) FROM node_executions WHERE execution_id = $1 AND status = 'RUNNING'", id); n != 0 {
		t.Fatal("stale worker left a RUNNING node record")
	}
	if n := e.count(t, "SELECT count(*) FROM node_executions WHERE execution_id = $1 AND node_id = 'mid' AND status = 'COMPLETED'", id); n != 1 {
		t.Fatalf("mid completed %d times in the database", n)
	}
}

// A healthy, heartbeating worker running far longer than its lease duration
// is never reaped, even by a reaper polling continuously.
func TestHealthyWorkerIsNeverReaped(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// The lease is many heartbeats long (25 x 40ms): a renewal delayed by
	// machine load must not let a healthy lease genuinely expire (which the
	// reaper would then rightly recover).
	const lease = time.Second
	var ids []uuid.UUID
	var runs sync.WaitGroup
	errs := make(chan error, 5)
	for i := 0; i < 5; i++ {
		id, _ := e.create(t, 3, time.Minute, "sleep:1200ms")
		ids = append(ids, id)
		r := e.runner(t, runnerOpts{backoff: fast, keeper: e.heartbeat(t, lease, 40*time.Millisecond), lease: lease})
		runs.Add(1)
		go func() { defer runs.Done(); _, err := r.Run(ctx, id); errs <- err }()
	}
	stop := make(chan struct{})
	var recovered atomic.Int64
	var reapers sync.WaitGroup
	for i := 0; i < 3; i++ {
		reapers.Add(1)
		go func() {
			defer reapers.Done()
			r := e.reaper(t)
			for {
				select {
				case <-stop:
					return
				default:
				}
				n, _ := r.RunOnce(ctx)
				recovered.Add(int64(n))
				time.Sleep(5 * time.Millisecond)
			}
		}()
	}
	runs.Wait()
	close(stop)
	reapers.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("healthy run failed: %v", err)
		}
	}
	if recovered.Load() != 0 {
		t.Fatalf("reaped %d healthy attempts", recovered.Load())
	}
	for _, id := range ids {
		ex := e.get(t, id)
		if ex.Status != execution.StatusCompleted || ex.Attempt != 1 {
			t.Fatalf("%s: %+v", id, ex)
		}
	}
}

// Many reapers racing over the same orphaned attempts recover each exactly
// once.
func TestConcurrentReapersRecoverEachAttemptOnce(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	const n = 12
	var keys []string
	var ids []uuid.UUID
	var runs sync.WaitGroup
	for i := 0; i < n; i++ {
		id, key := e.create(t, 3, time.Minute, "block", "ok")
		ids, keys = append(ids, id), append(keys, key)
		r := e.runner(t, runnerOpts{backoff: fast, keeper: deadKeeper{}, lease: 100 * time.Millisecond})
		runs.Add(1)
		go func() { defer runs.Done(); _, _ = r.Run(ctx, id) }()
		e.waitStarted(t, key, 1)
	}
	t.Cleanup(func() {
		for _, k := range keys {
			func() { defer func() { _ = recover() }(); e.s.unblock(k) }()
		}
		runs.Wait()
	})
	time.Sleep(150 * time.Millisecond)

	var total atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := e.reaper(t)
			<-start
			for j := 0; j < 3; j++ {
				k, err := r.RunOnce(ctx)
				if err != nil {
					t.Error(err)
				}
				total.Add(int64(k))
			}
		}()
	}
	close(start)
	wg.Wait()
	if total.Load() != n {
		t.Fatalf("recovered %d attempts, want %d", total.Load(), n)
	}
	for _, id := range ids {
		h := e.history(t, id)
		want := []string{"NULL->PENDING@0", "PENDING->RUNNING@1", "RUNNING->PENDING@1"}
		if !equal(h, want) {
			t.Fatalf("%s history = %v", id, h)
		}
	}
}

// An attempt that overruns the execution deadline while its worker keeps
// heartbeating (a node ignoring cancellation) is failed as EXECUTION_TIMEOUT
// by the reaper, never retried; the heartbeat then stops the worker.
func TestOverdueAttemptWithLiveLeaseIsTimedOut(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, key := e.create(t, 3, 250*time.Millisecond, "block", "ok")
	r := e.runner(t, runnerOpts{backoff: fast, keeper: e.heartbeat(t, time.Second, 50*time.Millisecond)})
	done := make(chan error, 1)
	go func() { _, err := r.Run(ctx, id); done <- err }()
	e.waitStarted(t, key, 1)
	time.Sleep(350 * time.Millisecond)
	if n, err := e.reaper(t).RunOnce(ctx); err != nil || n != 1 {
		t.Fatalf("recovered n=%d err=%v", n, err)
	}
	ex := e.get(t, id)
	if ex.Status != execution.StatusFailed || ex.Error == nil || ex.Error.Code != execution.CodeTimeout || ex.Error.Retryable || ex.Attempt != 1 {
		t.Fatalf("after deadline recovery: %+v err=%+v", ex, ex.Error)
	}
	e.s.unblock(key)
	if err := <-done; err == nil {
		t.Fatal("the overdue worker must not report success")
	}
	if got := e.get(t, id); got.Status != execution.StatusFailed || got.Attempt != 1 || e.s.count(key) != 1 {
		t.Fatalf("overdue attempt was retried or overwritten: %+v", got)
	}
}

// Losing the worker of the last allowed attempt dead-letters the execution
// (DB-authoritative) and publishes a non-authoritative Redis notice carrying
// only IDs and codes.
func TestRecoveryOfLastAttemptIsDeadLettered(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, key := e.create(t, 1, time.Minute, "block")
	r := e.runner(t, runnerOpts{backoff: fast, keeper: deadKeeper{}, lease: 100 * time.Millisecond})
	done := make(chan error, 1)
	go func() { _, err := r.Run(ctx, id); done <- err }()
	e.waitStarted(t, key, 1)
	time.Sleep(150 * time.Millisecond)

	list, err := redisinfra.NewList(e.redis, e.keyPrefix+":dead-letter")
	if err != nil {
		t.Fatal(err)
	}
	reaper := e.reaper(t)
	reaper.Notify = reliability.DeadLetterNotifier(list, quiet)
	if n, err := reaper.RunOnce(ctx); err != nil || n != 1 {
		t.Fatalf("recovered n=%d err=%v", n, err)
	}
	ex := e.get(t, id)
	if ex.Status != execution.StatusFailed || ex.Error.Code != execution.CodeWorkerLost || ex.Attempt != 1 {
		t.Fatalf("after recovery: %+v", ex)
	}
	dl, err := e.rel.DeadLetter(ctx, id)
	if err != nil || dl == nil || dl.Reason != execution.DeadLetterAttemptsExhausted || dl.Attempt != 1 || dl.Error.Code != execution.CodeWorkerLost {
		t.Fatalf("dead letter = %+v err=%v", dl, err)
	}
	items, err := e.rawRedis.LRange(ctx, e.keyPrefix+":dead-letter", 0, -1).Result()
	if err != nil || len(items) != 1 {
		t.Fatalf("redis notices = %v err=%v", items, err)
	}
	var notice map[string]any
	if err := json.Unmarshal([]byte(items[0]), &notice); err != nil || notice["execution_id"] != id.String() || notice["reason"] != "attempts_exhausted" {
		t.Fatalf("notice = %s", items[0])
	}
	for k := range notice {
		switch k {
		case "v", "execution_id", "attempt", "reason", "code":
		default:
			t.Fatalf("notice carries payload field %q", k)
		}
	}
	// Exhausted: nothing brings it back to the queue.
	if n := e.count(t, "SELECT count(*) FROM execution_dispatch WHERE execution_id = $1", id); n != 0 {
		t.Fatal("dead-lettered execution is dispatchable")
	}
	e.s.unblock(key)
	<-done
}

// A cancellation request beats recovery: the orphaned attempt is cancelled,
// not retried.
func TestCancellationBeatsRecovery(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, key := e.create(t, 3, time.Minute, "block", "ok")
	r := e.runner(t, runnerOpts{backoff: fast, keeper: deadKeeper{}, lease: 100 * time.Millisecond})
	done := make(chan error, 1)
	go func() { _, err := r.Run(ctx, id); done <- err }()
	e.waitStarted(t, key, 1)
	if err := e.rel.RequestCancel(ctx, id); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if n, err := e.reaper(t).RunOnce(ctx); err != nil || n != 1 {
		t.Fatalf("recovered n=%d err=%v", n, err)
	}
	if ex := e.get(t, id); ex.Status != execution.StatusCancelled || ex.Attempt != 1 {
		t.Fatalf("after recovery: %+v", ex)
	}
	e.s.unblock(key)
	<-done
	if e.s.count(key) != 1 {
		t.Fatal("cancelled execution ran again")
	}
}

// A recovery candidate is bound to the claim it was listed for: once that
// attempt has been recovered and a new attempt claimed, the stale candidate
// can never recover the new attempt, even when the new attempt's lease has
// expired too. Only a candidate for the new claim recovers it.
func TestStaleRecoveryCandidateIsFenced(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, key := e.create(t, 3, time.Minute, "block", "block", "ok")
	var runs sync.WaitGroup
	start := func(owner string) {
		r := e.runner(t, runnerOpts{backoff: fast, keeper: deadKeeper{}, owner: owner, lease: 100 * time.Millisecond})
		runs.Add(1)
		go func() { defer runs.Done(); _, _ = r.Run(ctx, id) }()
	}
	t.Cleanup(func() { e.s.unblock(key); runs.Wait() })

	start("worker-A")
	e.waitStarted(t, key, 1)
	time.Sleep(150 * time.Millisecond)
	stale, err := e.rel.Candidates(ctx, 10)
	if err != nil || len(stale) != 1 || stale[0].Attempt != 1 {
		t.Fatalf("candidates = %+v %v", stale, err)
	}
	reaper := e.reaper(t)
	if n, _ := reaper.RunOnce(ctx); n != 1 {
		t.Fatal("attempt 1 not recovered")
	}
	time.Sleep(time.Until(*e.get(t, id).NextAttemptAt) + 20*time.Millisecond)
	start("worker-B")
	e.waitStarted(t, key, 2)
	time.Sleep(150 * time.Millisecond) // B's lease expires too

	res, err := e.rel.Recover(ctx, stale[0], reaper.Plan(stale[0]))
	if err != nil || res.Recovered {
		t.Fatalf("stale candidate recovered attempt 2: %+v %v", res, err)
	}
	if ex := e.get(t, id); ex.Status != execution.StatusRunning || ex.Attempt != 2 {
		t.Fatalf("attempt 2 disturbed: %+v", ex)
	}
	if n, _ := reaper.RunOnce(ctx); n != 1 {
		t.Fatal("attempt 2 not recovered by its own candidate")
	}
	want := []string{"NULL->PENDING@0", "PENDING->RUNNING@1", "RUNNING->PENDING@1", "PENDING->RUNNING@2", "RUNNING->PENDING@2"}
	if got := e.history(t, id); !equal(got, want) {
		t.Fatalf("history = %v", got)
	}
}
