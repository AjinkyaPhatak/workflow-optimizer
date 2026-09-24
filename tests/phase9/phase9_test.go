package phase9_test

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/app"
	"workflow-optimizer/internal/config"
	"workflow-optimizer/internal/execution"
	"workflow-optimizer/internal/queue"
	"workflow-optimizer/internal/worker"
)

func dispatcher(t *testing.T, e *env) *queue.QueueDispatcher {
	t.Helper()
	d, err := queue.NewDispatcher(e.queue)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestWorkerCompletesExecutionFromRedis(t *testing.T) {
	e := newEnv(t, 0)
	wf, v := e.seed(t, "test.count")
	ex, key := e.createPending(t, wf, v)
	e.enqueue(t, ex.ID)
	pool := e.startPool(t, 1)

	eventually(t, 15*time.Second, "COMPLETED", func() bool { return e.get(t, ex.ID).Status == execution.StatusCompleted })
	got := e.get(t, ex.ID)
	if got.Output == nil || got.StartedAt == nil || got.FinishedAt == nil || got.Error != nil {
		t.Fatalf("completed execution = %+v", got)
	}
	if n := e.counts.get(key); n != 1 {
		t.Fatalf("graph ran %d times", n)
	}
	eventually(t, 5*time.Second, "outcome recorded", func() bool { return outcomes(pool)[worker.OutcomeCompleted] == 1 })
	if e.queueLen(t) != 0 {
		t.Fatal("queue should be drained")
	}
}

func TestWorkerRecordsFailedExecution(t *testing.T) {
	e := newEnv(t, 0)
	wf, v := e.seed(t, "test.fail")
	ex, key := e.createPending(t, wf, v)
	e.enqueue(t, ex.ID)
	pool := e.startPool(t, 1)

	eventually(t, 15*time.Second, "FAILED", func() bool { return e.get(t, ex.ID).Status == execution.StatusFailed })
	got := e.get(t, ex.ID)
	if got.Error == nil || got.Error.NodeID == nil || *got.Error.NodeID != "mid" {
		t.Fatalf("failure not recorded: %+v", got.Error)
	}
	if e.counts.get(key) != 1 {
		t.Fatal("failing node must run exactly once (no retry)")
	}
	eventually(t, 5*time.Second, "outcome recorded", func() bool { return outcomes(pool)[worker.OutcomeFailed] == 1 })
}

// MANDATORY: several jobs for the SAME execution, several workers. Exactly
// one worker claims it and exactly one GraphExecutor run happens.
func TestDuplicateJobsExactlyOneClaimAndOneGraphRun(t *testing.T) {
	// The node is slow so duplicates are dequeued while the first worker
	// still holds the execution.
	e := newEnv(t, 300*time.Millisecond)
	wf, v := e.seed(t, "test.count")
	ex, key := e.createPending(t, wf, v)
	const dupes = 6
	for i := 0; i < dupes; i++ {
		e.enqueue(t, ex.ID)
	}
	pool := e.startPool(t, 4)

	eventually(t, 15*time.Second, "all duplicate jobs processed", func() bool { return processed(pool) == dupes })
	got := e.get(t, ex.ID)
	if got.Status != execution.StatusCompleted {
		t.Fatalf("status = %s", got.Status)
	}
	if n := e.counts.get(key); n != 1 {
		t.Fatalf("GraphExecutor ran %d times for one execution", n)
	}
	if n := e.nodeRows(t, ex.ID, "mid"); n != 1 {
		t.Fatalf("node execution rows = %d", n)
	}
	if n := e.historyCount(t, ex.ID, execution.StatusRunning); n != 1 {
		t.Fatalf("RUNNING transitions = %d", n)
	}
	oc := outcomes(pool)
	if oc[worker.OutcomeCompleted] != 1 || oc[worker.OutcomeCompleted]+oc[worker.OutcomeClaimLost]+oc[worker.OutcomeSkipped] != dupes {
		t.Fatalf("outcomes = %v", oc)
	}
	executedBy := 0
	for _, w := range pool.Status().Workers {
		executedBy += w.Stats.Outcomes[worker.OutcomeCompleted]
	}
	if executedBy != 1 {
		t.Fatalf("%d workers executed the execution", executedBy)
	}
}

// The strongest form of the duplicate test: processors race on the SAME
// PENDING execution simultaneously, so every one passes the PENDING pre-check
// and only the PostgreSQL claim can keep them apart.
func TestConcurrentProcessorsRaceForTheSameClaim(t *testing.T) {
	e := newEnv(t, 0)
	wf, v := e.seed(t, "test.count")
	const rounds, racers = 25, 8
	claimLost := 0
	for r := 0; r < rounds; r++ {
		ex, key := e.createPending(t, wf, v)
		var (
			start   = make(chan struct{})
			wg      sync.WaitGroup
			mu      sync.Mutex
			results []worker.Result
		)
		for i := 0; i < racers; i++ {
			p := e.processor(t) // independent processors, as in separate workers
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				res := p.Process(context.Background(), queue.Job{ExecutionID: ex.ID})
				mu.Lock()
				results = append(results, res)
				mu.Unlock()
			}()
		}
		close(start)
		wg.Wait()
		executed := 0
		for _, res := range results {
			switch res.Outcome {
			case worker.OutcomeCompleted:
				executed++
			case worker.OutcomeClaimLost:
				claimLost++
			case worker.OutcomeSkipped:
			default:
				t.Fatalf("round %d: unexpected result %+v", r, res)
			}
		}
		if executed != 1 || e.counts.get(key) != 1 {
			t.Fatalf("round %d: executed=%d graph runs=%d", r, executed, e.counts.get(key))
		}
		if e.historyCount(t, ex.ID, execution.StatusRunning) != 1 {
			t.Fatalf("round %d: RUNNING transitions != 1", r)
		}
	}
	if claimLost == 0 {
		t.Fatal("no racer ever lost the atomic claim; the race was not exercised")
	}
	t.Logf("%d rounds x %d racers: %d lost the PostgreSQL claim", rounds, racers, claimLost)
}

