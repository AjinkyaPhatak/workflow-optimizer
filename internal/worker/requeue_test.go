package worker_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"workflow-optimizer/internal/queue"
	"workflow-optimizer/internal/worker"
)

// Regression tests for pre-claim infrastructure failures (audit finding F2)
// and the worker's last-resort panic guard (F1).

// A Requeue result puts the job back and paces the loop; the job is then
// handled again (here: successfully) instead of being lost.
func TestRequeueResultReturnsJobAndPaces(t *testing.T) {
	q := queue.NewMemoryQueue()
	var (
		mu    sync.Mutex
		times []time.Time
	)
	proc := procFunc(func(_ context.Context, job queue.Job) worker.Result {
		mu.Lock()
		defer mu.Unlock()
		times = append(times, time.Now())
		if len(times) == 1 {
			return worker.Result{ExecutionID: job.ExecutionID, Outcome: worker.OutcomeError,
				Err: errors.New("load execution: connection reset"), Requeue: true}
		}
		return worker.Result{ExecutionID: job.ExecutionID, Outcome: worker.OutcomeCompleted}
	})
	opts := quiet
	opts.ErrorPause = 100 * time.Millisecond
	w, _ := worker.NewWorker("w", q, proc, opts)
	id := uuid.New()
	_ = q.Enqueue(context.Background(), queue.Job{ExecutionID: id})
	_ = w.Start(context.Background())
	eventually(t, "second attempt", func() bool { return w.Stats().Outcomes[worker.OutcomeCompleted] == 1 })
	stopWithin(t, w, time.Second)
	s := w.Stats()
	if s.Requeued != 1 || s.RequeueFailed != 0 || s.Outcomes[worker.OutcomeError] != 1 || q.Len() != 0 {
		t.Fatalf("stats = %+v, queue len %d", s, q.Len())
	}
	mu.Lock()
	defer mu.Unlock()
	if gap := times[1].Sub(times[0]); gap < 90*time.Millisecond {
		t.Fatalf("requeued job retaken after %v: the loop is not paced", gap)
	}
}

// A persistent failure (every load fails) is bounded by the pause: no hot
// loop, and the job is still in the queue when the worker stops.
func TestPersistentPreClaimFailureDoesNotBusyLoop(t *testing.T) {
	q := queue.NewMemoryQueue()
	var calls atomic.Int32
	proc := procFunc(func(_ context.Context, job queue.Job) worker.Result {
		calls.Add(1)
		return worker.Result{ExecutionID: job.ExecutionID, Outcome: worker.OutcomeError,
			Err: errors.New("database is down"), Requeue: true}
	})
	w, _ := worker.NewWorker("w", q, proc, quiet) // ErrorPause 20ms
	_ = q.Enqueue(context.Background(), queue.Job{ExecutionID: uuid.New()})
	_ = w.Start(context.Background())
	time.Sleep(300 * time.Millisecond)
	stopWithin(t, w, time.Second)
	n := calls.Load()
	if n < 3 || n > 20 { // ~15 at 20ms pacing; a hot loop would be thousands
		t.Fatalf("processed %d times in 300ms", n)
	}
	if q.Len() != 1 {
		t.Fatalf("the unclaimed job must survive the outage; queue len = %d", q.Len())
	}
}

// failingEnqueueQueue accepts jobs from the test but refuses every
// Enqueue made by the worker (simulating Redis going away).
type failingEnqueueQueue struct {
	*queue.MemoryQueue
	refuse atomic.Bool
}

func (f *failingEnqueueQueue) Enqueue(ctx context.Context, job queue.Job) error {
	if f.refuse.Load() {
		return errors.New("redis: connection refused")
	}
	return f.MemoryQueue.Enqueue(ctx, job)
}

