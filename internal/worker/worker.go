package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"workflow-optimizer/internal/queue"
)

// Worker is a long-running job consumer.
type Worker interface {
	// Start begins consuming in the background and returns immediately.
	// Cancelling ctx stops the worker just like Stop.
	Start(ctx context.Context) error
	// Stop asks the worker to stop and waits until it has, or until ctx ends
	// (then the worker keeps stopping in the background and ctx.Err() is
	// returned).
	Stop(ctx context.Context) error
}

// State is a worker's in-memory lifecycle state (never persisted).
type State string

const (
	StateIdle     State = "idle" // constructed, not started
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateStopped  State = "stopped"
)

var (
	// ErrAlreadyStarted is returned by Start on a worker that was started before.
	ErrAlreadyStarted = errors.New("worker: already started")
	// ErrProcessorPanicked marks a job whose processing panicked outside the
	// node boundary (recovered by the worker; see QueueWorker.process).
	ErrProcessorPanicked = errors.New("worker: job processing panicked")
	errNilDependency     = errors.New("worker: nil dependency")
)

// Options tunes a worker. Zero values select the defaults.
type Options struct {
	// Logger receives every non-routine event (malformed jobs, queue errors,
	// failed executions, lost jobs). Default: slog.Default().
	Logger *slog.Logger
	// ErrorPause is how long the loop waits after a queue error before
	// dequeueing again, so an unreachable queue does not cause a hot loop.
	// It is loop pacing, not a job retry. Default 1s.
	ErrorPause time.Duration
	// RequeueTimeout bounds returning an unclaimed job to the queue (on stop,
	// or after an infrastructure failure before the claim). Default 5s.
	RequeueTimeout time.Duration
	// OnDeadLetter, when set, is called for every dead-lettered execution
	// (e.g. to publish a non-authoritative notice).
	OnDeadLetter func(ctx context.Context, res Result)
}

func (o Options) withDefaults() Options {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.ErrorPause <= 0 {
		o.ErrorPause = time.Second
	}
	if o.RequeueTimeout <= 0 {
		o.RequeueTimeout = 5 * time.Second
	}
	return o
}

// Stats counts what a worker has done.
type Stats struct {
	Outcomes      map[Outcome]int
	Malformed     int // payloads that were not valid jobs (dropped, logged)
	QueueErrors   int // Dequeue failures other than cancellation
	Requeued      int // unclaimed jobs returned to the queue (stop or pre-claim failure)
	RequeueFailed int // unclaimed jobs that could not be returned (lost; logged)
	Panics        int // processor panics recovered by the worker (logged with stack)
}

// QueueWorker consumes jobs from a queue.JobQueue and hands each to a
// JobProcessor, one job at a time. It owns exactly one goroutine, which
// Stop (or cancelling the Start context) ends.
type QueueWorker struct {
	id        string
	queue     queue.JobQueue
	processor JobProcessor
	opts      Options

	mu     sync.Mutex
	state  State
	cancel context.CancelFunc
	done   chan struct{}
	stats  Stats
}

var _ Worker = (*QueueWorker)(nil)

// NewWorker constructs an idle worker.
func NewWorker(id string, q queue.JobQueue, p JobProcessor, opts Options) (*QueueWorker, error) {
	if q == nil || p == nil {
		return nil, fmt.Errorf("%w: queue and processor are required", errNilDependency)
	}
	return &QueueWorker{
		id: id, queue: q, processor: p, opts: opts.withDefaults(),
		state: StateIdle, done: make(chan struct{}),
		stats: Stats{Outcomes: map[Outcome]int{}},
	}, nil
}

// ID returns the worker's identifier (used in logs).
func (w *QueueWorker) ID() string { return w.id }

// Start launches the consume loop.
func (w *QueueWorker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state != StateIdle {
		return fmt.Errorf("%w: worker %s is %s", ErrAlreadyStarted, w.id, w.state)
	}
	loopCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.state = StateStarting
	go w.loop(loopCtx)
	return nil
}

// Stop cancels the loop and waits for it to finish.
func (w *QueueWorker) Stop(ctx context.Context) error {
	w.signalStop()
	return w.wait(ctx)
}

// signalStop asks the loop to stop without waiting.
func (w *QueueWorker) signalStop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch w.state {
	case StateIdle:
		w.state = StateStopped
		close(w.done)
	case StateStarting, StateRunning:
		w.state = StateStopping
		w.cancel()
	}
}

func (w *QueueWorker) wait(ctx context.Context) error {
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("worker %s did not stop in time: %w", w.id, ctx.Err())
	}
}

// Done is closed when the worker has stopped.
func (w *QueueWorker) Done() <-chan struct{} { return w.done }

// State returns the current lifecycle state.
func (w *QueueWorker) State() State {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state
}

// Stats returns a snapshot of the worker's counters.
func (w *QueueWorker) Stats() Stats {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.stats
	s.Outcomes = make(map[Outcome]int, len(w.stats.Outcomes))
	for k, v := range w.stats.Outcomes {
		s.Outcomes[k] = v
	}
	return s
}

