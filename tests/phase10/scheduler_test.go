package phase10_test

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/queue"
	"workflow-optimizer/internal/reliability"
)

// markUndelivered makes executions look like retries whose delivery is still
// owed (what a RUNNING -> PENDING transition leaves behind).
func (e *env) markUndelivered(t *testing.T, ids []uuid.UUID) {
	t.Helper()
	e.mustExec(t, "UPDATE execution_dispatch SET dispatched_at = NULL WHERE execution_id = ANY($1)", ids)
}

// Concurrent schedulers claim disjoint sets: every due execution is handed
// out exactly once per re-dispatch interval.
func TestConcurrentSchedulersClaimDisjointly(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	const n = 80
	var ids []uuid.UUID
	for i := 0; i < n; i++ {
		id, _ := e.create(t, 3, time.Minute)
		ids = append(ids, id)
	}
	e.markUndelivered(t, ids)

	var mu sync.Mutex
	seen := map[uuid.UUID]int{}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for s := 0; s < 6; s++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for {
				batch, err := e.rel.ClaimDue(ctx, 7, time.Minute)
				if err != nil {
					t.Error(err)
					return
				}
				if len(batch) == 0 {
					return
				}
				mu.Lock()
				claims := 0
				for _, d := range batch {
					seen[d.ExecutionID]++
				}
				for _, c := range seen {
					claims += c
				}
				mu.Unlock()
				if claims > 2*n { // bounded: repeated claims fail below, not hang
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	if len(seen) != n {
		t.Fatalf("claimed %d distinct executions, want %d", len(seen), n)
	}
	for id, c := range seen {
		if c != 1 {
			t.Fatalf("%s claimed %d times", id, c)
		}
	}
	if again, _ := e.rel.ClaimDue(ctx, 100, time.Minute); len(again) != 0 {
		t.Fatalf("re-claimed %d within the re-dispatch interval", len(again))
	}
}

// Executions that are not due are never dispatched: a retry still waiting,
// a running attempt, a terminal execution. A lost delivery is re-dispatched
// after the interval, and a cancel request makes a waiting retry due at once.
func TestSchedulerDispatchesOnlyDueWork(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	slowBackoff := constantBackoff(time.Hour)

	waiting, _ := e.create(t, 3, 2*time.Hour, "503")
	if _, err := e.runner(t, runnerOpts{backoff: slowBackoff}).Run(ctx, waiting); !errors.As(err, new(*execution.RetryScheduledError)) {
		t.Fatalf("waiting: %v", err)
	}
	done, _ := e.create(t, 3, time.Minute, "ok")
	if _, err := e.runner(t, runnerOpts{}).Run(ctx, done); err != nil {
		t.Fatal(err)
	}
	fresh, _ := e.create(t, 3, time.Minute) // dispatched by its creator
	running, rkey := e.create(t, 3, time.Minute, "block")
	go func() { _, _ = e.runner(t, runnerOpts{}).Run(ctx, running) }()
	e.waitStarted(t, rkey, 1)
	t.Cleanup(func() { e.s.unblock(rkey) })

	if got, _ := e.rel.ClaimDue(ctx, 100, time.Minute); len(got) != 0 {
		t.Fatalf("claimed not-due work: %+v", got)
	}
	// The creator's delivery was lost: re-dispatched once the interval passed.
	time.Sleep(120 * time.Millisecond)
	got, _ := e.rel.ClaimDue(ctx, 100, 100*time.Millisecond)
	if len(got) != 1 || got[0].ExecutionID != fresh {
		t.Fatalf("re-dispatch = %+v", got)
	}
	// Cancelling the waiting retry makes it due (to be claimed and cancelled).
	if err := e.rel.RequestCancel(ctx, waiting); err != nil {
		t.Fatal(err)
	}
	got, _ = e.rel.ClaimDue(ctx, 100, time.Minute)
	if len(got) != 1 || got[0].ExecutionID != waiting {
		t.Fatalf("cancel-requested retry = %+v", got)
	}
	if _, err := e.runner(t, runnerOpts{backoff: slowBackoff}).Run(ctx, waiting); !errors.Is(err, execution.ErrCancelRequested) {
		t.Fatalf("claiming a cancel-requested retry: %v", err)
	}
	if ex := e.get(t, waiting); ex.Status != execution.StatusCancelled || ex.Attempt != 2 {
		t.Fatalf("cancelled retry: %+v", ex)
	}
}

// The scheduler turns a due retry into a Redis job carrying only IDs, and
// several schedulers running together enqueue it once.
func TestSchedulerEnqueuesDueRetriesToRedis(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var ids []uuid.UUID
	for i := 0; i < 20; i++ {
		id, _ := e.create(t, 3, time.Minute, "503", "ok")
		if _, err := e.runner(t, runnerOpts{backoff: fast}).Run(ctx, id); !errors.As(err, new(*execution.RetryScheduledError)) {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	time.Sleep(fast.MaxDelay + 50*time.Millisecond)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		s, err := reliability.NewScheduler(e.rel, e.queue, time.Hour, time.Minute, quiet)
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = s.RunOnce(ctx) }()
	}
	wg.Wait()
	raw, err := e.rawRedis.LRange(ctx, e.keyPrefix+":executions", 0, -1).Result()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, item := range raw {
		j, err := queue.DecodeJob([]byte(item))
		if err != nil {
			t.Fatalf("job %q: %v", item, err)
		}
		got = append(got, j.ExecutionID.String())
	}
	var want []string
	for _, id := range ids {
		want = append(want, id.String())
	}
	sort.Strings(got)
	sort.Strings(want)
	if !equal(got, want) {
		t.Fatalf("enqueued %d jobs for %d due retries", len(got), len(want))
	}
}

// Two workers racing for the same execution: exactly one claims each attempt
// and the node runs once.
func TestTwoWorkersRaceForTheSameExecution(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for i := 0; i < 25; i++ {
		id, key := e.create(t, 3, time.Minute, "sleep:20ms")
		a := e.runner(t, runnerOpts{keeper: e.heartbeat(t, time.Second, 100*time.Millisecond)})
		b := e.runner(t, runnerOpts{keeper: e.heartbeat(t, time.Second, 100*time.Millisecond)})
		errs := make([]error, 2)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for j, r := range []*execution.Runner{a, b} {
			wg.Add(1)
			go func() { defer wg.Done(); <-start; _, errs[j] = r.Run(ctx, id) }()
		}
		close(start)
		wg.Wait()
		ok := 0
		for _, err := range errs {
			if err == nil {
				ok++
			} else if !errors.Is(err, execution.ErrExecutionNotClaimable) {
				t.Fatalf("loser returned %v", err)
			}
		}
		if ok != 1 || e.s.count(key) != 1 {
			t.Fatalf("%d winners, node ran %d times (errs %v)", ok, e.s.count(key), errs)
		}
		if ex := e.get(t, id); ex.Status != execution.StatusCompleted || ex.Attempt != 1 {
			t.Fatalf("%+v", ex)
		}
	}
}

type constantBackoff time.Duration

func (c constantBackoff) Delay(int, time.Duration) time.Duration {
	return time.Duration(c)
}
