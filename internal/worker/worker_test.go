package worker_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/queue"
	"workflow-optimizer/internal/worker"
)

var quiet = worker.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), ErrorPause: 20 * time.Millisecond}

// procFunc adapts a function to worker.JobProcessor.
type procFunc func(ctx context.Context, job queue.Job) worker.Result

func (f procFunc) Process(ctx context.Context, job queue.Job) worker.Result { return f(ctx, job) }

// recordingProcessor records every job it sees.
type recordingProcessor struct {
	mu   sync.Mutex
	jobs []uuid.UUID
}

func (r *recordingProcessor) Process(_ context.Context, job queue.Job) worker.Result {
	r.mu.Lock()
	r.jobs = append(r.jobs, job.ExecutionID)
	r.mu.Unlock()
	return worker.Result{ExecutionID: job.ExecutionID, Outcome: worker.OutcomeCompleted}
}

func (r *recordingProcessor) seen() []uuid.UUID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]uuid.UUID(nil), r.jobs...)
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func stopWithin(t *testing.T, w worker.Worker, d time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	if err := w.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestWorkerLifecycleAndConsumption(t *testing.T) {
	q := queue.NewMemoryQueue()
	proc := &recordingProcessor{}
	w, err := worker.NewWorker("w1", q, proc, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if w.State() != worker.StateIdle {
		t.Fatalf("state = %s", w.State())
	}
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := w.Start(context.Background()); !errors.Is(err, worker.ErrAlreadyStarted) {
		t.Fatalf("second start: %v", err)
	}
	eventually(t, "running state", func() bool { return w.State() == worker.StateRunning })

	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	for _, id := range ids {
		_ = q.Enqueue(context.Background(), queue.Job{ExecutionID: id})
	}
	eventually(t, "all jobs processed", func() bool { return len(proc.seen()) == 3 })
	for i, id := range proc.seen() {
		if id != ids[i] {
			t.Fatalf("job order %v, want %v", proc.seen(), ids)
		}
	}
	stopWithin(t, w, time.Second)
	if w.State() != worker.StateStopped {
		t.Fatalf("state after stop = %s", w.State())
	}
	stopWithin(t, w, time.Second) // idempotent
	if got := w.Stats().Outcomes[worker.OutcomeCompleted]; got != 3 {
		t.Fatalf("completed = %d", got)
	}
}

func TestStopBeforeStart(t *testing.T) {
	w, _ := worker.NewWorker("w", queue.NewMemoryQueue(), &recordingProcessor{}, quiet)
	stopWithin(t, w, time.Second)
	if w.State() != worker.StateStopped {
		t.Fatalf("state = %s", w.State())
	}
	if err := w.Start(context.Background()); !errors.Is(err, worker.ErrAlreadyStarted) {
		t.Fatalf("start after stop: %v", err)
	}
}

func TestNilDependenciesRejected(t *testing.T) {
	if _, err := worker.NewWorker("w", nil, &recordingProcessor{}, quiet); err == nil {
		t.Fatal("nil queue accepted")
	}
	if _, err := worker.NewWorker("w", queue.NewMemoryQueue(), nil, quiet); err == nil {
		t.Fatal("nil processor accepted")
	}
	if _, err := worker.NewPool(0, queue.NewMemoryQueue(), &recordingProcessor{}, quiet); err == nil {
		t.Fatal("pool of zero accepted")
	}
	if _, err := worker.NewExecutionProcessor(nil, nil); err == nil {
		t.Fatal("processor without dependencies accepted")
	}
}

// Cancelling the Start context stops the worker and releases a Dequeue that
// is blocked on an empty queue.
func TestParentCancellationStopsBlockedWorker(t *testing.T) {
	w, _ := worker.NewWorker("w", queue.NewMemoryQueue(), &recordingProcessor{}, quiet)
	ctx, cancel := context.WithCancel(context.Background())
	_ = w.Start(ctx)
	eventually(t, "running", func() bool { return w.State() == worker.StateRunning })
	cancel()
	select {
	case <-w.Done():
	case <-time.After(time.Second):
		t.Fatal("worker did not stop after context cancellation")
	}
}

// The job in progress observes cancellation, and Stop waits for it.
func TestInFlightJobObservesCancellation(t *testing.T) {
	q := queue.NewMemoryQueue()
	entered := make(chan struct{})
	var sawCancel atomic.Bool
	proc := procFunc(func(ctx context.Context, job queue.Job) worker.Result {
		close(entered)
		<-ctx.Done()
		time.Sleep(30 * time.Millisecond) // finishing up after cancellation
		sawCancel.Store(true)
		return worker.Result{ExecutionID: job.ExecutionID, Outcome: worker.OutcomeCancelled}
	})
	w, _ := worker.NewWorker("w", q, proc, quiet)
	_ = w.Start(context.Background())
	_ = q.Enqueue(context.Background(), queue.Job{ExecutionID: uuid.New()})
	<-entered
	stopWithin(t, w, 2*time.Second)
	if !sawCancel.Load() {
		t.Fatal("Stop returned before the in-flight job finished")
	}
}

// Stop is bounded: a job that ignores cancellation makes Stop return the
// context error instead of hanging.
func TestStopIsBoundedByContext(t *testing.T) {
	q := queue.NewMemoryQueue()
	release := make(chan struct{})
	entered := make(chan struct{})
	proc := procFunc(func(_ context.Context, job queue.Job) worker.Result {
		close(entered)
		<-release // ignores ctx
		return worker.Result{ExecutionID: job.ExecutionID, Outcome: worker.OutcomeCompleted}
	})
	w, _ := worker.NewWorker("w", q, proc, quiet)
	_ = w.Start(context.Background())
	_ = q.Enqueue(context.Background(), queue.Job{ExecutionID: uuid.New()})
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := w.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	close(release)
	select {
	case <-w.Done():
	case <-time.After(time.Second):
		t.Fatal("worker never finished stopping")
	}
}

func TestMalformedPayloadIsCountedAndSkipped(t *testing.T) {
	q := queue.NewMemoryQueue()
	proc := &recordingProcessor{}
	w, _ := worker.NewWorker("w", q, proc, quiet)
	_ = q.PushRaw([]byte("not a job"))
	id := uuid.New()
	_ = q.Enqueue(context.Background(), queue.Job{ExecutionID: id})
	_ = w.Start(context.Background())
	eventually(t, "valid job after malformed", func() bool { return len(proc.seen()) == 1 })
	stopWithin(t, w, time.Second)
	if s := w.Stats(); s.Malformed != 1 || proc.seen()[0] != id {
		t.Fatalf("stats = %+v", s)
	}
}

// flakyQueue fails a number of Dequeues before delegating.
type flakyQueue struct {
	*queue.MemoryQueue
	failures atomic.Int32
	calls    atomic.Int32
}

func (f *flakyQueue) Dequeue(ctx context.Context) (queue.Job, error) {
	f.calls.Add(1)
	if f.failures.Add(-1) >= 0 {
		return queue.Job{}, errors.New("connection refused")
	}
	return f.MemoryQueue.Dequeue(ctx)
}

func TestQueueErrorsArePacedNotHotLooped(t *testing.T) {
	fq := &flakyQueue{MemoryQueue: queue.NewMemoryQueue()}
	fq.failures.Store(3)
	proc := &recordingProcessor{}
	w, _ := worker.NewWorker("w", fq, proc, quiet) // ErrorPause 20ms
	id := uuid.New()
	_ = fq.Enqueue(context.Background(), queue.Job{ExecutionID: id})
	start := time.Now()
	_ = w.Start(context.Background())
	eventually(t, "job after queue recovers", func() bool { return len(proc.seen()) == 1 })
	if elapsed := time.Since(start); elapsed < 55*time.Millisecond {
		t.Fatalf("3 errors handled in %s: the loop is not pausing", elapsed)
	}
	stopWithin(t, w, time.Second)
	if s := w.Stats(); s.QueueErrors != 3 {
		t.Fatalf("stats = %+v", s)
	}
}

// A job whose claim was not attempted because the worker is stopping goes
// back to the queue instead of being lost.
func TestUnclaimedJobIsReturnedOnStop(t *testing.T) {
	q := queue.NewMemoryQueue()
	entered := make(chan struct{})
	proc := procFunc(func(ctx context.Context, job queue.Job) worker.Result {
		close(entered)
		<-ctx.Done()
		return worker.Result{ExecutionID: job.ExecutionID, Outcome: worker.OutcomeNotAttempted, Err: ctx.Err()}
	})
	w, _ := worker.NewWorker("w", q, proc, quiet)
	id := uuid.New()
	_ = q.Enqueue(context.Background(), queue.Job{ExecutionID: id})
	_ = w.Start(context.Background())
	<-entered
	stopWithin(t, w, time.Second)
	j, err := q.Dequeue(context.Background())
	if err != nil || j.ExecutionID != id || w.Stats().Requeued != 1 {
		t.Fatalf("requeued job = %v, %v, stats %+v", j, err, w.Stats())
	}
}

// A result that does not ask for Requeue is not re-enqueued while the worker
// keeps running: only the processor decides that a job is safely unclaimed
// (see TestRequeueResultReturnsJobAndPaces).
func TestNotAttemptedWhileRunningIsNotRetried(t *testing.T) {
	q := queue.NewMemoryQueue()
	var calls atomic.Int32
	proc := procFunc(func(_ context.Context, job queue.Job) worker.Result {
		calls.Add(1)
		return worker.Result{ExecutionID: job.ExecutionID, Outcome: worker.OutcomeNotAttempted, Err: errors.New("claim refused")}
	})
	w, _ := worker.NewWorker("w", q, proc, quiet)
	_ = q.Enqueue(context.Background(), queue.Job{ExecutionID: uuid.New()})
	_ = w.Start(context.Background())
	eventually(t, "processed", func() bool { return calls.Load() == 1 })
	time.Sleep(50 * time.Millisecond)
	stopWithin(t, w, time.Second)
	if calls.Load() != 1 || q.Len() != 0 || w.Stats().Requeued != 0 {
		t.Fatalf("calls=%d len=%d stats=%+v", calls.Load(), q.Len(), w.Stats())
	}
}

// stopRacingQueue returns a job at the very moment the worker is told to stop.
type stopRacingQueue struct {
	*queue.MemoryQueue
	cancel context.CancelFunc
	once   sync.Once
	id     uuid.UUID
}

func (s *stopRacingQueue) Dequeue(ctx context.Context) (queue.Job, error) {
	var job queue.Job
	fired := false
	s.once.Do(func() { s.cancel(); job, fired = queue.Job{ExecutionID: s.id}, true })
	if fired {
		return job, nil
	}
	return s.MemoryQueue.Dequeue(ctx)
}

func TestJobDequeuedDuringStopIsReturned(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sq := &stopRacingQueue{MemoryQueue: queue.NewMemoryQueue(), cancel: cancel, id: uuid.New()}
	proc := &recordingProcessor{}
	w, _ := worker.NewWorker("w", sq, proc, quiet)
	_ = w.Start(ctx)
	<-w.Done()
	if len(proc.seen()) != 0 {
		t.Fatal("job must not be processed after stop")
	}
	if j, err := sq.MemoryQueue.Dequeue(context.Background()); err != nil || j.ExecutionID != sq.id {
		t.Fatalf("job was dropped: %v %v", j, err)
	}
}

// The pool runs exactly WORKER_COUNT workers, and they process concurrently.
func TestPoolRunsConfiguredWorkersConcurrently(t *testing.T) {
	const workers = 5
	q := queue.NewMemoryQueue()
	var inFlight, maxInFlight atomic.Int32
	release := make(chan struct{})
	proc := procFunc(func(_ context.Context, job queue.Job) worker.Result {
		n := inFlight.Add(1)
		for {
			m := maxInFlight.Load()
			if n <= m || maxInFlight.CompareAndSwap(m, n) {
				break
			}
		}
		<-release
		inFlight.Add(-1)
		return worker.Result{ExecutionID: job.ExecutionID, Outcome: worker.OutcomeCompleted}
	})
	pool, err := worker.NewPool(workers, q, proc, quiet)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		_ = q.Enqueue(context.Background(), queue.Job{ExecutionID: uuid.New()})
	}
	_ = pool.Start(context.Background())
	eventually(t, "all workers busy", func() bool { return inFlight.Load() == workers })
	time.Sleep(30 * time.Millisecond)
	if st := pool.Status(); st.Configured != workers || st.Running != workers || maxInFlight.Load() != workers {
		t.Fatalf("status=%+v maxInFlight=%d", st, maxInFlight.Load())
	}
	close(release)
	eventually(t, "queue drained", func() bool { return q.Len() == 0 && inFlight.Load() == 0 })
	stopWithin(t, pool, time.Second)
	total := 0
	for _, ws := range pool.Status().Workers {
		total += ws.Stats.Outcomes[worker.OutcomeCompleted]
		if ws.State != worker.StateStopped {
			t.Fatalf("worker %s is %s", ws.ID, ws.State)
		}
	}
	if total != 20 {
		t.Fatalf("processed %d of 20", total)
	}
}

// Graceful shutdown leaves no goroutines behind.
func TestPoolShutdownLeaksNoGoroutines(t *testing.T) {
	before := runtime.NumGoroutine()
	q := queue.NewMemoryQueue()
	pool, _ := worker.NewPool(8, q, &recordingProcessor{}, quiet)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- pool.Run(ctx, time.Second) }()
	eventually(t, "pool running", func() bool { return pool.Status().Running == 8 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	eventually(t, "goroutines released", func() bool { return runtime.NumGoroutine() <= before })
}

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func TestHealth(t *testing.T) {
	pool, _ := worker.NewPool(2, queue.NewMemoryQueue(), &recordingProcessor{}, quiet)
	h := worker.CheckHealth(context.Background(), fakePinger{}, pool)
	if !h.QueueReachable || h.WorkersRunning != 0 || h.Healthy() {
		t.Fatalf("before start: %+v", h)
	}
	_ = pool.Start(context.Background())
	eventually(t, "running", func() bool { return pool.Status().Running == 2 })
	if h := worker.CheckHealth(context.Background(), fakePinger{}, pool); !h.Healthy() {
		t.Fatalf("running: %+v", h)
	}
	if h := worker.CheckHealth(context.Background(), fakePinger{err: errors.New("down")}, pool); h.QueueReachable || h.Healthy() || h.QueueError == "" {
		t.Fatalf("redis down: %+v", h)
	}
	stopWithin(t, pool, time.Second)
}
