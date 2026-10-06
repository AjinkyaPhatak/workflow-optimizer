package reliability

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"workflow-optimizer/internal/queue"
)

// Scheduler defaults.
const (
	DefaultSchedulerInterval = time.Second
	DefaultBatchSize         = 100
)

// Scheduler dispatches due PENDING executions to the queue:
//
//	PostgreSQL (next_attempt_at <= now, claimed atomically) -> Redis -> worker
//
// A retry is persisted (PENDING with next_attempt_at) before anything reaches
// the queue, so Redis never carries a job PostgreSQL has not made due; the
// claim guard in the database additionally refuses early claims of stale
// jobs. The dispatch mark is written before the enqueue: if the enqueue then
// fails (or the job is lost later, e.g. a worker dies right after BRPOP), the
// execution is still PENDING and is dispatched again after RedispatchAfter.
// Duplicate jobs are harmless: the atomic claim executes each attempt once.
type Scheduler struct {
	Store           DispatchStore
	Queue           queue.JobQueue
	Interval        time.Duration
	RedispatchAfter time.Duration
	Batch           int
	Logger          *slog.Logger
}

// NewScheduler validates the configuration.
func NewScheduler(store DispatchStore, q queue.JobQueue, interval, redispatchAfter time.Duration, logger *slog.Logger) (*Scheduler, error) {
	if store == nil || q == nil {
		return nil, fmt.Errorf("%w: scheduler requires a dispatch store and a queue", ErrInvalidConfig)
	}
	if interval <= 0 || redispatchAfter <= 0 {
		return nil, fmt.Errorf("%w: scheduler interval and re-dispatch interval must be positive", ErrInvalidConfig)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{Store: store, Queue: q, Interval: interval, RedispatchAfter: redispatchAfter, Batch: DefaultBatchSize, Logger: logger}, nil
}

// RunOnce claims and dispatches due executions until none are left (bounded
// by a few batches per call). It returns how many jobs were enqueued.
func (s *Scheduler) RunOnce(ctx context.Context) (int, error) {
	batch := s.Batch
	if batch <= 0 {
		batch = DefaultBatchSize
	}
	sent := 0
	for round := 0; round < 10; round++ {
		due, err := s.Store.ClaimDue(ctx, batch, s.RedispatchAfter)
		if err != nil {
			return sent, err
		}
		for _, d := range due {
			if err := s.Queue.Enqueue(ctx, queue.Job{ExecutionID: d.ExecutionID}); err != nil {
				// Marked dispatched but not delivered: it is claimed again
				// after RedispatchAfter.
				s.Logger.Error("dispatch failed; will re-dispatch", "execution_id", d.ExecutionID, "attempt", d.Attempt, "error", err)
				continue
			}
			sent++
		}
		if len(due) < batch {
			break
		}
	}
	return sent, nil
}

// Run dispatches every Interval until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	loop(ctx, s.Interval, func(ctx context.Context) {
		if _, err := s.RunOnce(ctx); err != nil && ctx.Err() == nil {
			s.Logger.Error("scheduler pass failed", "error", err)
		}
	})
}

// loop runs fn now and then every interval until ctx is done.
func loop(ctx context.Context, interval time.Duration, fn func(context.Context)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		fn(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