func TestHundredExecutionsFiveWorkers(t *testing.T) {
	e := newEnv(t, 5*time.Millisecond)
	wf, v := e.seed(t, "test.count")
	const n, workers = 100, 5
	ids := make([]uuid.UUID, n)
	keys := make([]string, n)
	for i := range ids {
		ex, key := e.createPending(t, wf, v)
		ids[i], keys[i] = ex.ID, key
	}
	e.enqueue(t, ids...)
	e.enqueue(t, ids[:20]...) // duplicates on top
	pool := e.startPool(t, workers)

	eventually(t, 60*time.Second, "all jobs processed", func() bool { return processed(pool) == n+20 })
	for i, id := range ids {
		if s := e.get(t, id).Status; s != execution.StatusCompleted {
			t.Fatalf("execution %d status %s", i, s)
		}
		if c := e.counts.get(keys[i]); c != 1 {
			t.Fatalf("execution %d ran %d times", i, c)
		}
	}
	if e.counts.total() != n {
		t.Fatalf("total graph runs = %d", e.counts.total())
	}
	oc := outcomes(pool)
	if oc[worker.OutcomeCompleted] != n {
		t.Fatalf("outcomes = %v", oc)
	}
	active := 0
	for _, w := range pool.Status().Workers {
		if w.Stats.Outcomes[worker.OutcomeCompleted] > 0 {
			active++
		}
	}
	if st := pool.Status(); st.Configured != workers || st.Running != workers || active < 2 {
		t.Fatalf("configured=%d running=%d workers that executed=%d", st.Configured, st.Running, active)
	}
}