func (w *QueueWorker) record(f func(*Stats)) {
	w.mu.Lock()
	f(&w.stats)
	w.mu.Unlock()
}

func (w *QueueWorker) loop(ctx context.Context) {
	defer func() {
		w.mu.Lock()
		w.state = StateStopped
		w.mu.Unlock()
		close(w.done)
	}()
	w.mu.Lock()
	if w.state == StateStarting {
		w.state = StateRunning
	}
	w.mu.Unlock()
	log := w.opts.Logger.With("worker", w.id)

	for {
		job, err := w.queue.Dequeue(ctx)
		if err != nil {
			switch {
			case ctx.Err() != nil:
				return // stopping: blocked consumption was released
			case errors.Is(err, queue.ErrMalformedJob):
				w.record(func(s *Stats) { s.Malformed++ })
				log.Error("dropping malformed job payload", "error", err)
				continue
			case errors.Is(err, queue.ErrQueueClosed):
				log.Error("queue closed; worker stopping", "error", err)
				return
			default:
				w.record(func(s *Stats) { s.QueueErrors++ })
				log.Error("dequeue failed", "error", err, "pause", w.opts.ErrorPause)
				if !sleep(ctx, w.opts.ErrorPause) {
					return
				}
				continue
			}
		}
		if ctx.Err() != nil {
			// A job was removed just as we were asked to stop: give it back
			// instead of dropping it.
			w.requeue(ctx, log, job)
			return
		}

		res := w.process(ctx, log, job)
		w.record(func(s *Stats) { s.Outcomes[res.Outcome]++ })
		w.report(log, res)
		if res.DeadLetter != "" && w.opts.OnDeadLetter != nil {
			w.opts.OnDeadLetter(context.WithoutCancel(ctx), res)
		}
		if res.Requeue || (res.Outcome == OutcomeNotAttempted && ctx.Err() != nil) {
			// Not claimed by this worker (stopping, or an infrastructure
			// failure before the claim): the execution may still be PENDING
			// and this job must not be lost. The database claim makes the
			// returned job harmless if another job for it exists.
			w.requeue(ctx, log, job)
			if ctx.Err() == nil && !sleep(ctx, w.opts.ErrorPause) {
				// Paced like a queue error, so a persistent outage cannot turn
				// dequeue -> fail -> requeue into a hot loop.
				return
			}
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// process runs the processor with a last-resort recover. Node panics are
// already turned into ordinary execution failures by the executor; this only
// catches a panic in the surrounding processing code, so one bad job cannot
// kill the whole process (and every other worker's in-flight execution). The
// job is not returned to the queue, because whether it was claimed is unknown.
func (w *QueueWorker) process(ctx context.Context, log *slog.Logger, job queue.Job) (res Result) {
	defer func() {
		if r := recover(); r != nil {
			w.record(func(s *Stats) { s.Panics++ })
			log.Error("job processing panicked; job dropped, execution may need attention",
				"execution_id", job.ExecutionID, "panic", r, "stack", string(debug.Stack()))
			res = Result{ExecutionID: job.ExecutionID, Outcome: OutcomeError,
				Err: fmt.Errorf("%w: %v", ErrProcessorPanicked, r)}
		}
	}()
	return w.processor.Process(ctx, job)
}

func (w *QueueWorker) report(log *slog.Logger, res Result) {
	attrs := []any{"execution_id", res.ExecutionID, "outcome", res.Outcome, "status", res.Status}
	switch res.Outcome {
	case OutcomeError:
		log.Error("job could not be handled", append(attrs, "error", res.Err)...)
	case OutcomeFailed, OutcomeNotFound, OutcomeLeaseLost:
		log.Warn("job finished without success", append(attrs, "error", res.Err, "dead_letter", res.DeadLetter)...)
	case OutcomeRetryScheduled:
		log.Info("attempt failed; retry scheduled", append(attrs, "error", res.Err)...)
	case OutcomeNotAttempted:
		if res.Err != nil {
			log.Warn("execution not claimed", append(attrs, "error", res.Err)...)
		}
	default:
		log.Debug("job handled", attrs...)
	}
}

// requeue returns an unclaimed job to the queue on a context that survives
// the stop signal but is bounded.
func (w *QueueWorker) requeue(ctx context.Context, log *slog.Logger, job queue.Job) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), w.opts.RequeueTimeout)
	defer cancel()
	if err := w.queue.Enqueue(rctx, job); err != nil {
		w.record(func(s *Stats) { s.RequeueFailed++ })
		log.Error("JOB LOST: could not return unclaimed job to the queue; execution stays PENDING until dispatched again",
			"execution_id", job.ExecutionID, "error", err)
		return
	}
	w.record(func(s *Stats) { s.Requeued++ })
	log.Warn("returned unclaimed job to the queue", "execution_id", job.ExecutionID)
}

// sleep waits d or until ctx is done; it reports whether ctx is still live.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
