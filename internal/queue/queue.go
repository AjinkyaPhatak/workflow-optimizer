package queue

import (
	"context"
	"errors"
)

// ErrQueueClosed is returned by a queue that has been closed.
var ErrQueueClosed = errors.New("queue: closed")

// JobQueue is the application's queue abstraction.
//
// Delivery is at-least-once oriented: Enqueue may be called more than once
// for the same execution and consumers must tolerate duplicates. There is no
// acknowledgement, visibility timeout, retry or dead-letter mechanism (Phase
// 9 scope): a job removed by Dequeue is gone from the queue.
type JobQueue interface {
	// Enqueue adds a job. It validates the job and respects ctx.
	Enqueue(ctx context.Context, job Job) error
	// Dequeue blocks until a job is available or ctx is done. A payload that
	// cannot be decoded is returned as a *MalformedJobError (it has already
	// been removed). If a job was removed from the queue, Dequeue returns it
	// even if ctx was cancelled meanwhile, so a removed job is never dropped
	// silently by the queue itself.
	Dequeue(ctx context.Context) (Job, error)
}