func TestJobsForNonPendingExecutionsAreIgnored(t *testing.T) {
	e := newEnv(t, 0)
	wf, v := e.seed(t, "test.count")
	ctx := context.Background()

	running, _ := e.createPending(t, wf, v)
	completed, _ := e.createPending(t, wf, v)
	failed, _ := e.createPending(t, wf, v)
	cancelled, _ := e.createPending(t, wf, v)
	for _, id := range []uuid.UUID{running.ID, completed.ID, failed.ID, cancelled.ID} {
		if err := e.svc.Start(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.svc.Complete(ctx, completed.ID, map[string]any{"done": true}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Fail(ctx, failed.ID, execution.ExecutionError{Code: "X", Message: "boom"}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Cancel(ctx, cancelled.ID); err != nil {
		t.Fatal(err)
	}
	unknown := uuid.New()
	e.enqueue(t, running.ID, completed.ID, failed.ID, cancelled.ID, unknown)
	pool := e.startPool(t, 2)

	eventually(t, 15*time.Second, "all jobs processed", func() bool { return processed(pool) == 5 })
	want := map[uuid.UUID]execution.ExecutionStatus{
		running.ID: execution.StatusRunning, completed.ID: execution.StatusCompleted,
		failed.ID: execution.StatusFailed, cancelled.ID: execution.StatusCancelled,
	}
	for id, s := range want {
		if got := e.get(t, id).Status; got != s {
			t.Fatalf("%s changed from %s to %s", id, s, got)
		}
	}
	if e.counts.total() != 0 {
		t.Fatal("no graph may run for non-PENDING executions")
	}
	if oc := outcomes(pool); oc[worker.OutcomeSkipped] != 4 || oc[worker.OutcomeNotFound] != 1 {
		t.Fatalf("outcomes = %v", oc)
	}
}

func TestSubmitterCreatesPendingAndEnqueuesIdentityOnly(t *testing.T) {
	e := newEnv(t, 0)
	wf, v := e.seed(t, "test.count")
	s, err := queue.NewSubmitter(e.svc, dispatcher(t, e))
	if err != nil {
		t.Fatal(err)
	}
	key := uuid.NewString()
	ex, err := s.Submit(context.Background(), wf, v, map[string]any{"key": key})
	if err != nil {
		t.Fatal(err)
	}
	// The request path persisted PENDING and executed nothing.
	if got := e.get(t, ex.ID); got.Status != execution.StatusPending || got.StartedAt != nil {
		t.Fatalf("after submit: %+v", got)
	}
	if e.counts.get(key) != 0 {
		t.Fatal("submit must not execute")
	}
	vals, err := e.rawRedis.LRange(context.Background(), e.queue.Name(), 0, -1).Result()
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"v":1,"execution_id":"` + ex.ID.String() + `"}`; len(vals) != 1 || vals[0] != want {
		t.Fatalf("redis payload = %v, want [%s]", vals, want)
	}
	e.startPool(t, 1)
	eventually(t, 15*time.Second, "COMPLETED", func() bool { return e.get(t, ex.ID).Status == execution.StatusCompleted })
	if e.counts.get(key) != 1 {
		t.Fatal("graph must run once")
	}
}

func TestGracefulShutdownCancelsInFlightAndReleasesWorkers(t *testing.T) {
	e := newEnv(t, 0)
	wf, v := e.seed(t, "test.block")
	ex, key := e.createPending(t, wf, v)
	e.enqueue(t, ex.ID)
	baseline := runtime.NumGoroutine()

	pool, err := worker.NewPool(3, e.queue, e.processor(t), worker.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- pool.Run(ctx, 10*time.Second) }()

	select {
	case got := <-e.started:
		if got != key {
			t.Fatalf("started %s", got)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("execution never started")
	}
	if s := e.get(t, ex.ID).Status; s != execution.StatusRunning {
		t.Fatalf("status while in flight = %s", s)
	}
	// Two idle workers are blocked in BRPOP; one runs the execution.
	stopAt := time.Now()
	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("pool did not shut down")
	}
	if d := time.Since(stopAt); d > 5*time.Second {
		t.Fatalf("shutdown took %v", d)
	}
	for _, w := range pool.Status().Workers {
		if w.State != worker.StateStopped {
			t.Fatalf("%s state %s", w.ID, w.State)
		}
	}
	got := e.get(t, ex.ID)
	if got.Status != execution.StatusCancelled || got.FinishedAt == nil {
		t.Fatalf("in-flight execution after shutdown = %s", got.Status)
	}
	if e.queueLen(t) != 0 {
		t.Fatal("a claimed execution must not be requeued")
	}
	eventually(t, 5*time.Second, "worker goroutines to exit", func() bool { return runtime.NumGoroutine() <= baseline+2 })
}

func TestWorkerRuntimeEndToEndWithHealth(t *testing.T) {
	e := newEnv(t, 0)
	wf, v := e.seed(t, "test.count")
	cfg := config.Config{
		DatabaseURL: e.dbURL, RedisURL: e.redisURL,
		RedisQueueName: e.queue.Name(), WorkerCount: 3, WorkerShutdownTimeout: 10 * time.Second,
	}
	if _, err := app.NewWorkerRuntime(context.Background(), config.Config{DatabaseURL: e.dbURL}, e.app, nil); !errors.Is(err, config.ErrInvalidConfig) {
		t.Fatalf("missing REDIS_URL must be rejected: %v", err)
	}
	rt, err := app.NewWorkerRuntime(context.Background(), cfg, e.app, nil)
	if err != nil {
		t.Fatal(err)
	}
	if h := rt.Health(context.Background()); !h.QueueReachable || h.WorkersConfigured != 3 || h.WorkersRunning != 0 || h.Healthy() {
		t.Fatalf("before run: %+v", h)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- rt.Run(ctx) }()
	eventually(t, 5*time.Second, "healthy", func() bool { return rt.Health(context.Background()).Healthy() })

	s, err := rt.Submitter()
	if err != nil {
		t.Fatal(err)
	}
	key := uuid.NewString()
	ex, err := s.Submit(context.Background(), wf, v, map[string]any{"key": key})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, 15*time.Second, "COMPLETED", func() bool { return e.get(t, ex.ID).Status == execution.StatusCompleted })
	if e.counts.get(key) != 1 {
		t.Fatal("graph must run once")
	}
	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runtime did not stop")
	}
	if h := rt.Health(context.Background()); h.QueueReachable || h.WorkersRunning != 0 {
		t.Fatalf("after shutdown Redis must be closed and workers stopped: %+v", h)
	}
}