// If the job cannot be returned, the loss is counted and logged loudly.
func TestFailedRequeueIsSurfaced(t *testing.T) {
	fq := &failingEnqueueQueue{MemoryQueue: queue.NewMemoryQueue()}
	id := uuid.New()
	_ = fq.Enqueue(context.Background(), queue.Job{ExecutionID: id})
	fq.refuse.Store(true)
	var (
		mu  sync.Mutex
		buf bytes.Buffer
	)
	opts := quiet
	opts.Logger = slog.New(slog.NewTextHandler(lockedWriter{&mu, &buf}, nil))
	proc := procFunc(func(_ context.Context, job queue.Job) worker.Result {
		return worker.Result{ExecutionID: job.ExecutionID, Outcome: worker.OutcomeError,
			Err: errors.New("database is down"), Requeue: true}
	})
	w, _ := worker.NewWorker("w", fq, proc, opts)
	_ = w.Start(context.Background())
	eventually(t, "requeue attempt", func() bool { return w.Stats().RequeueFailed == 1 })
	stopWithin(t, w, time.Second)
	if s := w.Stats(); s.Requeued != 0 || s.RequeueFailed != 1 {
		t.Fatalf("stats = %+v", s)
	}
	mu.Lock()
	logs := buf.String()
	mu.Unlock()
	if !strings.Contains(logs, "JOB LOST") || !strings.Contains(logs, id.String()) || !strings.Contains(logs, "level=ERROR") {
		t.Fatalf("loss not surfaced in logs:\n%s", logs)
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	b  *bytes.Buffer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

// Results that do not ask for Requeue (claimed, terminal, lost claim, not
// found, post-claim error) are never re-enqueued.
func TestNoRequeueWithoutFlag(t *testing.T) {
	for _, o := range []worker.Outcome{worker.OutcomeCompleted, worker.OutcomeFailed, worker.OutcomeCancelled,
		worker.OutcomeSkipped, worker.OutcomeClaimLost, worker.OutcomeNotFound, worker.OutcomeError} {
		q := queue.NewMemoryQueue()
		var calls atomic.Int32
		proc := procFunc(func(_ context.Context, job queue.Job) worker.Result {
			calls.Add(1)
			return worker.Result{ExecutionID: job.ExecutionID, Outcome: o, Err: errors.New("x")}
		})
		w, _ := worker.NewWorker("w", q, proc, quiet)
		_ = q.Enqueue(context.Background(), queue.Job{ExecutionID: uuid.New()})
		_ = w.Start(context.Background())
		eventually(t, "processed", func() bool { return calls.Load() == 1 })
		time.Sleep(60 * time.Millisecond)
		stopWithin(t, w, time.Second)
		if calls.Load() != 1 || q.Len() != 0 || w.Stats().Requeued != 0 {
			t.Fatalf("%s: calls=%d len=%d stats=%+v", o, calls.Load(), q.Len(), w.Stats())
		}
	}
}

// A Requeue result that arrives while stopping returns the job without
// waiting out the pause, so shutdown stays prompt.
func TestRequeueDuringStopDoesNotDelayShutdown(t *testing.T) {
	q := queue.NewMemoryQueue()
	entered := make(chan struct{})
	proc := procFunc(func(ctx context.Context, job queue.Job) worker.Result {
		close(entered)
		<-ctx.Done()
		return worker.Result{ExecutionID: job.ExecutionID, Outcome: worker.OutcomeNotAttempted, Err: ctx.Err(), Requeue: true}
	})
	opts := quiet
	opts.ErrorPause = 10 * time.Second
	w, _ := worker.NewWorker("w", q, proc, opts)
	id := uuid.New()
	_ = q.Enqueue(context.Background(), queue.Job{ExecutionID: id})
	_ = w.Start(context.Background())
	<-entered
	start := time.Now()
	stopWithin(t, w, time.Second)
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("stop took %v", d)
	}
	if j, err := q.Dequeue(context.Background()); err != nil || j.ExecutionID != id || w.Stats().Requeued != 1 {
		t.Fatalf("job = %v %v stats %+v", j, err, w.Stats())
	}
}

// A panic outside the node boundary (in processing code) is recovered by the
// worker: it is counted and reported as an error, the job is not requeued
// (its claim state is unknown), and the worker keeps consuming.
func TestProcessorPanicDoesNotKillWorker(t *testing.T) {
	q := queue.NewMemoryQueue()
	bad, good := uuid.New(), uuid.New()
	proc := &recordingProcessor{}
	panicky := procFunc(func(ctx context.Context, job queue.Job) worker.Result {
		if job.ExecutionID == bad {
			var m map[string]int
			m["x"] = 1
		}
		return proc.Process(ctx, job)
	})
	w, _ := worker.NewWorker("w", q, panicky, quiet)
	_ = q.Enqueue(context.Background(), queue.Job{ExecutionID: bad})
	_ = q.Enqueue(context.Background(), queue.Job{ExecutionID: good})
	_ = w.Start(context.Background())
	eventually(t, "next job processed", func() bool { return len(proc.seen()) == 1 })
	if w.State() != worker.StateRunning {
		t.Fatalf("worker state = %s", w.State())
	}
	stopWithin(t, w, time.Second)
	s := w.Stats()
	if s.Panics != 1 || s.Outcomes[worker.OutcomeError] != 1 || s.Outcomes[worker.OutcomeCompleted] != 1 || s.Requeued != 0 || q.Len() != 0 {
		t.Fatalf("stats = %+v len=%d", s, q.Len())
	}
	if proc.seen()[0] != good {
		t.Fatalf("seen = %v", proc.seen())
	}
}
