package worker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"workflow-optimizer/internal/queue"
)

// Pool runs a configurable number of QueueWorkers that share one queue and
// one processor (and therefore one PostgreSQL claim mechanism).
type Pool struct {
	workers []*QueueWorker

	mu      sync.Mutex
	started bool
}

var _ Worker = (*Pool)(nil)

// NewPool builds count idle workers.
func NewPool(count int, q queue.JobQueue, p JobProcessor, opts Options) (*Pool, error) {
	if count < 1 {
		return nil, fmt.Errorf("worker: pool size must be at least 1, got %d", count)
	}
	pool := &Pool{}
	for i := 0; i < count; i++ {
		w, err := NewWorker(fmt.Sprintf("worker-%d", i+1), q, p, opts)
		if err != nil {
			return nil, err
		}
		pool.workers = append(pool.workers, w)
	}
	return pool, nil
}

// Start starts every worker. If one cannot start, the others are stopped.
func (p *Pool) Start(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started {
		return fmt.Errorf("%w: pool", ErrAlreadyStarted)
	}
	p.started = true
	for i, w := range p.workers {
		if err := w.Start(ctx); err != nil {
			for _, started := range p.workers[:i] {
				started.signalStop()
			}
			return err
		}
	}
	return nil
}

// Stop signals every worker at once, then waits for all of them (bounded by
// ctx). In-flight jobs observe cancellation through their context.
func (p *Pool) Stop(ctx context.Context) error {
	for _, w := range p.workers {
		w.signalStop()
	}
	var errs []error
	for _, w := range p.workers {
		if err := w.wait(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Run starts the pool, blocks until ctx is done, then stops the pool within
// shutdownTimeout. It returns nil after a clean shutdown.
func (p *Pool) Run(ctx context.Context, shutdownTimeout time.Duration) error {
	if err := p.Start(ctx); err != nil {
		return err
	}
	<-ctx.Done()
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	return p.Stop(sctx)
}

// WorkerStatus is one worker's health snapshot.
type WorkerStatus struct {
	ID    string
	State State
	Stats Stats
}

// Status reports the pool's workers.
type Status struct {
	Configured int
	Running    int
	Workers    []WorkerStatus
}

// Status returns a snapshot of every worker.
func (p *Pool) Status() Status {
	s := Status{Configured: len(p.workers)}
	for _, w := range p.workers {
		ws := WorkerStatus{ID: w.ID(), State: w.State(), Stats: w.Stats()}
		if ws.State == StateRunning {
			s.Running++
		}
		s.Workers = append(s.Workers, ws)
	}
	return s
}

// Pinger reports whether a dependency (Redis) is reachable.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Health is the minimal Phase 9 health view: is the queue backend reachable,
// and are workers running.
type Health struct {
	QueueReachable    bool
	QueueError        string
	WorkersConfigured int
	WorkersRunning    int
}

// Healthy reports whether the queue is reachable and every worker runs.
func (h Health) Healthy() bool {
	return h.QueueReachable && h.WorkersConfigured > 0 && h.WorkersRunning == h.WorkersConfigured
}

// CheckHealth pings the queue backend and inspects the pool.
func CheckHealth(ctx context.Context, queueBackend Pinger, pool *Pool) Health {
	var h Health
	if queueBackend != nil {
		if err := queueBackend.Ping(ctx); err != nil {
			h.QueueError = err.Error()
		} else {
			h.QueueReachable = true
		}
	} else {
		h.QueueError = "no queue backend configured"
	}
	if pool != nil {
		st := pool.Status()
		h.WorkersConfigured, h.WorkersRunning = st.Configured, st.Running
	}
	return h
}
